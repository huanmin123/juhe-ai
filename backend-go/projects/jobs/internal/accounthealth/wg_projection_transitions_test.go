package accounthealth

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wgTransitionOutcome 构造指定 transition kind 的投影 outcome（fence 形状
// 与归档一致：expected/输出共用同一不可变 fence）。
func wgTransitionOutcome(kind string, observedAt time.Time, withOutputFence bool, expectedStatus string) Outcome {
	fence := &CooldownFence{ObservationStartedAt: observedAt.Add(-time.Hour), Generation: "gen-" + kind, SourceConfigRevision: nil}
	projection := &Projection{
		TargetAccountID:       "acct-1",
		TransitionKind:        kind,
		InputVersion:          1,
		ConfigRevision:        5,
		DispatchRevision:      7,
		ExpectedAccountStatus: expectedStatus,
		Values: map[string]any{
			"last_health_check_at":          observedAt.Format(time.RFC3339Nano),
			"last_health_check_status_code": 503,
		},
	}
	if kind == "cooldown_defer" || kind == "cooldown_failure" || kind == "temporary_unavailable" || kind == "cooldown_error" {
		projection.CooldownFence = fence
		projection.ExpectedCooldownFence = fence
	}
	// outcome 与 transition 的匹配真值表见 outcomeMatchesTransition：
	// 成功族需要 Success；defer 需要 Neutral；failure/error 需要 UpstreamFailed。
	outcomeKind := OutcomeUpstreamFailed
	switch kind {
	case "cooldown_defer":
		outcomeKind = OutcomeNeutral
	case "health_success":
		outcomeKind = OutcomeSuccess
	}
	outcome := Outcome{
		OutcomeID:        "outcome-" + kind,
		RequestID:        "request-" + kind,
		AccountID:        "acct-1",
		Outcome:          outcomeKind,
		InputVersion:     1,
		ConfigRevision:   5,
		DispatchRevision: 7,
		StatusCode:       503,
		ErrorCode:        "upstream_5xx",
		ErrorMessage:     "上游失败",
		NextDueAt:        ptrTime(observedAt.Add(30 * time.Minute)),
		FailureCount:     2,
		FailureStartedAt: ptrTime(observedAt.Add(-2 * time.Hour)),
		Projection:       projection,
	}
	if withOutputFence {
		outcome.CooldownFence = fence
	}
	return outcome
}

// TestProjectionTransitionBranches 表驱动覆盖 applyProjectionUpdate 的全部
// transition 分支（写入列经 drain 全链生效并落 applied receipt）。
func TestProjectionTransitionBranches(t *testing.T) {
	cases := []struct {
		kind            string
		withOutputFence bool
		seedStatus      string
		expectStatus    string
		expectSched     int64
	}{
		{"health_success", false, "active", "active", 1},
		{"health_failure", false, "active", "active", 1},
		{"activation_error", false, "pending_test", "error", 0},
		{"temporary_unavailable", true, "active", "temporary_unavailable", 1},
		{"cooldown_defer", true, "temporary_unavailable", "temporary_unavailable", 1},
		{"cooldown_failure", true, "temporary_unavailable", "temporary_unavailable", 1},
		{"cooldown_error", true, "temporary_unavailable", "error", 0},
	}
	for _, item := range cases {
		t.Run(item.kind, func(t *testing.T) {
			fixture := newProjectionFixture(t)
			observed := projectionFixtureNow.Add(-time.Minute)
			observation := observed.Add(-time.Hour) // 与 wgTransitionOutcome 的 fence 派生一致
			fixture.seedAccount(t, map[string]any{
				"status":                                 item.seedStatus,
				"schedulable":                            1,
				"cooldown_until":                         projectionFixtureNow.Add(time.Hour).Format(time.RFC3339Nano),
				"cooldown_retest_observation_started_at": fenceGuardText(observation),
				"cooldown_retest_generation":             "gen-" + item.kind,
			})
			outcome := wgTransitionOutcome(item.kind, observed, item.withOutputFence, item.seedStatus)
			fixture.insertOutcome(observed, outcome)
			result := fixture.drain(t)
			if result.Processed != 1 {
				t.Fatalf("Processed = %d, 期望 1", result.Processed)
			}
			row := fixture.accountRow(t)
			if row["status"] != item.expectStatus {
				t.Fatalf("status = %v, 期望 %s", row["status"], item.expectStatus)
			}
			if row["schedulable"].(int64) != item.expectSched {
				t.Fatalf("schedulable = %v, 期望 %d", row["schedulable"], item.expectSched)
			}
			// 健康失败族写入健康错误列；带 last_error 的 transition 另写
			// last_error_*（health_failure 只写健康列，不写 last_error_*）。
			if item.kind == "health_failure" {
				// accountRow 只投影 fixture 固定列；健康错误列直查。
				var errorCode string
				if err := fixture.business.QueryRow(`SELECT last_health_check_error_code FROM accounts WHERE id = 'acct-1'`).Scan(&errorCode); err != nil {
					t.Fatal(err)
				}
				if errorCode != "upstream_5xx" {
					t.Fatalf("last_health_check_error_code = %q", errorCode)
				}
			}
			if item.kind == "activation_error" || item.kind == "temporary_unavailable" || item.kind == "cooldown_failure" {
				if row["last_error_code"] != "upstream_5xx" {
					t.Fatalf("last_error_code = %v", row["last_error_code"])
				}
			}
			disposition, reason := fixture.receipt(t, "outcome-"+item.kind)
			if disposition != "applied" {
				t.Fatalf("receipt = %s/%s, 期望 applied", disposition, reason)
			}
		})
	}
	// cooldown_error 无输出 fence 的 legacy 分支：清空冷却列。
	fixture := newProjectionFixture(t)
	observation := projectionFixtureNow.Add(-2 * time.Hour)
	fixture.seedAccount(t, map[string]any{
		"status":                                 "temporary_unavailable",
		"schedulable":                            1,
		"cooldown_retest_observation_started_at": fenceGuardText(observation),
		"cooldown_retest_generation":             "gen-cooldown_error",
	})
	observed := projectionFixtureNow.Add(-time.Minute)
	outcome := wgTransitionOutcome("cooldown_error", observed, false, "temporary_unavailable")
	outcome.Projection.CooldownFence = nil
	outcome.Projection.ExpectedCooldownFence = &CooldownFence{ObservationStartedAt: observation, Generation: "gen-cooldown_error"}
	fixture.insertOutcome(observed, outcome)
	if result := fixture.drain(t); result.Processed != 1 {
		t.Fatalf("legacy cooldown_error Processed = %d", result.Processed)
	}
	if row := fixture.accountRow(t); row["status"] != "error" || row["cooldown_until"] != "" {
		t.Fatalf("legacy cooldown_error 必须置 error 并清冷却: %v", row)
	}
}

// TestProjectionUnknownTransitionFailsClosed：未知 transition 使 drain 报错
// （游标不推进，失败 loud）。
func TestProjectionUnknownTransitionFailsClosed(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, map[string]any{"status": "active", "schedulable": 1})
	observed := projectionFixtureNow.Add(-time.Minute)
	outcome := wgTransitionOutcome("gopher_transition", observed, false, "active")
	outcome.Projection.ExpectedCooldownFence = nil
	outcome.Projection.CooldownFence = nil
	fixture.insertOutcome(observed, outcome)
	result, err := fixture.projector.DrainOnce(context.Background())
	if err != nil {
		t.Fatalf("未知 transition 经校验拒绝后不得传播错误: %v", err)
	}
	_ = result
	// 校验拒绝 → receipt 落 rejected（不是 applied）。
	disposition, _ := fixture.receipt(t, "outcome-gopher_transition")
	if disposition != "rejected" {
		t.Fatalf("未知 transition receipt = %s, 期望 rejected", disposition)
	}
}

// TestProjectionIgnoresRowsOlderThanCursor 锁定游标单调语义：晚到的旧
// observed_at 行不会被再次消费（时钟回拨不重放历史投影）。
func TestProjectionIgnoresRowsOlderThanCursor(t *testing.T) {
	fixture := newProjectionFixture(t)
	fixture.seedAccount(t, map[string]any{"status": "active", "schedulable": 1})
	late := projectionFixtureNow.Add(-time.Hour)
	fresh := projectionFixtureNow.Add(-time.Minute)
	// 先写入并消费较新的 outcome（游标停在 fresh）。
	fixture.insertOutcome(fresh, wgTransitionOutcome("health_success", fresh, false, "active"))
	if result := fixture.drain(t); result.Processed != 1 {
		t.Fatalf("首轮 Processed = %d", result.Processed)
	}
	// 追加一条更旧的 outcome（时钟回拨场景）：游标之后不再回放。
	fixture.insertOutcome(late, wgTransitionOutcome("health_failure", late, false, "active"))
	if result := fixture.drain(t); result.Processed != 0 {
		t.Fatalf("游标之前的旧行不得重放: %d", result.Processed)
	}
	if count := fixture.receiptCount(t, "outcome-health_failure"); count != 0 {
		t.Fatalf("旧行不得落 receipt: %d", count)
	}
}

// TestNewOutcomeProjectorGuards 覆盖投影器构造的守卫分支。
func TestNewOutcomeProjectorGuards(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: t.TempDir() + "/j.sqlite3"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := NewOutcomeProjector(nil, OutcomeProjectorConfig{}); err == nil {
		t.Fatal("缺 store 必须报错")
	}
	if _, err := NewOutcomeProjector(store, OutcomeProjectorConfig{}); err == nil {
		t.Fatal("缺业务库句柄必须报错")
	}
	handle, handleErr := NewProjectionBusinessDB(sqlOpenSQLiteFileForTest(t), false)
	if handleErr != nil {
		t.Fatal(handleErr)
	}
	base := OutcomeProjectorConfig{Business: handle, CredentialSecret: "s", PollInterval: time.Second, BatchSize: 10}
	if _, err := NewOutcomeProjector(store, OutcomeProjectorConfig{Business: handle}); err == nil {
		t.Fatal("缺凭据 secret 必须报错")
	}
	if _, err := NewOutcomeProjector(store, func() OutcomeProjectorConfig {
		config := base
		config.PollInterval = time.Millisecond
		return config
	}()); err == nil {
		t.Fatal("poll 下界必须报错")
	}
	if _, err := NewOutcomeProjector(store, func() OutcomeProjectorConfig {
		config := base
		config.ConsumerKey = strings.Repeat("k", 201)
		return config
	}()); err == nil {
		t.Fatal("超长 consumer key 必须报错")
	}
	if _, err := NewOutcomeProjector(store, base); err != nil {
		t.Fatalf("合法配置必须通过: %v", err)
	}
}

// sqlOpenSQLiteFileForTest 打开内存业务库并建最小 accounts 表。
func sqlOpenSQLiteFileForTest(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "biz.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, status TEXT DEFAULT '', config_revision INTEGER DEFAULT 1, dispatch_revision INTEGER DEFAULT 1, deleted_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestVerifyResponseSSEDispatch 覆盖 verifyResponse 的 SSE 分派（走顶层
// switch 而非各 verifier 直测）。
func TestVerifyResponseSSEDispatch(t *testing.T) {
	goodChat := "data: {\"choices\":[{\"delta\":{\"content\":\"juhe\"}}]}\n\ndata: [DONE]\n\n"
	if err := verifyResponse(wgSSEInput("chat_sse", "openai", "profile_openai_openai_v1"), []byte(goodChat)); err != nil {
		t.Fatalf("chat_sse 分派: %v", err)
	}
	goodResponses := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"juhe\"}\n\n" +
		"data: {\"type\":\"response.completed\"}\n\n"
	if err := verifyResponse(wgSSEInput("responses_sse", "openai", "profile_openai_openai_v1"), []byte(goodResponses)); err != nil {
		t.Fatalf("responses_sse 分派: %v", err)
	}
	goodMessages := "data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"juhe\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	if err := verifyResponse(wgSSEInput("messages_sse", "anthropic", "profile_anthropic_anthropic_v1"), []byte(goodMessages)); err != nil {
		t.Fatalf("messages_sse 分派: %v", err)
	}
	goodGemini := "data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"juhe\"}]}}]}\n\n"
	if err := verifyResponse(wgSSEInput("generate_content_sse", "gemini", "profile_gemini_native_v1beta"), []byte(goodGemini)); err != nil {
		t.Fatalf("generate_content_sse 分派: %v", err)
	}
	goodInteractions := "data: {\"status\":\"completed\"}\n\ndata: {\"interaction\":{\"output\":\"juhe\"}}\n\ndata: [DONE]\n\n"
	if err := verifyResponse(wgSSEInput("interactions_sse", "gemini", "profile_gemini_native_v1beta"), []byte(goodInteractions)); err != nil {
		t.Fatalf("interactions_sse 分派: %v", err)
	}
	// OAuth 过期分支（validateInput）。
	oauth := wgSSEInput("responses_sse", "gpt", "profile_gpt_openai_v1")
	oauth.Type = "oauth"
	oauth.OAuthExpiresAt = ptrTime(time.Now().Add(-time.Minute))
	if err := validateInput(oauth, ProbeOptions{}); err == nil {
		t.Fatal("OAuth token 过期必须报错")
	}
}

// TestCooldownDeferBounds 覆盖冷却推迟时长的边界钳制（负步长、下限、上限）。
func TestCooldownDeferBounds(t *testing.T) {
	if got := cooldownDefer(-1, 10*time.Second, time.Hour); got < 3*time.Second {
		t.Fatalf("负步长必须钳到最小推迟: %v", got)
	}
	if got := cooldownDefer(0, time.Second, time.Hour); got != 3*time.Second {
		t.Fatalf("过小 base 必须钳到 3s: %v", got)
	}
	if got := cooldownDefer(0, 48*time.Hour, time.Hour); got != time.Hour {
		t.Fatalf("base 超过 maximum 必须钳到 maximum: %v", got)
	}
}

// TestSignedPayloadVerificationBranches 覆盖签名载荷校验的损坏分支。
func TestSignedPayloadVerificationBranches(t *testing.T) {
	keys := map[string][]byte{"runtime-v1": []byte("0123456789abcdef0123456789abcdef")}
	if _, err := VerifySignedPayload([]byte("not-json"), keys); err == nil {
		t.Fatal("非法 envelope 必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"hs256_v1","key_id":"","payload":"","signature":""}`), keys); err == nil {
		t.Fatal("缺 key ID 必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"hs256_v1","key_id":"runtime-v1","payload":"!!!","signature":""}`), keys); err == nil {
		t.Fatal("非法 payload 编码必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"hs256_v1","key_id":"runtime-v1","payload":"e30","signature":"!!!"}`), keys); err == nil {
		t.Fatal("非法 signature 编码必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"rs256","key_id":"runtime-v1","payload":"e30","signature":"AA"}`), keys); err == nil {
		t.Fatal("不支持算法必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"hs256_v1","key_id":"missing","payload":"e30","signature":"AA"}`), keys); err == nil {
		t.Fatal("未知 key 必须报错")
	}
	if _, err := VerifySignedPayload([]byte(`{"algorithm":"hs256_v1","key_id":"runtime-v1","payload":"e30","signature":"AA"}`), keys); err == nil {
		t.Fatal("签名不匹配必须报错")
	}
}
