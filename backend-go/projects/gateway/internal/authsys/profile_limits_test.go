package authsys

import (
	"net/http"
	"testing"
	"time"
)

func TestProfileIncludesEffectiveRequestLimits(t *testing.T) {
	deps, _, server := newTestEnv(t)
	perDay := 42
	expiresOn := "2026-09-05"
	if _, err := deps.Accounts.Create(nil, CreateInput{
		Username:           "limited",
		DisplayName:        "Limited",
		Password:           "limited-pass",
		Role:               "user",
		MustChangePassword: boolPtr(false),
		RequestLimits:      &UserRequestLimits{PerDay: &perDay, ExpiresOn: &expiresOn},
	}); err != nil {
		t.Fatal(err)
	}
	deps.Now = func() time.Time {
		return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	}

	cookie := login(t, server, "limited", "limited-pass")
	response, payload := getJSON(t, server, "/__aisys__/api/auth/profile", cookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("profile: %d %v", response.StatusCode, payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("profile data missing: %v", payload)
	}
	limits, ok := data["effectiveRequestLimits"].(map[string]any)
	if !ok {
		t.Fatalf("effectiveRequestLimits missing: %v", data)
	}
	perMinute, ok := limits["perMinute"].(map[string]any)
	if !ok || perMinute["limit"] != float64(0) || perMinute["source"] != "global" {
		t.Fatalf("per-minute global limit mismatch: %v", limits["perMinute"])
	}
	perDayResult, ok := limits["perDay"].(map[string]any)
	if !ok || perDayResult["limit"] != float64(42) || perDayResult["source"] != "user" {
		t.Fatalf("per-day user limit mismatch: %v", limits["perDay"])
	}
	if limits["timezone"] != "Asia/Shanghai" || limits["overrideActive"] != true || limits["overrideExpiresOn"] != expiresOn {
		t.Fatalf("override metadata mismatch: %v", limits)
	}
}

func TestProfileIncludesEffectiveAccountLimits(t *testing.T) {
	deps, _, server := newTestEnv(t)
	aiLimit := 25
	expeditedLimit := 0
	if _, err := deps.Accounts.Create(nil, CreateInput{
		Username:               "capped",
		DisplayName:            "Capped",
		Password:               "capped-pass",
		Role:                   "user",
		MustChangePassword:     boolPtr(false),
		AIAccountLimit:         &aiLimit,
		ExpeditedAccountLimit:  &expeditedLimit,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Accounts.Create(nil, CreateInput{
		Username:           "inherit",
		DisplayName:        "Inherit",
		Password:           "inherit-pass",
		Role:               "user",
		MustChangePassword: boolPtr(false),
	}); err != nil {
		t.Fatal(err)
	}

	// 覆盖用户：AI 上限取覆盖值；特供名额取覆盖值（0 = 禁止，原样透出）。
	cookie := login(t, server, "capped", "capped-pass")
	response, payload := getJSON(t, server, "/__aisys__/api/auth/profile", cookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("profile: %d %v", response.StatusCode, payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("profile data missing: %v", payload)
	}
	aiAccountLimit, ok := data["effectiveAiAccountLimit"].(map[string]any)
	if !ok || aiAccountLimit["limit"] != float64(25) || aiAccountLimit["source"] != "user" {
		t.Fatalf("ai account limit override mismatch: %v", data["effectiveAiAccountLimit"])
	}
	expeditedAccountLimit, ok := data["effectiveExpeditedAccountLimit"].(map[string]any)
	if !ok || expeditedAccountLimit["limit"] != float64(0) || expeditedAccountLimit["source"] != "user" {
		t.Fatalf("expedited account limit override mismatch: %v", data["effectiveExpeditedAccountLimit"])
	}

	// 未覆盖用户：AI 上限回退全局设置（未配置时取 schema 缺省 100）；
	// 特供名额回退内置默认 3。
	inheritCookie := login(t, server, "inherit", "inherit-pass")
	inheritResponse, inheritPayload := getJSON(t, server, "/__aisys__/api/auth/profile", inheritCookie)
	if inheritResponse.StatusCode != http.StatusOK {
		t.Fatalf("inherit profile: %d %v", inheritResponse.StatusCode, inheritPayload)
	}
	inheritData, ok := inheritPayload["data"].(map[string]any)
	if !ok {
		t.Fatalf("inherit profile data missing: %v", inheritPayload)
	}
	inheritAI, ok := inheritData["effectiveAiAccountLimit"].(map[string]any)
	if !ok || inheritAI["limit"] != float64(100) || inheritAI["source"] != "global" {
		t.Fatalf("ai account limit global fallback mismatch: %v", inheritData["effectiveAiAccountLimit"])
	}
	inheritExpedited, ok := inheritData["effectiveExpeditedAccountLimit"].(map[string]any)
	if !ok || inheritExpedited["limit"] != float64(3) || inheritExpedited["source"] != "global" {
		t.Fatalf("expedited account limit default mismatch: %v", inheritData["effectiveExpeditedAccountLimit"])
	}
}

func TestProfileExpiredRequestLimitOverrideFallsBackToGlobal(t *testing.T) {
	deps, _, server := newTestEnv(t)
	perDay := 42
	expiresOn := "2026-09-03"
	if _, err := deps.Accounts.Create(nil, CreateInput{
		Username:           "expired",
		DisplayName:        "Expired",
		Password:           "expired-pass",
		Role:               "user",
		MustChangePassword: boolPtr(false),
		RequestLimits:      &UserRequestLimits{PerDay: &perDay, ExpiresOn: &expiresOn},
	}); err != nil {
		t.Fatal(err)
	}
	deps.Now = func() time.Time {
		return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	}

	cookie := login(t, server, "expired", "expired-pass")
	response, payload := getJSON(t, server, "/__aisys__/api/auth/profile", cookie)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("profile: %d %v", response.StatusCode, payload)
	}
	limits := payload["data"].(map[string]any)["effectiveRequestLimits"].(map[string]any)
	perDayResult := limits["perDay"].(map[string]any)
	if perDayResult["limit"] != float64(0) || perDayResult["source"] != "global" {
		t.Fatalf("expired override must fall back to global: %v", perDayResult)
	}
	if limits["overrideActive"] != false || limits["overrideExpiresOn"] != expiresOn {
		t.Fatalf("expired override metadata mismatch: %v", limits)
	}
}

func TestProfileRequestLimitExpiresOnWithoutWindowIsIgnored(t *testing.T) {
	deps, _, _ := newTestEnv(t)
	expiresOn := "2026-09-05"
	deps.Now = func() time.Time {
		return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	}
	effective, err := deps.effectiveUserRequestLimits(nil, &UserRequestLimits{ExpiresOn: &expiresOn})
	if err != nil {
		t.Fatal(err)
	}
	if effective.OverrideActive || effective.OverrideExpiresOn != nil {
		t.Fatalf("expiresOn without a window must be ignored: %+v", effective)
	}
}

func TestProfileRequestLimitInvalidStoredOverrideFallsBackToUnset(t *testing.T) {
	tooLarge := maxUserRequestLimit + 1
	invalidDate := "2026-02-31"
	for name, limits := range map[string]*UserRequestLimits{
		"too large":    {PerDay: &tooLarge},
		"invalid date": {PerDay: func() *int { value := 10; return &value }(), ExpiresOn: &invalidDate},
		"legacy empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			if normalized := normalizedProfileRequestLimitOverrides(limits); normalized != nil {
				t.Fatalf("invalid stored override must be ignored: %+v", normalized)
			}
		})
	}
}

func TestProfileRequestLimitEmptyExpiresOnIsOmitted(t *testing.T) {
	perDay := 10
	empty := ""
	normalized := normalizedProfileRequestLimitOverrides(&UserRequestLimits{PerDay: &perDay, ExpiresOn: &empty})
	if normalized == nil || normalized.ExpiresOn != nil {
		t.Fatalf("empty expiresOn must be omitted: %+v", normalized)
	}
}

func TestParseUserRequestLimitsMatchesNodeNormalization(t *testing.T) {
	for raw, wantNil := range map[string]bool{
		`{}`:                                    true,
		`null`:                                  true,
		`{"expiresOn":"2026-09-05"}`:            true,
		`{"perDay":-1}`:                         true,
		`{"perDay":1000000001}`:                 true,
		`{"perDay":1,"expiresOn":"2026-02-31"}`: true,
		`{"perDay":1.0}`:                        false,
		`{"perDay":4.2e1}`:                      false,
		`{"perDay":42,"perWeek":null}`:          true,
	} {
		parsed := parseUserRequestLimits(raw)
		if (parsed == nil) != wantNil {
			t.Fatalf("%s: parsed=%+v want nil=%v", raw, parsed, wantNil)
		}
	}
}

func TestMarshalRequestLimitsMatchesNodeNormalization(t *testing.T) {
	perDay := 1
	empty := ""
	if encoded, err := marshalRequestLimits(&UserRequestLimits{ExpiresOn: &empty}); err != nil || encoded != nil {
		t.Fatalf("empty override must serialize as nil: encoded=%v err=%v", encoded, err)
	}
	if encoded, err := marshalRequestLimits(&UserRequestLimits{PerDay: &perDay, ExpiresOn: &empty}); err != nil || encoded == nil || *encoded != `{"perDay":1}` {
		t.Fatalf("empty expiresOn must be omitted: encoded=%v err=%v", encoded, err)
	}
	invalidDate := "2026-02-31"
	if _, err := marshalRequestLimits(&UserRequestLimits{PerDay: &perDay, ExpiresOn: &invalidDate}); err == nil {
		t.Fatal("invalid calendar date must be rejected")
	}
}
