package policyreads

import (
	"database/sql"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 纯 helper 单测：错误类型、zod 消息、时间与字符串工具
// ---------------------------------------------------------------------------

func weStringPtr(value string) *string { return &value }

func TestWeErrorTypesMessages(t *testing.T) {
	validation := &ValidationError{Message: "参数错误"}
	if validation.Error() != "参数错误" {
		t.Fatalf("ValidationError.Error() = %s", validation.Error())
	}
	conflict := &ConflictError{Message: "版本冲突"}
	if conflict.Error() != "版本冲突" {
		t.Fatalf("ConflictError.Error() = %s", conflict.Error())
	}
	oidc := &OidcCiphertextError{Message: "密文无效"}
	if oidc.Error() != "密文无效" {
		t.Fatalf("OidcCiphertextError.Error() = %s", oidc.Error())
	}
	var asValidation *ValidationError
	if !errors.As(validation, &asValidation) {
		t.Fatal("errors.As 应识别 ValidationError")
	}
	var asConflict *ConflictError
	if !errors.As(conflict, &asConflict) {
		t.Fatal("errors.As 应识别 ConflictError")
	}
}

func TestWeZodMessageShims(t *testing.T) {
	// zodReceived 的类型投影与 Node zod v3 一致。
	tests := []struct {
		value any
		want  string
	}{
		{nil, "null"},
		{true, "boolean"},
		{"x", "string"},
		{float64(1), "number"},
		{[]any{}, "array"},
		{map[string]any{}, "object"},
		{struct{}{}, "unknown"},
	}
	for _, tt := range tests {
		if got := zodReceived(tt.value); got != tt.want {
			t.Fatalf("zodReceived(%v) = %s, want %s", tt.value, got, tt.want)
		}
	}
	if got := zodInvalidType("string", 42); got != "Expected string, received number" {
		t.Fatalf("zodInvalidType = %s", got)
	}
	if got := zodEnumMessage([]string{"a", "b"}, "c"); got != "Invalid enum value. Expected 'a' | 'b', received 'c'" {
		t.Fatalf("zodEnumMessage = %s", got)
	}
	if got := zodStringMin(1); got != "String must contain at least 1 character(s)" {
		t.Fatalf("zodStringMin = %s", got)
	}
	if got := zodStringMax(80); got != "String must contain at most 80 character(s)" {
		t.Fatalf("zodStringMax = %s", got)
	}
	if got := zodArrayMin(1); got != "Array must contain at least 1 element(s)" {
		t.Fatalf("zodArrayMin = %s", got)
	}
	if got := zodArrayMax(8); got != "Array must contain at most 8 element(s)" {
		t.Fatalf("zodArrayMax = %s", got)
	}
	if got := zodNumberMin(1); got != "Number must be greater than or equal to 1" {
		t.Fatalf("zodNumberMin = %s", got)
	}
	if got := zodNumberMax(100); got != "Number must be less than or equal to 100" {
		t.Fatalf("zodNumberMax = %s", got)
	}
	if got := zodUnrecognizedKeys([]string{"b", "a"}); got != "Unrecognized key(s) in object: a, b" {
		t.Fatalf("zodUnrecognizedKeys = %s", got)
	}
}

func TestWeBaseStoreTableAndBind(t *testing.T) {
	// 白盒：SQLite 模式原样返回，PostgreSQL 模式加 schema 前缀并把 ? 换成 $n。
	sqlite := &baseStore{}
	if got := sqlite.table("providers"); got != "providers" {
		t.Fatalf("sqlite table = %s", got)
	}
	if got := sqlite.bind("WHERE a = ? AND b = ?"); got != "WHERE a = ? AND b = ?" {
		t.Fatalf("sqlite bind = %s", got)
	}
	pg := &baseStore{pg: true}
	if got := pg.table("providers"); got != "juhe_business.providers" {
		t.Fatalf("pg table = %s", got)
	}
	if got := pg.bind("WHERE a = ? AND b = ?"); got != "WHERE a = $1 AND b = $2" {
		t.Fatalf("pg bind = %s", got)
	}
	if _, err := newBaseStore(nil, false, nil, nil, nil); err == nil {
		t.Fatal("缺 db 应报错")
	}
}

func TestWeTimeHelpers(t *testing.T) {
	if ensureCtx(nil) == nil {
		t.Fatal("ensureCtx(nil) 应回落 Background")
	}
	canonical, ok := canonicalRFC3339Millis(" 2026-01-01T08:00:00+08:00 ")
	if !ok || canonical != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("canonical = %s ok = %v", canonical, ok)
	}
	if _, ok := canonicalRFC3339Millis("2026-01-01T00:00:00"); ok {
		t.Fatal("缺少 offset 应解析失败")
	}
	if _, ok := canonicalRFC3339Millis("not-a-time"); ok {
		t.Fatal("非法文本应解析失败")
	}
	ms, ok := parseRFC3339Millis("2026-01-01T00:00:00.250Z")
	if !ok || ms != 1767225600250 {
		t.Fatalf("ms = %d ok = %v", ms, ok)
	}
	if _, ok := parseRFC3339Millis(" "); ok {
		t.Fatal("空白应解析失败")
	}
	// nextRFC3339Millis：now 早于当前值时单调推进。
	next, err := nextRFC3339Millis("2030-01-01T00:00:00.000Z", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "版本")
	if err != nil || next != "2030-01-01T00:00:00.001Z" {
		t.Fatalf("next = %s err = %v", next, err)
	}
	if _, err := nextRFC3339Millis("bad", time.Now(), "版本"); err == nil {
		t.Fatal("非法当前版本应报错")
	}
	if got := isoMillis(time.Date(2026, 1, 1, 0, 0, 0, 999999999, time.UTC)); got != "2026-01-01T00:00:00.999Z" {
		t.Fatalf("isoMillis 截断毫秒 = %s", got)
	}
}

func TestWeStringHelpers(t *testing.T) {
	if got := uniqueSortedStrings([]string{"b", "a", "b", "c"}); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("uniqueSortedStrings = %v", got)
	}
	if safeChangeText(nil) != "" {
		t.Fatal("nil 应为空串")
	}
	if safeChangeText(true) != "true" {
		t.Fatalf("bool = %s", safeChangeText(true))
	}
	if safeChangeText(42) != "42" || safeChangeText(int64(-7)) != "-7" {
		t.Fatal("整数投影错误")
	}
	if safeChangeText(1.5) != "1.5" {
		t.Fatalf("float = %s", safeChangeText(1.5))
	}
	long := strings.Repeat("字", 300)
	if got := safeChangeText(long); len([]rune(got)) != 203 || !strings.HasSuffix(got, "...") {
		t.Fatalf("超长应截断到 200+..., got %d", len([]rune(got)))
	}
	if got := safeChangeText(map[string]any{"k": "v"}); got != `{"k":"v"}` {
		t.Fatalf("对象序列化 = %s", got)
	}
	if truncateRunes("abcd", 2) != "ab..." {
		t.Fatalf("truncateRunes = %s", truncateRunes("abcd", 2))
	}
	if escapeLikePrefix(`50%_off\`) != `50\%\_off\\` {
		t.Fatalf("escapeLikePrefix = %s", escapeLikePrefix(`50%_off\`))
	}
	if runeLen("汉字") != 2 {
		t.Fatal("runeLen 语义错误")
	}
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatal("boolToInt 语义错误")
	}
	if ptrString("") != nil || *ptrString("x") != "x" {
		t.Fatal("ptrString 语义错误")
	}
	if nullPtrString(sql.NullString{}) != nil || *nullPtrString(sql.NullString{String: "v", Valid: true}) != "v" {
		t.Fatal("nullPtrString 语义错误")
	}
	if nullStringText(nil) != "" || nullStringText(weStringPtr("v")) != "v" {
		t.Fatal("nullStringText 语义错误")
	}
}

func TestWeIDGenerators(t *testing.T) {
	id := randomPrefixedID("extsrc")
	if !strings.HasPrefix(id, "extsrc_") || len(id) != len("extsrc_")+24 {
		t.Fatalf("id = %s", id)
	}
	token := createExternalSourceTokenValue()
	if !strings.HasPrefix(token, "juis_") || len(token) < 40 {
		t.Fatalf("token = %s", token)
	}
	uid := newUUIDv4()
	if len(uid) != 36 || strings.Count(uid, "-") != 4 {
		t.Fatalf("uuid = %s", uid)
	}
	encoded := base64RawURL([]byte{0xfb, 0xff})
	if encoded != "-_8" {
		t.Fatalf("base64RawURL = %s", encoded)
	}
}

func TestWeExternalScopeAndRateLimitNormalizers(t *testing.T) {
	// scopes：nil → 空数组；去重排序；非法输入按仓库语义报错。
	scopes, err := normalizeExternalScopes(nil)
	if err != nil || len(scopes) != 0 {
		t.Fatalf("nil scopes = %v err = %v", scopes, err)
	}
	if _, err := normalizeExternalScopes("not-array"); err == nil {
		t.Fatal("非数组 scopes 应报错")
	}
	if _, err := normalizeExternalScopes([]any{1}); err == nil {
		t.Fatal("非字符串项应报错")
	}
	if _, err := normalizeExternalScopes([]any{"  "}); err == nil || !strings.Contains(err.Error(), "不能为空") {
		t.Fatalf("空白 scope 应报错: %v", err)
	}
	if _, err := normalizeExternalScopes([]any{"nope"}); err == nil || !strings.Contains(err.Error(), "不受支持") {
		t.Fatalf("未知 scope 应报错: %v", err)
	}
	scopes, err = normalizeExternalScopes([]any{" juhe_ai_public:group_list:read ", "juhe_ai_public:api_key_list:read", "juhe_ai_public:group_list:read"})
	if err != nil || len(scopes) != 2 || scopes[0] != "juhe_ai_public:api_key_list:read" {
		t.Fatalf("scopes = %v err = %v", scopes, err)
	}
	// decodeExternalScopes：未知存量 scope 被丢弃。
	decoded, err := decodeExternalScopes(`["juhe_ai_public:group_list:read","legacy:scope"]`)
	if err != nil || len(decoded) != 1 {
		t.Fatalf("decoded = %v err = %v", decoded, err)
	}
	if _, err := decodeExternalScopes("{bad"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}

	// rate limits：排序、上限、未知字段与整数校验。
	rules, err := normalizeExternalRateLimits(nil)
	if err != nil || len(rules) != 0 {
		t.Fatalf("nil rules = %v err = %v", rules, err)
	}
	if _, err := normalizeExternalRateLimits("x"); err == nil {
		t.Fatal("非数组限频应报错")
	}
	many := []any{}
	for i := 0; i < 9; i++ {
		many = append(many, map[string]any{"windowSeconds": float64(i + 1), "maxRequests": float64(1)})
	}
	if _, err := normalizeExternalRateLimits(many); err == nil || !strings.Contains(err.Error(), "最多 8 条") {
		t.Fatalf("超过 8 条应报错: %v", err)
	}
	if _, err := normalizeExternalRateLimits([]any{"x"}); err == nil {
		t.Fatal("非对象限频应报错")
	}
	if _, err := normalizeExternalRateLimits([]any{map[string]any{"windowSeconds": 1.5, "maxRequests": float64(1)}}); err == nil {
		t.Fatal("非整数窗口应报错")
	}
	if _, err := normalizeExternalRateLimits([]any{map[string]any{"windowSeconds": float64(0), "maxRequests": float64(1)}}); err == nil || !strings.Contains(err.Error(), "限频窗口") {
		t.Fatalf("窗口越界应报错: %v", err)
	}
	if _, err := normalizeExternalRateLimits([]any{map[string]any{"windowSeconds": float64(1), "maxRequests": float64(100001)}}); err == nil {
		t.Fatal("次数越界应报错")
	}
	if _, err := normalizeExternalRateLimits([]any{map[string]any{"windowSeconds": float64(1), "bogus": float64(1)}}); err == nil || !strings.Contains(err.Error(), "未知字段") {
		t.Fatalf("未知字段应报错: %v", err)
	}
	sorted, err := normalizeExternalRateLimits([]any{
		map[string]any{"windowSeconds": float64(300), "maxRequests": float64(5)},
		map[string]any{"windowSeconds": float64(60), "maxRequests": float64(10)},
		map[string]any{"windowSeconds": float64(60), "maxRequests": float64(9)},
	})
	if err == nil || !strings.Contains(err.Error(), "不能重复") {
		t.Fatalf("重复窗口应报错: %v err = %v", sorted, err)
	}
	sorted, err = normalizeExternalRateLimits([]any{
		map[string]any{"windowSeconds": float64(300), "maxRequests": float64(5)},
		map[string]any{"windowSeconds": float64(60), "maxRequests": float64(10)},
	})
	if err != nil || sorted[0].WindowSeconds != 60 || sorted[1].MaxRequests != 5 {
		t.Fatalf("sorted = %+v err = %v", sorted, err)
	}
	if rules, err := decodeExternalRateLimits(""); err != nil || len(rules) != 0 {
		t.Fatalf("空串限频 = %v err = %v", rules, err)
	}
	if _, err := decodeExternalRateLimits("{bad"); err == nil {
		t.Fatal("非法限频 JSON 应报错")
	}
	// externalRateLimitInteger 分支。
	if _, err := externalRateLimitInteger("x", 1, 10, "字段"); err == nil || !strings.Contains(err.Error(), "必须是整数") {
		t.Fatalf("非数字应报错: %v", err)
	}
	if _, err := externalRateLimitInteger(1.5, 1, 10, "字段"); err == nil {
		t.Fatal("小数应报错")
	}
	if _, err := externalRateLimitInteger(float64(99), 1, 10, "字段"); err == nil || !strings.Contains(err.Error(), "1 到 10") {
		t.Fatalf("越界应报错: %v", err)
	}
}

func TestWeExternalTextNormalizers(t *testing.T) {
	if value, err := normalizeExternalNullableText(nil); err != nil || value != nil {
		t.Fatalf("nil notes = %v err = %v", value, err)
	}
	if _, err := normalizeExternalNullableText(42); err == nil {
		t.Fatal("非字符串备注应报错")
	}
	if _, err := normalizeExternalNullableText(strings.Repeat("字", 501)); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("超长备注应报错: %v", err)
	}
	if value, err := normalizeExternalNullableText("   "); err != nil || value != nil {
		t.Fatalf("空白备注应归一为 nil: %v err = %v", value, err)
	}
	if value, err := normalizeExternalNullableText(" 备注 "); err != nil || *value != "备注" {
		t.Fatalf("notes = %v err = %v", value, err)
	}

	if _, err := normalizeExternalName(42, "来源系统名称不能为空"); err == nil {
		t.Fatal("非字符串名称应报错")
	}
	if _, err := normalizeExternalName("  ", "来源系统名称不能为空"); err == nil {
		t.Fatal("空名称应报错")
	}
	if _, err := normalizeExternalName(strings.Repeat("字", 81), "x"); err == nil || !strings.Contains(err.Error(), "80") {
		t.Fatalf("超长名称应报错: %v", err)
	}

	if _, err := normalizeSourceStatus("bogus"); err == nil {
		t.Fatal("非法来源状态应报错")
	}
	if _, err := normalizeTokenStatus("bogus"); err == nil {
		t.Fatal("非法 token 状态应报错")
	}
	if status, err := normalizeSourceStatusInput(nil); err != nil || status != "active" {
		t.Fatalf("nil 状态回落 active: %s err = %v", status, err)
	}
	if _, err := normalizeSourceStatusInput(42); err == nil {
		t.Fatal("非字符串状态应报错")
	}
	if status, err := normalizeTokenStatusInput(nil); err != nil || status != "active" {
		t.Fatalf("token nil 状态回落 active: %s err = %v", status, err)
	}
	if _, err := normalizeTokenStatusInput(42); err == nil {
		t.Fatal("非字符串 token 状态应报错")
	}

	if value, err := normalizeExternalNullableISO(nil); err != nil || value != nil {
		t.Fatalf("nil 过期时间 = %v err = %v", value, err)
	}
	if _, err := normalizeExternalNullableISO(42); err == nil {
		t.Fatal("非字符串过期时间应报错")
	}
	if _, err := normalizeExternalNullableISO("   "); err == nil {
		t.Fatal("空白过期时间应报错")
	}
	if _, err := normalizeExternalNullableISO("2026-13-99T00:00:00Z"); err == nil {
		t.Fatal("非法过期时间应报错")
	}
}

func TestWeExternalTokenHelpers(t *testing.T) {
	if isUniqueConstraintError(nil) {
		t.Fatal("nil 不是唯一约束错误")
	}
	if !isUniqueConstraintError(errors.New("UNIQUE constraint failed: x.name")) {
		t.Fatal("SQLite 唯一约束未识别")
	}
	if !isUniqueConstraintError(errors.New("pq: duplicate key value violates unique constraint (SQLSTATE 23505)")) {
		t.Fatal("PG 唯一约束未识别")
	}
	if isUniqueConstraintError(errors.New("其他错误")) {
		t.Fatal("普通错误不应识别为唯一约束")
	}
	// externalTokenSlice 镜像 JS slice 语义。
	token := "juis_abcdefghij"
	if externalTokenSlice(token, 0, 8) != "juis_abc" {
		t.Fatalf("prefix = %s", externalTokenSlice(token, 0, 8))
	}
	if got := externalTokenSlice(token, len(token)-8, len(token)+10); got != "cdefghij" {
		t.Fatalf("suffix = %s", got)
	}
	if got := externalTokenSlice(token, -5, 2); got != "ju" {
		t.Fatalf("负 start = %s", got)
	}
	if got := externalTokenSlice(token, 5, 5); got != "" {
		t.Fatalf("start>=end 应为空: %s", got)
	}
}

func TestWeRateLimitChangeFormatters(t *testing.T) {
	if formatExternalRateLimits(nil) != "不限制" {
		t.Fatal("空限频应显示不限制")
	}
	formatted := formatExternalRateLimits([]ExternalRateLimitRule{{WindowSeconds: 60, MaxRequests: 10}})
	if formatted != "60s/10次" {
		t.Fatalf("formatted = %s", formatted)
	}
	// asRateLimitRules：规范化值 / zod 原始值 / 非法输入。
	typed := asRateLimitRules([]ExternalRateLimitRule{{WindowSeconds: 1, MaxRequests: 2}})
	if len(typed) != 1 || typed[0].WindowSeconds != 1 {
		t.Fatalf("typed = %v", typed)
	}
	raw := asRateLimitRules([]any{map[string]any{"windowSeconds": float64(3), "maxRequests": float64(4)}})
	if len(raw) != 1 || raw[0].MaxRequests != 4 {
		t.Fatalf("raw = %v", raw)
	}
	if empty := asRateLimitRules("x"); len(empty) != 0 {
		t.Fatalf("非数组应返回空: %v", empty)
	}
	if partial := asRateLimitRules([]any{"x", 5}); len(partial) != 0 {
		t.Fatalf("非对象项跳过: %v", partial)
	}
	if got := formatScopes([]any{"a", "b"}); got != "a, b" {
		t.Fatalf("formatScopes = %s", got)
	}
	if got := formatScopes(42); got != "" {
		t.Fatalf("非列表 formatScopes = %s", got)
	}
	if got := formatScopes(nil); got != "" {
		t.Fatalf("nil formatScopes = %s", got)
	}
}

func TestWeQueryHelpers(t *testing.T) {
	page, pageSize, keyword, status, message := parseExternalListQuery(map[string][]string{
		"page": {"2"}, "pageSize": {"50"}, "keyword": {" 关键词 "}, "status": {"active"},
	})
	if message != "" || page == nil || *page != 2 || pageSize == nil || *pageSize != 50 || keyword != "关键词" || status != "active" {
		t.Fatalf("query = %v %v %s %s %s", page, pageSize, keyword, status, message)
	}
	if _, _, _, _, msg := parseExternalListQuery(map[string][]string{"page": {"abc"}}); msg != "Expected number, received nan" {
		t.Fatalf("非数字 page = %s", msg)
	}
	if _, _, _, _, msg := parseExternalListQuery(map[string][]string{"page": {"1.5"}}); msg != "Expected integer, received float" {
		t.Fatalf("小数 page = %s", msg)
	}
	if _, _, _, _, msg := parseExternalListQuery(map[string][]string{"page": {"0"}}); msg != zodNumberMin(1) {
		t.Fatalf("page=0 = %s", msg)
	}
	if _, _, _, _, msg := parseExternalListQuery(map[string][]string{"pageSize": {"101"}}); msg != zodNumberMax(100) {
		t.Fatalf("pageSize=101 = %s", msg)
	}
	if _, _, _, _, msg := parseExternalListQuery(map[string][]string{"pageSize": {""}}); msg != zodNumberMin(1) {
		t.Fatalf("空白 pageSize 触发 min(1) = %s", msg)
	}
	if _, _, _, _, msg := parseExternalListQuery(map[string][]string{"keyword": {"a", "b"}}); msg == "" {
		t.Fatal("重复 keyword 应报错")
	}
	if _, _, _, _, msg := parseExternalListQuery(map[string][]string{"status": {"bogus"}}); msg == "" || !strings.Contains(msg, "Invalid enum value") {
		t.Fatalf("非法 status = %s", msg)
	}
	if _, issue := coerceQueryNumber([]string{"a", "b"}); issue != "Expected number, received nan" {
		t.Fatalf("多值 = %s", issue)
	}
	if number, issue := coerceQueryNumber([]string{"  "}); issue != "" || number != 0 {
		t.Fatalf("空白 = %v %s", number, issue)
	}
	if got := normalizeExternalListPage(0, 20); got != 1 {
		t.Fatalf("page<1 = %d", got)
	}
	if got := normalizeExternalListPage(999, 20); got != 49 {
		t.Fatalf("超出上界应夹到 (1000-1)/20 = %d", got)
	}
	if got := normalizeExternalListPage(999, 2000); got != 1 {
		t.Fatalf("大 pageSize 上界至少为 1: %d", got)
	}
}

func TestWeInspectionRowScansAndFields(t *testing.T) {
	// scanInspectionOverviewRow：enabled 整数 → bool 投影。
	overview, err := scanInspectionOverviewRow(func(targets ...any) error {
		if len(targets) != 10 {
			t.Fatalf("targets = %d", len(targets))
		}
		*(targets[0].(*string)) = "rip_1"
		*(targets[1].(*string)) = "规则"
		*(targets[2].(*int)) = 1
		*(targets[3].(*int)) = 7
		*(targets[4].(*string)) = "provider"
		*(targets[5].(*string)) = "openai"
		*(targets[6].(*sql.NullString)) = sql.NullString{String: "gpt", Valid: true}
		*(targets[7].(*sql.NullString)) = sql.NullString{String: "GPT 官方", Valid: true}
		*(targets[8].(*string)) = "observe"
		*(targets[9].(*string)) = "2026-01-01T00:00:00.000Z"
		return nil
	})
	if err != nil || !overview.enabled || overview.priority != 7 || overview.providerName.String != "GPT 官方" {
		t.Fatalf("overview = %+v err = %v", overview, err)
	}
	disabled, err := scanInspectionOverviewRow(func(targets ...any) error {
		*(targets[2].(*int)) = 0
		return nil
	})
	if err != nil || disabled.enabled {
		t.Fatalf("disabled = %+v err = %v", disabled, err)
	}
	if scanErr := error(nil); scanErr != nil {
		t.Fatal("unreachable")
	}
	patch, err := scanInspectionPatchRow(func(targets ...any) error {
		*(targets[2].(*int)) = 1
		*(targets[9].(*sql.NullString)) = sql.NullString{String: "备注", Valid: true}
		return nil
	})
	if err != nil || !patch.enabled || patch.notes.String != "备注" {
		t.Fatalf("patch = %+v err = %v", patch, err)
	}
	if _, err := scanInspectionOverviewRow(func(...any) error { return errors.New("扫描失败") }); err == nil {
		t.Fatal("扫描错误应传播")
	}

	// inspectionFieldValue 覆盖全部字段与默认分支。
	detail := &InspectionDetail{
		Name: "策略", Enabled: true, Priority: 3, ScopeType: "protocol", ProtocolCode: "openai",
		ProviderCode: weStringPtr("gpt"), Match: InspectionMatch{"errorCodes": {"x"}}, Action: "observe", Notes: weStringPtr("备注"),
	}
	fields := map[string]string{
		"name": "策略", "enabled": "true", "priority": "3", "scopeType": "protocol",
		"protocolCode": "openai", "providerCode": `"gpt"` /* 指针经 JSON 序列化带引号 */, "match": `{"errorCodes":["x"]}`,
		"action": "observe", "notes": `"备注"` /* 同指针 JSON 投影 */, "unknown": "",
	}
	for field, want := range fields {
		if got := inspectionFieldValue(field, detail); got != want {
			t.Fatalf("inspectionFieldValue(%s) = %s, want %s", field, got, want)
		}
	}

	// storeErrorMessage 的错误分类。
	if got := storeErrorMessage(&ValidationError{Message: "校验失败"}, "fallback"); got != "校验失败" {
		t.Fatalf("validation = %s", got)
	}
	if got := storeErrorMessage(&ConflictError{Message: "冲突"}, "fallback"); got != "冲突" {
		t.Fatalf("conflict = %s", got)
	}
	if got := storeErrorMessage(errors.New("磁盘故障"), "fallback"); got != "磁盘故障" {
		t.Fatalf("other = %s", got)
	}
	if got := storeErrorMessage(errUnknownStoreFailure, "fallback"); got != "fallback" {
		t.Fatalf("unknown = %s", got)
	}

	// matchEqual / sameNullable。
	if !matchEqual(InspectionMatch{"a": {"x"}}, InspectionMatch{"a": {"x"}}) {
		t.Fatal("相同 match 应相等")
	}
	if matchEqual(InspectionMatch{"a": {"x"}}, InspectionMatch{"a": {"y"}}) {
		t.Fatal("不同 match 不应相等")
	}
	empty := ""
	value := "v"
	if !sameNullable(nil, &empty) || !sameNullable(&empty, nil) {
		t.Fatal("空串与 nil 应视为相同")
	}
	if !sameNullable(&value, &value) {
		t.Fatal("同值应相同")
	}
	if sameNullable(&value, nil) {
		t.Fatal("值与 nil 不应相同")
	}

	// actor 解析：匿名请求回落 anonymous。
	resolver := policyreadsActorResolver(httptest.NewRequest("POST", "/x", nil))
	if resolver != "anonymous" {
		t.Fatalf("resolver = %s", resolver)
	}

	// catalogFieldOf 的字段投影。
	field := catalogFieldOf("page", "number", true, "页码")
	if field.Name != "page" || field.Type != "number" || !field.Required || field.Description != "页码" {
		t.Fatalf("field = %+v", field)
	}
}

func TestWeInspectionTextNormalizers(t *testing.T) {
	if text, err := requiredTextField(" 名称 ", "规则名称", 100); err != nil || text != "名称" {
		t.Fatalf("text = %s err = %v", text, err)
	}
	if _, err := requiredTextField(42, "规则名称", 100); err == nil || err.Error() != "规则名称无效" {
		t.Fatalf("非字符串 = %v", err)
	}
	if _, err := requiredTextField(" ", "规则名称", 100); err == nil || err.Error() != "规则名称不能为空" {
		t.Fatalf("空白 = %v", err)
	}
	if _, err := requiredTextField(strings.Repeat("字", 101), "规则名称", 100); err == nil || !strings.Contains(err.Error(), "100") {
		t.Fatalf("超长 = %v", err)
	}
	if _, err := normalizeScopeType("elsewhere"); err == nil || err.Error() != "响应检查策略作用层级无效" {
		t.Fatalf("scopeType = %v", err)
	}
	if _, err := normalizeInspectionAction("explode"); err == nil || err.Error() != "响应检查策略动作无效" {
		t.Fatalf("action = %v", err)
	}
	if err := positiveIntBounds(0, 1, 9999, "优先级"); err == nil {
		t.Fatal("优先级下界应报错")
	}
	if err := positiveIntBounds(10000, 1, 9999, "优先级"); err == nil {
		t.Fatal("优先级上界应报错")
	}
	if _, err := normalizeInspectionProtocolCode("ollama"); err == nil || !strings.Contains(err.Error(), "只支持") {
		t.Fatalf("协议 = %v", err)
	}
	// normalizeKnownStringList：合法值放行、未知值报错。
	if items, err := normalizeKnownStringList([]any{"codex"}, "clientProfiles", inspectionClientProfiles); err != nil || len(items) != 1 {
		t.Fatalf("items = %v err = %v", items, err)
	}
	if _, err := normalizeKnownStringList([]any{"bogus_profile"}, "clientProfiles", inspectionClientProfiles); err == nil || !strings.Contains(err.Error(), "不支持的值") {
		t.Fatalf("未知 profile = %v", err)
	}
	// normalizeStringList：去重、上限与空白校验。
	if items, err := normalizeStringList(nil, "标签"); err != nil || len(items) != 0 {
		t.Fatalf("nil = %v err = %v", items, err)
	}
	if _, err := normalizeStringList(42, "标签"); err == nil {
		t.Fatal("非数组应报错")
	}
	tooMany := []any{}
	for i := 0; i < 51; i++ {
		tooMany = append(tooMany, "x")
	}
	if _, err := normalizeStringList(tooMany, "标签"); err == nil || !strings.Contains(err.Error(), "50 项") {
		t.Fatalf("超量 = %v", err)
	}
	if _, err := normalizeStringList([]any{strings.Repeat("字", 201)}, "标签"); err == nil {
		t.Fatal("超长项应报错")
	}
	deduped, err := normalizeStringList([]any{" a ", "a", "b"}, "标签")
	if err != nil || len(deduped) != 2 || deduped[0] != "a" {
		t.Fatalf("deduped = %v err = %v", deduped, err)
	}
}
