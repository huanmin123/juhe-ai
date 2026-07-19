package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestPageDataDirtyDomainQueriesPreserveGenerationCAS(t *testing.T) {
	raw, err := os.ReadFile("queries/w7_page_data_dirty_domains.sql")
	if err != nil {
		t.Fatalf("read page-data dirty-domain queries: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"WHERE is_dirty = TRUE",
		"generation = juhe_business.page_data_dirty_domains.generation + 1",
		"WHERE domain = sqlc.arg(domain)::text",
		"AND generation = sqlc.arg(generation)::bigint",
		"AND is_dirty = TRUE",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("page-data dirty-domain queries missing %q", want)
		}
	}
}
