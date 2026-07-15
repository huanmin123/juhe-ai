package migrationcatalog

import (
	"bytes"
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

func TestInspectAcceptsMigrationLinesAboveOneMiB(t *testing.T) {
	line := append([]byte("-- +goose Up\nSELECT '"), bytes.Repeat([]byte("x"), 2*1024*1024)...)
	line = append(line, []byte("';\n-- +goose Down\n")...)
	if _, err := Inspect(fstest.MapFS{"000001_large.sql": {Data: line}}); err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
}

func TestInspectAcceptsMarkedProceduralBlocks(t *testing.T) {
	fsys := fstest.MapFS{
		"000001_blocks.sql": {Data: []byte("-- +goose Up   \n" +
			"-- +goose StatementBegin   \n" +
			"DO $$\nBEGIN\n  PERFORM 1;\nEND $$;\n" +
			"-- +goose StatementEnd\n" +
			"--   +goose statementbegin\n" +
			"DO LANGUAGE plpgsql $body$\nBEGIN\n  PERFORM 2;\nEND $body$;\n" +
			"-- +goose statementend\n")},
		"000002_literals.sql": {Data: []byte("-- +goose Up\n-- DO $$ in a comment\nSELECT 1; /* comment starts\n/* nested */\nDO $$ in a block comment\n*/\nSELECT 'first line\nDO $$ in a string';\nSELECT E'first \\'\nDO $$ in an escape string';\nSELECT $text$first line\nDO $$ in a dollar string$text$;\n-- +goose Down\n")},
	}

	if _, err := Inspect(fsys); err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
}

func TestInspectRejectsInvalidCatalogEntries(t *testing.T) {
	tests := []struct {
		name    string
		fsys    fstest.MapFS
		wantErr string
	}{
		{
			name: "procedural block after standard string ending in backslash",
			fsys: fstest.MapFS{
				"000005_block.sql": {Data: []byte("-- +goose Up\nSELECT '\\'; DO $$ BEGIN PERFORM 1; END $$;\n")},
			},
			wantErr: `migration "000005_block.sql" contains procedural DO outside goose StatementBegin at line 2`,
		},
		{
			name: "comment-separated procedural block",
			fsys: fstest.MapFS{
				"000005_block.sql": {Data: []byte("-- +goose Up\nDO/* comment */ LANGUAGE plpgsql $body$\nBEGIN\nEND $body$;\n")},
			},
			wantErr: `migration "000005_block.sql" contains procedural DO outside goose StatementBegin at line 2`,
		},
		{
			name: "same-line procedural block",
			fsys: fstest.MapFS{
				"000005_block.sql": {Data: []byte("-- +goose Up\nSELECT 1; DO $$ BEGIN PERFORM 1; END $$;\n")},
			},
			wantErr: `migration "000005_block.sql" contains procedural DO outside goose StatementBegin at line 2`,
		},
		{
			name: "catalog structure before SQL content",
			fsys: fstest.MapFS{
				"000001_block.sql": {Data: []byte("-- +goose Up\nDO $$\nBEGIN\nEND $$;\n")},
				"notes.go":         {Data: []byte("package notes")},
			},
			wantErr: `invalid migration filename "notes.go"`,
		},
		{
			name: "unmarked procedural block",
			fsys: fstest.MapFS{
				"000005_block.sql": {Data: []byte("-- +goose Up\nDO $$\nBEGIN\n  PERFORM 1;\nEND $$;\n")},
			},
			wantErr: `migration "000005_block.sql" contains procedural DO outside goose StatementBegin at line 2`,
		},
		{
			name: "unmarked language procedural block",
			fsys: fstest.MapFS{
				"000005_block.sql": {Data: []byte("-- +goose Up\nDO LANGUAGE plpgsql $body$\nBEGIN\n  PERFORM 1;\nEND $body$;\n")},
			},
			wantErr: `migration "000005_block.sql" contains procedural DO outside goose StatementBegin at line 2`,
		},
		{
			name: "unmarked multiline procedural block",
			fsys: fstest.MapFS{
				"000005_block.sql": {Data: []byte("-- +goose Up\nDO\n$body$\nBEGIN\n  PERFORM 1;\nEND\n$body$;\n")},
			},
			wantErr: `migration "000005_block.sql" contains procedural DO outside goose StatementBegin at line 2`,
		},
		{
			name: "indented goose annotation",
			fsys: fstest.MapFS{
				"000005_block.sql": {Data: []byte("-- +goose Up\n  -- +goose StatementBegin\nDO $$\nBEGIN\nEND $$;\n-- +goose StatementEnd\n")},
			},
			wantErr: `migration "000005_block.sql" has invalid goose annotation at line 2`,
		},
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
