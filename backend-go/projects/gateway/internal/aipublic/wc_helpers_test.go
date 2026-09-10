// aipublic 纯函数补测：共享 helper（helpers.go）、zod 消息镜像与请求解析器
// （zod.go）、token/限流 JSON 解码（auth.go）、capture 内部构件
// （capture.go）、操作日志字段投影（operationlog.go）、目标/错误映射
// （target.go）与 DTO 投影（dto.go）。这些函数都是 Node 迁移镜像，断言
// 锁定消息文案与边界分支，保证 400/404/409 语义与 Node 一致。
package aipublic

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/publicapilogs"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/routestrategies"
)

// ---------------------------------------------------------------------------
// helpers.go
// ---------------------------------------------------------------------------

// TestWCBind：PG 方言把 ? 重写为 $1..$n；SQLite 方言原样返回。
func TestWCBind(t *testing.T) {
	deps := &Deps{PGDialect: true}
	got := deps.bind("SELECT * FROM t WHERE a = ? AND b = ? LIMIT ?")
	if want := "SELECT * FROM t WHERE a = $1 AND b = $2 LIMIT $3"; got != want {
		t.Fatalf("bind: %q，期望 %q", got, want)
	}
	sqlite := &Deps{PGDialect: false}
	if plain := "SELECT ?"; sqlite.bind(plain) != plain {
		t.Fatalf("sqlite bind 必须原样返回: %q", sqlite.bind(plain))
	}
}

// TestWCSmallHelpers：nil ctx 兜底、JSON 封装、数值/字符串投影。
func TestWCSmallHelpers(t *testing.T) {
	if ensureCtx(nil) == nil {
		t.Fatalf("ensureCtx(nil) 必须回退 context.Background")
	}
	ctx := context.Background()
	if ensureCtx(ctx) != ctx {
		t.Fatalf("ensureCtx 必须原样返回非 nil ctx")
	}
	encoded, err := jsonMarshal(map[string]int{"a": 1})
	if err != nil || string(encoded) != `{"a":1}` {
		t.Fatalf("jsonMarshal: %s %v", encoded, err)
	}
	var target map[string]any
	if err := jsonUnmarshal([]byte(`{"b":2}`), &target); err != nil || target["b"] != float64(2) {
		t.Fatalf("jsonUnmarshal: %v %v", target, err)
	}
	if value, err := strconvParseFloat(" 12.5 "); err != nil || value != 12.5 {
		t.Fatalf("strconvParseFloat: %v %v", value, err)
	}
	if _, err := strconvParseFloat("x"); err == nil {
		t.Fatalf("strconvParseFloat 非数字必须报错")
	}
	if got, ok := numberToInt(float64(7)); !ok || got != 7 {
		t.Fatalf("numberToInt 整数: %d %v", got, ok)
	}
	if _, ok := numberToInt(float64(7.5)); ok {
		t.Fatalf("numberToInt 非整数必须失败")
	}
	if _, ok := numberToInt("7"); ok {
		t.Fatalf("numberToInt 非数值必须失败")
	}
	if got, ok := numberFrom(float64(3)); !ok || got != 3 {
		t.Fatalf("numberFrom: %d %v", got, ok)
	}
	if runeLen("福利站") != 3 {
		t.Fatalf("runeLen 按 rune 计数")
	}
	if !sameText(" A ", "a") {
		t.Fatalf("sameText 大小写不敏感且去空白")
	}
	if sameText("a", "b") {
		t.Fatalf("sameText 不同值必须为 false")
	}
	if got := normalizedText(" x "); got != "x" {
		t.Fatalf("normalizedText: %q", got)
	}
	if got := normalizedText(12); got != "" {
		t.Fatalf("normalizedText 非字符串必须为空: %q", got)
	}
	list := normalizedStringList([]string{" a ", "a", "", "b"})
	if len(list) != 2 || list[0] != "a" || list[1] != "b" {
		t.Fatalf("normalizedStringList 去重去空白保序: %v", list)
	}
	values := []string{"b", "a"}
	sortStrings(values)
	if values[0] != "a" || values[1] != "b" {
		t.Fatalf("sortStrings: %v", values)
	}
	uniq := uniqueSortedStrings([]string{"b", "a", "b", "c"})
	if len(uniq) != 3 || uniq[0] != "a" || uniq[2] != "c" {
		t.Fatalf("uniqueSortedStrings: %v", uniq)
	}
	if !containsString([]string{"x"}, "x") || containsString([]string{"x"}, "y") {
		t.Fatalf("containsString 分支错误")
	}
	if !scopeSupported(scopeGroupListRead) || scopeSupported("bogus") {
		t.Fatalf("scopeSupported 必须只认内置 scope")
	}
	if base64URLEncode([]byte{255}) != "_w" {
		t.Fatalf("base64URLEncode: %q", base64URLEncode([]byte{255}))
	}
}

// TestWCValueConversions：时间毫秒与 sql.Null 投影。
func TestWCValueConversions(t *testing.T) {
	if millis := rfc3339Millis("2026-01-02T03:04:05.678Z"); millis == nil || *millis != 1767323045678 {
		t.Fatalf("rfc3339Millis: %v", millis)
	}
	if rfc3339Millis("nope") != nil {
		t.Fatalf("rfc3339Millis 非法输入必须返回 nil")
	}
	if nullMillis(sql.NullString{}) != nil {
		t.Fatalf("nullMillis invalid 必须为 nil")
	}
	if nullMillis(sql.NullString{String: "bad", Valid: true}) != nil {
		t.Fatalf("nullMillis 非法时间必须为 nil")
	}
	if nullPtrString(sql.NullString{}) != nil {
		t.Fatalf("nullPtrString invalid 必须为 nil")
	}
	value := "x"
	if got := ptrToNullString(nil); got.Valid {
		t.Fatalf("ptrToNullString(nil) 必须 invalid")
	}
	if got := ptrToNullString(&value); !got.Valid || got.String != "x" {
		t.Fatalf("ptrToNullString: %v", got)
	}
	if nullStringText(nil) != "" {
		t.Fatalf("nullStringText(nil) 必须为空")
	}
	if nullStringText(&value) != "x" {
		t.Fatalf("nullStringText: %q", nullStringText(&value))
	}
}

// ---------------------------------------------------------------------------
// zod.go 消息与解析器
// ---------------------------------------------------------------------------

// TestWCZodMessages：zod v3 消息文案逐条镜像。
func TestWCZodMessages(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"received null", zodReceived(nil), "null"},
		{"received bool", zodReceived(true), "boolean"},
		{"received string", zodReceived("x"), "string"},
		{"received float", zodReceived(1.5), "number"},
		{"received int", zodReceived(1), "number"},
		{"received array", zodReceived([]any{}), "array"},
		{"received object", zodReceived(map[string]any{}), "object"},
		{"received unknown", zodReceived(struct{}{}), "unknown"},
		{"invalid type", zodInvalidType("string", 1.5), "Expected string, received number"},
		{"string min", zodStringMin(2), "String must contain at least 2 character(s)"},
		{"string max", zodStringMax(8), "String must contain at most 8 character(s)"},
		{"number min", zodNumberMin(1), "Number must be greater than or equal to 1"},
		{"number max", zodNumberMax(100), "Number must be less than or equal to 100"},
		{"enum", zodEnumMessage([]string{"active", "disabled"}, "x"), "Invalid enum value. Expected 'active' | 'disabled', received 'x'"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.got != testCase.want {
				t.Fatalf("得到 %q，期望 %q", testCase.got, testCase.want)
			}
		})
	}
	if got := zodUnrecognizedKeys("b", "a"); got != "Unrecognized key(s) in object: a, b" {
		t.Fatalf("zodUnrecognizedKeys 需排序: %q", got)
	}
}

// TestWCParseQueryHelpers：query 解析器的缺失/越界/非法分支。
func TestWCParseQueryHelpers(t *testing.T) {
	values := url.Values{}
	if text, issue := parseQueryString(values, "k", true, 2, 8); issue != zodRequired || text != "" {
		t.Fatalf("缺失必填: %q %q", text, issue)
	}
	if text, issue := parseQueryString(values, "k", false, 2, 8); issue != "" || text != "" {
		t.Fatalf("缺失可选: %q %q", text, issue)
	}
	values.Set("k", " ab ")
	if text, issue := parseQueryString(values, "k", true, 2, 8); issue != "" || text != "ab" {
		t.Fatalf("正常解析: %q %q", text, issue)
	}
	values.Set("short", "a")
	if _, issue := parseQueryString(values, "short", true, 2, 8); issue != zodStringMin(2) {
		t.Fatalf("过短: %q", issue)
	}
	values.Set("long", strings.Repeat("x", 9))
	if _, issue := parseQueryString(values, "long", true, 2, 8); issue != zodStringMax(8) {
		t.Fatalf("过长: %q", issue)
	}

	if _, present, issue := parseOptionalQueryString(values, "absent", 1, 8); present || issue != "" {
		t.Fatalf("可选缺失: %v %q", present, issue)
	}
	values.Set("blank", "  ")
	if _, present, issue := parseOptionalQueryString(values, "blank", 1, 8); present || issue != zodStringMin(1) {
		t.Fatalf("可选空白: %v %q", present, issue)
	}
	values.Set("opt", " v ")
	if text, present, issue := parseOptionalQueryString(values, "opt", 1, 8); !present || issue != "" || text != "v" {
		t.Fatalf("可选正常: %q %v %q", text, present, issue)
	}
	values.Set("optlong", strings.Repeat("y", 9))
	if _, _, issue := parseOptionalQueryString(values, "optlong", 1, 8); issue != zodStringMax(8) {
		t.Fatalf("可选过长: %q", issue)
	}

	if _, present, issue := parseOptionalQueryEnum(values, "absent", []string{"a"}); present || issue != "" {
		t.Fatalf("enum 缺失: %v %q", present, issue)
	}
	values.Set("mode", "bogus")
	if _, _, issue := parseOptionalQueryEnum(values, "mode", []string{"a"}); issue != zodEnumMessage([]string{"a"}, "bogus") {
		t.Fatalf("enum 非法: %q", issue)
	}
	values.Set("mode", "a")
	if text, present, issue := parseOptionalQueryEnum(values, "mode", []string{"a"}); !present || issue != "" || text != "a" {
		t.Fatalf("enum 正常: %q %v %q", text, present, issue)
	}

	// coerce int：缺失、空串、非数、浮点、越界、正常。
	if _, present, issue := parseOptionalQueryInt(values, "absent", 1, 0); present || issue != "" {
		t.Fatalf("int 缺失: %v %q", present, issue)
	}
	values.Set("empty", "")
	if _, _, issue := parseOptionalQueryInt(values, "empty", 1, 0); issue != zodNumberMin(1) {
		t.Fatalf("int 空串: %q", issue)
	}
	values.Set("empty0", "")
	if value, present, issue := parseOptionalQueryInt(values, "empty0", 0, 0); !present || issue != "" || value != 0 {
		t.Fatalf("int 空串 min=0: %d %v %q", value, present, issue)
	}
	values.Set("nan", "abc")
	if _, _, issue := parseOptionalQueryInt(values, "nan", 1, 0); issue != "Expected number, received nan" {
		t.Fatalf("int 非数: %q", issue)
	}
	values.Set("frac", "1.5")
	if _, _, issue := parseOptionalQueryInt(values, "frac", 1, 0); issue != "Expected integer, received float" {
		t.Fatalf("int 浮点: %q", issue)
	}
	values.Set("low", "0")
	if _, _, issue := parseOptionalQueryInt(values, "low", 1, 0); issue != zodNumberMin(1) {
		t.Fatalf("int 过小: %q", issue)
	}
	values.Set("high", "101")
	if _, _, issue := parseOptionalQueryInt(values, "high", 1, 100); issue != zodNumberMax(100) {
		t.Fatalf("int 过大: %q", issue)
	}
	values.Set("ok", " 3 ")
	if value, present, issue := parseOptionalQueryInt(values, "ok", 1, 0); !present || issue != "" || value != 3 {
		t.Fatalf("int 正常: %d %v %q", value, present, issue)
	}
	if _, ok := coerceNumber("abc"); ok {
		t.Fatalf("coerceNumber 非数必须失败")
	}
	if value, ok := coerceNumber("2.5"); !ok {
		t.Fatalf("coerceNumber 浮点必须成功")
	} else if _, isInt := value.(int); isInt {
		t.Fatalf("coerceNumber 浮点必须保持 float 形态")
	}
}

// TestWCBodyHelpers：body 字段链路的 zod 分支。
func TestWCBodyHelpers(t *testing.T) {
	body := map[string]any{"s": " v ", "n": 1.5, "i": float64(3), "b": true, "e": "active", "nil": nil, "long": strings.Repeat("x", 6)}
	if text, issue := trimmedBodyString(body["s"], true, 1, 3); issue != "" || text == nil || *text != "v" {
		t.Fatalf("trimmedBodyString 正常: %v %q", text, issue)
	}
	if _, issue := trimmedBodyString(body["i"], true, 1, 3); issue != zodInvalidType("string", float64(3)) {
		t.Fatalf("trimmedBodyString 非字符串: %q", issue)
	}
	if _, issue := trimmedBodyString("x", true, 2, 3); issue != zodStringMin(2) {
		t.Fatalf("trimmedBodyString 过短: %q", issue)
	}
	if _, issue := trimmedBodyString(body["long"], true, 1, 3); issue != zodStringMax(3) {
		t.Fatalf("trimmedBodyString 过长: %q", issue)
	}
	if text, issue := trimmedBodyString(nil, false, 1, 3); text != nil || issue != "" {
		t.Fatalf("trimmedBodyString 缺失: %v %q", text, issue)
	}
	if text, issue := nullableTrimmedBodyString(nil, true, 3); text != nil || issue != "" {
		t.Fatalf("nullable 显式 null 合法: %v %q", text, issue)
	}
	if _, issue := nullableTrimmedBodyString(1.5, true, 3); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("nullable 非字符串: %q", issue)
	}
	if _, issue := nullableTrimmedBodyString(body["long"], true, 3); issue != zodStringMax(3) {
		t.Fatalf("nullable 过长: %q", issue)
	}
	if text, present, issue := bodyOptionalString(body["s"], true); !present || issue != "" || text != " v " {
		t.Fatalf("bodyOptionalString: %q %v %q", text, present, issue)
	}
	if _, _, issue := bodyOptionalString(1.5, true); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("bodyOptionalString 非法: %q", issue)
	}
	if flag, present, issue := bodyOptionalBool(body["b"], true); !present || issue != "" || !flag {
		t.Fatalf("bodyOptionalBool: %v %v %q", flag, present, issue)
	}
	if _, _, issue := bodyOptionalBool("x", true); issue != zodInvalidType("boolean", "x") {
		t.Fatalf("bodyOptionalBool 非法: %q", issue)
	}
	if value, present, issue := bodyOptionalInt(body["n"], true, 1, 0); present || issue != "Expected integer, received float" {
		t.Fatalf("bodyOptionalInt 浮点: %d %v %q", value, present, issue)
	}
	if value, present, issue := bodyOptionalInt(body["i"], true, 5, 0); present || issue != zodNumberMin(5) {
		t.Fatalf("bodyOptionalInt 过小: %d %v %q", value, present, issue)
	}
	if value, present, issue := bodyOptionalInt(body["i"], true, 1, 2); present || issue != zodNumberMax(2) {
		t.Fatalf("bodyOptionalInt 过大: %d %v %q", value, present, issue)
	}
	if _, _, issue := bodyOptionalInt("x", true, 1, 0); issue != zodInvalidType("number", "x") {
		t.Fatalf("bodyOptionalInt 非数: %q", issue)
	}
	if value, present, issue := bodyOptionalInt(body["i"], false, 1, 0); present || issue != "" || value != 0 {
		t.Fatalf("bodyOptionalInt 缺失: %d %v %q", value, present, issue)
	}
	if text, present, issue := bodyOptionalEnum(body["e"], true, []string{"active", "disabled"}); !present || issue != "" || text != "active" {
		t.Fatalf("bodyOptionalEnum: %q %v %q", text, present, issue)
	}
	if _, _, issue := bodyOptionalEnum("pending", true, []string{"active", "disabled"}); issue != zodEnumMessage([]string{"active", "disabled"}, "pending") {
		t.Fatalf("bodyOptionalEnum 非法: %q", issue)
	}
	if _, _, issue := bodyOptionalEnum(1.5, true, []string{"active"}); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("bodyOptionalEnum 非字符串: %q", issue)
	}
	if !bodyHas(body, "s") || bodyHas(body, "absent") {
		t.Fatalf("bodyHas 分支错误")
	}
	if !hasAnyField(body, []string{"absent", "s"}) || hasAnyField(body, []string{"absent"}) {
		t.Fatalf("hasAnyField 分支错误")
	}
	if unknown := strictObjectKeys(body, "s", "n"); unknown == nil || len(unknown) != len(body)-2 {
		t.Fatalf("strictObjectKeys: %v", unknown)
	}
	if unknown := strictObjectKeys(map[string]any{"a": 1}, "a"); unknown != nil {
		t.Fatalf("strictObjectKeys 全合法必须为 nil: %v", unknown)
	}
	query := url.Values{}
	query.Set("k", "a")
	if got := valuesAsMap(query)["k"]; got != "a" {
		t.Fatalf("valuesAsMap: %v", got)
	}
}

// TestWCBodyChainHelpers：required/optional/nullable 组合字段的完整链路。
func TestWCBodyChainHelpers(t *testing.T) {
	if text, issue := requiredTrimmedBody(map[string]any{"k": " v "}, "k", 1, 3); issue != "" || text != "v" {
		t.Fatalf("requiredTrimmedBody 正常: %q %q", text, issue)
	}
	if _, issue := requiredTrimmedBody(map[string]any{}, "k", 1, 3); issue != zodRequired {
		t.Fatalf("requiredTrimmedBody 缺失: %q", issue)
	}
	if _, issue := requiredTrimmedBody(map[string]any{"k": nil}, "k", 1, 3); issue != zodInvalidType("string", nil) {
		t.Fatalf("requiredTrimmedBody null: %q", issue)
	}
	if text, issue := optionalTrimmedBody(map[string]any{}, "k", 1, 3); text != nil || issue != "" {
		t.Fatalf("optionalTrimmedBody 缺失: %v %q", text, issue)
	}
	if _, issue := optionalTrimmedBody(map[string]any{"k": nil}, "k", 1, 3); issue != zodInvalidType("string", nil) {
		t.Fatalf("optionalTrimmedBody 显式 null: %q", issue)
	}
	if text, issue := optionalTrimmedBody(map[string]any{"k": " v "}, "k", 1, 3); issue != "" || text == nil || *text != "v" {
		t.Fatalf("optionalTrimmedBody 正常: %v %q", text, issue)
	}
	if text, issue := nullableTrimmedBodyField(map[string]any{"k": nil}, "k", 3); text != nil || issue != "" {
		t.Fatalf("nullableTrimmedBodyField null: %v %q", text, issue)
	}
	if _, issue := nullableTrimmedBodyField(map[string]any{"k": 1.5}, "k", 3); issue != zodInvalidType("string", 1.5) {
		t.Fatalf("nullableTrimmedBodyField 非法: %q", issue)
	}
	if flag, present, issue := bodyOptionalBoolField(map[string]any{}, "k"); present || issue != "" || flag {
		t.Fatalf("bodyOptionalBoolField 缺失: %v %v %q", flag, present, issue)
	}
	if flag, present, issue := bodyOptionalBoolField(map[string]any{"k": false}, "k"); !present || issue != "" || flag {
		t.Fatalf("bodyOptionalBoolField false: %v %v %q", flag, present, issue)
	}
	if _, present, issue := bodyOptionalEnumField(map[string]any{"k": "x"}, "k", []string{"a"}); present || issue == "" {
		t.Fatalf("bodyOptionalEnumField 非法必须报错: %v %q", present, issue)
	}
}

// ---------------------------------------------------------------------------
// auth.go 解码与 token 工具
// ---------------------------------------------------------------------------

// TestWCDecodeScopesList：scope JSON 解码的容错与规范化。
func TestWCDecodeScopesList(t *testing.T) {
	if got := decodeScopesList("not json"); len(got) != 0 {
		t.Fatalf("非法 JSON 必须为空: %v", got)
	}
	if got := decodeScopesList(`{"a":1}`); len(got) != 0 {
		t.Fatalf("非数组必须为空: %v", got)
	}
	got := decodeScopesList(`["` + scopeGroupListRead + `"," ` + scopeApiKeyListRead + ` ","bogus","",` + `"` + scopeGroupListRead + `",1]`)
	if len(got) != 2 || got[0] != scopeApiKeyListRead || got[1] != scopeGroupListRead {
		t.Fatalf("scope 解码需去未知/去空/去重/排序: %v", got)
	}
}

// TestWCDecodeRateLimitsList：限流规则 JSON 解码的容错、边界与排序。
func TestWCDecodeRateLimitsList(t *testing.T) {
	if got := decodeRateLimitsList("  "); len(got) != 0 {
		t.Fatalf("空白必须为空: %v", got)
	}
	if got := decodeRateLimitsList("not json"); len(got) != 0 {
		t.Fatalf("非法 JSON 必须为空: %v", got)
	}
	if got := decodeRateLimitsList(`"x"`); len(got) != 0 {
		t.Fatalf("非数组必须为空: %v", got)
	}
	got := decodeRateLimitsList(`[
		{"windowSeconds":3600,"maxRequests":10},
		{"windowSeconds":60,"maxRequests":2},
		{"windowSeconds":60,"maxRequests":9},
		{"windowSeconds":0,"maxRequests":5},
		{"windowSeconds":86401,"maxRequests":5},
		{"windowSeconds":30,"maxRequests":0},
		{"windowSeconds":30,"maxRequests":100001},
		{"windowSeconds":"60","maxRequests":1},
		{"windowSeconds":30,"maxRequests":5}
	]`)
	if len(got) != 3 || got[0].WindowSeconds != 30 || got[1].WindowSeconds != 60 || got[2].WindowSeconds != 3600 {
		t.Fatalf("限流规则需过滤非法并按窗口排序: %v", got)
	}
	if got[0].MaxRequests != 5 || got[1].MaxRequests != 2 {
		t.Fatalf("首个同窗口规则生效: %v", got)
	}
}

// TestWCShouldTouchLastUsed：60 秒节流的分支矩阵。
func TestWCShouldTouchLastUsed(t *testing.T) {
	now := int64(1_000_000)
	if shouldTouchLastUsed(sql.NullString{}, nil) {
		t.Fatalf("now 为 nil 不得触碰")
	}
	if !shouldTouchLastUsed(sql.NullString{}, &now) {
		t.Fatalf("无历史必须触碰")
	}
	if shouldTouchLastUsed(sql.NullString{String: "bad", Valid: true}, &now) {
		t.Fatalf("历史不可解析不得触碰")
	}
	if shouldTouchLastUsed(sql.NullString{String: time.UnixMilli(now - 30_000).UTC().Format(time.RFC3339Nano), Valid: true}, &now) {
		t.Fatalf("60 秒内不得重复触碰")
	}
	if !shouldTouchLastUsed(sql.NullString{String: time.UnixMilli(now - 61_000).UTC().Format(time.RFC3339Nano), Valid: true}, &now) {
		t.Fatalf("超过 60 秒必须触碰")
	}
}

// TestWCBearerToken：Authorization 头解析矩阵。
func TestWCBearerToken(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"缺失", "", ""},
		{"非 Bearer", "Basic abc", ""},
		{"空 token", "Bearer   ", ""},
		{"标准", "Bearer tok", "tok"},
		{"小写 scheme", "bearer tok", "tok"},
		{"多空白", "Bearer   tok  ", "tok"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			if testCase.header != "" {
				request.Header.Set("Authorization", testCase.header)
			}
			if got := bearerToken(request); got != testCase.want {
				t.Fatalf("得到 %q，期望 %q", got, testCase.want)
			}
		})
	}
}

// TestWCAuthContextPlumbing：auth context 进出 request context。
func TestWCAuthContextPlumbing(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	if AuthContextFrom(request) != nil {
		t.Fatalf("无 auth context 必须为 nil")
	}
	expected := &AuthContext{SourceRefID: "src"}
	got := AuthContextFrom(request.WithContext(withAuthContext(request.Context(), expected)))
	if got != expected {
		t.Fatalf("auth context 必须原样取回")
	}
}

// ---------------------------------------------------------------------------
// capture.go 内部构件
// ---------------------------------------------------------------------------

// TestWCSpecQuery：express req.query 的单值/重复键投影。
func TestWCSpecQuery(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/x?a=1&a=2&b=3", nil)
	query := specQuery(request)
	if query["b"] != "3" {
		t.Fatalf("单值必须保持字符串: %v", query["b"])
	}
	list, isList := query["a"].([]any)
	if !isList || len(list) != 2 || list[0] != "1" || list[1] != "2" {
		t.Fatalf("重复键必须聚合数组: %v", query["a"])
	}
}

// TestWCDecodeCapturePayload：响应快照的 JSON/非 JSON/空分支。
func TestWCDecodeCapturePayload(t *testing.T) {
	if decodeCapturePayload([]byte("  ")) != nil {
		t.Fatalf("空负载必须为 nil")
	}
	if document, ok := decodeCapturePayload([]byte(`{"a":1}`)).(map[string]any); !ok || document["a"] != float64(1) {
		t.Fatalf("JSON 必须解码为文档: %v", decodeCapturePayload([]byte(`{"a":1}`)))
	}
	if text, ok := decodeCapturePayload([]byte("plain")).(string); !ok || text != "plain" {
		t.Fatalf("非 JSON 必须退化为字符串: %v", decodeCapturePayload([]byte("plain")))
	}
}

// TestWCDecodeCaptureBody：请求快照的缺失/解析失败/正常分支。
func TestWCDecodeCaptureBody(t *testing.T) {
	if decodeCaptureBody(nil) != nil {
		t.Fatalf("无 body 必须为 nil")
	}
	if decodeCaptureBody(&captureRequestBody{parseFailed: true, raw: []byte(`{`)}) != nil {
		t.Fatalf("解析失败必须为 nil")
	}
	if decodeCaptureBody(&captureRequestBody{raw: []byte("  ")}) != nil {
		t.Fatalf("空白 body 必须为 nil")
	}
	if document, ok := decodeCaptureBody(&captureRequestBody{raw: []byte(`{"k":1}`)}).(map[string]any); !ok || document["k"] != float64(1) {
		t.Fatalf("合法 body 必须解码: %v", decodeCaptureBody(&captureRequestBody{raw: []byte(`{"k":1}`)}))
	}
}

// TestWCBufferCaptureRequestBody：body 缓存的方法分支与解析失败标记。
func TestWCBufferCaptureRequestBody(t *testing.T) {
	if bufferCaptureRequestBody(httptest.NewRequest(http.MethodGet, "/", nil)) != nil {
		t.Fatalf("GET 不缓存 body")
	}
	if buffered := bufferCaptureRequestBody(httptest.NewRequest(http.MethodPost, "/", nil)); buffered == nil || buffered.parseFailed || len(buffered.raw) != 0 {
		t.Fatalf("空 body POST 必须返回空缓存: %+v", buffered)
	}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"a":1}`))
	buffered := bufferCaptureRequestBody(request)
	if buffered.parseFailed {
		t.Fatalf("合法 JSON 不得标记失败")
	}
	// 缓存后 body 仍可读（不消费请求体）。
	body, err := ioAll(request)
	if err != nil || string(body) != `{"a":1}` {
		t.Fatalf("缓存后 body 必须仍可读: %q %v", body, err)
	}
	broken := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{broken`))
	if buffered := bufferCaptureRequestBody(broken); !buffered.parseFailed {
		t.Fatalf("非法 JSON 必须标记 parseFailed")
	}
}

func ioAll(request *http.Request) ([]byte, error) {
	buffer := make([]byte, 128)
	count, err := request.Body.Read(buffer)
	return buffer[:count], err
}

// wcFlushRecorder 用于构造不支持 Flush 的 ResponseWriter。
type wcFlushRecorder struct {
	*httptest.ResponseRecorder
}

// TestWCCaptureResponseWriter：状态捕获、预算截断与 Flush 透传。
func TestWCCaptureResponseWriter(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &captureResponseWriter{ResponseWriter: recorder, status: http.StatusOK}
	writer.WriteHeader(http.StatusTeapot)
	writer.WriteHeader(http.StatusAccepted) // 第二次必须被忽略
	if writer.status != http.StatusTeapot || recorder.Code != http.StatusTeapot {
		t.Fatalf("WriteHeader 首次生效: %d %d", writer.status, recorder.Code)
	}
	if _, err := writer.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if recorder.Body.String() != "hello" {
		t.Fatalf("Write 必须透传: %q", recorder.Body.String())
	}
	// 预算外字节只截断快照，不透传失败。
	writer.payload = make([]byte, captureSnapshotBudget)
	if _, err := writer.Write(make([]byte, 100)); err != nil {
		t.Fatalf("预算外 Write: %v", err)
	}
	if len(writer.payload) != captureSnapshotBudget {
		t.Fatalf("快照必须停在预算: %d", len(writer.payload))
	}
	writer.Flush() // httptest.ResponseRecorder 支持 Flush，不得 panic
	plain := &captureResponseWriter{ResponseWriter: &wcFlushRecorder{recorder}}
	plain.Flush() // 不支持 Flush 的底层 writer 必须静默跳过
}

// TestWCBufferCaptureRequestBodyPutPatch：PUT/PATCH 同样缓存。
func TestWCBufferCaptureRequestBodyPutPatch(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		request := httptest.NewRequest(method, "/", strings.NewReader(`{"m":1}`))
		if buffered := bufferCaptureRequestBody(request); buffered == nil || buffered.parseFailed {
			t.Fatalf("%s 必须缓存 body: %+v", method, buffered)
		}
	}
}

// TestWCCaptureSourceContext：auth context 到 SourceContext 的投影与 nil 容错。
func TestWCCaptureSourceContext(t *testing.T) {
	if captureSourceContext(nil) != nil {
		t.Fatalf("nil context 必须返回 nil")
	}
	source := captureSourceContext(&AuthContext{SourceRefID: "s1", SourceName: "n1", TokenID: "t1", TokenName: "tn", TokenPrefix: "pre", IsTestToken: true})
	if source == nil || source.SourceRefID != "s1" || source.TokenPrefix != "pre" || !source.IsTestToken {
		t.Fatalf("SourceContext 投影错误: %+v", source)
	}
}

// TestWCPublicApiLogCaptureSink：sink 适配器的 finish/closed/nil 分支。
func TestWCPublicApiLogCaptureSink(t *testing.T) {
	var sink PublicApiLogCaptureSink
	sink.CaptureAIPublic(publicapilogs.CaptureSpec{}) // nil sink 必须安全

	var inputs []publicapilogs.Input
	adapter := PublicApiLogCaptureSink(func(input publicapilogs.Input) bool {
		inputs = append(inputs, input)
		return true
	})
	started := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	spec := publicapilogs.CaptureSpec{
		Method: "GET", BaseURL: Prefix, Path: "/group/list",
		OriginalURL: Prefix + "/group/list?a=1",
		Query:       map[string]any{"a": "1"},
		StatusCode:  200, ResponsePayload: map[string]any{"ok": true},
		StartedAt: started, EndedAt: started.Add(10 * time.Millisecond), DurationMS: 10,
		Source: &publicapilogs.SourceContext{SourceRefID: "s1"},
	}
	adapter.CaptureAIPublic(spec)
	if len(inputs) != 1 {
		t.Fatalf("finish 必须记录一次: %d", len(inputs))
	}
	if inputs[0].StatusCode != 200 || !inputs[0].Success {
		t.Fatalf("finish 投影错误: %+v", inputs[0])
	}

	adapter.CaptureAIPublic(publicapilogs.CaptureSpec{
		Method: "GET", StatusCode: 200, Closed: true,
		StartedAt: started, EndedAt: started,
	})
	if len(inputs) != 2 {
		t.Fatalf("closed 必须记录一次: %d", len(inputs))
	}
	if inputs[1].StatusCode != 499 || inputs[1].Success || inputs[1].ErrorCode != "public_api_client_closed" {
		t.Fatalf("closed 投影错误: %+v", inputs[1])
	}
}

// ---------------------------------------------------------------------------
// operationlog.go
// ---------------------------------------------------------------------------

// TestWCScalarText：操作日志字段投影的类型矩阵。
func TestWCScalarText(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"nil", scalarText(nil), ""},
		{"string", scalarText("x"), "x"},
		{"bool true", scalarText(true), "true"},
		{"bool false", scalarText(false), "false"},
		{"number", scalarText(3.5), "3.5"},
		{"map", scalarText(map[string]any{"a": 1}), `{"a":1}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.got != testCase.want {
				t.Fatalf("得到 %q，期望 %q", testCase.got, testCase.want)
			}
		})
	}
	if text := boolText(true); text != "true" {
		t.Fatalf("boolText(true): %q", text)
	}
	if text := boolText(false); text != "false" {
		t.Fatalf("boolText(false): %q", text)
	}
	if raw := marshalRawMessage(map[string]any{"k": "v"}); string(raw) != `{"k":"v"}` {
		t.Fatalf("marshalRawMessage: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// target.go
// ---------------------------------------------------------------------------

// TestWCServiceMessage：错误优先、fallback 兜底。
func TestWCServiceMessage(t *testing.T) {
	if got := serviceMessage(nil, "fallback"); got != "fallback" {
		t.Fatalf("nil err: %q", got)
	}
	if got := serviceMessage(nil, ""); got != "" {
		t.Fatalf("nil err 空 fallback: %q", got)
	}
	if got := serviceMessage(assertError("业务消息"), "fallback"); got != "业务消息" {
		t.Fatalf("err 优先: %q", got)
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }

// TestWCWriteServiceError：不存在→404、已存在/重复→409、其余→400。
func TestWCWriteServiceError(t *testing.T) {
	deps := &Deps{}
	cases := []struct {
		name    string
		err     error
		status  int
		message string
	}{
		{"不存在", assertError("分组不存在"), http.StatusNotFound, "分组不存在"},
		{"err 为 nil 走 fallback 404", nil, http.StatusNotFound, "分组不存在"},
		{"已存在", assertError("账号已存在：x"), http.StatusConflict, "账号已存在：x"},
		{"重复", assertError("名称重复"), http.StatusConflict, "名称重复"},
		{"其他", assertError("参数有误"), http.StatusBadRequest, "参数有误"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			deps.writeServiceError(recorder, testCase.err, "分组不存在")
			if recorder.Code != testCase.status {
				t.Fatalf("status: %d %s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), `"message":"`+testCase.message+`"`) {
				t.Fatalf("message: %s", recorder.Body.String())
			}
		})
	}
	recorder := httptest.NewRecorder()
	writeConflict(recorder, "冲突")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("writeConflict: %d", recorder.Code)
	}
}

// TestWCAssertSupportedPushAccountType：公开推送只允许 api_key 账户。
func TestWCAssertSupportedPushAccountType(t *testing.T) {
	profile := &providerProfile{AccountTypes: []string{"api_key"}}
	if err := assertSupportedPushAccountType("", profile); err == nil || err.Error() != "账号类型不能为空" {
		t.Fatalf("空类型: %v", err)
	}
	if err := assertSupportedPushAccountType("oauth", profile); err == nil || err.Error() != "账号新增仅支持 API Key 账户" {
		t.Fatalf("非 api_key: %v", err)
	}
	if err := assertSupportedPushAccountType("api_key", &providerProfile{AccountTypes: []string{"oauth"}}); err == nil || err.Error() != "当前供应商不支持 API Key 账户" {
		t.Fatalf("档案不支持: %v", err)
	}
	if err := assertSupportedPushAccountType("api_key", profile); err != nil {
		t.Fatalf("合法组合: %v", err)
	}
	if err := assertSupportedPushAccountType("api_key", nil); err != nil {
		t.Fatalf("nil 档案跳过档案检查: %v", err)
	}
}

// TestWCRandomSecret：随机密钥为 18 字节 base64url。
func TestWCRandomSecret(t *testing.T) {
	secret, err := randomSecret()
	if err != nil {
		t.Fatalf("randomSecret: %v", err)
	}
	if len(secret) != 24 { // 18 字节 base64url 无填充 = 24 字符
		t.Fatalf("长度: %d", len(secret))
	}
	if strings.ContainsAny(secret, "+/=") {
		t.Fatalf("必须是 base64url 字母表: %q", secret)
	}
}

// TestWCSmallGroupHelpers：分页/排序/字段工具的边界。
func TestWCSmallGroupHelpers(t *testing.T) {
	if got := textPrefixUpperBound("abc"); got != "abd" {
		t.Fatalf("textPrefixUpperBound ascii: %q", got)
	}
	if got := textPrefixUpperBound("福利"); got == "" || got <= "福利" {
		t.Fatalf("textPrefixUpperBound 中文需大于原值: %q", got)
	}
	if got := textPrefixUpperBound(string(rune(0x10FFFF))); !strings.HasSuffix(got, "\uffff") {
		t.Fatalf("最大码点走 ffff 兜底: %q", got)
	}
	if pagedTotalUpperBound(2, 20, 5, true) != 26 {
		t.Fatalf("pagedTotalUpperBound hasMore: %d", pagedTotalUpperBound(2, 20, 5, true))
	}
	if pagedTotalUpperBound(2, 20, 5, false) != 25 {
		t.Fatalf("pagedTotalUpperBound: %d", pagedTotalUpperBound(2, 20, 5, false))
	}
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatalf("boolToInt")
	}
	if groupTypeOr("", false) != "personal" || groupTypeOr("", true) != "personal" || groupTypeOr("high_concurrency", true) != "high_concurrency" {
		t.Fatalf("groupTypeOr")
	}
	if got := isPlainObject(map[string]any{}); !got {
		t.Fatalf("isPlainObject object")
	}
	if isPlainObject([]any{1}) {
		t.Fatalf("isPlainObject 数组必须为 false")
	}
	if ownerID(nil) != "" || ownerAccountID(nil) != "" || ownerUpdatedAt(nil) != "" {
		t.Fatalf("owner 助手 nil 必须为空串")
	}
	owner := &strategyOwnerLookup{ID: "i", SystemAccountID: "s", UpdatedAt: "u"}
	if ownerID(owner) != "i" || ownerAccountID(owner) != "s" || ownerUpdatedAt(owner) != "u" {
		t.Fatalf("owner 助手投影错误")
	}
	if got := trimSpaces("  a\tb  "); got != "a\tb" {
		t.Fatalf("trimSpaces: %q", got)
	}
	override := "new"
	if valueOrEmpty(&override, "old") != "new" {
		t.Fatalf("valueOrEmpty 覆盖优先")
	}
	if valueOrEmpty(nil, " old ") != "old" {
		t.Fatalf("valueOrEmpty 当前值去空白")
	}
	if valueOrEmpty(nil, 1.5) != "" {
		t.Fatalf("valueOrEmpty 非字符串为空")
	}
	deps := &Deps{}
	if page, size := deps.paging(true, 3, true, 7); page != 3 || size != 7 {
		t.Fatalf("paging 显式值: %d %d", page, size)
	}
	if page, size := deps.paging(false, 0, false, 0); page != 1 || size != 20 {
		t.Fatalf("paging 默认值: %d %d", page, size)
	}
	if page, size := deps.strategyPaging(false, 0, false, 0); page != 1 || size != 50 {
		t.Fatalf("strategyPaging 默认值: %d %d", page, size)
	}
	if page, size := deps.accountPaging(false, 0, false, 0); page != 1 || size != 50 {
		t.Fatalf("accountPaging 默认值: %d %d", page, size)
	}
	if page, size := deps.apiKeyPaging(false, 0, false, 0); page != 1 || size != 50 {
		t.Fatalf("apiKeyPaging 默认值: %d %d", page, size)
	}
	if page, size := deps.mockPaging(true, 2, true, 9); page != 2 || size != 9 {
		t.Fatalf("mockPaging: %d %d", page, size)
	}
}

// ---------------------------------------------------------------------------
// dto.go 信封与投影
// ---------------------------------------------------------------------------

// TestWCEnvelopes：stats/mock 信封与 201 信封的形态。
func TestWCEnvelopes(t *testing.T) {
	deps := &Deps{Now: func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }}
	recorder := httptest.NewRecorder()
	deps.writeStatsEnvelope(recorder, map[string]any{"action": "updated"})
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"source":"stats"`) {
		t.Fatalf("stats 信封: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.writeStatsCreated(recorder, map[string]any{"action": "created"})
	if recorder.Code != http.StatusCreated || !strings.Contains(recorder.Body.String(), `"source":"stats"`) {
		t.Fatalf("stats created 信封: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.writeMockEnvelope(recorder, http.StatusOK, map[string]any{"action": "mock"})
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"source":"mock"`) {
		t.Fatalf("mock 信封: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	deps.writeMockEnvelope(recorder, http.StatusCreated, map[string]any{"action": "mock"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("mock 201 信封: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := deps.generatedAt(); !strings.HasPrefix(got, "2026-09-04T12:00:00") {
		t.Fatalf("generatedAt: %q", got)
	}
}

// TestWCEmptyTargets：not_found 目标投影保留用户输入。
func TestWCEmptyTargets(t *testing.T) {
	target := emptyTarget(" u ")
	if target.Username != "u" || target.DisplayName != "u" || target.SystemAccountID != "" {
		t.Fatalf("emptyTarget: %+v", target)
	}
	groupTarget := emptyGroupTarget(" u ", " g ")
	if groupTarget.Username != "u" || groupTarget.GroupName != "g" || groupTarget.GroupID != "" {
		t.Fatalf("emptyGroupTarget: %+v", groupTarget)
	}
}

// TestWCSanitizeProjections：公开 DTO 只投影白名单字段。
func TestWCSanitizeProjections(t *testing.T) {
	description := "d"
	group := sanitizeGroup(&groups.Detail{ID: "g1", Name: "n", ProviderCode: "gpt", Description: &description, Enabled: true, GroupType: "personal", IsDefault: true})
	if group.ID != "g1" || group.Description == nil || *group.Description != "d" || !group.IsDefault {
		t.Fatalf("sanitizeGroup: %+v", group)
	}
	normalConfig := &routestrategies.NormalRoutingConfig{}
	binding := routestrategies.GroupBinding{ID: "b1", GroupID: "g1", Priority: 2, Weight: 30, Status: "active", GroupEnabled: true}
	strategy := sanitizeStrategy(&routestrategies.Detail{
		ID: "s1", Name: "策略", Mode: "normal", Status: "active",
		NormalRoutingConfig: normalConfig, GroupBindings: []routestrategies.GroupBinding{binding},
		APIKeyCount: 2, CreatedAt: "c", UpdatedAt: "u",
	})
	if strategy.NormalRoutingConfig == nil || len(strategy.GroupBindings) != 1 || strategy.GroupBindings[0].GroupID != "g1" || strategy.APIKeyCount != 2 {
		t.Fatalf("sanitizeStrategy: %+v", strategy)
	}
	if summary := sanitizeStrategy(&routestrategies.Detail{ID: "s2"}); summary.NormalRoutingConfig != nil || summary.GroupBindings == nil || len(summary.GroupBindings) != 0 {
		t.Fatalf("sanitizeStrategy 空配置: %+v", summary)
	}
	expires := "2030-01-01T00:00:00Z"
	item := sanitizeApiKeyItem(&apikeys.ListItem{
		ID: "k1", Name: "key", KeyPrefix: "pre", Status: "active",
		RouteStrategyID: "s1", ExpiresAt: &expires,
	})
	if item.ID != "k1" || item.Key != nil || item.ExpiresAt == nil {
		t.Fatalf("sanitizeApiKeyItem: %+v", item)
	}
	account := sanitizeAccountItem(&accounts.ListItem{
		ID: "a1", Name: "acc", ProviderCode: "gpt", ProviderProtocolProfileID: "p1",
		ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key",
		ClientCompatibility: "openai_standard", Status: "active", Schedulable: true,
		ConcurrencyLimit: 9, Priority: 4,
	}, []string{"gpt-4o"})
	if account.ID != "a1" || account.ProviderProtocolProfileID == nil || len(account.SupportedModels) != 1 || account.ConcurrencyLimit != 9 || account.Priority != 4 {
		t.Fatalf("sanitizeAccountItem: %+v", account)
	}
	bare := sanitizeAccountItem(&accounts.ListItem{ID: "a2", Type: "api_key"}, nil)
	if bare.ProviderProtocolProfileID != nil || bare.SupportedModels != nil {
		t.Fatalf("空档案/模型字段必须省略: %+v", bare)
	}
}
