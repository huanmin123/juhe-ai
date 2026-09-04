package gatewayclientip

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newSQLitePolicySource(t *testing.T) (*SQLPolicySource, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := []string{
		`CREATE TABLE client_ip_registry (
			ip_hash TEXT PRIMARY KEY,
			aggregate_ip_key TEXT NOT NULL,
			client_ip TEXT NOT NULL
		)`,
		`CREATE TABLE client_ip_policies (
			id TEXT PRIMARY KEY,
			ip_hash TEXT NOT NULL,
			policy_type TEXT NOT NULL,
			status TEXT NOT NULL,
			reason TEXT,
			expires_at TEXT,
			created_by_system_account_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE client_ip_policy_hits (
			ip_hash TEXT NOT NULL,
			stat_date TEXT NOT NULL,
			policy_id TEXT NOT NULL,
			hit_count INTEGER NOT NULL,
			last_hit_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (ip_hash, stat_date, policy_id)
		)`,
	}
	for _, statement := range schema {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	source, err := NewSQLPolicySource(db, false, func() time.Time { return time.UnixMilli(1_000_000) },
		func(context.Context) (string, error) { return "UTC", nil })
	if err != nil {
		t.Fatal(err)
	}
	return source, db
}

func seedPolicy(t *testing.T, db *sql.DB, id, ipHash, policyType, status string, expiresAt *string) {
	t.Helper()
	reason := "abuse"
	if _, err := db.Exec(`INSERT INTO client_ip_registry (ip_hash, aggregate_ip_key, client_ip) VALUES (?, ?, ?)`,
		ipHash, ipHash+"-agg", ipHash+"-ip"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO client_ip_policies (id, ip_hash, policy_type, status, reason, expires_at,
		created_by_system_account_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'sys', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`,
		id, ipHash, policyType, status, reason, expiresAt); err != nil {
		t.Fatal(err)
	}
}

func TestSQLPolicySourceListAndFindDualMode(t *testing.T) {
	source, db := newSQLitePolicySource(t)
	ctx := context.Background()
	activeExpires := canonicalRFC3339(time.UnixMilli(2_000_000))
	expiredExpires := canonicalRFC3339(time.UnixMilli(500_000))
	seedPolicy(t, db, "p-active", hashOf("10.0.0.1"), PolicyTypeBlacklist, "active", &activeExpires)
	seedPolicy(t, db, "p-expired", hashOf("10.0.0.2"), PolicyTypeBlacklist, "active", &expiredExpires)
	seedPolicy(t, db, "p-disabled", hashOf("10.0.0.3"), PolicyTypeBlacklist, "disabled", nil)
	seedPolicy(t, db, "p-allow", hashOf("10.0.0.4"), PolicyTypeAllowlist, "active", nil)

	policies, err := source.ListActiveClientIPPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// active + 未过期：p-active、p-allow；expired/disabled 过滤。
	if len(policies) != 2 {
		t.Fatalf("policies=%+v", policies)
	}
	found, err := source.FindActiveClientIPPolicyByHash(ctx, hashOf("10.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != "p-active" || found.PolicyType != PolicyTypeBlacklist {
		t.Fatalf("found=%+v", found)
	}
	if found.Reason == nil || *found.Reason != "abuse" {
		t.Fatalf("reason=%v", found.Reason)
	}
	// 过期策略按未命中处理。
	expired, err := source.FindActiveClientIPPolicyByHash(ctx, hashOf("10.0.0.2"))
	if err != nil {
		t.Fatal(err)
	}
	if expired != nil {
		t.Fatalf("expired=%+v", expired)
	}
	// 非法 hash → nil（Node normalizeIpHash guard）。
	bogus, err := source.FindActiveClientIPPolicyByHash(ctx, "not-a-hash")
	if err != nil {
		t.Fatal(err)
	}
	if bogus != nil {
		t.Fatalf("bogus=%+v", bogus)
	}
}

func TestSQLPolicySourceRecordHitsUpserts(t *testing.T) {
	source, db := newSQLitePolicySource(t)
	ctx := context.Background()
	hitAt := canonicalRFC3339(time.UnixMilli(1_500_000))
	if err := source.RecordClientIPPolicyHits(ctx, []PolicyHitInput{
		{IPHash: hashOf("10.0.0.1"), PolicyID: "p1", HitCount: 3, HitAt: hitAt},
		{IPHash: "not-a-hash", PolicyID: "p2", HitCount: 5, HitAt: hitAt}, // 归一化后跳过
	}); err != nil {
		t.Fatal(err)
	}
	// 同 key 再投 → 累加 + last_hit_at 取较大值。
	later := canonicalRFC3339(time.UnixMilli(1_600_000))
	if err := source.RecordClientIPPolicyHits(ctx, []PolicyHitInput{
		{IPHash: hashOf("10.0.0.1"), PolicyID: "p1", HitCount: 2, HitAt: later},
	}); err != nil {
		t.Fatal(err)
	}
	var hitCount int64
	var lastHitAt string
	if err := db.QueryRow(`SELECT hit_count, last_hit_at FROM client_ip_policy_hits WHERE ip_hash = ? AND policy_id = ?`,
		hashOf("10.0.0.1"), "p1").Scan(&hitCount, &lastHitAt); err != nil {
		t.Fatal(err)
	}
	if hitCount != 5 {
		t.Fatalf("hitCount=%d want 5", hitCount)
	}
	if lastHitAt != later {
		t.Fatalf("lastHitAt=%s want %s", lastHitAt, later)
	}
	// stat_date = UTC date of hitAt。
	var statDate string
	if err := db.QueryRow(`SELECT stat_date FROM client_ip_policy_hits WHERE ip_hash = ?`, hashOf("10.0.0.1")).Scan(&statDate); err != nil {
		t.Fatal(err)
	}
	if statDate != time.UnixMilli(1_500_000).UTC().Format("2006-01-02") {
		t.Fatalf("statDate=%s", statDate)
	}
	// 空 hitAt 回退 batch updatedAt。
	if err := source.RecordClientIPPolicyHits(ctx, []PolicyHitInput{
		{IPHash: hashOf("10.0.0.9"), PolicyID: "p9", HitCount: 1},
	}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM client_ip_policy_hits`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("rows=%d", rows)
	}
	// 非法 hitAt → Node optionalRfc3339Instant 抛错文案。
	err := source.RecordClientIPPolicyHits(ctx, []PolicyHitInput{
		{IPHash: hashOf("10.0.0.1"), PolicyID: "p1", HitCount: 1, HitAt: "bogus"},
	})
	if err == nil || err.Error() != "Client-IP 策略 hitAt 必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("err=%v", err)
	}
}
