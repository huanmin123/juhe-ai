package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/migrationcatalog"
)

func TestRunSchemaUpCatalogExecutesCurrentMigrationAndVerifiesVersion(t *testing.T) {
	var migratedTo int64
	var out bytes.Buffer
	err := runSchemaUpCatalog(
		t.Context(),
		filepath.Join("..", "..", "db", "migrations"),
		&out,
		func(_ context.Context, target int64) error {
			migratedTo = target
			return nil
		},
		func(context.Context) (int64, error) { return migrationcatalog.CurrentSchemaVersion, nil },
	)
	if err != nil {
		t.Fatalf("runSchemaUpCatalog() error = %v", err)
	}
	if migratedTo != migrationcatalog.CurrentSchemaVersion {
		t.Fatalf("migrated target = %d, want %d", migratedTo, migrationcatalog.CurrentSchemaVersion)
	}

	var result SchemaUpResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !result.Success || result.TargetVersion != migrationcatalog.CurrentSchemaVersion || result.CurrentVersion != migrationcatalog.CurrentSchemaVersion {
		t.Fatalf("result = %+v, want exact current schema version", result)
	}
}

func TestValidateSchemaUpSourceStateAcceptsOnlyFreshOrTrackedDatabase(t *testing.T) {
	tests := []struct {
		name    string
		state   schemaUpSourceState
		wantErr string
	}{
		{name: "fresh", state: schemaUpSourceState{}},
		{name: "tracked", state: schemaUpSourceState{gooseLedgerPresent: true, gooseLedgerRows: 71, juheRelationCount: 76}},
		{
			name:    "untracked node schema",
			state:   schemaUpSourceState{juheRelationCount: 158},
			wantErr: "without Goose history",
		},
		{
			name:    "empty ledger",
			state:   schemaUpSourceState{gooseLedgerPresent: true},
			wantErr: "without migration history",
		},
		{
			name:    "inconsistent ledger count",
			state:   schemaUpSourceState{gooseLedgerRows: 1},
			wantErr: "inconsistent",
		},
		{
			name:    "negative relation count",
			state:   schemaUpSourceState{juheRelationCount: -1},
			wantErr: "invalid count",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSchemaUpSourceState(test.state)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSchemaUpSourceState() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestRunSchemaUpCatalogRejectsUnexpectedFinalVersion(t *testing.T) {
	err := runSchemaUpCatalog(
		t.Context(),
		filepath.Join("..", "..", "db", "migrations"),
		&bytes.Buffer{},
		func(context.Context, int64) error { return nil },
		func(context.Context) (int64, error) { return migrationcatalog.CurrentSchemaVersion - 1, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "current Goose version") {
		t.Fatalf("error = %v, want exact-version rejection", err)
	}
}

func TestRunSchemaUpCatalogPreservesMigrationFailure(t *testing.T) {
	want := errors.New("synthetic migration failure")
	err := runSchemaUpCatalog(
		t.Context(),
		filepath.Join("..", "..", "db", "migrations"),
		&bytes.Buffer{},
		func(context.Context, int64) error { return want },
		func(context.Context) (int64, error) {
			t.Fatal("version read must not run")
			return 0, nil
		},
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped migration failure", err)
	}
}

func TestRunSchemaUpRejectsMissingConnectionWithoutLeakingURL(t *testing.T) {
	var out bytes.Buffer
	err := RunSchemaUp(t.Context(), "", filepath.Join("..", "..", "db", "migrations"), &out)
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("error = %v, want missing URL rejection", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output = %q, want empty", out.String())
	}
}

func TestSchemaUpSourceNeverWritesGooseLedgerDirectly(t *testing.T) {
	source, err := os.ReadFile("schemaup.go")
	if err != nil {
		t.Fatalf("read schemaup.go: %v", err)
	}
	text := strings.ToLower(string(source))
	for _, forbidden := range []string{"insert into goose_db_version", "update goose_db_version", "delete from goose_db_version"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("schemaup.go contains forbidden direct ledger mutation %q", forbidden)
		}
	}
	lockIndex := strings.Index(text, "locker.sessionlock(ctx, conn)")
	inspectIndex := strings.Index(text, "inspectschemaupsource(ctx, conn)")
	providerIndex := strings.Index(text, "goose.newprovider(")
	if lockIndex < 0 || inspectIndex < 0 || providerIndex < 0 || !(lockIndex < inspectIndex && inspectIndex < providerIndex) {
		t.Fatal("schemaup.go must lock, validate the source, then create the Goose provider")
	}
	if strings.Contains(text, "goose.uptocontext") {
		t.Fatal("schemaup.go must not use the unlocked legacy Goose UpToContext API")
	}
}
