package migrationtests

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/migrationcatalog"
)

func migrationPath(name string) string {
	return filepath.Join("..", "migrations", name)
}

func TestDeepSeekProviderOptionsMigrationMatchesCurrentContract(t *testing.T) {
	const migrationName = "000055_w2_sync_deepseek_provider_options.sql"
	source, err := os.ReadFile(migrationPath(migrationName))
	if err != nil {
		t.Fatalf("read %s: %v", migrationName, err)
	}
	sql := strings.ReplaceAll(string(source), "\r\n", "\n")

	for _, want := range []string{
		`'["deepseek-v4-flash","deepseek-v4-pro"]'`,
		`WHERE code = 'deepseek'`,
		`'profile_deepseek_openai_v1'`,
		`'https://api.deepseek.com'`,
		`'["chat","passthrough"]'`,
		`'profile_deepseek_anthropic_v1'`,
		`'https://api.deepseek.com/anthropic'`,
		`'["messages","models","passthrough"]'`,
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("%s missing %q", migrationName, want)
		}
	}
	for _, legacy := range []string{"deepseek-ai-v4-flash", "deepseek-ai-v4-pro"} {
		if strings.Contains(sql, legacy) {
			t.Fatalf("%s retains removed model %q", migrationName, legacy)
		}
	}

	catalog, err := migrationcatalog.Inspect(os.DirFS(migrationPath(".")))
	if err != nil {
		t.Fatalf("inspect migration catalog: %v", err)
	}
	gotTail := catalog.Entries[len(catalog.Entries)-2:]
	wantTail := []migrationcatalog.Entry{
		{Version: 56, Name: "000056_w7_page_data_dirty_domains.sql"},
		{Version: 57, Name: "000057_w1b_account_temporary_unavailable_continuous_probe.sql"},
	}
	if !reflect.DeepEqual(gotTail, wantTail) {
		t.Fatalf("migration catalog tail = %+v, want %+v", gotTail, wantTail)
	}
}

func TestMigrationCatalogContainsOnlyUniqueVersionedSQLFiles(t *testing.T) {
	catalog, err := migrationcatalog.Inspect(os.DirFS(migrationPath(".")))
	if err != nil {
		t.Fatalf("inspect migration catalog: %v", err)
	}
	if len(catalog.Entries) == 0 {
		t.Fatal("migration catalog must not be empty")
	}
}
