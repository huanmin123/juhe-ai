package modelcheckdurable

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// w9h_store_units_test.go 补齐 durable store 的 sqlite 可达分支：
// CheckSchema 契约校验、outcome payload 规范化往返、声明守卫与
// LoadInput/CommitOutcome 的边界臂。

func TestW9HCheckSchemaArms(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "w9h-check.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("check schema err=%v", err)
	}
	// 缺表时按表名报错。
	bare, err := OpenSQLite(filepath.Join(t.TempDir(), "w9h-bare.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	if err := bare.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "verify model check durable schema") {
		t.Fatalf("bare check err=%v", err)
	}
}

func TestW9HClaimGuardsAfterTakeover(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "w9h-claim.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	issued, err := store.Issue(ctx, validDraft(now))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, issued.Input.InputID, "owner-1", "token-1", "outcome-1", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// 二次声明（同一 owner token）被幂等接收。
	sameClaim, err := store.Claim(ctx, issued.Input.InputID, "owner-1", "token-1", "outcome-1", now, time.Minute)
	if err != nil || sameClaim.OwnerID != "owner-1" {
		t.Fatalf("idempotent claim=%+v err=%v", sameClaim, err)
	}
	// 换 owner 声明在租约活跃期被拒绝。
	if _, err := store.Claim(ctx, issued.Input.InputID, "owner-2", "token-2", "outcome-2", now, time.Minute); err == nil {
		t.Fatal("active lease must reject a different owner")
	}
	// 释放后其他 owner 可接管（fence 保留）。
	if err := store.ReleaseClaim(ctx, claim, now); err != nil {
		t.Fatal(err)
	}
	takeover, err := store.Claim(ctx, issued.Input.InputID, "owner-2", "token-2", "outcome-2", now, time.Minute)
	if err != nil || takeover.OwnerID != "owner-2" {
		t.Fatalf("takeover claim=%+v err=%v", takeover, err)
	}
	// LoadInput 对缺失输入报错；对已发行输入返回冻结 fence。
	if _, err := store.LoadInput(ctx, "w9h-absent", now); err == nil {
		t.Fatal("absent input must fail")
	}
	reloaded, err := store.LoadInput(ctx, issued.Input.InputID, now)
	if err != nil || reloaded.IdentityKey == "" {
		t.Fatalf("reload=%+v err=%v", reloaded, err)
	}
	// CommitOutcome 用伪造 token 被拒。
	outcome := Outcome{
		OutcomeID: "outcome-3", InputID: issued.Input.InputID, InputDigest: issued.Input.InputDigest,
		FenceToken: claim.FenceToken, ObservedAt: now, StoredAt: now, Payload: []byte(`{"ok":true}`),
	}
	forged := claim
	forged.ClaimToken = "wrong-token"
	if err := store.CommitOutcome(ctx, outcome, forged, now); err == nil {
		t.Fatal("forged claim commit must fail")
	}
	// 伪造 fence 同样被拒。
	badFence := claim
	badFence.FenceToken = claim.FenceToken + 5
	if err := store.CommitOutcome(ctx, outcome, badFence, now); err == nil {
		t.Fatal("forged fence commit must fail")
	}
}

func forgedClaim(claim Claim) Claim {
	forged := claim
	forged.ClaimToken = "wrong-token"
	return forged
}

func TestW9HCanonicalOutcomePayloadStable(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "w9h-canonical.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// sqlite 模式：payload 原样透传（jsonb 规范化是 PG 专属臂，sqlite 无法
	// 驱动 `::jsonb` 转换；该分支登记为 PG 门禁未覆盖）。
	raw := []byte(`{"b":2,"a":1}`)
	first, err := store.canonicalOutcomePayload(ctx, tx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(raw) {
		t.Fatalf("sqlite passthrough got %s", first)
	}
}
