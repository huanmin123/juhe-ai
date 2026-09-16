package ownermanifest

// w12g 波次：补齐 J3c 只读边界 AST 审计的全部 finding 分支与
// ScanNodeJ3bActivePaths 的输入校验/归档/扩展名分支。
// J3c fixture 走真实 go/parser（语法合法即可，不做类型检查），
// activepath 用临时目录构造 active source 与 final-archive 的组合。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestW12GVerifyJ3cReadOnlyBoundaryRequiresRootAndParseableBoundary(t *testing.T) {
	if _, err := VerifyJ3cReadOnlyBoundary("   "); err == nil || !strings.Contains(err.Error(), "J3c audit root is required") {
		t.Fatalf("空根目录必须拒绝: %v", err)
	}
	// 边界文件缺失 → 解析错误上抛。
	if _, err := VerifyJ3cReadOnlyBoundary(t.TempDir()); err == nil || !strings.Contains(err.Error(), "parse J3c read-only boundary") {
		t.Fatalf("缺失边界文件必须报解析错误: %v", err)
	}
}

func TestW12GInspectJ3cBoundaryFindingMatrix(t *testing.T) {
	writeBoundary := func(t *testing.T, source string) []string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "reader.go")
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		findings, err := inspectJ3cBoundary(path)
		if err != nil {
			t.Fatalf("inspectJ3cBoundary: %v", err)
		}
		return findings
	}
	contains := func(findings []string, needle string) bool {
		for _, item := range findings {
			if strings.Contains(item, needle) {
				return true
			}
		}
		return false
	}

	t.Run("forbidden imports", func(t *testing.T) {
		source := `package j3creadonly
import "database/sql"
import "os/exec"
import "net/http"
type HealthSource interface { ReadHealthFact(any, string, string) (any, bool, error) }
type Reader struct { source HealthSource }
func (r *Reader) Read(any, string, string) (any, bool, error) { return nil, false, nil }
`
		findings := writeBoundary(t, source)
		for _, forbidden := range []string{"forbidden import database/sql", "forbidden import os/exec", "forbidden import net/http"} {
			if !contains(findings, forbidden) {
				t.Fatalf("缺少 %q: %v", forbidden, findings)
			}
		}
	})

	t.Run("HealthSource not interface", func(t *testing.T) {
		source := `package j3creadonly
type HealthSource struct{ value any }
type Reader struct { source HealthSource }
func (r *Reader) Read(any, string, string) (any, bool, error) { return nil, false, nil }
`
		findings := writeBoundary(t, source)
		if !contains(findings, "HealthSource is not an interface") || !contains(findings, "HealthSource method count=0") {
			t.Fatalf("非接口 HealthSource 必须双重报告: %v", findings)
		}
	})

	t.Run("HealthSource embeds interface", func(t *testing.T) {
		source := `package j3creadonly
type HealthSource interface { other }
type Reader struct { source HealthSource }
func (r *Reader) Read(any, string, string) (any, bool, error) { return nil, false, nil }
`
		findings := writeBoundary(t, source)
		if !contains(findings, "HealthSource embeds another interface") || !contains(findings, "HealthSource method count=0") {
			t.Fatalf("嵌入接口必须报告: %v", findings)
		}
	})

	t.Run("HealthSource exposes extra method", func(t *testing.T) {
		source := `package j3creadonly
type HealthSource interface {
	ReadHealthFact(any, string, string) (any, bool, error)
	WriteAnything(any) error
}
type Reader struct { source HealthSource }
func (r *Reader) Read(any, string, string) (any, bool, error) { return nil, false, nil }
`
		findings := writeBoundary(t, source)
		if !contains(findings, "HealthSource exposes WriteAnything") {
			t.Fatalf("额外方法必须报告: %v", findings)
		}
	})

	t.Run("Reader not struct and read missing", func(t *testing.T) {
		source := `package j3creadonly
type HealthSource interface { ReadHealthFact(any, string, string) (any, bool, error) }
type Reader interface { Read() }
`
		findings := writeBoundary(t, source)
		if !contains(findings, "Reader is not a struct") || !contains(findings, "Reader.Read is missing") {
			t.Fatalf("非结构体 Reader 必须报告: %v", findings)
		}
	})

	t.Run("Reader type missing", func(t *testing.T) {
		source := `package j3creadonly
type HealthSource interface { ReadHealthFact(any, string, string) (any, bool, error) }
`
		findings := writeBoundary(t, source)
		if !contains(findings, "Reader type is missing") || !contains(findings, "Reader.Read is missing") {
			t.Fatalf("缺失 Reader 必须报告: %v", findings)
		}
	})

	t.Run("Reader.Read method missing", func(t *testing.T) {
		source := `package j3creadonly
type HealthSource interface { ReadHealthFact(any, string, string) (any, bool, error) }
type Reader struct { source HealthSource }
`
		findings := writeBoundary(t, source)
		if !contains(findings, "Reader.Read is missing") {
			t.Fatalf("缺失 Read 必须报告: %v", findings)
		}
	})

	t.Run("non ident receiver falls back to empty name", func(t *testing.T) {
		// 数组接收者语法合法但不是 Ident/StarExpr，覆盖 receiverName default 分支。
		source := `package j3creadonly
type HealthSource interface { ReadHealthFact(any, string, string) (any, bool, error) }
type Reader struct { source HealthSource }
func (r [2]int) Read(any, string, string) (any, bool, error) { return nil, false, nil }
`
		findings := writeBoundary(t, source)
		if !contains(findings, "Reader.Read is missing") {
			t.Fatalf("非标准接收者不得计入 Reader 方法: %v", findings)
		}
	})
}

func TestW12GScanNodeJ3bActivePathsInputValidation(t *testing.T) {
	if _, err := ScanNodeJ3bActivePaths("   "); err == nil || !strings.Contains(err.Error(), "scan root is required") {
		t.Fatalf("空扫描根必须拒绝: %v", err)
	}

	// backend/src 缺失且归档缺失。
	root := t.TempDir()
	_, err := ScanNodeJ3bActivePaths(root)
	if err == nil || !strings.Contains(err.Error(), "requires active source") {
		t.Fatalf("缺失活跃源与归档必须拒绝: %v", err)
	}

	// backend/src 缺失且归档路径是普通文件。
	rootFile := t.TempDir()
	writeFixtureFile(t, rootFile, "migration-backup/node/final-archive/backend/src", "not a dir")
	_, err = ScanNodeJ3bActivePaths(rootFile)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("归档为文件必须拒绝: %v", err)
	}

	// backend/src 是普通文件（非目录）。
	rootSrcFile := t.TempDir()
	writeFixtureFile(t, rootSrcFile, "backend/src", "file")
	_, err = ScanNodeJ3bActivePaths(rootSrcFile)
	if err == nil || !strings.Contains(err.Error(), "Node active-path scan source root") {
		t.Fatalf("活跃源为文件必须拒绝: %v", err)
	}

	// 非 .ts 文件不扫描：.md 文件即使包含 needle 也不产生 finding。
	rootScan := t.TempDir()
	writeFixtureFile(t, rootScan, "backend/src/notes/README.md", "modelChecksRouter must be removed\n")
	writeFixtureFile(t, rootScan, "backend/src/modules/real.routes.ts", "modelChecksRouter.post('/run', handler)\n")
	writeFixtureFile(t, rootScan, "migration-backup/node/final-archive/backend/src", "archived")
	report, err := ScanNodeJ3bActivePaths(rootScan)
	if err != nil {
		t.Fatal(err)
	}
	if report.ScannedFiles != 1 || report.BlockedFindings != 1 {
		t.Fatalf("仅 .ts 参与扫描: scanned=%d blocked=%d", report.ScannedFiles, report.BlockedFindings)
	}
}
