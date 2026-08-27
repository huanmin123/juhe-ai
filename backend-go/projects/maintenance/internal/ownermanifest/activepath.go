package ownermanifest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ActivePathReport is a read-only inventory of Node J3b entry points. A
// non-empty finding list is expected before cutover and blocks the release.
type ActivePathReport struct {
	Root         string          `json:"root"`
	ScannedFiles int             `json:"scannedFiles"`
	Findings     []ActiveFinding `json:"findings"`
}

type ActiveFinding struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Pattern string `json:"pattern"`
}

var nodeJ3bPatterns = []struct {
	name   string
	needle string
}{
	{name: "model-check-route", needle: "modelChecksRouter"},
	{name: "model-check-proxy", needle: "modelCheckHttpProxy"},
	{name: "model-check-token-worker", needle: "startModelCheckTokenWorker"},
	{name: "model-quality-scheduler", needle: "model-quality-scheduled-check"},
	{name: "model-quality-command", needle: "model_quality_command"},
	{name: "model-check-dataset-write", needle: "model_check_runs"},
	{name: "model-check-dataset-write", needle: "model_check_items"},
	{name: "model-check-health-write", needle: "account_quality_health_hourly"},
}

// ScanNodeJ3bActivePaths scans only backend/src and intentionally ignores
// generated/dependency directories. The result is suitable for CI evidence;
// it does not attempt to infer runtime reachability from text alone.
func ScanNodeJ3bActivePaths(root string) (ActivePathReport, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return ActivePathReport{}, fmt.Errorf("Node active-path scan root is required")
	}
	sourceRoot := filepath.Join(root, "backend", "src")
	if info, err := os.Stat(sourceRoot); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return ActivePathReport{}, fmt.Errorf("Node active-path scan source root %s: %w", sourceRoot, err)
	}
	report := ActivePathReport{Root: sourceRoot}
	err := filepath.Walk(sourceRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if info.Name() == "node_modules" || info.Name() == "dist" || info.Name() == ".git" || info.Name() == "regression" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".ts" && filepath.Ext(path) != ".tsx" {
			return nil
		}
		report.ScannedFiles++
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		line := 0
		for scanner.Scan() {
			line++
			text := scanner.Text()
			for _, pattern := range nodeJ3bPatterns {
				if strings.Contains(text, pattern.needle) {
					rel, relErr := filepath.Rel(root, path)
					if relErr != nil {
						rel = path
					}
					report.Findings = append(report.Findings, ActiveFinding{Path: filepath.ToSlash(rel), Line: line, Pattern: pattern.name})
				}
			}
		}
		return scanner.Err()
	})
	if err != nil {
		return ActivePathReport{}, fmt.Errorf("scan Node J3b active paths: %w", err)
	}
	return report, nil
}
