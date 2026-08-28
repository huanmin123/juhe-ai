package ownermanifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validJ3cReader = `package j3creadonly
type HealthSource interface { ReadHealthFact(any, string, string) (any, bool, error) }
type Reader struct { source HealthSource }
func (r *Reader) Read(any, string, string) (any, bool, error) { return nil, false, nil }
`

func TestVerifyJ3cReadOnlyBoundaryReportsNodeOwnerSeparately(t *testing.T) {
	root := t.TempDir()
	boundary := filepath.Join(root, "backend-go", "projects", "gateway", "internal", "j3creadonly")
	if err := os.MkdirAll(boundary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boundary, "reader.go"), []byte(validJ3cReader), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, relative := range j3cNodeOwnerPaths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("// legacy J3c owner fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	report, err := VerifyJ3cReadOnlyBoundary(root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.GoBoundaryReady || !report.ReadOnlyAuditReady || report.J3cOwnerReady || !report.NodeOwnerPresent || len(report.NodeOwnerFiles) != len(j3cNodeOwnerPaths) {
		t.Fatalf("report=%+v", report)
	}
}

func TestVerifyJ3cReadOnlyBoundaryRejectsExtraReaderMethod(t *testing.T) {
	root := t.TempDir()
	boundary := filepath.Join(root, "backend-go", "projects", "gateway", "internal", "j3creadonly")
	if err := os.MkdirAll(boundary, 0o755); err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(validJ3cReader, "func (r *Reader) Read", "func (r *Reader) Mutate(any) error { return nil }\nfunc (r *Reader) Read", 1)
	if err := os.WriteFile(filepath.Join(boundary, "reader.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyJ3cReadOnlyBoundary(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.GoBoundaryReady || len(report.ForbiddenFindings) == 0 {
		t.Fatalf("extra mutation method must fail closed: %+v", report)
	}
}

func TestVerifyJ3cReadOnlyBoundaryRejectsExtraReaderField(t *testing.T) {
	root := t.TempDir()
	boundary := filepath.Join(root, "backend-go", "projects", "gateway", "internal", "j3creadonly")
	if err := os.MkdirAll(boundary, 0o755); err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(validJ3cReader, "type Reader struct { source HealthSource }", "type Reader struct { source HealthSource; writer any }", 1)
	if err := os.WriteFile(filepath.Join(boundary, "reader.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyJ3cReadOnlyBoundary(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.GoBoundaryReady || len(report.ForbiddenFindings) == 0 {
		t.Fatalf("extra reader field must fail closed: %+v", report)
	}
}
