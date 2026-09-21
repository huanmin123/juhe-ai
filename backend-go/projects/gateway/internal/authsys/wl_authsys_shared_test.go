package authsys

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	businesssettings "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/settings"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// newWlRecorder 返回干净的响应记录器。
func newWlRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// wlStrPtr 返回字符串指针。
func wlStrPtr(value string) *string { return &value }

// ---- Redis runtime-state 存储 ----

func TestWlNewRedisNamespacedStateStore(t *testing.T) {
	t.Run("空 URL 拒绝", func(t *testing.T) {
		if _, _, err := NewRedisNamespacedStateStore("  ", "ns", "name"); err == nil {
			t.Fatal("空 URL 必须拒绝")
		}
	})
	t.Run("非法 URL 拒绝", func(t *testing.T) {
		if _, _, err := NewRedisNamespacedStateStore("://bad", "ns", "name"); err == nil {
			t.Fatal("非法 URL 必须拒绝")
		}
	})
	t.Run("miniredis 建立连接", func(t *testing.T) {
		server := miniredis.RunT(t)
		store, closeFn, err := NewRedisNamespacedStateStore("redis://"+server.Addr(), "dev", "auth_captcha")
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if err := store.SetJSON(context.Background(), "k", map[string]any{"a": 1}, 1000); err != nil {
			t.Fatalf("SetJSON err=%v", err)
		}
		if got, _ := server.Get("juhe-ai:dev:state:auth_captcha:k"); got == "" {
			t.Fatal("键必须带完整命名空间前缀")
		}
		closeFn()
	})
	t.Run("空 namespace 拒绝", func(t *testing.T) {
		if _, err := redisStateStoreKeyPrefix("  ", "name"); err == nil {
			t.Fatal("空 namespace 必须拒绝")
		}
		prefix, err := redisStateStoreKeyPrefix("juhe ai/dev", "cap cha")
		_ = err
		if err != nil || prefix != "juhe-ai:juhe_ai_dev:state:cap_cha:" {
			t.Fatalf("prefix=%q err=%v", prefix, err)
		}
	})
	t.Run("键名清洗", func(t *testing.T) {
		if got := sanitizeRedisStateKeyPart("  "); got != "default" {
			t.Fatalf("空键名必须回退 default: %q", got)
		}
		if got := sanitizeRedisStateKeyPart("a b!c"); got != "a_b_c" {
			t.Fatalf("非法字符必须替换: %q", got)
		}
	})
}

func TestWlRedisStateStoreOperations(t *testing.T) {
	server := miniredis.RunT(t)
	store, closeFn, err := NewRedisNamespacedStateStore("redis://"+server.Addr(), "dev", "state_test")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	t.Cleanup(closeFn)
	ctx := context.Background()
	t.Run("SetJSON 与 GetJSON 往返", func(t *testing.T) {
		if err := store.SetJSON(ctx, "round", map[string]any{"v": float64(3)}, 60_000); err != nil {
			t.Fatalf("err=%v", err)
		}
		var payload map[string]any
		ok, err := store.GetJSON(ctx, "round", &payload)
		if err != nil || !ok || payload["v"] != float64(3) {
			t.Fatalf("ok=%v payload=%v err=%v", ok, payload, err)
		}
	})
	t.Run("缺失键读为不存在", func(t *testing.T) {
		var payload map[string]any
		ok, err := store.GetJSON(ctx, "missing", &payload)
		if err != nil || ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
	})
	t.Run("毒化键删除后读为不存在", func(t *testing.T) {
		if err := server.Set("juhe-ai:dev:state:state_test:poison", "not-json"); err != nil {
			t.Fatalf("err=%v", err)
		}
		var payload map[string]any
		ok, err := store.GetJSON(ctx, "poison", &payload)
		if err != nil || ok {
			t.Fatalf("毒化键必须读为不存在: ok=%v err=%v", ok, err)
		}
		if _, err := server.Get("juhe-ai:dev:state:state_test:poison"); err == nil {
			t.Fatal("毒化键必须被删除")
		}
	})
	t.Run("GetDeleteJSON 原子消费", func(t *testing.T) {
		if err := store.SetJSON(ctx, "consume", "x", 60_000); err != nil {
			t.Fatalf("err=%v", err)
		}
		var value string
		ok, err := store.GetDeleteJSON(ctx, "consume", &value)
		if err != nil || !ok || value != "x" {
			t.Fatalf("ok=%v value=%q err=%v", ok, value, err)
		}
		if ok2, _ := store.GetDeleteJSON(ctx, "consume", &value); ok2 {
			t.Fatal("键必须被消费删除")
		}
	})
	t.Run("Incr 计数与上限", func(t *testing.T) {
		count, err := store.Incr(ctx, "counter", 60_000, 2)
		if err != nil || count != 1 {
			t.Fatalf("count=%d err=%v", count, err)
		}
		count, _ = store.Incr(ctx, "counter", 60_000, 2)
		if count != 2 {
			t.Fatalf("count=%d", count)
		}
		// 超过 max 的下一次递增不写回。
		count, _ = store.Incr(ctx, "counter", 60_000, 2)
		if count != 3 {
			t.Fatalf("count=%d", count)
		}
		if value, _ := server.Get("juhe-ai:dev:state:state_test:counter"); value != "2" {
			t.Fatalf("存储值=%q, 应停留在 max", value)
		}
	})
	t.Run("DeleteJSON", func(t *testing.T) {
		if err := store.SetJSON(ctx, "del", "1", 60_000); err != nil {
			t.Fatalf("err=%v", err)
		}
		if err := store.DeleteJSON(ctx, "del"); err != nil {
			t.Fatalf("err=%v", err)
		}
		if ok, _ := store.GetJSON(ctx, "del", new(any)); ok {
			t.Fatal("键必须被删除")
		}
	})
	t.Run("TTL 与 max 的边界格式化", func(t *testing.T) {
		if got := formatRedisMillis(0); got != "1" {
			t.Fatalf("formatRedisMillis(0)=%q", got)
		}
		if got := formatRedisMax(-1); got != "" {
			t.Fatalf("formatRedisMax(-1)=%q", got)
		}
		if got := normalizeRedisTTL(0); got != time.Millisecond {
			t.Fatalf("normalizeRedisTTL(0)=%v", got)
		}
		if got := normalizeRedisTTL(1500); got != 1500*time.Millisecond {
			t.Fatalf("normalizeRedisTTL(1500)=%v", got)
		}
	})
}

// ---- Shared captcha / login guard ----

func TestWlSharedCaptchaService(t *testing.T) {
	server := miniredis.RunT(t)
	service, closeFn, err := NewRedisCaptchaService("redis://"+server.Addr(), "dev", nil)
	if err != nil {
		t.Fatalf("NewRedisCaptchaService err=%v", err)
	}
	t.Cleanup(closeFn)
	t.Run("签发并验证", func(t *testing.T) {
		result, err := service.Issue("127.0.0.1")
		if err != nil || result.Blocked {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		if result.Challenge.CaptchaID == "" || !strings.HasPrefix(result.Challenge.Image, "data:image/png;base64,") {
			t.Fatalf("challenge=%+v", result.Challenge)
		}
		// 无法从图像读出答案，但空答案验证失败本身就是一条消费路径。
		if service.Verify(result.Challenge.CaptchaID, "ZZZZZ") {
			t.Fatal("错误答案不能通过")
		}
		if service.Verify(result.Challenge.CaptchaID, "ZZZZZ") {
			t.Fatal("挑战必须一次性消费")
		}
	})
	t.Run("Issue 计数超限封锁", func(t *testing.T) {
		limited := NewSharedCaptchaService(nil, nil) // 只验证构造器 nil now 回退。
		_ = limited
		store := newWlFakeStateStore()
		service := NewSharedCaptchaService(store, func() time.Time { return time.Unix(0, 0) })
		store.preset("issue:10.0.0.9", int64(sharedCaptchaIssueLimit))
		result, err := service.Issue("10.0.0.9")
		if err != nil || !result.Blocked || result.RetryAfter != 60 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("存储失败上抛", func(t *testing.T) {
		store := newWlFakeStateStore()
		store.incrErr = errors.New("down")
		service := NewSharedCaptchaService(store, nil)
		if _, err := service.Issue("1.2.3.4"); err == nil {
			t.Fatal("状态存储失败必须上抛")
		}
	})
	t.Run("键与归一化", func(t *testing.T) {
		if got := (*SharedCaptchaService)(nil).issueKey("  "); got != "issue:unknown" {
			t.Fatalf("空 IP 必须回退 unknown: %q", got)
		}
		if got := normalizeSharedCaptchaCode(" a b c "); got != "ABC" {
			t.Fatalf("归一化=%q", got)
		}
	})
}

func TestWlSharedLoginGuard(t *testing.T) {
	server := miniredis.RunT(t)
	guard, closeFn, err := NewRedisLoginGuard("redis://"+server.Addr(), "dev", nil)
	if err != nil {
		t.Fatalf("NewRedisLoginGuard err=%v", err)
	}
	t.Cleanup(closeFn)
	t.Run("失败达到阈值后锁定并成功清除", func(t *testing.T) {
		var blocked bool
		var retryAfter int
		for i := 0; i < int(sharedLoginLimit); i++ {
			blocked, retryAfter, _, _ = guard.Failed("10.1.1.1", "LockedUser")
		}
		if !blocked || retryAfter <= 0 {
			t.Fatalf("达到阈值必须锁定: blocked=%v retry=%d", blocked, retryAfter)
		}
		if got, _, _, _ := guard.Check("10.1.1.1", "ignored"); !got {
			t.Fatal("IP 锁必须优先生效")
		}
		if _, _, message, _ := guard.Check("10.1.1.2", "lockeduser"); message != "账号暂时锁定，请稍后再试" {
			t.Fatalf("message=%q", message)
		}
		guard.Success("10.1.1.1", "LockedUser")
		if got, _, _, _ := guard.Check("10.1.1.1", "lockeduser"); got {
			t.Fatal("Success 后必须解除锁定")
		}
	})
	t.Run("已过期的锁不拦截", func(t *testing.T) {
		store := newWlFakeStateStore()
		guard := NewSharedLoginGuard(store, func() time.Time { return time.Unix(1000, 0) })
		store.preset("login:ip:10.9.9.9:lock", int64(999))
		if blocked, _, _, _ := guard.Check("10.9.9.9", "u"); blocked {
			t.Fatal("过期锁不得拦截")
		}
	})
	t.Run("计数失败上抛错误（D8 fail-closed）", func(t *testing.T) {
		store := newWlFakeStateStore()
		store.incrErr = errors.New("down")
		guard := NewSharedLoginGuard(store, nil)
		blocked, _, _, err := guard.Failed("10.8.8.8", "u")
		if blocked || err == nil {
			t.Fatalf("状态存储失败必须 fail-closed 上抛: blocked=%v err=%v", blocked, err)
		}
	})
	t.Run("retryAfter 边界", func(t *testing.T) {
		now := time.Unix(1000, 0)
		if got := retryAfterSeconds(now.Add(-time.Second), now); got != 0 {
			t.Fatalf("过去时间=%d", got)
		}
		if got := retryAfterSeconds(now.Add(300*time.Millisecond), now); got != 1 {
			t.Fatalf("不足一秒必须进位到 1: %d", got)
		}
		if got := retryAfterSeconds(now.Add(90*time.Second), now); got != 90 {
			t.Fatalf("90 秒=%d", got)
		}
	})
}

// ---- 默认资源 ----

// wlStringSealer 返回固定信封；empty 为 true 时返回空信封驱动校验失败分支。
type wlStringSealer struct{ empty bool }

func (s wlStringSealer) SealSecret(_ context.Context, plaintext string) (string, error) {
	if s.empty {
		return "", nil
	}
	return "sealed:" + plaintext, nil
}

func TestWlEnsureDefaultResourcesBoundaries(t *testing.T) {
	ctx := context.Background()
	nowText := time.Now().UTC().Format(time.RFC3339Nano)
	t.Run("nil ensurer 拒绝", func(t *testing.T) {
		var ensurer *SQLDefaultResources
		if err := ensurer.EnsureDefaultResources(ctx, nil, "acc", nowText); err == nil {
			t.Fatal("nil ensurer 必须拒绝")
		}
	})
	t.Run("sealer 缺失拒绝", func(t *testing.T) {
		db := newContractTestDB(t)
		store, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		ensurer := NewSQLDefaultResources(store, nil)
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if err := ensurer.EnsureDefaultResources(ctx, tx, "acc", nowText); err == nil || !strings.Contains(err.Error(), "sealer is required") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("无默认分组时策略路由失败关闭", func(t *testing.T) {
		db := newContractTestDB(t)
		store, _ := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
		ensurer := NewSQLDefaultResources(store, wlStringSealer{})
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		err := ensurer.ensureDefaultRouteStrategies(ctx, tx, "acc", nowText)
		if err == nil || !strings.Contains(err.Error(), missingDefaultGroupError) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("无 GPT 路由时 chat key 失败关闭", func(t *testing.T) {
		db := newContractTestDB(t)
		store, _ := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
		ensurer := NewSQLDefaultResources(store, wlStringSealer{})
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		err := ensurer.ensureChatAPIKey(ctx, tx, "acc", nowText)
		if err == nil || !strings.Contains(err.Error(), missingGPTRouteError) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("sealer 空信封拒绝", func(t *testing.T) {
		db := newContractTestDB(t)
		store, _ := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
		ensurer := NewSQLDefaultResources(store, wlStringSealer{empty: true})
		tx, _ := db.BeginTx(ctx, nil)
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `INSERT INTO groups (id, system_account_id, name, provider_code, enabled, is_default, created_at, updated_at) VALUES ('g1','acc','GPT 组','gpt',1,1,'t','t')`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO route_strategies (id, system_account_id, name, status, is_default, created_at, updated_at) VALUES ('r1','acc','GPT 路由','active',1,'t','t')`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, status, created_at, updated_at) VALUES ('b1','r1','acc','g1','active','t','t')`); err != nil {
			t.Fatal(err)
		}
		err := ensurer.ensureChatAPIKey(ctx, tx, "acc", nowText)
		if err == nil || !strings.Contains(err.Error(), "empty envelope") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("完整流水线幂等", func(t *testing.T) {
		db := newContractTestDB(t)
		store, _ := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
		ensurer := NewSQLDefaultResources(store, wlStringSealer{})
		run := func() int {
			tx, _ := db.BeginTx(ctx, nil)
			defer tx.Rollback()
			if err := ensurer.EnsureDefaultResources(ctx, tx, "acc2", nowText); err != nil {
				t.Fatalf("err=%v", err)
			}
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM groups WHERE system_account_id='acc2'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			return count
		}
		first := run()
		if first != len(defaultResourceGroupSeeds) {
			t.Fatalf("分组数=%d want=%d", first, len(defaultResourceGroupSeeds))
		}
		// 已存在时 ensureDefaultGroups 走 continue 分支，不重复插入。
		if again := run(); again != first {
			t.Fatalf("重复执行分组数=%d", again)
		}
	})
	t.Run("默认命名派生", func(t *testing.T) {
		tests := []struct {
			groupName, wantRoute, wantKey string
		}{
			{"默认 GPT 分组", "默认 GPT 路由", "默认 GPT API Key"},
			{"定制组", "定制组", "定制组"},
			{"", defaultRouteStrategyName, "默认API Key"},
		}
		for _, test := range tests {
			if got := defaultRouteStrategyNameForGroup(test.groupName); got != test.wantRoute {
				t.Fatalf("group %q route=%q want=%q", test.groupName, got, test.wantRoute)
			}
			if got := defaultAPIKeyNameForRouteStrategy(test.wantRoute); got != test.wantKey {
				t.Fatalf("route %q key=%q want=%q", test.wantRoute, got, test.wantKey)
			}
		}
	})
}

// ---- 账户存储辅助 ----

func TestWlStoreHelpersAndDialect(t *testing.T) {
	db := newContractTestDB(t)
	store, err := NewAccountStore(db, modelcheckauth.Postgres, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("Postgres 方言", func(t *testing.T) {
		if got := store.table("groups"); got != "juhe_business.groups" {
			t.Fatalf("table=%q", got)
		}
		if got := store.bind(`SELECT * FROM t WHERE a = ? AND b = ?`); got != `SELECT * FROM t WHERE a = $1 AND b = $2` {
			t.Fatalf("bind=%q", got)
		}
		// SQLite 模式单写事务已串行化，锁必须直接返回 nil。
		tx, _ := db.BeginTx(context.Background(), nil)
		defer tx.Rollback()
		sqliteStore, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		if err := sqliteStore.lockSuperAdminInvariant(context.Background(), tx); err != nil {
			t.Fatalf("SQLite 锁必须为 nil: %v", err)
		}
	})
	t.Run("密码哈希与凭据指纹", func(t *testing.T) {
		hash, err := HashNodePassword("some-password-1")
		if err != nil || !strings.HasPrefix(hash, "scrypt$") && !strings.Contains(hash, "$") {
			t.Fatalf("hash=%q err=%v", hash, err)
		}
		sum := sha256.Sum256([]byte("text"))
		if got := SecretHash("text"); got != hex.EncodeToString(sum[:]) {
			t.Fatalf("SecretHash=%q", got)
		}
	})
	t.Run("UpdatePassword 校验", func(t *testing.T) {
		ctx := context.Background()
		if _, err := store.UpdatePassword(ctx, "id", ""); err == nil || !strings.Contains(err.Error(), "不能为空") {
			t.Fatalf("err=%v", err)
		}
		if _, err := store.UpdatePassword(ctx, "id", "a b c"); err == nil || !strings.Contains(err.Error(), "不能包含空格") {
			t.Fatalf("err=%v", err)
		}
		// 不存在的账户：affected != 1，返回空 summary 且无错误（SQLite 方言验证）。
		sqliteStore, err := NewAccountStore(db, modelcheckauth.SQLite, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		summary, err := sqliteStore.UpdatePassword(ctx, "missing-id", "new-pass-1")
		if err != nil || summary.ID != "" {
			t.Fatalf("summary=%+v err=%v", summary, err)
		}
	})
	t.Run("超管不变式锁触发条件", func(t *testing.T) {
		if superAdminInvariantLockNeeded(PatchInput{}) {
			t.Fatal("无 role/status 修改不需要锁")
		}
		role := "admin"
		if !superAdminInvariantLockNeeded(PatchInput{Role: &role}) {
			t.Fatal("role 修改需要锁")
		}
	})
	t.Run("小工具函数", func(t *testing.T) {
		if minInt(1, 2) != 1 || minInt(3, 2) != 2 {
			t.Fatal("minInt 语义错误")
		}
		a, b := "x", "x"
		c, d := "x", "y"
		if !sameOptionalString(&a, &b) || !sameOptionalString(&a, &c) || sameOptionalString(&c, &d) {
			t.Fatal("sameOptionalString 语义错误")
		}
		if sameOptionalString(&a, nil) || sameOptionalString(nil, &a) {
			t.Fatal("nil 组合必须为 false")
		}
		if !sameOptionalString(nil, nil) {
			t.Fatal("双 nil 必须为 true")
		}
		one, two := 1, 2
		if !sameOptionalInt(&one, &one) || sameOptionalInt(&one, &two) || !sameOptionalInt(nil, nil) {
			t.Fatal("sameOptionalInt 语义错误")
		}
	})
}

// ---- Profile 限额 ----

// wlFakeSettings 实现 SystemSettingReader，注入设置值或读取错误。
type wlFakeSettings struct {
	values map[string]string
	err    error
}

func (s *wlFakeSettings) GetSystem(_ context.Context, _, key string) (businesssettings.Setting, bool, error) {
	if s.err != nil {
		return businesssettings.Setting{}, false, s.err
	}
	value, ok := s.values[key]
	if !ok {
		return businesssettings.Setting{}, false, nil
	}
	return businesssettings.Setting{Key: key, ValueJSON: value}, true, nil
}

func wlLimitSettings() map[string]string {
	return map[string]string{
		"gatewayUserRequestLimitPerMinute": "60",
		"gatewayUserRequestLimitPerDay":    "1000",
		"gatewayUserRequestLimitPerWeek":   "7000",
		"gatewayUserRequestLimitPerMonth":  "30000",
		"usageStatsTimezone":               `"Asia/Shanghai"`,
	}
}

func TestWlLoadUserRequestLimitSettings(t *testing.T) {
	buildDeps := func(settings *wlFakeSettings) *Deps {
		return &Deps{Settings: settings, Now: func() time.Time { return time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC) }}
	}
	t.Run("正常加载", func(t *testing.T) {
		deps := buildDeps(&wlFakeSettings{values: wlLimitSettings()})
		settings, err := deps.loadUserRequestLimitSettings(context.Background())
		if err != nil || settings.PerMinute != 60 || settings.Timezone != "Asia/Shanghai" {
			t.Fatalf("settings=%+v err=%v", settings, err)
		}
	})
	t.Run("缺省限额回退为 0", func(t *testing.T) {
		values := wlLimitSettings()
		delete(values, "gatewayUserRequestLimitPerMinute")
		deps := buildDeps(&wlFakeSettings{values: values})
		settings, err := deps.loadUserRequestLimitSettings(context.Background())
		if err != nil || settings.PerMinute != 0 {
			t.Fatalf("settings=%+v err=%v", settings, err)
		}
	})
	t.Run("缺 timezone 报错", func(t *testing.T) {
		values := wlLimitSettings()
		delete(values, "usageStatsTimezone")
		if _, err := buildDeps(&wlFakeSettings{values: values}).loadUserRequestLimitSettings(context.Background()); err == nil {
			t.Fatal("缺 timezone 必须报错")
		}
	})
	t.Run("限额非法报错", func(t *testing.T) {
		values := wlLimitSettings()
		values["gatewayUserRequestLimitPerDay"] = "-5"
		if _, err := buildDeps(&wlFakeSettings{values: values}).loadUserRequestLimitSettings(context.Background()); err == nil {
			t.Fatal("负数限额必须报错")
		}
	})
	t.Run("timezone 非法报错", func(t *testing.T) {
		values := wlLimitSettings()
		values["usageStatsTimezone"] = `"Not/AZone"`
		if _, err := buildDeps(&wlFakeSettings{values: values}).loadUserRequestLimitSettings(context.Background()); err == nil {
			t.Fatal("非法 timezone 必须报错")
		}
	})
	t.Run("settings 读取失败上抛", func(t *testing.T) {
		if _, err := buildDeps(&wlFakeSettings{err: errors.New("db down")}).loadUserRequestLimitSettings(context.Background()); err == nil {
			t.Fatal("读取失败必须上抛")
		}
	})
	t.Run("Settings 未初始化", func(t *testing.T) {
		if _, err := (&Deps{}).effectiveUserRequestLimits(context.Background(), nil); err == nil {
			t.Fatal("未初始化 settings 必须报错")
		}
	})
}

func TestWlProfileLimitPureFunctions(t *testing.T) {
	limit := 100
	overrides := &UserRequestLimits{PerMinute: &limit}
	t.Run("effectiveLimit 来源", func(t *testing.T) {
		if got := effectiveLimit(60, nil, overridesFieldPerMinute); got.Source != "global" || got.Limit != 60 {
			t.Fatalf("got=%+v", got)
		}
		if got := effectiveLimit(60, overrides, overridesFieldPerMinute); got.Source != "user" || got.Limit != 100 {
			t.Fatalf("got=%+v", got)
		}
		if got := effectiveLimit(60, overrides, overridesFieldPerDay); got.Source != "global" {
			t.Fatalf("got=%+v", got)
		}
	})
	t.Run("overrideExpiresOn", func(t *testing.T) {
		if overrideExpiresOn(nil) != nil {
			t.Fatal("nil overrides 必须返回 nil")
		}
		empty := ""
		if got := overrideExpiresOn(&UserRequestLimits{ExpiresOn: &empty}); got != nil {
			t.Fatalf("空 expiresOn=%v", got)
		}
		date := "2026-01-01"
		if got := overrideExpiresOn(&UserRequestLimits{ExpiresOn: &date}); got == nil || *got != date {
			t.Fatalf("got=%v", got)
		}
	})
	t.Run("normalizedProfileRequestLimitOverrides", func(t *testing.T) {
		if normalizedProfileRequestLimitOverrides(nil) != nil {
			t.Fatal("nil 必须返回 nil")
		}
		if normalizedProfileRequestLimitOverrides(&UserRequestLimits{}) != nil {
			t.Fatal("全空字段必须返回 nil")
		}
		negative := -1
		if normalizedProfileRequestLimitOverrides(&UserRequestLimits{PerMinute: &negative}) != nil {
			t.Fatal("负值必须返回 nil")
		}
		badDate := "2026-13-40"
		if normalizedProfileRequestLimitOverrides(&UserRequestLimits{PerMinute: &limit, ExpiresOn: &badDate}) != nil {
			t.Fatal("非法日期必须返回 nil")
		}
		noExpiry := normalizedProfileRequestLimitOverrides(&UserRequestLimits{PerMinute: &limit})
		if noExpiry == nil || noExpiry.ExpiresOn != nil {
			t.Fatalf("无过期时间必须清空: %+v", noExpiry)
		}
		goodDate := "2026-01-01"
		if got := normalizedProfileRequestLimitOverrides(&UserRequestLimits{PerMinute: &limit, ExpiresOn: &goodDate}); got == nil || *got.ExpiresOn != goodDate {
			t.Fatalf("合法日期必须保留: %+v", got)
		}
	})
	t.Run("validUserRequestLimitExpiresOn", func(t *testing.T) {
		if validUserRequestLimitExpiresOn("2026-02-30") {
			t.Fatal("2 月 30 日必须无效")
		}
		if validUserRequestLimitExpiresOn("bad-date") || validUserRequestLimitExpiresOn("0100-01-01x") {
			t.Fatal("格式错误必须无效")
		}
		if validUserRequestLimitExpiresOn("0099-12-31") {
			t.Fatal("早于 0100 必须无效")
		}
		if !validUserRequestLimitExpiresOn("0100-01-01") {
			t.Fatal("0100-01-01 必须有效")
		}
	})
	t.Run("userRequestLimitOverrideActive", func(t *testing.T) {
		if active, _ := userRequestLimitOverrideActive(nil, "UTC", time.Now()); active {
			t.Fatal("nil overrides 不激活")
		}
		always := &UserRequestLimits{PerMinute: &limit}
		if active, err := userRequestLimitOverrideActive(always, "UTC", time.Now()); err != nil || !active {
			t.Fatalf("无过期时间必须始终激活: %v %v", active, err)
		}
		past := "2001-01-01"
		if active, _ := userRequestLimitOverrideActive(&UserRequestLimits{ExpiresOn: &past}, "UTC", time.Now()); active {
			t.Fatal("过期日期不激活")
		}
		future := "2999-12-31"
		if active, _ := userRequestLimitOverrideActive(&UserRequestLimits{ExpiresOn: &future}, "Asia/Shanghai", time.Now()); !active {
			t.Fatal("未过期日期激活")
		}
		if _, err := userRequestLimitOverrideActive(&UserRequestLimits{ExpiresOn: &past}, "Not/AZone", time.Now()); err == nil {
			t.Fatal("非法 timezone 必须报错")
		}
	})
	t.Run("parseLimitSetting", func(t *testing.T) {
		if value, err := parseLimitSetting("42"); err != nil || value != 42 {
			t.Fatalf("value=%d err=%v", value, err)
		}
		for _, raw := range []string{"abc", "-1", "1.5", "NaN", "1000000001", ""} {
			if _, err := parseLimitSetting(raw); err == nil {
				t.Fatalf("raw=%q 必须报错", raw)
			}
		}
	})
	t.Run("Deps.now 缺省回退", func(t *testing.T) {
		if (*Deps)(nil).now().IsZero() {
			t.Fatal("now() 必须返回时间")
		}
	})
}

// ---- 操作日志与 app 辅助 ----

func TestWlOperationLogRendering(t *testing.T) {
	t.Run("changeValue 契约", func(t *testing.T) {
		if got := changeValue(nil, ""); got != nil {
			t.Fatalf("空值必须折叠为 nil: %v", got)
		}
		if got := changeValue(nil, "text"); got != "text" {
			t.Fatalf("文本=%v", got)
		}
		if got := changeValue(7, "ignored"); got != 7 {
			t.Fatalf("原生值优先: %v", got)
		}
		if got := changeValue(map[string]any{"a": 1}, ""); got != `{"a":1}` {
			t.Fatalf("对象必须展开为 JSON 文本: %v", got)
		}
		if got := changeValue(json.RawMessage(`[1,2]`), "x"); got != `[1,2]` {
			t.Fatalf("RawMessage 必须按原文: %v", got)
		}
	})
	t.Run("truncateChangeRender", func(t *testing.T) {
		long := strings.Repeat("长", 600)
		if got := truncateChangeRender(long); len([]rune(got)) != maxCompositeChangeValueRender {
			t.Fatalf("截断长度=%d", len([]rune(got)))
		}
		if got := truncateChangeRender("short"); got != "short" {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("noop sink 与记录回退", func(t *testing.T) {
		noopSink{}.Record(OperationLogEntry{Module: "m"}, nil)
		deps := &Deps{}
		// Sink 为 nil 时不得 panic；Actor 缺失时从请求上下文回填。
		deps.recordOperationLog(withWlAuth(httptest.NewRequest(http.MethodPost, "/", nil), "acc", "user", "admin", false), OperationLogEntry{})
	})
}

func TestWlAppPureHelpers(t *testing.T) {
	t.Run("NewDeps 组装", func(t *testing.T) {
		deps := NewDeps(nil, nil, nil, nil, nil)
		if deps == nil || deps.Now != nil || deps.Settings != nil {
			t.Fatalf("deps=%+v", deps)
		}
		withSettings := NewDeps(nil, nil, nil, nil, nil, &wlFakeSettings{})
		if withSettings.Settings == nil {
			t.Fatal("可选 settings 必须生效")
		}
	})
	t.Run("writeAccountError 映射", func(t *testing.T) {
		recorder := newWlRecorder()
		writeAccountError(recorder, &ConflictError{Message: "conflict"}, "fallback")
		if recorder.Code != http.StatusConflict {
			t.Fatalf("conflict code=%d", recorder.Code)
		}
		recorder = newWlRecorder()
		writeAccountError(recorder, &ValidationError{Message: "validation"}, "fallback")
		if recorder.Code != http.StatusConflict {
			t.Fatalf("validation code=%d", recorder.Code)
		}
		recorder = newWlRecorder()
		writeAccountError(recorder, errors.New("other"), "fallback")
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("default code=%d", recorder.Code)
		}
	})
	t.Run("valueOr 与 parseIntOrDefault", func(t *testing.T) {
		value := "v"
		if valueOr(&value, "d") != "v" || valueOr(nil, "d") != "d" {
			t.Fatal("valueOr 语义错误")
		}
		if parseIntOrDefault("", 3) != 3 || parseIntOrDefault("abc", 3) != 3 || parseIntOrDefault("7", 3) != 7 {
			t.Fatal("parseIntOrDefault 语义错误")
		}
	})
	t.Run("normalizeDescriptionInput", func(t *testing.T) {
		if _, ok := normalizeDescriptionInput(nil); !ok {
			t.Fatal("nil 必须有效")
		}
		spaces := "  padded  "
		trimmed, ok := normalizeDescriptionInput(&spaces)
		if !ok || trimmed == nil || *trimmed != "padded" {
			t.Fatalf("trimmed=%v ok=%v", trimmed, ok)
		}
		blank := "   "
		out, ok := normalizeDescriptionInput(&blank)
		if !ok || out != nil {
			t.Fatalf("空白必须折叠为 nil: %v %v", out, ok)
		}
		tooLong := strings.Repeat("字", maxDescriptionLen+1)
		if _, ok := normalizeDescriptionInput(&tooLong); ok {
			t.Fatal("超长必须无效")
		}
	})
	t.Run("buildChanges 与 summary", func(t *testing.T) {
		if summaryFor("reset_password", "n") != "重置系统账户密码：n" || summaryFor("update", "n") != "更新系统账户：n" {
			t.Fatal("summaryFor 语义错误")
		}
		flag := true
		status := "disabled"
		hundred := 100
		changes := buildChanges(AccountSummary{}, PatchInput{Password: wlStrPtr("new-pass")}, AccountMutationResult{
			DisplayName: wlStrPtr("new-name"), Status: &status, MustChangePassword: &flag,
		})
		fields := map[string]bool{}
		for _, change := range changes {
			fields[change.Field] = true
		}
		if !fields["displayName"] || !fields["status"] || !fields["mustChangePassword"] || !fields["password"] {
			t.Fatalf("changes=%+v", changes)
		}
		if boolText(true) != "true" || boolText(false) != "false" {
			t.Fatal("boolText 语义错误")
		}
		if intPtrText(nil) != "" || intPtrText(&hundred) != "100" {
			t.Fatal("intPtrText 语义错误")
		}
	})
	t.Run("ParseCookie 与令牌解析", func(t *testing.T) {
		cookies := ParseCookie(`a=1; b=c=d;  ; =x; empty=`)
		if cookies["a"] != "1" || cookies["b"] != "c=d" || cookies["empty"] != "" {
			t.Fatalf("cookies=%v", cookies)
		}
		if kind, _, temporary := ResolveSystemAccessToken("Basic abc", ""); kind != "invalid" || temporary {
			t.Fatalf("kind=%q", kind)
		}
		if kind, _, _ := ResolveSystemAccessToken("Bearer short", ""); kind != "invalid" {
			t.Fatalf("kind=%q", kind)
		}
		temporaryToken := "juhe_tmp_" + strings.Repeat("a", 43)
		if kind, token, temporary := ResolveSystemAccessToken("Bearer "+temporaryToken, ""); kind != "token" || token != temporaryToken || !temporary {
			t.Fatalf("临时令牌解析=%q %v", kind, temporary)
		}
		if kind, token, temporary := ResolveSystemAccessToken("", "cookie-token"); kind != "token" || token != "cookie-token" || temporary {
			t.Fatalf("cookie 解析=%q %v", kind, temporary)
		}
		if kind, _, _ := ResolveSystemAccessToken("", ""); kind != "none" {
			t.Fatalf("kind=%q", kind)
		}
	})
	t.Run("secretHash", func(t *testing.T) {
		sum := sha256.Sum256([]byte("t"))
		if secretHash("t") != hex.EncodeToString(sum[:]) {
			t.Fatal("secretHash 必须是 sha256 hex")
		}
	})
	t.Run("ForceSelfAccessScope", func(t *testing.T) {
		recorder := newWlRecorder()
		ForceSelfAccessScope(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("未登录 code=%d", recorder.Code)
		}
		passed := false
		request := withWlAuth(httptest.NewRequest(http.MethodGet, "/", nil), "a", "u", "admin", false)
		ForceSelfAccessScope(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { passed = true })).ServeHTTP(newWlRecorder(), request)
		if !passed {
			t.Fatal("已登录必须放行")
		}
	})
	t.Run("sameSiteMode", func(t *testing.T) {
		if sameSiteMode("strict") != http.SameSiteStrictMode || sameSiteMode("NONE") != http.SameSiteNoneMode || sameSiteMode("other") != http.SameSiteLaxMode {
			t.Fatal("sameSiteMode 映射错误")
		}
	})
}

func TestWlParsePatchInputExtraBranches(t *testing.T) {
	t.Run("未知字段排序报错", func(t *testing.T) {
		_, err := parsePatchInput(map[string]any{"zeta": 1, "alpha": 2, "expectedUpdatedAt": "2026-01-01T00:00:00Z"})
		if err == nil || !strings.Contains(err.Error(), "alpha、zeta") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("缺 expectedUpdatedAt", func(t *testing.T) {
		if _, err := parsePatchInput(map[string]any{"displayName": "x"}); err == nil {
			t.Fatal("缺 expectedUpdatedAt 必须报错")
		}
	})
	t.Run("无变更字段", func(t *testing.T) {
		if _, err := parsePatchInput(map[string]any{"expectedUpdatedAt": "2026-01-01T00:00:00Z"}); err == nil || !strings.Contains(err.Error(), "至少提交一个修改字段") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("requestLimits 形态", func(t *testing.T) {
		input, err := parsePatchInput(map[string]any{"expectedUpdatedAt": "2026-01-01T00:00:00Z", "requestLimits": nil})
		if err != nil || !input.RequestLimitsPresent || input.RequestLimits != nil {
			t.Fatalf("input=%+v err=%v", input, err)
		}
		if _, err := parsePatchInput(map[string]any{"expectedUpdatedAt": "2026-01-01T00:00:00Z", "requestLimits": "bad"}); err == nil {
			t.Fatal("非法 requestLimits 必须报错")
		}
	})
	t.Run("aiAccountLimit 形态", func(t *testing.T) {
		input, err := parsePatchInput(map[string]any{"expectedUpdatedAt": "2026-01-01T00:00:00Z", "aiAccountLimit": float64(5)})
		if err != nil || input.AIAccountLimit == nil || *input.AIAccountLimit != 5 {
			t.Fatalf("input=%+v err=%v", input, err)
		}
		if _, err := parsePatchInput(map[string]any{"expectedUpdatedAt": "2026-01-01T00:00:00Z", "aiAccountLimit": 1.5}); err == nil {
			t.Fatal("小数 limit 必须报错")
		}
	})
}
