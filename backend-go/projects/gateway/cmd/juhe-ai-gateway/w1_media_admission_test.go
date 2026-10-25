package main

// 冷却到期媒体准入臂与成功回写臂的覆盖（媒体账户生命周期定案表 R1）：
//
//	行级判定    chainPhysicalAccountAvailable 的 mediaAdmission 臂——仅
//	            video_generation 请求上的 video_create 账户可恢复准入
//	            status ∈ 四值 ∧ 冷却已过期
//	            或为空；schedulable/套餐过期门不被豁免。
//	候选窗口    ListOpenAIAccountsForGroupResult 的 SQL 臂与行级闸一致性：
//	            chat_completions/空族请求候选行为零变化。
//	成功回写    mediaJobsRuntime.markMediaAccountActiveAfterJobSuccess 的 CAS：
//	            video completed 后滞留 pending_test/冷却态的视频账户搬回
//	            active，chat 域行、未过期冷却行与已 active 行零写入。

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayupstream"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func mediaAdmissionRow(status string, mode, cooldown string) *chainCandidateRow {
	row := &chainCandidateRow{ID: "acc_media", Status: status, Schedulable: 1}
	if mode != "" {
		row.HealthCheckEndpointMode = sql.NullString{String: mode, Valid: true}
	}
	if cooldown != "" {
		row.CooldownUntil = sql.NullString{String: cooldown, Valid: true}
	}
	return row
}

// TestW1MediaAdmissionRowLevelArm 钉住行级准入臂的谓词与门：armed 只放行
// video_create 账户四状态 + 过期/空冷却；音频模式、chat 模式、未过期冷却、禁调度、套餐过期、
// disabled 都不放行；未武装（chat/images/空族请求）行为零变化。
func TestW1MediaAdmissionRowLevelArm(t *testing.T) {
	now := int64(1700000000000) // 2023-11-14T22:13:20Z
	past := "2020-01-01T00:00:00.000Z"
	future := "2999-01-01T00:00:00.000Z"

	armed := []struct {
		name string
		row  *chainCandidateRow
	}{
		{"pending_test 空冷却（存量滞留治愈）", mediaAdmissionRow("pending_test", "video_create", "")},
		{"rate_limited 过期冷却", mediaAdmissionRow("rate_limited", "video_create", past)},
		{"temporary_unavailable 过期冷却", mediaAdmissionRow("temporary_unavailable", "video_create", past)},
		{"active 过期冷却", mediaAdmissionRow("active", "video_create", past)},
	}
	for _, testCase := range armed {
		t.Run("armed/"+testCase.name, func(t *testing.T) {
			ok, err := chainPhysicalAccountAvailable(testCase.row, now, false, true)
			if err != nil || !ok {
				t.Fatalf("armed 准入臂应放行: got (%v, %v)", ok, err)
			}
		})
	}

	denied := []struct {
		name string
		row  *chainCandidateRow
	}{
		{"rate_limited 未过期冷却", mediaAdmissionRow("rate_limited", "video_create", future)},
		{"pending_test 未过期冷却（防御形态）", mediaAdmissionRow("pending_test", "video_create", future)},
		{"audio_speech 恢复态不准入", mediaAdmissionRow("rate_limited", "audio_speech", past)},
		{"chat 模式 pending_test", mediaAdmissionRow("pending_test", "chat_json", "")},
		{"images 模式 pending_test", mediaAdmissionRow("pending_test", "images_json", "")},
		{"disabled 状态", mediaAdmissionRow("disabled", "audio_speech", "")},
		{"error 状态", mediaAdmissionRow("error", "audio_speech", "")},
		{"禁调度", func() *chainCandidateRow {
			row := mediaAdmissionRow("pending_test", "audio_speech", "")
			row.Schedulable = 0
			return row
		}()},
		{"套餐过期", func() *chainCandidateRow {
			row := mediaAdmissionRow("pending_test", "audio_speech", "")
			row.AccountExpiresAt = sql.NullString{String: "2000-01-01T00:00:00.000Z", Valid: true}
			return row
		}()},
	}
	for _, testCase := range denied {
		t.Run("armed-denied/"+testCase.name, func(t *testing.T) {
			if ok, err := chainPhysicalAccountAvailable(testCase.row, now, false, true); err != nil || ok {
				t.Fatalf("armed 准入臂必须拒绝: got (%v, %v)", ok, err)
			}
		})
	}

	// 未武装（chat/images/空族请求）行级行为零变化：媒体账户滞留态按既有
	// 可用性闸拒绝。注意 includeUnavailable=true 的恢复专用窗口（preflight
	// 等待读使用）按既有语义放行 rate_limited/temporary_unavailable 行（冷却
	// 由调用方另判），本用例只钉 pending_test 不在三态白名单内。
	for _, family := range []struct {
		name               string
		mediaAdmission     bool
		includeUnavailable bool
	}{
		{"chat 请求", false, false},
		{"chat includeUnavailable", false, true},
	} {
		t.Run("unarmed/"+family.name, func(t *testing.T) {
			if ok, err := chainPhysicalAccountAvailable(mediaAdmissionRow("pending_test", "audio_speech", ""), now, family.includeUnavailable, family.mediaAdmission); err != nil || ok {
				t.Fatalf("未武装 pending_test 必须拒绝: got (%v, %v)", ok, err)
			}
			ok, err := chainPhysicalAccountAvailable(mediaAdmissionRow("rate_limited", "audio_speech", past), now, family.includeUnavailable, family.mediaAdmission)
			if err != nil {
				t.Fatalf("未武装 rate_limited 判定报错: %v", err)
			}
			if ok != family.includeUnavailable {
				t.Fatalf("未武装 rate_limited 冷却过期：includeUnavailable=%t 必须为 %t（恢复窗口既有语义放行、默认窗拒绝），got %t", family.includeUnavailable, family.includeUnavailable, ok)
			}
		})
	}
}

// TestW1MediaAdmissionArmedFamilies 钉住请求侧词表：仅 video_generation 武装，
// chat_completions/responses/messages/gemini 族与空族不武装（chat/images 与
// /v1/audio/jobs 的候选行为零变化）。
func TestW1MediaAdmissionArmedFamilies(t *testing.T) {
	armedFamilies := map[string]bool{
		"video_generation": true,
		"tts":              false,
		"":                 false,
		"chat_completions": false,
		"responses":        false,
		"messages":         false,
		"generate_content": false,
		"images":           false,
	}
	for family, want := range armedFamilies {
		if got := chainMediaAdmissionArmed(family); got != want {
			t.Fatalf("chainMediaAdmissionArmed(%q) = %v, want %v", family, got, want)
		}
	}
}

func TestMediaJobPollRecoveryAdmissionIsVideoOnly(t *testing.T) {
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	base := &chainMediaJobAccount{
		Status: "rate_limited", CapabilityDomain: "video",
		HealthCheckEndpointMode: "video_create", CooldownUntil: "2026-10-13T00:00:00.000Z",
	}
	videoJob := &gatewaymedia.MediaJobRecord{Kind: gatewaymedia.JobKindVideo}
	audioJob := &gatewaymedia.MediaJobRecord{Kind: gatewaymedia.JobKindAudioTranscription}
	if !chainMediaJobAccountPollEligible(base, videoJob, now) {
		t.Fatal("expired video cooldown should allow its async video job poll")
	}
	if chainMediaJobAccountPollEligible(base, audioJob, now) {
		t.Fatal("long audio transcription must remain active-only")
	}
	future := *base
	future.CooldownUntil = "2026-10-15T00:00:00.000Z"
	if chainMediaJobAccountPollEligible(&future, videoJob, now) {
		t.Fatal("video account with future cooldown must not be hydrated")
	}
	disabled := *base
	disabled.Status = "disabled"
	if chainMediaJobAccountPollEligible(&disabled, videoJob, now) {
		t.Fatal("disabled video account must not be hydrated")
	}
	chatMode := *base
	chatMode.HealthCheckEndpointMode = "audio_speech"
	if chainMediaJobAccountPollEligible(&chatMode, videoJob, now) {
		t.Fatal("non-video endpoint mode must not use video recovery admission")
	}
	activeAudio := *base
	activeAudio.Status = "active"
	if !chainMediaJobAccountPollEligible(&activeAudio, audioJob, now) {
		t.Fatal("active audio task polling must preserve existing behavior")
	}
}

// TestW1MediaAdmissionCandidateWindow 是候选窗口的 DB 级一致性覆盖：同一
// rate_limited + 过期冷却的 video_create 账户只在 video_generation 查询入选；
// TTS/chat_completions/空族不可见；未过期冷却不可入选；pending_test 滞留行
// 只在视频创建查询治愈入选。
func TestW1MediaAdmissionCandidateWindow(t *testing.T) {
	fixture := newChainFixture(t)
	db := fixture.db
	if _, err := db.Exec(fixtureBind(`UPDATE accounts SET health_check_endpoint_mode = 'video_create' WHERE id = ?`), fixture.accountID); err != nil {
		t.Fatalf("seed media mode: %v", err)
	}

	countAccounts := func(opts gatewayruntimecache.OpenAIAccountsForGroupOptions) int {
		t.Helper()
		result, err := fixture.selector.ListOpenAIAccountsForGroupResult(context.Background(), fixture.groupID, fixture.systemAccount, opts)
		if err != nil {
			t.Fatalf("selector: %v", err)
		}
		return len(result.Accounts)
	}

	// rate_limited + 过期冷却：视频族准入；TTS/chat/空族零变化。
	if _, err := db.Exec(fixtureBind(`UPDATE accounts SET status = 'rate_limited',
		cooldown_until = '2020-01-01T00:00:00.000Z' WHERE id = ?`), fixture.accountID); err != nil {
		t.Fatalf("seed rate_limited: %v", err)
	}
	if got := countAccounts(gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedEndpointFamily: "tts"}); got != 0 {
		t.Fatalf("同步 TTS 不应获得视频异步任务恢复准入: got %d", got)
	}
	if got := countAccounts(gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedEndpointFamily: "video_generation"}); got != 1 {
		t.Fatalf("video_generation 族应准入过期冷却媒体账户: got %d", got)
	}
	if got := countAccounts(gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedEndpointFamily: "chat_completions"}); got != 0 {
		t.Fatalf("chat 族不得改变候选行为: got %d", got)
	}
	if got := countAccounts(gatewayruntimecache.OpenAIAccountsForGroupOptions{}); got != 0 {
		t.Fatalf("空族（chat 缺省）不得改变候选行为: got %d", got)
	}

	// 未过期冷却：即使媒体族也不准入。
	if _, err := db.Exec(fixtureBind(`UPDATE accounts SET cooldown_until = '2999-01-01T00:00:00.000Z' WHERE id = ?`), fixture.accountID); err != nil {
		t.Fatalf("seed future cooldown: %v", err)
	}
	if got := countAccounts(gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedEndpointFamily: "tts"}); got != 0 {
		t.Fatalf("未过期冷却媒体账户不得入选: got %d", got)
	}

	// pending_test 滞留行（cooldown NULL）：媒体族治愈入选，chat 族不可见。
	if _, err := db.Exec(fixtureBind(`UPDATE accounts SET status = 'pending_test', cooldown_until = NULL WHERE id = ?`), fixture.accountID); err != nil {
		t.Fatalf("seed pending_test: %v", err)
	}
	if got := countAccounts(gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedEndpointFamily: "tts"}); got != 0 {
		t.Fatalf("同步 TTS 不应治愈入选 pending_test 滞留行: got %d", got)
	}
	if got := countAccounts(gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedEndpointFamily: "video_generation"}); got != 1 {
		t.Fatalf("视频创建族应治愈入选 pending_test 滞留行: got %d", got)
	}
	if got := countAccounts(gatewayruntimecache.OpenAIAccountsForGroupOptions{RequestedEndpointFamily: "chat_completions"}); got != 0 {
		t.Fatalf("chat 族不得入选 pending_test 滞留行: got %d", got)
	}
}

// newMediaWriteBackRuntime 建独立 accounts 表 + mediaJobsRuntime（回写 CAS
// 直连单测；列集覆盖 UPDATE 语句触达的全部投影列）。
func newMediaWriteBackRuntime(t *testing.T) (*mediaJobsRuntime, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "media-writeback.sqlite3"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE accounts (
		id TEXT PRIMARY KEY, capability_domain TEXT NOT NULL DEFAULT 'chat', health_check_endpoint_mode TEXT,
		status TEXT NOT NULL,
		schedulable INTEGER NOT NULL DEFAULT 1, cooldown_until TEXT, next_health_check_at TEXT,
		last_error_code TEXT, last_error_message TEXT, last_error_trace_id TEXT,
		cooldown_retest_failure_count INTEGER NOT NULL DEFAULT 0,
		cooldown_retest_observation_started_at TEXT, cooldown_retest_generation TEXT,
		cooldown_retest_last_at TEXT, cooldown_retest_last_status_code INTEGER,
		health_check_failure_count INTEGER NOT NULL DEFAULT 0, health_check_failure_started_at TEXT,
		last_health_check_error_code TEXT, last_health_check_error_message TEXT,
		updated_at TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatalf("create accounts: %v", err)
	}
	return &mediaJobsRuntime{db: db, transport: gatewayupstream.TransportDeps{}}, db
}

const mediaWriteBackSeedRow = `INSERT INTO accounts (id, capability_domain, status, schedulable, cooldown_until,
	next_health_check_at, last_error_code, last_error_message, last_error_trace_id,
	cooldown_retest_failure_count, cooldown_retest_observation_started_at, cooldown_retest_generation,
	cooldown_retest_last_at, cooldown_retest_last_status_code,
	health_check_failure_count, health_check_failure_started_at,
	last_health_check_error_code, last_health_check_error_message, updated_at)
	VALUES (?, ?, ?, ?, ?, '2026-01-01T00:00:00.000Z', 'upstream_error', 'boom', 'trace-1',
		2, '2026-01-01T00:00:00.000Z', 'cooldown:gen-1', '2026-01-01T00:01:00.000Z', 503,
		3, '2026-01-01T00:00:00.000Z', 'upstream_error', 'boom', '2026-01-01T00:00:00.000Z')`

func mediaWriteBackSnapshot(t *testing.T, db *sql.DB, id string) (string, int, sql.NullString, sql.NullString, string, int) {
	t.Helper()
	var status string
	var schedulable int
	var cooldown, nextCheck sql.NullString
	var updatedAt string
	var retestCount int
	if err := db.QueryRow(`SELECT status, schedulable, cooldown_until, next_health_check_at,
		updated_at, cooldown_retest_failure_count FROM accounts WHERE id = ?`, id).
		Scan(&status, &schedulable, &cooldown, &nextCheck, &updatedAt, &retestCount); err != nil {
		t.Fatalf("read back: %v", err)
	}
	return status, schedulable, cooldown, nextCheck, updatedAt, retestCount
}

// TestW1MediaJobSuccessReactivatesAccount 钉住成功回写 CAS：视频 completed 后
// video 域滞留行（pending_test 空冷却 / rate_limited 过期冷却）搬回 active；
// 长音频、chat 域、未过期冷却与已 active 行不被回写。
func TestW1MediaJobSuccessReactivatesAccount(t *testing.T) {
	runtime, db := newMediaWriteBackRuntime(t)
	ctx := context.Background()

	seed := func(id, domain, mode, status, cooldown string) {
		t.Helper()
		var cooldownArg any
		if cooldown != "" {
			cooldownArg = cooldown
		}
		if _, err := db.Exec(fixtureBind(`INSERT INTO accounts (id, capability_domain, health_check_endpoint_mode, status,
			schedulable, cooldown_until, next_health_check_at, last_error_code, last_error_message,
			last_error_trace_id, cooldown_retest_failure_count, cooldown_retest_observation_started_at,
			cooldown_retest_generation, cooldown_retest_last_at, cooldown_retest_last_status_code,
			health_check_failure_count, health_check_failure_started_at, last_health_check_error_code,
			last_health_check_error_message, updated_at)
			VALUES (?, ?, ?, ?, 1, ?, '2026-01-01T00:00:00.000Z', 'upstream_error', 'boom', 'trace-1',
			2, '2026-01-01T00:00:00.000Z', 'cooldown:gen-1', '2026-01-01T00:01:00.000Z', 503,
			3, '2026-01-01T00:00:00.000Z', 'upstream_error', 'boom', '2026-01-01T00:00:00.000Z')`),
			id, domain, mode, status, cooldownArg); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("acc_audio_pending", "audio", "audio_speech", "pending_test", "")
	seed("acc_video_ratelimited", "video", "video_create", "rate_limited", "2020-01-01T00:00:00.000Z")
	seed("acc_video_cooling", "video", "video_create", "temporary_unavailable", "2999-01-01T00:00:00.000Z")
	seed("acc_chat_pending", "chat", "chat_json", "pending_test", "")
	seed("acc_audio_active", "audio", "audio_speech", "active", "")

	runtime.markMediaAccountActiveAfterJobSuccess(ctx, "acc_video_ratelimited")
	runtime.markMediaAccountActiveAfterJobSuccess(ctx, "acc_video_cooling")
	runtime.markMediaAccountActiveAfterJobSuccess(ctx, "acc_chat_pending")
	runtime.markMediaAccountActiveAfterJobSuccess(ctx, "acc_audio_active")

	for _, id := range []string{"acc_video_ratelimited"} {
		status, schedulable, cooldown, nextCheck, _, retestCount := mediaWriteBackSnapshot(t, db, id)
		if status != "active" || schedulable != 1 {
			t.Fatalf("%s 应回写 active+可调度: got (%s, %d)", id, status, schedulable)
		}
		if cooldown.Valid || nextCheck.Valid || retestCount != 0 {
			t.Fatalf("%s 应清空冷却/排期/retest 投影: got (%v, %v, %d)", id, cooldown, nextCheck, retestCount)
		}
		var lastErrorCode, lastMessage, healthFailureCount int
		if err := db.QueryRow(`SELECT COUNT(last_error_code), COUNT(last_error_message),
			health_check_failure_count FROM accounts WHERE id = ?`, id).
			Scan(&lastErrorCode, &lastMessage, &healthFailureCount); err != nil {
			t.Fatal(err)
		}
		if lastErrorCode != 0 || lastMessage != 0 || healthFailureCount != 0 {
			t.Fatalf("%s 应清空错误与失败计数投影: got (%d, %d, %d)", id, lastErrorCode, lastMessage, healthFailureCount)
		}
	}
	if status, _, _, _, updatedAt, _ := mediaWriteBackSnapshot(t, db, "acc_audio_pending"); status != "pending_test" || updatedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("长音频转写账户不应使用视频任务 CAS: got (%s, %s)", status, updatedAt)
	}

	// 未过期冷却行：CAS 守卫拒绝回写（保持冷却态原样）。
	if status, _, cooldown, _, updatedAt, _ := mediaWriteBackSnapshot(t, db, "acc_video_cooling"); status != "temporary_unavailable" || !cooldown.Valid || updatedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("未过期冷却行不得回写: got (%s, %v, %s)", status, cooldown, updatedAt)
	}
	// chat 域滞留行不在回写范围。
	if status, _, _, _, updatedAt, _ := mediaWriteBackSnapshot(t, db, "acc_chat_pending"); status != "pending_test" || updatedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("chat 域行不得回写: got %s (%s)", status, updatedAt)
	}
	// 已 active 行零写入（updated_at 不变）。
	if status, _, _, _, updatedAt, _ := mediaWriteBackSnapshot(t, db, "acc_audio_active"); status != "active" || updatedAt != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("active 行零写入: got (%s, %s)", status, updatedAt)
	}
}
