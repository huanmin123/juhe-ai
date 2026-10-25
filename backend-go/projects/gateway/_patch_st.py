# 一次性脚本：systemteams 迁移补丁
base = r'F:/sub2api-lite/backend-go/projects/gateway/internal/systemteams/'

# --- systemteams_test.go ---
p = base + 'systemteams_test.go'
s = open(p, encoding='utf-8').read()
old = '''func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db, err := sql.Open("sqlite", "file:teams-"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range ddl {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})'''
new = '''func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := newSystemTeamsPGDatabase(t)
	service, err := businessauth.New(db, modelcheckauth.Postgres, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})'''
assert old in s, 'testenv head missing'
s = s.replace(old, new)
s = s.replace('accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)',
              'accounts, err := authsys.NewAccountStore(db, modelcheckauth.Postgres, nil)')
s = s.replace("\tauthzStore, err := authz.NewStore(db, false, nil)", "\tauthzStore, err := authz.NewStore(db, true, nil)")
s = s.replace("\tstore, err := NewStore(db, false, nil, authzStore, WithSideEffects(stats, inval))",
              "\tstore, err := NewStore(db, true, nil, authzStore, WithSideEffects(stats, inval))")

# 删除 ddl 变量（fixture 已由 pgkit 承载）
import re
pat = re.compile(r'var ddl = \[\]string\{.*?\n\}\n\n', re.DOTALL)
s, n = pat.subn('', s, count=1)
assert n == 1, 'ddl var miss'
open(p, 'w', encoding='utf-8', newline='\n').write(s)

# --- w11f_faults_test.go ---
p = base + 'w11f_faults_test.go'
s = open(p, encoding='utf-8').read()
old = '''	base, err := sqlite.NewConnector("file:w11f-" + strings.ReplaceAll(t.Name(), "/", "-") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	script := &w11fScript{t: t}
	t.Cleanup(script.assertRulesFired)
	db := sql.OpenDB(w11fConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	for _, statement := range ddl {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service, err := businessauth.New(db, modelcheckauth.SQLite, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})'''
new = '''	testDB := pgtestkit.NewTestDatabase(t, func(ctx context.Context, handle *sql.DB) error {
		_, err := handle.ExecContext(ctx, systemTeamsPGFixtureSchema)
		return err
	})
	_ = testDB.DB.Close()
	pgConfig, err := pgx.ParseConfig(testDB.DSN + "&search_path=juhe_business")
	if err != nil {
		t.Fatalf("parse pgtestkit DSN: %v", err)
	}
	base := stdlib.GetConnector(*pgConfig)
	script := &w11fScript{t: t}
	t.Cleanup(script.assertRulesFired)
	db := sql.OpenDB(w11fConnector{base: base, script: script})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	service, err := businessauth.New(db, modelcheckauth.Postgres, time.Now, businessauth.OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true})'''
assert old in s, 'w11f env head missing'
s = s.replace(old, new)
s = s.replace('accounts, err := authsys.NewAccountStore(db, modelcheckauth.SQLite, nil)',
              'accounts, err := authsys.NewAccountStore(db, modelcheckauth.Postgres, nil)')
s = s.replace("\tauthzStore, err := authz.NewStore(db, false, nil)", "\tauthzStore, err := authz.NewStore(db, true, nil)")
s = s.replace("\tstore, err := NewStore(db, false, nil, authzStore)", "\tstore, err := NewStore(db, true, nil, authzStore)")
s = s.replace("\treturn NewStore(e.env.store.db, false, nil, e.env.authz, options...)",
              "\treturn NewStore(e.env.store.db, true, nil, e.env.authz, options...)")

# ?-bearing 规则子串 → PG bind 后形式
s = s.replace('"AND system_account_id = ? AND status = \'active\'"', '"AND system_account_id = $2 AND status = \'active\'"')
s = s.replace('"WHERE team_id = ? AND status = \'active\'"', '"WHERE team_id = $1 AND status = \'active\'"')
open(p, 'w', encoding='utf-8', newline='\n').write(s)
print('ok')
