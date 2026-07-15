package migrationtests

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestGooseMigrationVersionsAreUnique(t *testing.T) {
	entries, err := os.ReadDir(migrationPath("."))
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	versionPattern := regexp.MustCompile(`^(\d{6})_.+\.sql$`)
	seen := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := versionPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		if previous, exists := seen[match[1]]; exists {
			t.Fatalf("duplicate Goose migration version %s: %s and %s", match[1], previous, entry.Name())
		}
		seen[match[1]] = filepath.Clean(entry.Name())
	}
}
