package migrationtests

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

var migrationFilenamePattern = regexp.MustCompile(`^(\d{6})_[a-z0-9_]+\.sql$`)

func migrationPath(name string) string {
	return filepath.Join("..", "migrations", name)
}

func migrationVersion(filename string) (int64, bool) {
	matches := migrationFilenamePattern.FindStringSubmatch(filename)
	if matches == nil {
		return 0, false
	}
	version, err := strconv.ParseInt(matches[1], 10, 64)
	return version, err == nil && version > 0
}

func TestMigrationCatalogContainsOnlyUniqueVersionedSQLFiles(t *testing.T) {
	entries, err := os.ReadDir(migrationPath("."))
	if err != nil {
		t.Fatalf("read migration catalog: %v", err)
	}

	versions := make(map[int64]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("migration catalog must not contain directory %q", entry.Name())
		}

		version, valid := migrationVersion(entry.Name())
		if !valid {
			t.Fatalf("migration catalog contains non-migration file %q", entry.Name())
		}
		if previous, exists := versions[version]; exists {
			t.Fatalf("migration version %d is duplicated by %q and %q", version, previous, entry.Name())
		}
		versions[version] = entry.Name()
	}
}

func TestMigrationFilenamePatternRequiresSixDigitPositiveVersion(t *testing.T) {
	for _, filename := range []string{"49_name.sql", "000000_name.sql", "000049-name.sql", "000049_NAME.sql"} {
		if _, valid := migrationVersion(filename); valid {
			t.Fatalf("migration filename pattern unexpectedly accepted %q", filename)
		}
	}
	if version, valid := migrationVersion("000049_name.sql"); !valid || version != 49 {
		t.Fatal("migration filename pattern rejected a valid six-digit version")
	}
}
