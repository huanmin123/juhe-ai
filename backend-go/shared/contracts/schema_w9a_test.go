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
	if BusinessSQLiteSchemaVersion != "business-sqlite-gateway-v17" {
		t.Fatalf("schema version drifted: %q", BusinessSQLiteSchemaVersion)
	}
	for _, table := range []string{
		"system_accounts", "system_sessions", "accounts", "groups",
		"vendors", "models", "account_connections", "account_endpoint_modes",
		"account_entry_mappings", "account_supported_models", "announcements",
		"account_circuit_incidents", "account_circuit_outbox",
	} {
		if _, ok := BusinessSQLiteSchema[table]; !ok {
			t.Fatalf("required table %s missing from contract", table)
		}
	}
	for _, retired := range []string{
		"providers", "provider_protocol_profiles", "provider_model_catalog",
		"account_model_mappings",
	} {
		if _, ok := BusinessSQLiteSchema[retired]; ok {
			t.Fatalf("retired table %s must not stay in contract", retired)
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

	// v16 冻结：vendor/model 目标形态（provider 面退役、连接/入口关系表、
	// 复合主键与部分唯一索引）。
	accounts := BusinessSQLiteSchema["accounts"]
	for _, retired := range []string{"provider_code", "provider_protocol_profile_id", "client_compatibility"} {
		for _, column := range accounts.Columns {
			if column == retired {
				t.Fatalf("accounts retired column %s must not stay in contract", retired)
			}
		}
	}
	for _, required := range []string{"vendor_code", "connection_driver_code", "oauth_type", "health_check_connection_id"} {
		if !containsString(accounts.Columns, required) {
			t.Fatalf("accounts column %s missing from contract", required)
		}
	}
	compositeHealthCheckFK := false
	for _, fk := range accounts.ForeignKeys {
		if strings.Join(fk.Columns, ",") == "id,health_check_connection_id" &&
			fk.RefTable == "account_connections" && strings.Join(fk.RefColumns, ",") == "account_id,id" {
			compositeHealthCheckFK = true
		}
	}
	if !compositeHealthCheckFK {
		t.Fatalf("accounts composite health-check connection foreign key drifted: %+v", accounts.ForeignKeys)
	}
	groups := BusinessSQLiteSchema["groups"]
	for _, column := range groups.Columns {
		if column == "provider_code" {
			t.Fatal("groups retired column provider_code must not stay in contract")
		}
	}
	// groups 的唯一性由 PG 目标 DDL 的两条唯一索引承载（表内无 UNIQUE 约束；
	// CheckBusinessPostgresSchema 只读 pg_constraint，纯唯一索引不产生约束行），
	// 契约必须与该方言对齐：无 UniqueConstraints，两条唯一索引都在
	// IndexDefinitions。
	if len(groups.UniqueConstraints) != 0 {
		t.Fatalf("groups uniqueness must ride the unique indexes (PG 方言对齐), got %+v", groups.UniqueConstraints)
	}
	hasOwnerNameUnique := false
	hasOwnerDefaultUnique := false
	for _, def := range groups.IndexDefinitions {
		switch def.Name {
		case "idx_groups_owner_name_unique":
			hasOwnerNameUnique = def.Unique && strings.Join(def.Columns, ",") == "system_account_id,name" && def.Predicate == ""
		case "idx_groups_owner_default_unique":
			hasOwnerDefaultUnique = def.Unique && strings.Join(def.Columns, ",") == "system_account_id" && def.Predicate != ""
		}
	}
	if !hasOwnerNameUnique {
		t.Fatalf("groups idx_groups_owner_name_unique (system_account_id, name) unique index drifted: %+v", groups.IndexDefinitions)
	}
	if !hasOwnerDefaultUnique {
		t.Fatalf("groups default partial unique index drifted: %+v", groups.IndexDefinitions)
	}
	supportedModels := BusinessSQLiteSchema["account_supported_models"]
	if strings.Join(supportedModels.PrimaryKey, ",") != "account_id,model_id" {
		t.Fatalf("account_supported_models composite primary key drifted: %+v", supportedModels.PrimaryKey)
	}
	models := BusinessSQLiteSchema["models"]
	if len(models.Columns) != 48 {
		t.Fatalf("models column count drifted: %d", len(models.Columns))
	}
	vendorFK := false
	for _, fk := range models.ForeignKeys {
		if strings.Join(fk.Columns, ",") == "vendor_code" && fk.RefTable == "vendors" && strings.Join(fk.RefColumns, ",") == "code" {
			vendorFK = true
		}
	}
	if !vendorFK {
		t.Fatalf("models vendor_code foreign key drifted: %+v", models.ForeignKeys)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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
