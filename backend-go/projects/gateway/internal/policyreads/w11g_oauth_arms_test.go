package policyreads

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

func w11gOAuthStore(t *testing.T, steps []w11gPStep) *OAuthStore {
	t.Helper()
	store, err := NewOAuthStore(w11gOpenScripted(t, steps), false, func() time.Time { return w11gClock }, nil, nil, "w11g-oidc-secret")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestW11GOAuthStoreArms(t *testing.T) {
	ctx := context.Background()
	// 构造参数校验。
	if _, err := NewOAuthStore(nil, false, time.Now, nil, nil, "s"); err == nil {
		t.Fatal("nil db must fail")
	}
	if got := decodeStringArray("not-json"); len(got) != 0 {
		t.Fatalf("bad json = %v", got)
	}
	// 列表扫描失败与查询失败。
	s := w11gOAuthStore(t, []w11gPStep{{rowsErr: errors.New("w11g list failed")}})
	if _, err := s.ListClients(ctx); err == nil {
		t.Fatal("list error must propagate")
	}
	s = w11gOAuthStore(t, []w11gPStep{{cols: makeCols(10), rows: [][]driver.Value{makeRowBad(10)}}})
	if _, err := s.ListClients(ctx); err == nil {
		t.Fatal("scan error must propagate")
	}
	// findClientRowForSecret：查询失败与缺失。
	s = w11gOAuthStore(t, []w11gPStep{{rowsErr: errors.New("w11g row failed")}})
	if _, err := s.findClientRowForSecret(ctx, "juhe_w11g"); err == nil {
		t.Fatal("row query error must propagate")
	}
	s = w11gOAuthStore(t, []w11gPStep{{}})
	row, err := s.findClientRowForSecret(ctx, "juhe_w11g")
	if err != nil || row != nil {
		t.Fatalf("missing row=%v err=%v", row, err)
	}
	// FindClientSecret：查询失败；空密文；public client；空 payload。
	s = w11gOAuthStore(t, []w11gPStep{{rowsErr: errors.New("w11g secret failed")}})
	if _, err := s.FindClientSecret(ctx, "juhe_w11g"); err == nil {
		t.Fatal("secret query error must propagate")
	}
	s = w11gOAuthStore(t, []w11gPStep{{cols: []string{"client_type", "ciphertext"}, rows: [][]driver.Value{{"confidential", nil}}}})
	if secret, err := s.FindClientSecret(ctx, "juhe_w11g"); secret != nil || err != nil {
		t.Fatalf("nil ciphertext secret=%v err=%v", secret, err)
	}
	s = w11gOAuthStore(t, []w11gPStep{{cols: []string{"client_type", "ciphertext"}, rows: [][]driver.Value{{"public", "x"}}}})
	if secret, err := s.FindClientSecret(ctx, "juhe_w11g"); secret != nil || err != nil {
		t.Fatalf("public client secret=%v err=%v", secret, err)
	}
	emptyEnvelope, err := encryptOidcValue("w11g-oidc-secret", map[string]string{"clientSecret": ""})
	if err != nil {
		t.Fatal(err)
	}
	s = w11gOAuthStore(t, []w11gPStep{{cols: []string{"client_type", "ciphertext"}, rows: [][]driver.Value{{"confidential", emptyEnvelope}}}})
	if secret, err := s.FindClientSecret(ctx, "juhe_w11g"); secret != nil || err != nil {
		t.Fatalf("empty payload secret=%v err=%v", secret, err)
	}
	// decryptOidcValue：tag 与 ciphertext 段非法 base64url。
	var target struct {
		ClientSecret string `json:"clientSecret"`
	}
	if err := decryptOidcValue("s", "QQ.!!!.QQ", &target); err == nil {
		t.Fatal("bad tag must fail")
	}
	if err := decryptOidcValue("s", "QQ.QQ.!!!", &target); err == nil {
		t.Fatal("bad ciphertext must fail")
	}
}

func TestW11GOAuthWriteArms(t *testing.T) {
	ctx := context.Background()
	input := OAuthClientCreateInput{DisplayName: "w11g 客户端", ClientType: "confidential", RedirectUris: []string{"https://w11g.example.com/cb"}, AllowedScopes: []string{"openid"}}
	// 空加密密钥 → oidcGCM 失败。
	emptyKeyStore, err := NewOAuthStore(w11gOpenScripted(t, []w11gPStep{{}}), false, func() time.Time { return w11gClock }, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyKeyStore.CreateClient(ctx, input); err == nil {
		t.Fatal("empty key secret must fail create")
	}
	// INSERT 失败。
	s := w11gOAuthStore(t, []w11gPStep{{execErr: errors.New("w11g insert failed")}})
	if _, err := s.CreateClient(ctx, input); err == nil {
		t.Fatal("insert error must propagate")
	}
	// 状态更新失败。
	s = w11gOAuthStore(t, []w11gPStep{{execErr: errors.New("w11g status failed")}})
	if _, err := s.UpdateClientStatus(ctx, "juhe_w11g", "disabled"); err == nil {
		t.Fatal("status update error must propagate")
	}
	// 状态更新影响行数非 1。
	s = w11gOAuthStore(t, []w11gPStep{{affected: 0}})
	if client, err := s.UpdateClientStatus(ctx, "juhe_w11g", "disabled"); client != nil || err != nil {
		t.Fatalf("affected=0 client=%v err=%v", client, err)
	}
	// 重签：查找失败 / 缺失 / 非 confidential。
	s = w11gOAuthStore(t, []w11gPStep{{rowsErr: errors.New("w11g find failed")}})
	if _, err := s.ReissueClientSecret(ctx, "juhe_w11g"); err == nil {
		t.Fatal("find error must propagate")
	}
	s = w11gOAuthStore(t, []w11gPStep{{}})
	if client, err := s.ReissueClientSecret(ctx, "juhe_w11g"); client != nil || err != nil {
		t.Fatalf("missing client=%v err=%v", client, err)
	}
	publicRow := makeRowStrings(10)
	publicRow[3] = "public"
	s = w11gOAuthStore(t, []w11gPStep{{cols: makeCols(10), rows: [][]driver.Value{publicRow}}})
	if client, err := s.ReissueClientSecret(ctx, "juhe_w11g"); client != nil || err != nil {
		t.Fatalf("public client=%v err=%v", client, err)
	}
	// 重签：空密钥加密失败 / UPDATE 失败 / 影响行数非 1 / 回读失败。
	confRow := makeRowStrings(10)
	confRow[3] = "confidential"
	emptyKeyStore, err = NewOAuthStore(w11gOpenScripted(t, []w11gPStep{{cols: makeCols(10), rows: [][]driver.Value{confRow}}}), false, func() time.Time { return w11gClock }, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyKeyStore.ReissueClientSecret(ctx, "juhe_w11g"); err == nil {
		t.Fatal("empty key must fail reissue")
	}
	s = w11gOAuthStore(t, []w11gPStep{{cols: makeCols(10), rows: [][]driver.Value{confRow}}, {execErr: errors.New("w11g update failed")}})
	if _, err := s.ReissueClientSecret(ctx, "juhe_w11g"); err == nil {
		t.Fatal("update error must propagate")
	}
	s = w11gOAuthStore(t, []w11gPStep{{cols: makeCols(10), rows: [][]driver.Value{confRow}}, {affected: 0}})
	if client, err := s.ReissueClientSecret(ctx, "juhe_w11g"); client != nil || err != nil {
		t.Fatalf("affected=0 client=%v err=%v", client, err)
	}
	s = w11gOAuthStore(t, []w11gPStep{{cols: makeCols(10), rows: [][]driver.Value{confRow}}, {affected: 1}, {rowsErr: errors.New("w11g reload failed")}})
	if _, err := s.ReissueClientSecret(ctx, "juhe_w11g"); err == nil {
		t.Fatal("reload error must propagate")
	}
}

func TestW11GParseExternalQueryAndBodyArms(t *testing.T) {
	// coerceQueryNumber：多值与非数字直接失败；空串由 min(1) 捕获。
	for _, values := range [][]string{{"1", "2"}, {"abc"}} {
		if _, message := coerceQueryNumber(values); message == "" {
			t.Fatalf("coerce %v must fail", values)
		}
	}
	if _, _, _, _, message := parseExternalListQuery(map[string][]string{"page": {""}}); message == "" {
		t.Fatal("blank page must fail min(1)")
	}
	// page 非整数 / 小于 1；pageSize 上限；keyword 多值；status 多值与非法。
	_, _, _, _, message := parseExternalListQuery(map[string][]string{"page": {"1.5"}})
	if message == "" {
		t.Fatal("float page must fail")
	}
	_, _, _, _, message = parseExternalListQuery(map[string][]string{"page": {"0"}})
	if message == "" {
		t.Fatal("zero page must fail")
	}
	_, _, _, _, message = parseExternalListQuery(map[string][]string{"page": {"", "2"}})
	if message == "" {
		t.Fatal("multi page must fail")
	}
	_, _, _, _, message = parseExternalListQuery(map[string][]string{"pageSize": {"101"}})
	if message == "" {
		t.Fatal("big pageSize must fail")
	}
	_, _, _, _, message = parseExternalListQuery(map[string][]string{"pageSize": {"x"}})
	if message == "" {
		t.Fatal("bad pageSize must fail")
	}
	_, _, _, _, message = parseExternalListQuery(map[string][]string{"keyword": {"a", "b"}})
	if message == "" {
		t.Fatal("multi keyword must fail")
	}
	_, _, _, _, message = parseExternalListQuery(map[string][]string{"status": {"a", "b"}})
	if message == "" {
		t.Fatal("multi status must fail")
	}
	_, _, _, _, message = parseExternalListQuery(map[string][]string{"status": {"bogus"}})
	if message == "" {
		t.Fatal("bad status must fail")
	}
	// parseExternalSourceBody：状态非法、scopes 非数组、scopes 元素非字符串、scopes 空串。
	base := func(mods ...func(map[string]any)) map[string]any {
		body := map[string]any{"name": "w11g 来源", "scopes": []any{"juhe_ai_public:api_key_list:read"}}
		for _, mod := range mods {
			mod(body)
		}
		return body
	}
	for name, body := range map[string]map[string]any{
		"statusType":  base(func(b map[string]any) { b["status"] = 5 }),
		"statusValue": base(func(b map[string]any) { b["status"] = "bogus" }),
		"scopesType":  base(func(b map[string]any) { b["scopes"] = "bad" }),
		"scopesItem":  base(func(b map[string]any) { b["scopes"] = []any{7} }),
		"scopesEmpty": base(func(b map[string]any) { b["scopes"] = []any{" "} }),
		"limitsType":  base(func(b map[string]any) { b["rateLimits"] = "bad" }),
		"limitsMax":   base(func(b map[string]any) { b["rateLimits"] = make([]any, 9) }),
		"expiresType": base(func(b map[string]any) { b["expiresAt"] = 5 }),
		"expiresBad":  base(func(b map[string]any) { b["expiresAt"] = "not-a-time" }),
		"notesType":   base(func(b map[string]any) { b["notes"] = 5 }),
		"unknownKey":  base(func(b map[string]any) { b["extra"] = 1 }),
	} {
		if _, message := parseExternalSourceBody(body); message == "" {
			t.Fatalf("source body case %s must fail", name)
		}
	}
	// parseExternalDeleteBody：缺失、非字符串、坏格式、未知键。
	for _, body := range []map[string]any{
		{},
		{"expectedUpdatedAt": 5},
		{"expectedUpdatedAt": "not-a-time"},
		{"expectedUpdatedAt": "2026-09-01T00:00:00Z", "extra": 1},
	} {
		if _, message := parseExternalDeleteBody(body); message == "" {
			t.Fatalf("delete body %v must fail", body)
		}
	}
}

func TestW11GParseExternalUpdateAndTokenArms(t *testing.T) {
	at := "2026-09-01T00:00:00Z"
	base := func(mods ...func(map[string]any)) map[string]any {
		body := map[string]any{"expectedUpdatedAt": at, "name": "w11g 新名"}
		for _, mod := range mods {
			mod(body)
		}
		return body
	}
	for name, body := range map[string]map[string]any{
		"missing":    {},
		"expectedTy": {"expectedUpdatedAt": 5, "name": "x"},
		"expectedBad": {"expectedUpdatedAt": "not-a-time", "name": "x"},
		"nameType":   base(func(b map[string]any) { b["name"] = 5 }),
		"nameEmpty":  base(func(b map[string]any) { b["name"] = " " }),
		"nameLong":   base(func(b map[string]any) { b["name"] = strings.Repeat("名", 81) }),
		"statusType": base(func(b map[string]any) { b["status"] = 5 }),
		"statusBad":  base(func(b map[string]any) { b["status"] = "bogus" }),
		"scopesType": base(func(b map[string]any) { b["scopes"] = "bad" }),
		"scopesItem": base(func(b map[string]any) { b["scopes"] = []any{7} }),
		"scopesEmpty": base(func(b map[string]any) { b["scopes"] = []any{" "} }),
		"limitsType": base(func(b map[string]any) { b["rateLimits"] = "bad" }),
		"limitsMax":  base(func(b map[string]any) { b["rateLimits"] = make([]any, 9) }),
		"expiresTy":  base(func(b map[string]any) { b["expiresAt"] = 5 }),
		"expiresBad": base(func(b map[string]any) { b["expiresAt"] = "not-a-time" }),
		"notesTy":    base(func(b map[string]any) { b["notes"] = 5 }),
		"notesLong":  base(func(b map[string]any) { b["notes"] = strings.Repeat("注", 501) }),
		"unknown":    base(func(b map[string]any) { b["extra"] = 1 }),
		"noChange":   {"expectedUpdatedAt": at},
	} {
		if _, message := parseExternalSourceUpdateBody(body); message == "" {
			t.Fatalf("update body case %s must fail", name)
		}
	}
	// expiresAt 显式 null 与 notes null 合法。
	updateInput, message := parseExternalSourceUpdateBody(map[string]any{"expectedUpdatedAt": at, "expiresAt": nil, "notes": nil})
	if message != "" || !updateInput.SetFields["expiresAt"] || !updateInput.SetFields["notes"] || updateInput.ExpiresAt != nil || updateInput.Notes != nil {
		t.Fatalf("null fields input=%+v message=%s", updateInput, message)
	}
	// parseExternalTokenBody：缺失、类型、空、超长、状态、scopes、expiresAt、未知键。
	tokenBase := func(mods ...func(map[string]any)) map[string]any {
		body := map[string]any{"name": "w11g token"}
		for _, mod := range mods {
			mod(body)
		}
		return body
	}
	for name, body := range map[string]map[string]any{
		"missing":    {},
		"nameType":   {"name": 5},
		"nameEmpty":  {"name": " "},
		"nameLong":   {"name": strings.Repeat("名", 81)},
		"statusType": tokenBase(func(b map[string]any) { b["status"] = 5 }),
		"statusBad":  tokenBase(func(b map[string]any) { b["status"] = "bogus" }),
		"scopesType": tokenBase(func(b map[string]any) { b["scopes"] = "bad" }),
		"scopesItem": tokenBase(func(b map[string]any) { b["scopes"] = []any{7} }),
		"scopesEmpty": tokenBase(func(b map[string]any) { b["scopes"] = []any{" "} }),
		"expiresType": tokenBase(func(b map[string]any) { b["expiresAt"] = 5 }),
		"expiresBad":  tokenBase(func(b map[string]any) { b["expiresAt"] = "not-a-time" }),
		"unknown":     tokenBase(func(b map[string]any) { b["extra"] = 1 }),
	} {
		if _, message := parseExternalTokenBody(body); message == "" {
			t.Fatalf("token body case %s must fail", name)
		}
	}
	// parseExternalTokenUpdateBody：各分支。
	tokenUpBase := func(mods ...func(map[string]any)) map[string]any {
		body := map[string]any{"expectedUpdatedAt": at, "name": "w11g 新"}
		for _, mod := range mods {
			mod(body)
		}
		return body
	}
	for name, body := range map[string]map[string]any{
		"missing":     {},
		"expectedTy":  {"expectedUpdatedAt": 5, "name": "x"},
		"expectedBad": {"expectedUpdatedAt": "not-a-time", "name": "x"},
		"nameType":    tokenUpBase(func(b map[string]any) { b["name"] = 5 }),
		"nameEmpty":   tokenUpBase(func(b map[string]any) { b["name"] = " " }),
		"nameLong":    tokenUpBase(func(b map[string]any) { b["name"] = strings.Repeat("名", 81) }),
		"statusType":  tokenUpBase(func(b map[string]any) { b["status"] = 5 }),
		"statusBad":   tokenUpBase(func(b map[string]any) { b["status"] = "bogus" }),
		"scopesType":  tokenUpBase(func(b map[string]any) { b["scopes"] = "bad" }),
		"scopesItem":  tokenUpBase(func(b map[string]any) { b["scopes"] = []any{7} }),
		"scopesEmpty": tokenUpBase(func(b map[string]any) { b["scopes"] = []any{" "} }),
		"expiresType": tokenUpBase(func(b map[string]any) { b["expiresAt"] = 5 }),
		"expiresBad":  tokenUpBase(func(b map[string]any) { b["expiresAt"] = "not-a-time" }),
		"unknown":     tokenUpBase(func(b map[string]any) { b["extra"] = 1 }),
		"noChange":    {"expectedUpdatedAt": at},
	} {
		if _, message := parseExternalTokenUpdateBody(body); message == "" {
			t.Fatalf("token update body case %s must fail", name)
		}
	}
	// expiresAt null 合法。
	tokenInput, message := parseExternalTokenUpdateBody(map[string]any{"expectedUpdatedAt": at, "expiresAt": nil})
	if message != "" || !tokenInput.SetFields["expiresAt"] || tokenInput.ExpiresAt != nil {
		t.Fatalf("null expiresAt input=%+v message=%s", tokenInput, message)
	}
}

func TestW11GValidateRateLimitRuleArms(t *testing.T) {
	for _, item := range []any{
		"not-object",
		map[string]any{"windowSeconds": float64(60), "maxRequests": float64(10), "extra": 1},
		map[string]any{"windowSeconds": "x", "maxRequests": float64(10)},
		map[string]any{"windowSeconds": 1.5, "maxRequests": float64(10)},
		map[string]any{"windowSeconds": float64(0), "maxRequests": float64(10)},
		map[string]any{"windowSeconds": float64(86401), "maxRequests": float64(10)},
		map[string]any{"windowSeconds": float64(60), "maxRequests": "x"},
		map[string]any{"windowSeconds": float64(60), "maxRequests": 1.5},
		map[string]any{"windowSeconds": float64(60), "maxRequests": float64(0)},
		map[string]any{"windowSeconds": float64(60), "maxRequests": float64(100001)},
	} {
		if message := validateExternalRateLimitRule(item); message == "" {
			t.Fatalf("rule %v must fail", item)
		}
	}
	if message := validateExternalRateLimitRule(map[string]any{"windowSeconds": float64(60), "maxRequests": float64(10)}); message != "" {
		t.Fatalf("valid rule message=%s", message)
	}
}
