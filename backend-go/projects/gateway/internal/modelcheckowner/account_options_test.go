package modelcheckowner

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestListAccountOptionsFiltersAndFreezesCatalog(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/business.db?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE accounts (id TEXT PRIMARY KEY,name TEXT,provider_code TEXT,provider_protocol_profile_id TEXT,protocol_code TEXT,protocol_version TEXT,status TEXT,schedulable INTEGER,deleted_at TEXT)`,
		`CREATE TABLE provider_protocol_profiles (id TEXT PRIMARY KEY,enabled INTEGER)`,
		`CREATE TABLE group_accounts (account_id TEXT,group_id TEXT,enabled INTEGER)`,
		`CREATE TABLE groups (id TEXT PRIMARY KEY,enabled INTEGER)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles VALUES ('profile_openai_openai_v1',1),('profile_unknown',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups VALUES ('g1',1)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range []string{
		`INSERT INTO accounts VALUES ('a1','Alpha','openai','profile_openai_openai_v1','openai','1','active',1,NULL)`,
		`INSERT INTO accounts VALUES ('a2','Beta','openai','profile_openai_openai_v1','openai','1','disabled',1,NULL)`,
		`INSERT INTO accounts VALUES ('a3','Hidden','openai','profile_openai_openai_v1','openai','1','active',1,'deleted')`,
	} {
		if _, err := db.Exec(row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO group_accounts VALUES ('a1','g1',1),('a2','g1',1),('a3','g1',1)`); err != nil {
		t.Fatal(err)
	}
	source, err := NewBusinessTargetSource(db, false, "secret")
	if err != nil {
		t.Fatal(err)
	}
	items, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{Purpose: "run", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "a1" || len(items[0].ModelCheckModels) == 0 {
		t.Fatalf("run options=%+v", items)
	}
	history, err := source.ListAccountOptions(context.Background(), AccountOptionsQuery{Purpose: "history", Keyword: "beta", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != "a2" {
		t.Fatalf("history options=%+v", history)
	}
	options := source.ModelCheckOptions()
	if options.DefaultModel != "gpt-5.6-sol" || options.DefaultProfile != "quick" || len(options.SupportedModels) == 0 {
		t.Fatalf("catalog=%+v", options)
	}
}
