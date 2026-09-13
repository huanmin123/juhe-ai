package groups

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// wkFailCipher 用于驱动加密失败的分支。
type wkFailCipher struct{}

func (wkFailCipher) Encrypt(context.Context, []byte) (string, error) {
	return "", errors.New("cipher failed")
}

type wkEmptyCipher struct{}

func (wkEmptyCipher) Encrypt(context.Context, []byte) (string, error) { return "  ", nil }

func wkReadyService(t *testing.T, db *sql.DB, cipher SecretCipher) *Service {
	t.Helper()
	svc, err := New(db, modelcheckauth.SQLite, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, nil, cipher)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func wkGroupFixture(t *testing.T, svc *Service, actor Actor) (Group, RouteStrategy) {
	t.Helper()
	ctx := context.Background()
	group, err := svc.CreateGroup(ctx, actor, GroupInput{Name: "g", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	strategy, err := svc.CreateRouteStrategy(ctx, actor, RouteStrategyInput{Name: "r", Bindings: []RouteBinding{{GroupID: group.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	return group, strategy
}

func TestNewValidation(t *testing.T) {
	db := testDB(t)
	if _, err := New(nil, modelcheckauth.SQLite, OwnerGate{}, nil, testCipher{}); err == nil {
		t.Fatal("nil db 必须被拒绝")
	}
	if _, err := New(db, modelcheckauth.Mode(99), OwnerGate{}, nil, nil); err == nil {
		t.Fatal("非法 mode 必须被拒绝")
	}
	if _, err := New(db, modelcheckauth.Postgres, OwnerGate{}, nil, nil); err == nil {
		t.Fatal("nil cipher 必须被拒绝")
	}
	svc, err := New(db, modelcheckauth.SQLite, OwnerGate{}, nil, testCipher{})
	if err != nil || svc.now == nil {
		t.Fatalf("默认时钟未注入: %v", err)
	}
}

func TestFindGroupScopesToOwner(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	group, _ := wkGroupFixture(t, svc, admin)
	// 未指定 owner 时回落到 actor 自身。
	found, err := svc.FindGroup(ctx, admin, "  ", group.ID)
	if err != nil || found.ID != group.ID || found.ProviderCode != "openai" {
		t.Fatalf("find group=%+v err=%v", found, err)
	}
	// 管理员显式选择其他 owner 可以读取（不隐式聚合）。
	if _, err := svc.FindGroup(ctx, admin, "sys-9", group.ID); err == nil {
		t.Fatal("其他 owner 下不应存在该分组")
	}
	// 非 admin 不能代查他人 owner。
	user := Actor{SystemAccountID: "sys-2", Role: "user"}
	if _, err := svc.FindGroup(ctx, user, "sys-1", group.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("跨 owner 读取 err=%v", err)
	}
	if _, err := svc.FindGroup(ctx, admin, "", "missing"); err == nil {
		t.Fatal("缺失分组必须报错")
	}
}

func TestListGroupsBoundaries(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	for _, name := range []string{"a", "b"} {
		if _, err := svc.CreateGroup(ctx, admin, GroupInput{Name: name, ProviderCode: "openai"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.ListGroups(ctx, admin, "", 0); err == nil {
		t.Fatal("limit 0 必须失败")
	}
	if _, err := svc.ListGroups(ctx, admin, "", 1001); err == nil {
		t.Fatal("limit>1000 必须失败")
	}
	listed, err := svc.ListGroups(ctx, admin, "", 1)
	if err != nil || len(listed) != 1 {
		t.Fatalf("分页列表=%+v err=%v", listed, err)
	}
	// 写权限被禁时的读路径同样拒绝（requireWrite 前置）。
	blocked := &Service{db: db, mode: modelcheckauth.SQLite, gate: OwnerGate{}, now: nil, cipher: testCipher{}}
	if _, err := blocked.ListGroups(ctx, admin, "", 10); !errors.Is(err, ErrOwnerGate) {
		t.Fatalf("gate 未就绪 err=%v", err)
	}
	// 空 actor 拒绝。
	if _, err := svc.ListGroups(ctx, Actor{Role: "admin"}, "", 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("空 actor err=%v", err)
	}
}

func TestUpdateGroupValidationAndConflict(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	group, _ := wkGroupFixture(t, svc, admin)
	if _, err := svc.UpdateGroup(ctx, admin, group.ID, " ", GroupInput{Name: "x", ProviderCode: "openai"}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("空 revision err=%v", err)
	}
	if _, err := svc.UpdateGroup(ctx, admin, " ", group.Revision, GroupInput{Name: "x", ProviderCode: "openai"}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("空 id err=%v", err)
	}
	if _, err := svc.UpdateGroup(ctx, admin, group.ID, group.Revision, GroupInput{Name: "  ", ProviderCode: "openai"}); err == nil {
		t.Fatal("空名称必须失败")
	}
	updated, err := svc.UpdateGroup(ctx, admin, group.ID, group.Revision, GroupInput{Name: "renamed", ProviderCode: "azure", Enabled: boolPtr(false)})
	if err != nil || updated.Name != "renamed" || updated.Enabled {
		t.Fatalf("update=%+v err=%v", updated, err)
	}
	// 旧 revision 再更新 → 冲突。
	if _, err := svc.UpdateGroup(ctx, admin, group.ID, group.Revision, GroupInput{Name: "again", ProviderCode: "openai"}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update err=%v", err)
	}
	// 描述可写回。
	withDesc, err := svc.UpdateGroup(ctx, admin, group.ID, updated.Revision, GroupInput{Name: "d", ProviderCode: "openai", Description: strPtr("desc")})
	if err != nil || withDesc.Description == nil || *withDesc.Description != "desc" {
		t.Fatalf("desc update=%+v err=%v", withDesc, err)
	}
}

func strPtr(v string) *string { return &v }
func boolPtr(v bool) *bool    { return &v }

func TestDeleteGroupGuards(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	group, strategy := wkGroupFixture(t, svc, admin)
	// 被策略引用时不可删除。
	if err := svc.DeleteGroup(ctx, admin, group.ID, group.Revision); err == nil || !strings.Contains(err.Error(), "referenced") {
		t.Fatalf("引用防护 err=%v", err)
	}
	// revision 冲突。
	if err := svc.DeleteGroup(ctx, admin, group.ID, "stale"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale delete err=%v", err)
	}
	// 未绑定任何策略的分组可以直接删除。
	unbound, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "unbound", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGroup(ctx, admin, unbound.ID, unbound.Revision); err != nil {
		t.Fatalf("delete unbound group: %v", err)
	}
	if err := svc.DeleteGroup(ctx, admin, unbound.ID, unbound.Revision); err == nil {
		t.Fatal("缺失分组删除必须报错")
	}
	// 行为存疑：DeleteRouteStrategy 不清理 route_strategy_groups 中的绑定行，
	// 导致曾被策略引用过的分组在策略删除后仍被"引用"，永远无法删除。
	// 当前按实际行为断言（删除被拒），生产代码不动。
	if err := svc.DeleteRouteStrategy(ctx, admin, strategy.ID, strategy.Revision); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteGroup(ctx, admin, group.ID, group.Revision); err == nil || !strings.Contains(err.Error(), "referenced") {
		t.Fatalf("策略删除后的孤儿绑定应仍阻止分组删除: %v", err)
	}
}

func TestDeleteGroupRejectsDefault(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO groups (id,system_account_id,name,provider_code,enabled,is_default,group_type,created_at,updated_at) VALUES ('g-def','sys-1','def','openai',1,1,'personal','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	svc := wkReadyService(t, db, testCipher{})
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	if err := svc.DeleteGroup(context.Background(), admin, "g-def", "2026-01-01"); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("默认分组删除 err=%v", err)
	}
}

func TestRouteStrategyCRUDGuards(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	group, strategy := wkGroupFixture(t, svc, admin)

	// 名称缺失。
	if _, err := svc.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: " ", Bindings: []RouteBinding{{GroupID: group.ID}}}); err == nil {
		t.Fatal("空名称必须失败")
	}
	// 非法 status。
	if _, err := svc.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: "r2", Status: "paused", Bindings: []RouteBinding{{GroupID: group.ID}}}); err == nil {
		t.Fatal("非法 status 必须失败")
	}
	// 查找与列表。
	found, err := svc.FindRouteStrategy(ctx, admin, "", strategy.ID)
	if err != nil || found.ID != strategy.ID || len(found.Bindings) != 1 || found.Bindings[0].Priority != 1 || found.Bindings[0].Status != "active" {
		t.Fatalf("find strategy=%+v err=%v", found, err)
	}
	if _, err := svc.FindRouteStrategy(ctx, admin, "", "missing"); err == nil {
		t.Fatal("缺失策略必须报错")
	}
	listed, err := svc.ListRouteStrategies(ctx, admin, "", 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list strategies=%+v err=%v", listed, err)
	}
	if _, err := svc.ListRouteStrategies(ctx, admin, "", 0); err == nil {
		t.Fatal("limit 0 必须失败")
	}
	if _, err := svc.ListRouteStrategies(ctx, admin, "", 1001); err == nil {
		t.Fatal("limit>1000 必须失败")
	}
	// 更新：改绑定（failover 模式需要两个 active 分组）。
	groupB, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g-b", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.UpdateRouteStrategy(ctx, admin, strategy.ID, strategy.Revision, RouteStrategyInput{
		Name: "r-updated", Mode: "failover", Description: strPtr("d"),
		Bindings: []RouteBinding{{GroupID: group.ID, Priority: 1}, {GroupID: groupB.ID}},
	})
	if err != nil || updated.Mode != "failover" || len(updated.Bindings) != 2 || updated.Bindings[1].Weight != 1 || updated.Bindings[1].Priority != 2 {
		t.Fatalf("update strategy=%+v err=%v", updated, err)
	}
	// 旧 revision 更新 → 冲突（normal 单绑定先过校验，再撞 revision）。
	if _, err := svc.UpdateRouteStrategy(ctx, admin, strategy.ID, strategy.Revision, RouteStrategyInput{Name: "x", Bindings: []RouteBinding{{GroupID: group.ID}}}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale strategy update err=%v", err)
	}
	// 空 revision → 冲突。
	if _, err := svc.UpdateRouteStrategy(ctx, admin, strategy.ID, "", RouteStrategyInput{Name: "x"}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("空 revision err=%v", err)
	}
	// 非法 status 更新。
	if _, err := svc.UpdateRouteStrategy(ctx, admin, strategy.ID, updated.Revision, RouteStrategyInput{Name: "x", Status: "zzz", Bindings: []RouteBinding{{GroupID: group.ID}, {GroupID: groupB.ID}}}); err == nil {
		t.Fatal("非法 status 必须失败")
	}
	// 删除：先删 API key 引用，再删策略。
	if err := svc.DeleteRouteStrategy(ctx, admin, strategy.ID, "stale"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale delete err=%v", err)
	}
	if err := svc.DeleteRouteStrategy(ctx, admin, strategy.ID, updated.Revision); err != nil {
		t.Fatalf("delete strategy: %v", err)
	}
}

func TestDeleteRouteStrategyRejectsDefaultAndReferences(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO route_strategies (id,system_account_id,name,mode,status,is_default,created_at,updated_at) VALUES ('r-def','sys-1','def','normal','active',1,'2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	svc := wkReadyService(t, db, testCipher{})
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	if err := svc.DeleteRouteStrategy(context.Background(), admin, "r-def", "2026-01-01"); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("默认策略删除 err=%v", err)
	}
	// 被 API key 引用的策略不可删除。
	if _, err := db.Exec(`INSERT INTO api_keys (id,system_account_id,route_strategy_id,name,key_hash,key_prefix,key_suffix,key_secret_encrypted,status,is_default,purpose,created_at,updated_at) VALUES ('k-x','sys-1','r-def','k','h','p','s','e','active',0,'general','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteRouteStrategy(context.Background(), admin, "r-def", "2026-01-01"); err == nil {
		t.Fatal("被引用策略必须拒绝删除")
	}
}

func TestValidateBindingsAndModes(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	group, _ := wkGroupFixture(t, svc, admin)
	groupB, err := svc.CreateGroup(ctx, admin, GroupInput{Name: "g-b", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		mode     string
		bindings []RouteBinding
		fragment string
	}{
		{name: "empty bindings", mode: "normal", bindings: nil, fragment: "at least one group binding"},
		{name: "blank id", mode: "normal", bindings: []RouteBinding{{GroupID: "  "}}, fragment: "binding id is required"},
		{name: "duplicate", mode: "normal", bindings: []RouteBinding{{GroupID: group.ID}, {GroupID: group.ID, Priority: 2}}, fragment: "duplicate group binding"},
		{name: "missing group", mode: "normal", bindings: []RouteBinding{{GroupID: "ghost"}}, fragment: "does not belong to owner"},
		{name: "invalid binding status", mode: "normal", bindings: []RouteBinding{{GroupID: group.ID, Status: "paused"}}, fragment: "binding status"},
		{name: "duplicate priorities", mode: "weight", bindings: []RouteBinding{{GroupID: group.ID, Priority: 1}, {GroupID: groupB.ID, Priority: 1}}, fragment: "priorities must be unique"},
		{name: "normal needs exactly one", mode: "normal", bindings: []RouteBinding{{GroupID: group.ID}, {GroupID: groupB.ID}}, fragment: "exactly one active group"},
		{name: "failover needs two", mode: "failover", bindings: []RouteBinding{{GroupID: group.ID}}, fragment: "primary and an active fallback"},
		{name: "custom needs active", mode: "hybrid", bindings: []RouteBinding{{GroupID: group.ID, Status: "disabled"}}, fragment: "requires an active group"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateRouteStrategy(ctx, admin, RouteStrategyInput{Name: "r-" + tc.name, Mode: tc.mode, Bindings: tc.bindings})
			if err == nil || !strings.Contains(err.Error(), tc.fragment) {
				t.Fatalf("err=%v want fragment=%q", err, tc.fragment)
			}
		})
	}
}

func TestAPIKeyCRUDGuards(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	group, strategy := wkGroupFixture(t, svc, admin)
	_ = group

	// 必填项缺失。
	if _, _, err := svc.CreateAPIKey(ctx, admin, APIKeyInput{RouteStrategyID: strategy.ID, Name: "k"}); err == nil {
		t.Fatal("缺 secret 必须失败")
	}
	if _, _, err := svc.CreateAPIKey(ctx, admin, APIKeyInput{RouteStrategyID: " ", Name: "k", Secret: "sk"}); err == nil {
		t.Fatal("缺 route 必须失败")
	}
	// 策略不存在或未激活。
	if _, _, err := svc.CreateAPIKey(ctx, admin, APIKeyInput{RouteStrategyID: "ghost", Name: "k", Secret: "sk"}); err == nil {
		t.Fatal("ghost route 必须失败")
	}
	created, secret, err := svc.CreateAPIKey(ctx, admin, APIKeyInput{RouteStrategyID: strategy.ID, Name: "k", Secret: "sk-1234567890", Status: "active", Purpose: "eval"})
	if err != nil || secret != "sk-1234567890" || created.KeyPrefix != "sk-12345" || created.KeySuffix != "34567890" || created.Purpose != "eval" {
		t.Fatalf("create key=%+v err=%v", created, err)
	}
	// 查找与列表。
	found, err := svc.FindAPIKey(ctx, admin, "", created.ID)
	if err != nil || found.ID != created.ID || found.IsDefault {
		t.Fatalf("find key=%+v err=%v", found, err)
	}
	if _, err := svc.FindAPIKey(ctx, admin, "", "missing"); err == nil {
		t.Fatal("缺失 key 必须报错")
	}
	keys, err := svc.ListAPIKeys(ctx, admin, "", 10)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list keys=%+v err=%v", keys, err)
	}
	if _, err := svc.ListAPIKeys(ctx, admin, "", 0); err == nil {
		t.Fatal("limit 0 必须失败")
	}
	// 更新。
	updated, err := svc.UpdateAPIKey(ctx, admin, created.ID, created.Revision, APIKeyInput{RouteStrategyID: strategy.ID, Name: "k2", Status: "disabled", Purpose: "bench"})
	if err != nil || updated.Status != "disabled" || updated.Name != "k2" || updated.Purpose != "bench" {
		t.Fatalf("update key=%+v err=%v", updated, err)
	}
	if _, err := svc.UpdateAPIKey(ctx, admin, created.ID, "", APIKeyInput{Name: "x", RouteStrategyID: strategy.ID}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("空 revision err=%v", err)
	}
	if _, err := svc.UpdateAPIKey(ctx, admin, created.ID, updated.Revision, APIKeyInput{Name: " ", RouteStrategyID: strategy.ID}); err == nil {
		t.Fatal("空名称必须失败")
	}
	if _, err := svc.UpdateAPIKey(ctx, admin, created.ID, updated.Revision, APIKeyInput{Name: "x", RouteStrategyID: strategy.ID, Status: "paused"}); err == nil {
		t.Fatal("非法 status 必须失败")
	}
	if _, err := svc.UpdateAPIKey(ctx, admin, created.ID, updated.Revision, APIKeyInput{Name: "x", RouteStrategyID: "ghost"}); err == nil {
		t.Fatal("ghost route 必须失败")
	}
	if _, err := svc.UpdateAPIKey(ctx, admin, "missing", "rev", APIKeyInput{Name: "x", RouteStrategyID: strategy.ID}); err == nil {
		t.Fatal("缺失 key 更新必须报错")
	}
	// 删除。
	if err := svc.DeleteAPIKey(ctx, admin, created.ID, "stale"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale delete err=%v", err)
	}
	if err := svc.DeleteAPIKey(ctx, admin, created.ID, updated.Revision); err != nil {
		t.Fatalf("delete key: %v", err)
	}
	if err := svc.DeleteAPIKey(ctx, admin, created.ID, updated.Revision); err == nil {
		t.Fatal("缺失 key 删除必须报错")
	}
}

func TestAPIKeyCipherFailureModes(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, wkFailCipher{})
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	group, strategy := wkGroupFixture(t, svc, admin)
	if _, _, err := svc.CreateAPIKey(context.Background(), admin, APIKeyInput{RouteStrategyID: strategy.ID, Name: "k", Secret: "sk"}); err == nil || !strings.Contains(err.Error(), "encrypt api key secret") {
		t.Fatalf("cipher 失败 err=%v", err)
	}
	rotateSvc := wkReadyService(t, db, wkEmptyCipher{})
	if _, _, err := rotateSvc.CreateAPIKey(context.Background(), admin, APIKeyInput{RouteStrategyID: strategy.ID, Name: "k", Secret: "sk"}); err == nil || !strings.Contains(err.Error(), "empty ciphertext") {
		t.Fatalf("空密文 err=%v", err)
	}
	if _, _, err := rotateSvc.RotateAPIKeySecret(context.Background(), admin, "k", "rev", "sk"); err == nil || !strings.Contains(err.Error(), "empty ciphertext") {
		t.Fatalf("轮换空密文 err=%v", err)
	}
	_ = group
}

func TestRotateAPIKeySecretGuards(t *testing.T) {
	db := testDB(t)
	svc := wkReadyService(t, db, testCipher{})
	ctx := context.Background()
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	_, strategy := wkGroupFixture(t, svc, admin)
	created, _, err := svc.CreateAPIKey(ctx, admin, APIKeyInput{RouteStrategyID: strategy.ID, Name: "k", Secret: "sk-original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RotateAPIKeySecret(ctx, admin, created.ID, " ", "sk-new"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("空 revision err=%v", err)
	}
	if _, _, err := svc.RotateAPIKeySecret(ctx, admin, created.ID, created.Revision, "  "); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("空 secret err=%v", err)
	}
	if _, _, err := svc.RotateAPIKeySecret(ctx, admin, "missing", "rev", "sk"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("缺失 key err=%v", err)
	}
	rotated, raw, err := svc.RotateAPIKeySecret(ctx, admin, created.ID, created.Revision, "sk-brand-new-secret")
	if err != nil || raw != "sk-brand-new-secret" || rotated.KeyPrefix != "sk-brand" || rotated.KeySuffix != "w-secret" {
		t.Fatalf("rotate=%+v err=%v", rotated, err)
	}
}

func TestDeleteAPIKeyRejectsDefault(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO api_keys (id,system_account_id,route_strategy_id,name,key_hash,key_prefix,key_suffix,key_secret_encrypted,status,is_default,purpose,created_at,updated_at) VALUES ('k-def','sys-1','r','k','h','p','s','e','active',1,'general','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	svc := wkReadyService(t, db, testCipher{})
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	if err := svc.DeleteAPIKey(context.Background(), admin, "k-def", "2026-01-01"); err == nil || !strings.Contains(err.Error(), "default") {
		t.Fatalf("默认 key 删除 err=%v", err)
	}
}

func TestTimestampMonotonicUnderFixedClock(t *testing.T) {
	db := testDB(t)
	// 固定时钟下连续写入 revision 必须严格递增（防同 tick 冲突）。
	tick := 0
	svc, err := New(db, modelcheckauth.SQLite, OwnerGate{Confirmed: true, SchemaReady: true, NodeWriterStopped: true}, func() time.Time {
		tick++
		return time.UnixMilli(1_000_000)
	}, testCipher{})
	if err != nil {
		t.Fatal(err)
	}
	admin := Actor{SystemAccountID: "sys-1", Role: "admin"}
	first, err := svc.CreateGroup(context.Background(), admin, GroupInput{Name: "t1", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateGroup(context.Background(), admin, GroupInput{Name: "t2", ProviderCode: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	firstTime, err1 := time.Parse(time.RFC3339Nano, first.Revision)
	secondTime, err2 := time.Parse(time.RFC3339Nano, second.Revision)
	if err1 != nil || err2 != nil {
		t.Fatalf("revision 解析失败: %v %v", err1, err2)
	}
	// 注意：RFC3339Nano 文本在整秒边界不做字典序单调（'Z' vs '.'），
	// 单调性契约指时间值而非文本。
	if !secondTime.After(firstTime) {
		t.Fatalf("固定时钟下 revision 未单调递增: %s -> %s", first.Revision, second.Revision)
	}
	if tick == 0 {
		t.Fatal("时钟未被使用")
	}
}
