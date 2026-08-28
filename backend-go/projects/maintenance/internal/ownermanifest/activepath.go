package ownermanifest

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ActivePathReport struct {
	RuleVersion     string           `json:"ruleVersion"`
	Root            string           `json:"root"`
	ScannedFiles    int              `json:"scannedFiles"`
	Findings        []ActiveFinding  `json:"findings"`
	BlockedFindings int              `json:"blockedFindings"`
	Skipped         []ActivePathSkip `json:"skipped"`
	Rules           []ActivePathRule `json:"rules"`
}

type ActiveFinding struct {
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Pattern     string `json:"pattern"`
	Category    string `json:"category"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
}

type ActivePathSkip struct {
	Path        string `json:"path"`
	Rule        string `json:"rule"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
}

type ActivePathRule struct {
	Name        string `json:"name"`
	Needle      string `json:"needle"`
	Category    string `json:"category"`
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
}

var nodeJ3bPatterns = []ActivePathRule{
	{Name: "model-check-route", Needle: "modelChecksRouter", Category: "management-route", Disposition: "block", Reason: "Node J3b management route must be removed or retargeted to Gateway"},
	{Name: "model-check-proxy", Needle: "modelCheckHttpProxy", Category: "management-proxy", Disposition: "block", Reason: "Node proxy remains an active J3b entry point"},
	{Name: "model-check-token-worker", Needle: "startModelCheckTokenWorker", Category: "token-worker", Disposition: "block", Reason: "Node token worker must be stopped before Gateway cutover"},
	{Name: "model-quality-scheduler", Needle: "model-quality-scheduled-check", Category: "scheduler", Disposition: "block", Reason: "Node quality scheduler must be drained before cutover"},
	{Name: "model-quality-command", Needle: "model_quality_command", Category: "business-command", Disposition: "block", Reason: "Node quality command is a Business writer path"},
	{Name: "model-check-dataset-write", Needle: "model_check_runs", Category: "dataset-writer", Disposition: "block", Reason: "Node J3b run writer must be archived after backfill"},
	{Name: "model-check-dataset-write", Needle: "model_check_items", Category: "dataset-writer", Disposition: "block", Reason: "Node J3b item writer must be archived after backfill"},
	{Name: "model-check-health-write", Needle: "account_quality_health_hourly", Category: "health-writer", Disposition: "block", Reason: "Node health projection writer must be removed or retargeted"},
}

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
	report := ActivePathReport{Root: sourceRoot, RuleVersion: "j3b-active-path-v2", Rules: append([]ActivePathRule(nil), nodeJ3bPatterns...)}
	addSkip := func(path, rule, reason string) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		report.Skipped = append(report.Skipped, ActivePathSkip{Path: filepath.ToSlash(rel), Rule: rule, Disposition: "allow", Reason: reason})
	}
	err := filepath.Walk(sourceRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", "dist", ".git":
				addSkip(path, "generated-or-dependency", "generated/dependency directory is outside production Node active path")
				return filepath.SkipDir
			case "regression":
				addSkip(path, "regression-fixture", "regression fixtures are evidence only")
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".ts" && ext != ".tsx" {
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
			for _, rule := range nodeJ3bPatterns {
				if !strings.Contains(text, rule.Needle) {
					continue
				}
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				report.Findings = append(report.Findings, ActiveFinding{Path: filepath.ToSlash(rel), Line: line, Pattern: rule.Name, Category: rule.Category, Disposition: rule.Disposition, Reason: rule.Reason})
				if rule.Disposition != "allow" {
					report.BlockedFindings++
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
