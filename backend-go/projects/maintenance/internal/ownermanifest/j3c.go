package ownermanifest

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// J3cReadOnlyReport is a source-only audit of the J3b -> J3c boundary. It
// proves the Go adapter shape and reports Node owners that still need a
// separate J3c migration; it never claims that those owners have been cut
// over.
type J3cReadOnlyReport struct {
	RuleVersion        string   `json:"ruleVersion"`
	GoBoundaryFile     string   `json:"goBoundaryFile"`
	GoBoundaryReady    bool     `json:"goBoundaryReady"`
	NodeOwnerFiles     []string `json:"nodeOwnerFiles"`
	NodeOwnerPresent   bool     `json:"nodeOwnerPresent"`
	ForbiddenFindings  []string `json:"forbiddenFindings"`
	ReadOnlyAuditReady bool     `json:"readOnlyAuditReady"`
	J3cOwnerReady      bool     `json:"j3cOwnerReady"`
}

var j3cNodeOwnerPaths = []string{
	"backend/src/modules/background/account-quality-failure-precheck.service.ts",
	"backend/src/modules/background/background-jobs.ts",
	"backend/src/modules/background/background-job-registry.entries.ts",
	"backend/src/storage/account-quality.repository.ts",
	"backend/src/storage/model-quality.repository.ts",
	"backend/src/storage/model-quality-health.repository.ts",
	"backend/src/storage/usage-stats-account-quality-writer.ts",
}

// VerifyJ3cReadOnlyBoundary checks the narrow Go adapter with the AST and
// records known Node J3c owner files. A present Node file is expected while
// J3c remains unmigrated and therefore keeps the owner gate closed.
func VerifyJ3cReadOnlyBoundary(root string) (J3cReadOnlyReport, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return J3cReadOnlyReport{}, errors.New("J3c audit root is required")
	}
	boundary := filepath.Join(root, "backend-go", "projects", "gateway", "internal", "j3creadonly", "reader.go")
	report := J3cReadOnlyReport{RuleVersion: "j3c-readonly-v1", GoBoundaryFile: filepath.ToSlash(filepath.Join("backend-go", "projects", "gateway", "internal", "j3creadonly", "reader.go"))}
	findings, err := inspectJ3cBoundary(boundary)
	if err != nil {
		return J3cReadOnlyReport{}, err
	}
	report.ForbiddenFindings = make([]string, 0, len(findings))
	report.ForbiddenFindings = append(report.ForbiddenFindings, findings...)
	report.GoBoundaryReady = len(findings) == 0
	for _, relative := range j3cNodeOwnerPaths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			report.NodeOwnerFiles = append(report.NodeOwnerFiles, filepath.ToSlash(relative))
		}
	}
	sort.Strings(report.NodeOwnerFiles)
	report.NodeOwnerPresent = len(report.NodeOwnerFiles) > 0
	report.ReadOnlyAuditReady = report.GoBoundaryReady
	report.J3cOwnerReady = report.ReadOnlyAuditReady && !report.NodeOwnerPresent
	return report, nil
}

func inspectJ3cBoundary(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse J3c read-only boundary %s: %w", path, err)
	}
	var findings []string
	for _, imported := range file.Imports {
		name := strings.Trim(imported.Path.Value, `"`)
		if name == "database/sql" || name == "os/exec" || name == "net/http" {
			findings = append(findings, "forbidden import "+name)
		}
	}
	healthSourceMethods := 0
	readerTypeFound := false
	readerMethods := make(map[string]struct{})
	for _, declaration := range file.Decls {
		gen, ok := declaration.(*ast.GenDecl)
		if ok && gen.Tok.String() == "type" {
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				switch typeSpec.Name.Name {
				case "HealthSource":
					interfaceType, ok := typeSpec.Type.(*ast.InterfaceType)
					if !ok {
						findings = append(findings, "HealthSource is not an interface")
						continue
					}
					for _, method := range interfaceType.Methods.List {
						if len(method.Names) == 0 {
							findings = append(findings, "HealthSource embeds another interface")
							continue
						}
						for _, name := range method.Names {
							healthSourceMethods++
							if name.Name != "ReadHealthFact" {
								findings = append(findings, "HealthSource exposes "+name.Name)
							}
						}
					}
				case "Reader":
					readerTypeFound = true
					structType, ok := typeSpec.Type.(*ast.StructType)
					if !ok {
						findings = append(findings, "Reader is not a struct")
						continue
					}
					if len(structType.Fields.List) != 1 || len(structType.Fields.List[0].Names) != 1 || structType.Fields.List[0].Names[0].Name != "source" {
						findings = append(findings, "Reader must contain only the source field")
					}
				}
			}
		}
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || len(function.Recv.List) == 0 {
			continue
		}
		if receiverName(function.Recv.List[0].Type) == "Reader" {
			readerMethods[function.Name.Name] = struct{}{}
		}
	}
	if healthSourceMethods != 1 {
		findings = append(findings, fmt.Sprintf("HealthSource method count=%d, want 1", healthSourceMethods))
	}
	if !readerTypeFound {
		findings = append(findings, "Reader type is missing")
	}
	if _, ok := readerMethods["Read"]; !ok {
		findings = append(findings, "Reader.Read is missing")
	}
	for name := range readerMethods {
		if name != "Read" {
			findings = append(findings, "Reader exposes "+name)
		}
	}
	sort.Strings(findings)
	return findings, nil
}

func receiverName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return receiverName(value.X)
	default:
		return ""
	}
}
