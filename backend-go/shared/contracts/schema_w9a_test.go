package contracts

import (
	"strings"
	"testing"
)

// TestW9ABusinessSQLiteSchemaContract freezes structural invariants of the
// Business SQLite schema contract: every table is populated, key and index
// references stay inside declared columns/tables and foreign keys point at
// existing tables.
func TestW9ABusinessSQLiteSchemaContract(t *testing.T) {
	if BusinessSQLiteSchemaVersion == "" {
		t.Fatal("BusinessSQLiteSchemaVersion must not be empty")
	}
	if len(BusinessSQLiteSchema) == 0 {
		t.Fatal("BusinessSQLiteSchema must not be empty")
	}
	for table, spec := range BusinessSQLiteSchema {
		if len(spec.Columns) == 0 {
			t.Fatalf("table %s has no columns", table)
		}
		seen := map[string]bool{}
		for _, col := range spec.Columns {
			if col == "" || seen[col] {
				t.Fatalf("table %s has empty or duplicate column %q", table, col)
			}
			seen[col] = true
		}
		for _, pk := range spec.PrimaryKey {
			if !seen[pk] {
				t.Fatalf("table %s primary key column %q not declared", table, pk)
			}
		}
		for _, unique := range spec.UniqueConstraints {
			for _, col := range unique {
				if !seen[col] {
					t.Fatalf("table %s unique constraint references undeclared column %q", table, col)
				}
			}
		}
		indexNames := map[string]bool{}
		for _, idx := range spec.Indexes {
			if idx == "" {
				t.Fatalf("table %s has empty index name", table)
			}
			indexNames[idx] = true
		}
		for _, def := range spec.IndexDefinitions {
			if !indexNames[def.Name] {
				t.Fatalf("table %s index definition %q not listed in Indexes", table, def.Name)
			}
			if len(def.Columns) == 0 {
				t.Fatalf("table %s index definition %q has no columns", table, def.Name)
			}
		}
		for _, fk := range spec.ForeignKeys {
			if len(fk.Columns) == 0 || fk.RefTable == "" || len(fk.RefColumns) == 0 {
				t.Fatalf("table %s has incomplete foreign key %+v", table, fk)
			}
			if _, ok := BusinessSQLiteSchema[fk.RefTable]; !ok {
				t.Fatalf("table %s foreign key references unknown table %q", table, fk.RefTable)
			}
			for _, col := range fk.Columns {
				if !seen[col] {
					t.Fatalf("table %s foreign key references undeclared column %q", table, col)
				}
			}
		}
	}
}

// TestW9ABusinessSQLiteSchemaFrozenEntries freezes the contract version and a
// set of load-bearing table entries that Gateway owners depend on.
func TestW9ABusinessSQLiteSchemaFrozenEntries(t *testing.T) {
	if BusinessSQLiteSchemaVersion != "business-sqlite-gateway-v12" {
		t.Fatalf("schema version drifted: %q", BusinessSQLiteSchemaVersion)
	}
	for _, table := range []string{
		"system_accounts", "system_sessions", "accounts", "groups",
		"providers", "provider_protocol_profiles", "announcements",
		"account_circuit_incidents", "account_circuit_outbox",
	} {
		if _, ok := BusinessSQLiteSchema[table]; !ok {
			t.Fatalf("required table %s missing from contract", table)
		}
	}
	systemAccounts := BusinessSQLiteSchema["system_accounts"]
	if len(systemAccounts.UniqueConstraints) != 1 || systemAccounts.UniqueConstraints[0][0] != "username" {
		t.Fatalf("system_accounts username unique constraint drifted: %+v", systemAccounts.UniqueConstraints)
	}
	incidents := BusinessSQLiteSchema["account_circuit_incidents"]
	if len(incidents.IndexDefinitions) != 1 || !incidents.IndexDefinitions[0].Unique {
		t.Fatalf("account_circuit_incidents index definition drifted: %+v", incidents.IndexDefinitions)
	}
	if incidents.IndexDefinitions[0].Predicate == "" {
		t.Fatal("account_circuit_incidents partial index must keep its predicate")
	}
}

// TestW9AJ3AProxyLatencySchemaContract verifies structural coherence of the
// J3A proxy latency Postgres contract.
func TestW9AJ3AProxyLatencySchemaContract(t *testing.T) {
	tableSet := map[string]bool{}
	for _, table := range J3AProxyLatencyTables {
		if table == "" || tableSet[table] {
			t.Fatalf("J3A table empty or duplicated: %q", table)
		}
		tableSet[table] = true
	}
	for name, def := range J3AProxyLatencyIndexes {
		if name == "" || def == "" {
			t.Fatalf("J3A index has empty name or definition: %q -> %q", name, def)
		}
	}
	for table, columns := range J3AProxyLatencyColumns {
		if !tableSet[table] {
			t.Fatalf("J3A columns reference unknown table %q", table)
		}
		if len(columns) == 0 {
			t.Fatalf("J3A table %s has no column specs", table)
		}
		for col, spec := range columns {
			if col == "" || spec.DataType == "" || spec.UdtName == "" {
				t.Fatalf("J3A table %s column %q has empty spec: %+v", table, col, spec)
			}
		}
	}
	for table, constraints := range J3AProxyLatencyConstraints {
		if !tableSet[table] {
			t.Fatalf("J3A constraints reference unknown table %q", table)
		}
		if len(constraints) == 0 {
			t.Fatalf("J3A table %s has empty constraint list", table)
		}
		for _, c := range constraints {
			if !strings.HasPrefix(c, "primary key (") && !strings.HasPrefix(c, "unique (") {
				t.Fatalf("J3A table %s unexpected constraint %q", table, c)
			}
		}
	}
	for _, table := range J3AProxyLatencyTables {
		if _, ok := J3AProxyLatencyColumns[table]; !ok {
			t.Fatalf("J3A table %s missing column specs", table)
		}
		if _, ok := J3AProxyLatencyConstraints[table]; !ok {
			t.Fatalf("J3A table %s missing constraints", table)
		}
	}
}

// TestW9AJ3BModelCheckSchemaContract verifies structural coherence of the J3B
// model check Postgres contract.
func TestW9AJ3BModelCheckSchemaContract(t *testing.T) {
	tableSet := map[string]bool{}
	for _, table := range J3BModelCheckTables {
		if table == "" || tableSet[table] {
			t.Fatalf("J3B table empty or duplicated: %q", table)
		}
		tableSet[table] = true
	}
	for name, def := range J3BModelCheckIndexes {
		if !strings.HasPrefix(def, "on juhe_j3b.") || !strings.Contains(def, " using btree (") {
			t.Fatalf("J3B index %s has unexpected definition %q", name, def)
		}
	}
	for table, columns := range J3BModelCheckColumns {
		if !tableSet[table] {
			t.Fatalf("J3B columns reference unknown table %q", table)
		}
		if len(columns) == 0 {
			t.Fatalf("J3B table %s has no column specs", table)
		}
		for col, spec := range columns {
			if col == "" || spec.DataType == "" || spec.UdtName == "" {
				t.Fatalf("J3B table %s column %q has empty spec: %+v", table, col, spec)
			}
		}
	}
	for table, constraints := range J3BModelCheckConstraints {
		if !tableSet[table] {
			t.Fatalf("J3B constraints reference unknown table %q", table)
		}
		if len(constraints) == 0 {
			t.Fatalf("J3B table %s has empty constraint list", table)
		}
		for _, c := range constraints {
			if !strings.HasPrefix(c, "primary key (") && !strings.HasPrefix(c, "unique (") {
				t.Fatalf("J3B table %s unexpected constraint %q", table, c)
			}
		}
	}
	for _, table := range J3BModelCheckTables {
		if _, ok := J3BModelCheckColumns[table]; !ok {
			t.Fatalf("J3B table %s missing column specs", table)
		}
		if _, ok := J3BModelCheckConstraints[table]; !ok {
			t.Fatalf("J3B table %s missing constraints", table)
		}
	}
}
