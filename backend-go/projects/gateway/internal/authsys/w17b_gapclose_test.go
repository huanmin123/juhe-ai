package authsys

// w17b 覆盖收尾：94.9% → ≥95% 硬门槛的定向补测。只触达既有私有分支，
// 不改动生产代码；复用既有测试夹具（newTestEnv/seedAccount/wlFakeSettings/
// wlFakeStateStore）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestW17BSmallPureArms(t *testing.T) {
	// noopSink：K4 未绑定 F4 时的空操作 sink。
	noopSink{}.Record(OperationLogEntry{Module: "w17b"}, nil)

	// nowPlusOneMilli：previous 非法时直接返回 now。
	base := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if got := nowPlusOneMilli(base, "not-rfc3339"); got != base.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("非法 previous 必须回退 now: %s", got)
	}

	// verifyNodePassword：digest 段长度 %4==1 时 base64 解码必须报错。
	if _, err := verifyNodePassword("pw", "pbkdf2$sha512$120000$salt$abcde"); err == nil {
		t.Fatal("非法 digest 编码必须报错")
	}

	// renderSharedCaptchaImage：字母表之外的字符走 glyph 缺失臂。
	if image := renderSharedCaptchaImage("?"); !strings.HasPrefix(image, "data:image/png;base64,") {
		t.Fatalf("glyph 缺失臂仍必须输出图片: %q", image[:min(40, len(image))])
	}
}

func TestW17BPatchMeDisplayNameConflict(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	account := seedAccount(t, deps, "w17aown", "pass-1234", "admin")
	seedAccount(t, deps, "cris", "pass-1234", "user")

	request := wlJSONRequest(http.MethodPatch, "/me", `{"displayName":"cris_name"}`)
	request = withWlAuth(request, account.ID, "w17aown", "admin", false)
	recorder := httptest.NewRecorder()
	deps.patchMe(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("重名显示名称必须 409: %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "用户名称已存在") {
		t.Fatalf("冲突消息必须透传: %s", recorder.Body.String())
	}
}

func TestW17BLoadUserRequestLimitSettingsArms(t *testing.T) {
	build := func(mutate func(values map[string]string)) *Deps {
		values := wlLimitSettings()
		mutate(values)
		return &Deps{Settings: &wlFakeSettings{values: values}, Now: time.Now}
	}
	cases := map[string]struct {
		mutate func(values map[string]string)
	}{
		"perMinute 非数字 JSON": {mutate: func(v map[string]string) { v["gatewayUserRequestLimitPerMinute"] = `"nope"` }},
		"perDay 非法 JSON":     {mutate: func(v map[string]string) { v["gatewayUserRequestLimitPerDay"] = "abc" }},
		"perWeek 非整数":        {mutate: func(v map[string]string) { v["gatewayUserRequestLimitPerWeek"] = "1.5" }},
		"timezone 非字符串 JSON": {mutate: func(v map[string]string) { v["usageStatsTimezone"] = "123" }},
		"timezone 空白":        {mutate: func(v map[string]string) { v["usageStatsTimezone"] = `"  "` }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			deps := build(tc.mutate)
			if _, err := deps.loadUserRequestLimitSettings(context.Background()); err == nil {
				t.Fatal("非法设置必须报错")
			}
		})
	}
}

func TestW17BLoginGuardRecordAttemptBlockedArm(t *testing.T) {
	store := newWlFakeStateStore()
	now := time.Unix(1000, 0)
	guard := NewSharedLoginGuard(store, func() time.Time { return now })
	// 预置一个仍有效的 IP 锁（lockedUntil 为毫秒时间戳，须大于 now 的 1e6）：
	// recordAttempt 必须短路返回。
	store.preset("login:ip:10.9.9.9:lock", int64(2_000_000))
	blocked, retry, message, err := guard.Failed("10.9.9.9", "ignored-user")
	if err != nil || !blocked {
		t.Fatalf("已锁定 IP 必须短路: blocked=%v err=%v", blocked, err)
	}
	if message != "尝试过于频繁，请稍后再试" || retry <= 0 {
		t.Fatalf("message=%q retry=%d", message, retry)
	}
}

func TestW17BSessionDevAutoLoginRateLimitArm(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	seedAccount(t, deps, "w17adev", "pass-1234", "admin")
	deps.DevAutoLoginUsername = "w17adev"
	rejected := false
	deps.AuthenticatedRateLimit = func(w http.ResponseWriter, r *http.Request, systemAccountID string) bool {
		rejected = true
		w.WriteHeader(http.StatusTooManyRequests)
		return false
	}
	handler := deps.RequireSession(false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("限流拒绝后不得进入下游")
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/anything", nil))
	if !rejected || recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("开发自动登录必须经过限流: rejected=%v status=%d", rejected, recorder.Code)
	}
}
