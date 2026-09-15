package operationlogappend

// w7c contract tests: drive AppendPostgres through a scripted database/sql
// driver so every transaction arm (configure, insert, children, commit,
// rollback) is exercised without a live PostgreSQL. The SQL text itself is
// opaque to the stub, so assertions target the executed statement sequence
// and the scripted results/behavior of database/sql.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// scripted driver
// ---------------------------------------------------------------------------

// w7cExecStep scripts one ExecContext call: the RowsAffected it reports and
// the error it returns (nil = success).
type w7cExecStep struct {
	rows int64
	err  error
}

type w7cScript struct {
	mu sync.Mutex

	steps []w7cExecStep

	executed   []string
	argsByExec [][]string

	beginErr  error
	commitErr error

	committed  bool
	rolledBack bool
}

func (s *w7cScript) record(query string, args []driver.NamedValue) (driver.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executed = append(s.executed, query)
	stringified := make([]string, 0, len(args))
	for _, arg := range args {
		stringified = append(stringified, w7cValueString(arg.Value))
	}
	s.argsByExec = append(s.argsByExec, stringified)
	if len(s.steps) == 0 {
		return w7cResult{}, errors.New("w7c scripted driver: unexpected exec: " + query)
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	return w7cResult{rows: step.rows}, step.err
}

func (s *w7cScript) executedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.executed)
}

func (s *w7cScript) executedAt(index int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < len(s.executed) {
		return s.executed[index]
	}
	return ""
}

func (s *w7cScript) argsAt(index int) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < len(s.argsByExec) {
		return s.argsByExec[index]
	}
	return nil
}

func (s *w7cScript) executedAll() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.executed...)
}

func (s *w7cScript) snapshot() (committed, rolledBack bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.committed, s.rolledBack
}

func w7cValueString(value driver.Value) string {
	switch typed := value.(type) {
	case nil:
		return "<nil>"
	case []driver.Value:
		inner := make([]string, 0, len(typed))
		for _, item := range typed {
			inner = append(inner, w7cValueString(item))
		}
		return "[" + strings.Join(inner, ",") + "]"
	case []string:
		return "[" + strings.Join(typed, ",") + "]"
	case []byte:
		return string(typed)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

var (
	w7cScriptsMu sync.Mutex
	w7cScripts   = map[string]*w7cScript{}
)

func init() {
	sql.Register("w7c-oplog-scripted", w7cScriptedDriver{})
}

type w7cScriptedDriver struct{}

func (w7cScriptedDriver) Open(dsn string) (driver.Conn, error) {
	w7cScriptsMu.Lock()
	script, ok := w7cScripts[dsn]
	w7cScriptsMu.Unlock()
	if !ok {
		return nil, errors.New("w7c scripted driver: unknown dsn: " + dsn)
	}
	return &w7cScriptedConn{script: script}, nil
}

type w7cScriptedConn struct {
	script *w7cScript
}

func (c *w7cScriptedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("w7c scripted driver: unexpected prepare")
}

func (c *w7cScriptedConn) Close() error { return nil }

func (c *w7cScriptedConn) Begin() (driver.Tx, error) {
	return nil, errors.New("w7c scripted driver: unexpected legacy begin")
}

func (c *w7cScriptedConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.script.mu.Lock()
	beginErr := c.script.beginErr
	c.script.mu.Unlock()
	if beginErr != nil {
		return nil, beginErr
	}
	return &w7cScriptedTx{script: c.script}, nil
}

func (c *w7cScriptedConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.script.record(query, args)
}

// CheckNamedValue accepts every producer argument verbatim (including
// []string arrays passed to the unnest insert), mirroring how pgx handles
// PostgreSQL array parameters without default converter restrictions.
func (c *w7cScriptedConn) CheckNamedValue(*driver.NamedValue) error { return nil }

type w7cScriptedTx struct {
	script *w7cScript
}

func (t *w7cScriptedTx) Commit() error {
	t.script.mu.Lock()
	defer t.script.mu.Unlock()
	t.script.committed = true
	return t.script.commitErr
}

func (t *w7cScriptedTx) Rollback() error {
	t.script.mu.Lock()
	defer t.script.mu.Unlock()
	t.script.rolledBack = true
	return nil
}

type w7cResult struct {
	rows int64
}

func (r w7cResult) LastInsertId() (int64, error) { return 0, nil }
func (r w7cResult) RowsAffected() (int64, error) { return r.rows, nil }

func w7cNewScriptDB(t *testing.T, script *w7cScript) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("w7c-script-%p", script)
	w7cScriptsMu.Lock()
	w7cScripts[dsn] = script
	w7cScriptsMu.Unlock()
	t.Cleanup(func() {
		w7cScriptsMu.Lock()
		delete(w7cScripts, dsn)
		w7cScriptsMu.Unlock()
	})
	db, err := sql.Open("w7c-oplog-scripted", dsn)
	if err != nil {
		t.Fatal(err)
	}
	// AppendPostgres runs on a single transaction, so one connection is enough
	// and keeps the scripted step order deterministic.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// AppendPostgres contract arms
// ---------------------------------------------------------------------------

func w7cValidInput() Input {
	createdAt := time.Date(2026, time.September, 14, 8, 30, 0, 0, time.FixedZone("CST", 8*3600))
	return Input{
		ID:                            "oplog_w7c_full",
		TraceID:                       "trace-w7c",
		ActorSystemAccountID:          "actor-1",
		ActorUsername:                 "actor",
		ActorRole:                     "admin",
		OperationScopeSystemAccountID: "owner-9",
		Module:                        "proxies",
		Action:                        "update",
		OperationKey:                  "proxies.update",
		ResourceType:                  "proxy",
		ResourceID:                    "proxy-7",
		ResourceName:                  "东京节点",
		Summary:                       "更新代理：东京节点 proxy-7",
		Metadata:                      jsonRaw(`{"reason":"w7c"}`),
		Method:                        "POST",
		Path:                          "/api/proxies/proxy-7",
		ClientIP:                      "192.0.2.10",
		UserAgent:                     "w7c-agent/1.0",
		CreatedAt:                     createdAt,
	}
}

func jsonRaw(value string) jsonRawMessage { return jsonRawMessage(value) }

// jsonRawMessage aliases json.RawMessage so the helper above stays short.
type jsonRawMessage = []byte

func w7cFullScript() *w7cScript {
	// 3x SET LOCAL, insert log (affected), 1 target, 2 viewers (one exec per
	// viewer), 1 search terms batch.
	return &w7cScript{steps: []w7cExecStep{
		{}, {}, {},
		{rows: 1},
		{rows: 1},
		{rows: 1},
		{rows: 1},
		{rows: 12},
	}}
}

func TestW7CAppendPostgresFullContract(t *testing.T) {
	script := w7cFullScript()
	db := w7cNewScriptDB(t, script)

	err := AppendPostgres(context.Background(), db, w7cValidInput())
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	committed, rolledBack := script.snapshot()
	if !committed || rolledBack {
		t.Fatalf("commit=%v rollback=%v", committed, rolledBack)
	}
	if got := script.executedCount(); got != 8 {
		t.Fatalf("executed %d statements: %#v", got, script.executedAll())
	}
	for index, want := range []string{
		"SET LOCAL statement_timeout",
		"SET LOCAL lock_timeout",
		"SET LOCAL idle_in_transaction_session_timeout",
		"INSERT INTO juhe_dataset.operation_logs",
		"INSERT INTO juhe_dataset.operation_log_targets",
		"INSERT INTO juhe_dataset.operation_log_viewers",
		"INSERT INTO juhe_dataset.operation_log_viewers",
		"INSERT INTO juhe_dataset.operation_log_summary_search_terms",
	} {
		if !strings.Contains(script.executedAt(index), want) {
			t.Fatalf("statement %d = %q, want contains %q", index, script.executedAt(index), want)
		}
	}
	// Primary target child carries the deterministic child id and the
	// auto-derived "primary" relation.
	targetArgs := script.argsAt(4)
	joined := strings.Join(targetArgs, "|")
	if !strings.Contains(joined, "optgt_oplog_w7c_full_0") || !strings.Contains(joined, "primary") {
		t.Fatalf("target args = %#v", targetArgs)
	}
	viewerArgs := strings.Join(script.argsAt(5), "|") + "|" + strings.Join(script.argsAt(6), "|")
	if !strings.Contains(viewerArgs, "actor_self") || !strings.Contains(viewerArgs, "admin_managed_my_resource") {
		t.Fatalf("viewer args = %#v / %#v", script.argsAt(5), script.argsAt(6))
	}
	termArgs := script.argsAt(7)
	if len(termArgs) != 3 {
		t.Fatalf("search term arg count = %d (%#v)", len(termArgs), termArgs)
	}
	if !strings.Contains(termArgs[1], "更新代理") {
		t.Fatalf("search term array missing summary n-grams: %s", termArgs[1])
	}
}

func TestW7CAppendPostgresIdempotentRetrySkipsChildren(t *testing.T) {
	script := &w7cScript{steps: []w7cExecStep{{}, {}, {}, {rows: 0}}}
	db := w7cNewScriptDB(t, script)

	err := AppendPostgres(context.Background(), db, w7cValidInput())
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	committed, rolledBack := script.snapshot()
	if !committed {
		t.Fatal("identical retry must commit")
	}
	if rolledBack {
		t.Fatal("retry must not report a child-row rollback")
	}
	if got := script.executedCount(); got != 4 {
		t.Fatalf("retry executed %d statements, want only 3 configure + 1 log insert", got)
	}
	for index := 4; index < 7; index++ {
		if exec := script.executedAt(index); exec != "" && strings.Contains(exec, "operation_log_") {
			t.Fatalf("retry unexpectedly wrote child row: %s", exec)
		}
	}
}

func TestW7CAppendPostgresNilDB(t *testing.T) {
	err := AppendPostgres(context.Background(), nil, w7cValidInput())
	if err == nil || err.Error() != "operation log PostgreSQL database is required" {
		t.Fatalf("nil db error = %v", err)
	}
}

func TestW7CAppendPostgresNormalizeFailureBeforeAnySQL(t *testing.T) {
	script := &w7cScript{}
	db := w7cNewScriptDB(t, script)

	input := w7cValidInput()
	input.ID = "   "
	err := AppendPostgres(context.Background(), db, input)
	if err == nil || !strings.Contains(err.Error(), "missing id") {
		t.Fatalf("normalize error = %v", err)
	}
	if script.executedCount() != 0 {
		t.Fatalf("no SQL may run for invalid input, got %#v", script.executedAll())
	}
}

func TestW7CAppendPostgresBeginTxError(t *testing.T) {
	script := &w7cScript{beginErr: errors.New("pool exhausted")}
	db := w7cNewScriptDB(t, script)

	err := AppendPostgres(context.Background(), db, w7cValidInput())
	if err == nil || !strings.Contains(err.Error(), "begin operation log append transaction: pool exhausted") {
		t.Fatalf("begin error = %v", err)
	}
	if script.committed || script.rolledBack {
		t.Fatal("failed begin must not commit or rollback")
	}
}

func TestW7CAppendPostgresConfigureError(t *testing.T) {
	script := &w7cScript{steps: []w7cExecStep{{}, {err: errors.New("lock_timeout denied")}}}
	db := w7cNewScriptDB(t, script)

	err := AppendPostgres(context.Background(), db, w7cValidInput())
	if err == nil || !strings.Contains(err.Error(), "configure operation log append transaction: lock_timeout denied") {
		t.Fatalf("configure error = %v", err)
	}
	if !script.rolledBack || script.committed {
		t.Fatal("configure failure must rollback")
	}
}

func TestW7CAppendPostgresChangesMarshalError(t *testing.T) {
	script := &w7cScript{steps: []w7cExecStep{{}, {}, {}}}
	db := w7cNewScriptDB(t, script)

	input := w7cValidInput()
	input.Changes = []Change{{Field: "f", Label: "l", Before: make(chan int)}}
	err := AppendPostgres(context.Background(), db, input)
	if err == nil || !strings.Contains(err.Error(), "encode operation log changes") {
		t.Fatalf("marshal error = %v", err)
	}
	if got := script.executedCount(); got != 3 {
		t.Fatalf("marshal failure happens after the 3 configure statements, got %d", got)
	}
	if !script.rolledBack || script.committed {
		t.Fatal("marshal failure must rollback")
	}
}

func TestW7CAppendPostgresInsertLogError(t *testing.T) {
	script := &w7cScript{steps: []w7cExecStep{{}, {}, {}, {err: errors.New("relation does not exist")}}}
	db := w7cNewScriptDB(t, script)

	err := AppendPostgres(context.Background(), db, w7cValidInput())
	if err == nil || !strings.Contains(err.Error(), "insert operation log: relation does not exist") {
		t.Fatalf("insert log error = %v", err)
	}
	if !script.rolledBack || script.committed {
		t.Fatal("insert failure must rollback")
	}
}

func TestW7CAppendPostgresInsertTargetError(t *testing.T) {
	script := &w7cScript{steps: []w7cExecStep{{}, {}, {}, {rows: 1}, {err: errors.New("target fk violated")}}}
	db := w7cNewScriptDB(t, script)

	err := AppendPostgres(context.Background(), db, w7cValidInput())
	if err == nil || !strings.Contains(err.Error(), "insert operation log target: target fk violated") {
		t.Fatalf("target error = %v", err)
	}
	if !script.rolledBack || script.committed {
		t.Fatal("target failure must rollback")
	}
}

func TestW7CAppendPostgresInsertViewerError(t *testing.T) {
	script := &w7cScript{steps: []w7cExecStep{{}, {}, {}, {rows: 1}, {rows: 1}, {err: errors.New("viewer fk violated")}}}
	db := w7cNewScriptDB(t, script)

	err := AppendPostgres(context.Background(), db, w7cValidInput())
	if err == nil || !strings.Contains(err.Error(), "insert operation log viewer: viewer fk violated") {
		t.Fatalf("viewer error = %v", err)
	}
	if !script.rolledBack || script.committed {
		t.Fatal("viewer failure must rollback")
	}
}

func TestW7CAppendPostgresInsertTermsError(t *testing.T) {
	script := &w7cScript{steps: []w7cExecStep{
		{}, {}, {}, {rows: 1}, {rows: 1}, {rows: 1}, {rows: 1}, {err: errors.New("unnest failed")},
	}}
	db := w7cNewScriptDB(t, script)

	err := AppendPostgres(context.Background(), db, w7cValidInput())
	if err == nil || !strings.Contains(err.Error(), "insert operation log search terms: unnest failed") {
		t.Fatalf("terms error = %v", err)
	}
	if !script.rolledBack || script.committed {
		t.Fatal("terms failure must rollback")
	}
}

func TestW7CAppendPostgresCommitError(t *testing.T) {
	script := &w7cScript{
		steps:     []w7cExecStep{{}, {}, {}, {rows: 1}, {rows: 1}, {rows: 1}, {rows: 1}, {rows: 12}},
		commitErr: errors.New("serialization failure"),
	}
	db := w7cNewScriptDB(t, script)

	err := AppendPostgres(context.Background(), db, w7cValidInput())
	if err == nil || !strings.Contains(err.Error(), "commit operation log append: serialization failure") {
		t.Fatalf("commit error = %v", err)
	}
}

func TestW7CAppendPostgresNoSummaryStillCommits(t *testing.T) {
	// Summary is normalized out only for required-field validation when it is
	// whitespace; a minimal summary produces a single full-width term set.
	input := w7cValidInput()
	input.Summary = "更新代理"
	script := &w7cScript{steps: []w7cExecStep{
		{}, {}, {}, {rows: 1}, {rows: 1}, {rows: 1}, {rows: 1}, {rows: 3},
	}}
	db := w7cNewScriptDB(t, script)

	if err := AppendPostgres(context.Background(), db, input); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := script.executedCount(); got != 8 {
		t.Fatalf("executed %d statements, want 8 (terms still inserted)", got)
	}
	if committed, _ := script.snapshot(); !committed {
		t.Fatal("commit expected")
	}
}

// ---------------------------------------------------------------------------
// normalize branch matrix
// ---------------------------------------------------------------------------

func TestW7CNormalizeRequiredFieldErrors(t *testing.T) {
	base := w7cValidInput()
	cases := []struct {
		name   string
		mutate func(*Input)
		want   string
	}{
		{"id", func(i *Input) { i.ID = "" }, "missing id"},
		{"id whitespace", func(i *Input) { i.ID = " \t" }, "missing id"},
		{"actor", func(i *Input) { i.ActorSystemAccountID = "" }, "missing actorSystemAccountId"},
		{"role", func(i *Input) { i.ActorRole = "" }, "missing actorRole"},
		{"module", func(i *Input) { i.Module = "" }, "missing module"},
		{"action", func(i *Input) { i.Action = "" }, "missing action"},
		{"operationKey", func(i *Input) { i.OperationKey = "" }, "missing operationKey"},
		{"resourceType", func(i *Input) { i.ResourceType = "" }, "missing resourceType"},
		{"summary", func(i *Input) { i.Summary = "  " }, "missing summary"},
		{"createdAt", func(i *Input) { i.CreatedAt = time.Time{} }, "missing createdAt"},
		{"mode", func(i *Input) { i.Mode = "bulk" }, "enum value invalid"},
		{"detailLevel", func(i *Input) { i.DetailLevel = "raw" }, "enum value invalid"},
		{"visibilityScope", func(i *Input) { i.VisibilityScope = "everyone" }, "enum value invalid"},
		{"metadata", func(i *Input) { i.Metadata = jsonRaw("{broken") }, "metadata is not valid JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			tc.mutate(&input)
			if _, err := normalize(input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("normalize error = %v, want contains %q", err, tc.want)
			}
		})
	}
}

func TestW7CNormalizeDefaults(t *testing.T) {
	input := w7cValidInput()
	input.Mode = ""
	input.DetailLevel = ""
	input.VisibilityScope = ""
	input.Metadata = nil
	input.Changes = nil

	normalized, err := normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Mode != "self" || normalized.DetailLevel != "full" || normalized.VisibilityScope != "targeted" {
		t.Fatalf("enum defaults = %q/%q/%q", normalized.Mode, normalized.DetailLevel, normalized.VisibilityScope)
	}
	if string(normalized.Metadata) != "{}" {
		t.Fatalf("metadata default = %s", normalized.Metadata)
	}
	if normalized.Changes == nil || len(normalized.Changes) != 0 {
		t.Fatalf("changes default = %#v", normalized.Changes)
	}
}

func TestW7CNormalizeTargetedScopeViewerSynthesis(t *testing.T) {
	input := w7cValidInput() // actor-1 admin on owner-9's resource, targeted scope
	normalized, err := normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Viewers) != 2 {
		t.Fatalf("viewers = %#v", normalized.Viewers)
	}
	if normalized.Viewers[0].SystemAccountID != "actor-1" || normalized.Viewers[0].VisibilityReason != "actor_self" {
		t.Fatalf("actor viewer = %#v", normalized.Viewers[0])
	}
	if normalized.Viewers[1].SystemAccountID != "owner-9" || normalized.Viewers[1].VisibilityReason != "admin_managed_my_resource" {
		t.Fatalf("owner viewer = %#v", normalized.Viewers[1])
	}
	if normalized.Viewers[1].DetailLevel != "full" {
		t.Fatalf("viewer detail default = %#v", normalized.Viewers[1])
	}

	// Non-admin actor produces the plain resource_owner reason.
	input.ActorRole = "member"
	normalized, err = normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Viewers[1].VisibilityReason != "resource_owner" {
		t.Fatalf("member owner reason = %#v", normalized.Viewers[1])
	}

	// Self-owned resource must not duplicate the actor as owner viewer.
	input.OperationScopeSystemAccountID = "actor-1"
	normalized, err = normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Viewers) != 1 {
		t.Fatalf("self-owned viewers = %#v", normalized.Viewers)
	}
}

func TestW7CNormalizeExplicitViewersDedupAndValidate(t *testing.T) {
	// Explicit viewers survive normalization only for targeted scope; the
	// actor viewer is appended after them and dedup runs on the full set.
	input := w7cValidInput()
	input.Viewers = []Viewer{
		{SystemAccountID: " user-1 ", VisibilityReason: "team_member", DetailLevel: ""},   // trimmed, detail defaulted
		{SystemAccountID: "user-1", VisibilityReason: "team_member", DetailLevel: "full"}, // exact duplicate after default
		{SystemAccountID: "user-2", VisibilityReason: "team_member", DetailLevel: "full"},
		{SystemAccountID: " ", VisibilityReason: "team_member", DetailLevel: "full"}, // blank: skipped
	}
	normalized, err := normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Viewers) != 4 {
		t.Fatalf("viewers = %#v", normalized.Viewers)
	}
	if normalized.Viewers[0].SystemAccountID != "user-1" || normalized.Viewers[0].DetailLevel != "full" {
		t.Fatalf("first viewer = %#v", normalized.Viewers[0])
	}
	if normalized.Viewers[1].SystemAccountID != "user-2" {
		t.Fatalf("second viewer = %#v", normalized.Viewers[1])
	}

	// Targeted scope keeps explicit viewers ahead of the synthesized ones.
	normalized, err = normalize(w7cValidInput())
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Viewers[0].VisibilityReason != "actor_self" {
		t.Fatalf("synthesized viewer order = %#v", normalized.Viewers)
	}

	badReason := w7cValidInput()
	badReason.Viewers = []Viewer{{SystemAccountID: "u", VisibilityReason: "sneaky", DetailLevel: "full"}}
	if _, err := normalize(badReason); err == nil || !strings.Contains(err.Error(), "viewer is invalid") {
		t.Fatalf("invalid viewer error = %v", err)
	}

	badDetail := w7cValidInput()
	badDetail.Viewers = []Viewer{{SystemAccountID: "u", VisibilityReason: "actor_self", DetailLevel: "raw"}}
	if _, err := normalize(badDetail); err == nil || !strings.Contains(err.Error(), "viewer is invalid") {
		t.Fatalf("invalid viewer detail error = %v", err)
	}
}

func TestW7CNormalizeExplicitViewersWipedForNonTargeted(t *testing.T) {
	input := w7cValidInput()
	input.VisibilityScope = "admin_only"
	input.Viewers = []Viewer{{SystemAccountID: "peer-3", VisibilityReason: "team_member", DetailLevel: "summary"}}
	normalized, err := normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Viewers == nil || len(normalized.Viewers) != 0 {
		t.Fatalf("admin_only viewers = %#v", normalized.Viewers)
	}
}

func TestW7CNormalizeTargetValidation(t *testing.T) {
	base := w7cValidInput()

	// Explicit targets without a primary AND without resource fields stay
	// untouched apart from the relation default.
	input := base
	input.ResourceID = ""
	input.ResourceName = ""
	input.Targets = []Target{{TargetType: "credential", TargetID: "cred-1"}}
	normalized, err := normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Targets) != 1 || normalized.Targets[0].Relation != "affected" {
		t.Fatalf("targets = %#v", normalized.Targets)
	}

	// Explicit non-primary targets plus resource fields still gain the
	// auto-derived primary target for the acted-on resource.
	input.ResourceID = "proxy-7"
	input.ResourceName = "东京节点"
	input.Targets = []Target{{TargetType: "credential", TargetID: "cred-1"}}
	normalized, err = normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Targets) != 2 || normalized.Targets[1].Relation != "primary" {
		t.Fatalf("targets with resource fields = %#v", normalized.Targets)
	}

	// Missing target type is rejected.
	input.Targets = []Target{{TargetType: " "}}
	if _, err := normalize(input); err == nil || !strings.Contains(err.Error(), "target type is required") {
		t.Fatalf("target type error = %v", err)
	}

	// Invalid relation is rejected.
	input.Targets = []Target{{TargetType: "credential", Relation: "best_friend"}}
	if _, err := normalize(input); err == nil || !strings.Contains(err.Error(), "target relation is invalid") {
		t.Fatalf("target relation error = %v", err)
	}

	// Every allowed relation passes oneOf.
	for _, relation := range []string{"primary", "affected", "created", "deleted", "owner", "grantee", "team_member", "bound_resource"} {
		input.Targets = []Target{{TargetType: "credential", Relation: relation}}
		if _, err := normalize(input); err != nil {
			t.Fatalf("relation %q rejected: %v", relation, err)
		}
	}

	// No targets and no resource fields: no auto target is appended.
	input = base
	input.ResourceID = ""
	input.ResourceName = ""
	input.Targets = nil
	normalized, err = normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Targets) != 0 {
		t.Fatalf("unexpected targets = %#v", normalized.Targets)
	}
}

// ---------------------------------------------------------------------------
// searchTerms and normalizeSearchText
// ---------------------------------------------------------------------------

func TestW7CSearchTermsEmptyInputs(t *testing.T) {
	for _, value := range []string{"", "   ", "\n\t", "!!？？——", "、。！"} {
		if terms := searchTerms(value); terms != nil {
			t.Fatalf("searchTerms(%q) = %#v, want nil", value, terms)
		}
	}
}

func TestW7CSearchTermsCapAtMaxSearchTerms(t *testing.T) {
	// A pseudo-random 200-rune CJK word produces far more distinct n-grams
	// than the 1500-term budget, so the generator must stop exactly at the
	// cap (a constant-rune string would deduplicate and never reach it).
	seed := uint32(20260914)
	runes := make([]rune, 200)
	for index := range runes {
		seed = seed*1664525 + 1013904223
		runes[index] = rune(0x4E00 + seed%20000)
	}
	longWord := string(runes)
	terms := searchTerms(longWord)
	if len(terms) != maxSearchTerms {
		t.Fatalf("terms = %d, want capped at %d", len(terms), maxSearchTerms)
	}
	for index := 1; index < len(terms); index++ {
		if terms[index-1] >= terms[index] {
			t.Fatalf("terms not sorted/deduped at %d: %q >= %q", index, terms[index-1], terms[index])
		}
	}
	// The whole 200-rune word is over the 128-rune per-term limit and must
	// not appear verbatim.
	for _, term := range terms {
		if len([]rune(term)) > 128 {
			t.Fatalf("term over 128 runes: %d", len([]rune(term)))
		}
	}
}

func TestW7CSearchTermsFieldsAndCompact(t *testing.T) {
	terms := searchTerms("Update proxy: tokyo-01!")
	joined := strings.Join(terms, "\n")
	for _, want := range []string{"update proxy tokyo 01", "updateproxytokyo01", "update", "proxy", "tokyo", "01"} {
		if !strings.Contains(joined, want+"\n") && !strings.HasSuffix(joined, want) {
			t.Fatalf("terms missing %q: %#v", want, terms)
		}
	}
}

func TestW7CNormalizeSearchTextRules(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"ＡＢＣ", "abc"},                       // NFKC + lower
		{"  Hello, World!  ", "hello world"}, // punctuation collapses to one space
		{"a1ｂ２", "a1b2"},                     // digits kept, full-width normalized
		{"!!!", ""},                          // no letters/numbers
		{"\u00a0x\u200b", "x"},               // zero-width separators dropped
	}
	for _, tc := range cases {
		if got := normalizeSearchText(tc.in); got != tc.want {
			t.Fatalf("normalizeSearchText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// NewID
// ---------------------------------------------------------------------------

func TestW7CNewIDContract(t *testing.T) {
	first, err := NewID("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "oplog_") || len(first) != len("oplog_")+32 {
		t.Fatalf("default NewID = %q", first)
	}
	if _, err := hexDecodeString(strings.TrimPrefix(first, "oplog_")); err != nil {
		t.Fatalf("NewID suffix not hex: %v", err)
	}
	second, err := NewID("custom")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(second, "custom_") {
		t.Fatalf("custom NewID = %q", second)
	}
	// Whitespace-only prefix falls back to the default namespace.
	blank, err := NewID("   ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(blank, "oplog_") {
		t.Fatalf("blank prefix NewID = %q", blank)
	}
	if first == second {
		t.Fatal("NewID must be collision resistant")
	}
}

func hexDecodeString(value string) ([]byte, error) {
	var out []byte
	for index := 0; index < len(value); index += 2 {
		high, low := value[index], value[index+1]
		decode := func(c byte) (byte, bool) {
			switch {
			case c >= '0' && c <= '9':
				return c - '0', true
			case c >= 'a' && c <= 'f':
				return c - 'a' + 10, true
			default:
				return 0, false
			}
		}
		h, ok1 := decode(high)
		l, ok2 := decode(low)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("invalid hex at offset %d", index)
		}
		out = append(out, h<<4|l)
	}
	return out, nil
}
