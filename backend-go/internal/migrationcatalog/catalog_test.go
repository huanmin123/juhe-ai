package migrationcatalog

import (
	"io/fs"
	"reflect"
	"testing"
	"testing/fstest"
)

func TestInspectReturnsMigrationsSortedByNumericVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"000010_tenth.sql":  {Data: []byte("SELECT 10;")},
		"000002_second.sql": {Data: []byte("SELECT 2;")},
		"999999_last_1.sql": {Data: []byte("SELECT 999999;")},
	}

	got, err := Inspect(fsys)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	want := []Entry{
		{Version: 2, Name: "000002_second.sql"},
		{Version: 10, Name: "000010_tenth.sql"},
		{Version: 999999, Name: "999999_last_1.sql"},
	}
	if !reflect.DeepEqual(got.Entries, want) {
		t.Fatalf("Inspect().Entries = %#v, want %#v", got.Entries, want)
	}
}

func TestInspectAcceptsEmptyDirectory(t *testing.T) {
	fsys := fstest.MapFS{
		".": {Mode: fs.ModeDir | 0o755},
	}

	got, err := Inspect(fsys)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got.Entries == nil || len(got.Entries) != 0 {
		t.Fatalf("Inspect().Entries = %#v, want non-nil empty slice", got.Entries)
	}
}

func TestInspectRejectsInvalidCatalogEntries(t *testing.T) {
	tests := []struct {
		name    string
		fsys    fstest.MapFS
		wantErr string
	}{
		{
			name: "duplicate numeric version",
			fsys: fstest.MapFS{
				"000007_alpha.sql": {Data: []byte("SELECT 1;")},
				"000007_beta.sql":  {Data: []byte("SELECT 2;")},
			},
			wantErr: `migration version 7 is duplicated by "000007_alpha.sql" and "000007_beta.sql"`,
		},
		{
			name: "invalid filename",
			fsys: fstest.MapFS{
				"000001_VALID.sql": {Data: []byte("SELECT 1;")},
			},
			wantErr: `invalid migration filename "000001_VALID.sql"`,
		},
		{
			name: "zero version",
			fsys: fstest.MapFS{
				"000000_zero.sql": {Data: []byte("SELECT 0;")},
			},
			wantErr: `migration version must be positive in "000000_zero.sql"`,
		},
		{
			name: "subdirectory",
			fsys: fstest.MapFS{
				"nested/000001_nested.sql": {Data: []byte("SELECT 1;")},
			},
			wantErr: `migration catalog contains non-file entry "nested"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Inspect(tt.fsys)
			if err == nil {
				t.Fatal("Inspect() error = nil, want error")
			}
			if got := err.Error(); got != tt.wantErr {
				t.Fatalf("Inspect() error = %q, want %q", got, tt.wantErr)
			}
		})
	}
}
