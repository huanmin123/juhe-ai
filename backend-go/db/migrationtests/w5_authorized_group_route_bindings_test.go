package migrationtests

import (
	"os"
	"strings"
	"testing"
)

func TestW5AuthorizedGroupRouteBindingsMigrationMatchesCurrentSchema(t *testing.T) {
	source, err := os.ReadFile(migrationPath("000032_w5_authorized_group_route_bindings.sql"))
	if err != nil {
		t.Fatalf("read W5 authorized group route bindings migration: %v", err)
	}
	sql := string(source)
	up, down, found := strings.Cut(sql, "-- +goose Down")
	if !found {
		t.Fatal("migration is missing goose Down marker")
	}

	for _, required := range []string{
		"ALTER TABLE juhe_business.route_strategy_groups",
		"DROP CONSTRAINT IF EXISTS route_strategy_groups_group_id_system_account_id_fkey",
		"DROP CONSTRAINT IF EXISTS route_strategy_groups_group_id_fkey",
		"DROP CONSTRAINT IF EXISTS fk_route_strategy_groups_group",
		"ADD CONSTRAINT fk_route_strategy_groups_group",
		"FOREIGN KEY (group_id)",
		"REFERENCES juhe_business.groups(id)",
		"ON DELETE CASCADE",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("migration Up section missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"route_strategy_groups_route_strategy_id_system_account_id_fkey",
		"FOREIGN KEY (group_id, system_account_id)",
		"DELETE FROM",
		"TRUNCATE",
		"DROP TABLE",
		"DROP COLUMN",
	} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("migration Up section should not contain %q", forbidden)
		}
	}
	if !strings.Contains(down, "-- no-op:") {
		t.Fatal("migration Down section must remain a no-op")
	}
}

func TestW5AuthorizedGroupRouteBindingsKeepsRouteStrategyOwnerForeignKey(t *testing.T) {
	source, err := os.ReadFile(migrationPath("000004_w1b_public_groups.sql"))
	if err != nil {
		t.Fatalf("read W1b public groups migration: %v", err)
	}
	sql := string(source)
	tableStart := strings.Index(sql, "CREATE TABLE IF NOT EXISTS juhe_business.route_strategy_groups (")
	if tableStart < 0 {
		t.Fatal("route_strategy_groups table definition not found")
	}
	tableSQL := sql[tableStart:]
	tableEnd := strings.Index(tableSQL, "\n);")
	if tableEnd < 0 {
		t.Fatal("route_strategy_groups table definition end not found")
	}
	tableSQL = tableSQL[:tableEnd]
	for _, required := range []string{
		"FOREIGN KEY (route_strategy_id, system_account_id)",
		"REFERENCES juhe_business.route_strategies(id, system_account_id) ON DELETE CASCADE",
	} {
		if !strings.Contains(tableSQL, required) {
			t.Fatalf("route_strategy_groups table definition missing %q", required)
		}
	}
}
