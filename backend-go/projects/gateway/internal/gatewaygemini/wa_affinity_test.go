package gatewaygemini

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// waAffinityStore 是 AffinityStateStore 的确定性内存实现，可注入错误。
type waAffinityStore struct {
	data      map[string][]byte
	getErr    error
	setErr    error
	delErr    error
	setCalls  []string
	delCalls  []string
	lastValue map[string]AffinityBinding
}

func newWaAffinityStore() *waAffinityStore {
	return &waAffinityStore{data: map[string][]byte{}, lastValue: map[string]AffinityBinding{}}
}

func (s *waAffinityStore) GetJSON(_ context.Context, key string, dest any) (bool, error) {
	if s.getErr != nil {
		return false, s.getErr
	}
	raw, ok := s.data[key]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return false, err
	}
	return true, nil
}

func (s *waAffinityStore) SetJSON(_ context.Context, key string, value any, _ time.Duration) error {
	if s.setErr != nil {
		return s.setErr
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.data[key] = encoded
	if binding, ok := value.(AffinityBinding); ok {
		s.lastValue[key] = binding
	}
	s.setCalls = append(s.setCalls, key)
	return nil
}

func (s *waAffinityStore) Delete(_ context.Context, key string) error {
	if s.delErr != nil {
		return s.delErr
	}
	delete(s.data, key)
	s.delCalls = append(s.delCalls, key)
	return nil
}

// 固定时钟保证 CreatedAtMs 等时间输出确定。
var waFixedNow = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

func waGeminiAccount() UpstreamAccount {
	return UpstreamAccount{ID: "acc-1", Type: "api_key", BaseURL: "", ProviderCode: ProviderCode, ProviderProtocolProfileID: "profile-1"}
}

// Resolve/Remember/Delete 与续期语义（Redis store 注入）。
func TestWAInteractionAffinityWithStore(t *testing.T) {
	ctx := context.Background()
	scope := AffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	newAffinity := func(store *waAffinityStore) *InteractionAffinity {
		return NewInteractionAffinity(store).WithNowFunc(func() time.Time { return waFixedNow })
	}

	t.Run("Remember 与 Resolve 命中", func(t *testing.T) {
		store := newWaAffinityStore()
		affinity := newAffinity(store)
		result, err := affinity.Remember(ctx, " ix_1 ", waGeminiAccount(), scope)
		if err != nil {
			t.Fatalf("Remember error: %v", err)
		}
		if result.Action != AffinityActionRemembered || result.InteractionID != "ix_1" {
			t.Fatalf("Remember 结果 = %+v", result)
		}
		// 命中读取。
		request := httptest.NewRequest("GET", "/v1beta/interactions/ix_1", nil)
		binding, found, err := affinity.Resolve(ctx, request, scope)
		if err != nil || !found {
			t.Fatalf("Resolve = %v/%v", found, err)
		}
		if binding.AccountID != "acc-1" || binding.InteractionID != "ix_1" || binding.ProviderCode != "gemini" || binding.ProviderProtocolProfileID != "profile-1" {
			t.Fatalf("binding = %+v", binding)
		}
		if binding.CreatedAtMs != waFixedNow.UnixMilli() {
			t.Fatalf("CreatedAtMs = %d，期望固定时钟值", binding.CreatedAtMs)
		}
		// 命中续期：SetJSON 被再次调用。
		if len(store.setCalls) != 2 {
			t.Fatalf("命中应续期（set 调用 2 次），实际 %d", len(store.setCalls))
		}
	})
	t.Run("未命中与非法 ID", func(t *testing.T) {
		store := newWaAffinityStore()
		affinity := newAffinity(store)
		request := httptest.NewRequest("GET", "/v1beta/interactions/missing", nil)
		if _, found, _ := affinity.Resolve(ctx, request, scope); found {
			t.Fatal("未记忆的 ID 不应命中")
		}
		// 非资源请求直接未命中。
		create := httptest.NewRequest("POST", "/v1beta/interactions", nil)
		if _, found, _ := affinity.Resolve(ctx, create, scope); found {
			t.Fatal("create 请求无资源 ID 不应命中")
		}
		// Remember 空 ID 不写。
		result, err := affinity.Remember(ctx, "  ", waGeminiAccount(), scope)
		if err != nil || result.Action != AffinityActionNone {
			t.Fatalf("空 ID Remember = %+v/%v", result, err)
		}
		if len(store.setCalls) != 0 {
			t.Fatalf("空 ID 不应写 store: %v", store.setCalls)
		}
	})
	t.Run("Delete", func(t *testing.T) {
		store := newWaAffinityStore()
		affinity := newAffinity(store)
		if _, err := affinity.Remember(ctx, "ix_1", waGeminiAccount(), scope); err != nil {
			t.Fatalf("Remember error: %v", err)
		}
		result, err := affinity.Delete(ctx, "ix_1", scope)
		if err != nil || result.Action != AffinityActionDeleted || result.InteractionID != "ix_1" {
			t.Fatalf("Delete 结果 = %+v/%v", result, err)
		}
		request := httptest.NewRequest("DELETE", "/v1beta/interactions/ix_1", nil)
		if _, found, _ := affinity.Resolve(ctx, request, scope); found {
			t.Fatal("删除后不应命中")
		}
		// 空 ID Delete 不产生动作。
		result, err = affinity.Delete(ctx, "", scope)
		if err != nil || result.Action != AffinityActionNone {
			t.Fatalf("空 ID Delete = %+v/%v", result, err)
		}
	})
	t.Run("非法绑定读取后清除", func(t *testing.T) {
		store := newWaAffinityStore()
		affinity := newAffinity(store)
		// 写入一个字段缺失的非法绑定。
		key := affinityKey(scope, "ix_bad")
		if err := store.SetJSON(ctx, key, AffinityBinding{InteractionID: "ix_bad"}, time.Hour); err != nil {
			t.Fatalf("写入非法绑定失败: %v", err)
		}
		request := httptest.NewRequest("GET", "/v1beta/interactions/ix_bad", nil)
		if _, found, _ := affinity.Resolve(ctx, request, scope); found {
			t.Fatal("非法绑定不应命中")
		}
		if len(store.delCalls) != 1 || store.delCalls[0] != key {
			t.Fatalf("非法绑定应被清除: %v", store.delCalls)
		}
	})
	t.Run("store 错误注入", func(t *testing.T) {
		boom := errors.New("store boom")
		store := newWaAffinityStore()
		store.setErr = boom
		affinity := newAffinity(store)
		_, err := affinity.Remember(ctx, "ix_1", waGeminiAccount(), scope)
		var unavailable *AffinityUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("Remember 应返回 AffinityUnavailableError: %v", err)
		}
		if unavailable.StatusCode() != 503 || unavailable.ErrorType() != "service_unavailable" {
			t.Fatalf("错误状态/类型 = %d/%q", unavailable.StatusCode(), unavailable.ErrorType())
		}
		if !strings.Contains(unavailable.Error(), "记录暂时不可用") {
			t.Fatalf("remember 错误消息 = %q", unavailable.Error())
		}
		if !errors.Is(err, boom) {
			t.Fatal("原始错误应可 Unwrap")
		}

		store2 := newWaAffinityStore()
		store2.delErr = boom
		_, err = newAffinity(store2).Delete(ctx, "ix_1", scope)
		if !errors.As(err, &unavailable) || !strings.Contains(unavailable.Error(), "删除暂时不可用") {
			t.Fatalf("Delete 错误 = %v", err)
		}

		store3 := newWaAffinityStore()
		store3.getErr = boom
		request := httptest.NewRequest("GET", "/v1beta/interactions/ix_1", nil)
		if _, found, err := newAffinity(store3).Resolve(ctx, request, scope); found || err != nil {
			t.Fatalf("Resolve 读错误应吞掉并视为未命中: %v/%v", found, err)
		}
	})
}

// UpdateAfterSuccess 分支：create 记忆、delete 清除、其他请求续期。
func TestWAInteractionAffinityUpdateAfterSuccess(t *testing.T) {
	ctx := context.Background()
	scope := AffinityScope{SystemAccountID: "sys-1", APIKeyID: "key-1", GroupID: "grp-1"}

	t.Run("非 gemini 账户不动作", func(t *testing.T) {
		affinity := NewInteractionAffinity(nil).WithNowFunc(func() time.Time { return waFixedNow })
		request := httptest.NewRequest("POST", "/v1beta/interactions", nil)
		result, err := affinity.UpdateAfterSuccess(ctx, UpdateAfterSuccessInput{Request: request, Account: UpstreamAccount{ProviderCode: "openai"}, Scope: scope})
		if err != nil || result.Action != AffinityActionNone {
			t.Fatalf("非 gemini 账户 = %+v/%v", result, err)
		}
	})
	t.Run("create 请求按响应体记 ID", func(t *testing.T) {
		store := newWaAffinityStore()
		affinity := NewInteractionAffinity(store).WithNowFunc(func() time.Time { return waFixedNow })
		request := httptest.NewRequest("POST", "/v1beta/interactions", nil)
		body := "data: {\"interaction\":{\"id\":\" ix_body \"}}\n\ndata: [DONE]\n\n"
		result, err := affinity.UpdateAfterSuccess(ctx, UpdateAfterSuccessInput{Request: request, ResponseBodyText: body, Account: waGeminiAccount(), Scope: scope})
		if err != nil || result.Action != AffinityActionRemembered || result.InteractionID != "ix_body" {
			t.Fatalf("create 记忆 = %+v/%v", result, err)
		}
	})
	t.Run("create 请求优先用显式 ResourceID", func(t *testing.T) {
		store := newWaAffinityStore()
		affinity := NewInteractionAffinity(store).WithNowFunc(func() time.Time { return waFixedNow })
		request := httptest.NewRequest("POST", "/v1beta/interactions", nil)
		result, _ := affinity.UpdateAfterSuccess(ctx, UpdateAfterSuccessInput{
			Request: request, ResponseResourceID: " ix_direct ", ResponseBodyText: "not-json", Account: waGeminiAccount(), Scope: scope,
		})
		if result.Action != AffinityActionRemembered || result.InteractionID != "ix_direct" {
			t.Fatalf("显式 ID = %+v", result)
		}
	})
	t.Run("delete 请求清除", func(t *testing.T) {
		store := newWaAffinityStore()
		affinity := NewInteractionAffinity(store).WithNowFunc(func() time.Time { return waFixedNow })
		if _, err := affinity.Remember(ctx, "ix_1", waGeminiAccount(), scope); err != nil {
			t.Fatalf("Remember error: %v", err)
		}
		request := httptest.NewRequest("DELETE", "/v1beta/interactions/ix_1", nil)
		result, err := affinity.UpdateAfterSuccess(ctx, UpdateAfterSuccessInput{Request: request, Account: waGeminiAccount(), Scope: scope})
		if err != nil || result.Action != AffinityActionDeleted {
			t.Fatalf("delete 清除 = %+v/%v", result, err)
		}
	})
	t.Run("GET 请求命中续期与未命中", func(t *testing.T) {
		store := newWaAffinityStore()
		affinity := NewInteractionAffinity(store).WithNowFunc(func() time.Time { return waFixedNow })
		request := httptest.NewRequest("GET", "/v1beta/interactions/ix_1", nil)
		// 未命中。
		result, _ := affinity.UpdateAfterSuccess(ctx, UpdateAfterSuccessInput{Request: request, Account: waGeminiAccount(), Scope: scope})
		if result.Action != AffinityActionNone {
			t.Fatalf("未命中 = %+v", result)
		}
		// 记忆后命中 → refreshed。
		if _, err := affinity.Remember(ctx, "ix_1", waGeminiAccount(), scope); err != nil {
			t.Fatalf("Remember error: %v", err)
		}
		result, err := affinity.UpdateAfterSuccess(ctx, UpdateAfterSuccessInput{Request: request, Account: waGeminiAccount(), Scope: scope})
		if err != nil || result.Action != AffinityActionRefreshed || result.InteractionID != "ix_1" {
			t.Fatalf("续期 = %+v/%v", result, err)
		}
	})
	t.Run("非资源非 create 请求不动作", func(t *testing.T) {
		affinity := NewInteractionAffinity(nil).WithNowFunc(func() time.Time { return waFixedNow })
		request := httptest.NewRequest("POST", "/v1beta/models/m:generatecontent", nil)
		result, err := affinity.UpdateAfterSuccess(ctx, UpdateAfterSuccessInput{Request: request, Account: waGeminiAccount(), Scope: scope})
		if err != nil || result.Action != AffinityActionNone {
			t.Fatalf("普通请求 = %+v/%v", result, err)
		}
	})
}

// 无 store 的进程内回退缓存：读写、删除与 LRU 容量。
func TestWAAffinityMemoryCache(t *testing.T) {
	ctx := context.Background()
	affinity := NewInteractionAffinity(nil).WithNowFunc(func() time.Time { return waFixedNow })
	scope := AffinityScope{SystemAccountID: "sys", APIKeyID: "key", GroupID: "grp"}
	if _, err := affinity.Remember(ctx, "ix_1", waGeminiAccount(), scope); err != nil {
		t.Fatalf("内存回退 Remember error: %v", err)
	}
	request := httptest.NewRequest("GET", "/v1beta/interactions/ix_1", nil)
	if binding, found, _ := affinity.Resolve(ctx, request, scope); !found || binding.InteractionID != "ix_1" {
		t.Fatalf("内存回退 Resolve = %+v", binding)
	}
	// 直接操作内部缓存验证 LRU 淘汰（容量 2）。
	cache := newAffinityMemoryCache(2, time.Hour)
	first := AffinityBinding{InteractionID: "a", AccountID: "acc", GroupID: "g", ProviderCode: ProviderCode, CreatedAtMs: 1}
	second := AffinityBinding{InteractionID: "b", AccountID: "acc", GroupID: "g", ProviderCode: ProviderCode, CreatedAtMs: 2}
	third := AffinityBinding{InteractionID: "c", AccountID: "acc", GroupID: "g", ProviderCode: ProviderCode, CreatedAtMs: 3}
	cache.set("k1", first)
	cache.set("k2", second)
	cache.set("k3", third) // 容量满，淘汰 k1
	if _, ok := cache.get("k1"); ok {
		t.Fatal("k1 应被 LRU 淘汰")
	}
	if _, ok := cache.get("k2"); !ok {
		t.Fatal("k2 应保留")
	}
	cache.delete("k2")
	if _, ok := cache.get("k2"); ok {
		t.Fatal("删除后 k2 不应命中")
	}
	// 负 TTL 让条目立刻过期，覆盖过期分支（无 sleep）。
	expiring := newAffinityMemoryCache(4, -time.Hour)
	expiring.set("k", first)
	if _, ok := expiring.get("k"); ok {
		t.Fatal("过期条目不应命中")
	}
	// 已删除 key 重复删除不 panic（order 中不存在）。
	expiring.delete("k")
}

// affinityKey 契约：apiKeyID 缺省回退 internal:group；sha256 十六进制 64 位。
func TestWAAffinityKey(t *testing.T) {
	withKey := affinityKey(AffinityScope{SystemAccountID: "s", APIKeyID: "k", GroupID: "g"}, "ix")
	fallback := affinityKey(AffinityScope{SystemAccountID: "s", GroupID: "g"}, "ix")
	if len(withKey) != len("interaction:")+64 || !strings.HasPrefix(withKey, "interaction:") {
		t.Fatalf("key 形态 = %q", withKey)
	}
	if withKey == fallback {
		t.Fatal("apiKeyID 与 internal:group 回退必须产生不同 key")
	}
	if withKey != affinityKey(AffinityScope{SystemAccountID: "s", APIKeyID: "k", GroupID: "g"}, "ix") {
		t.Fatal("同 scope 同 ID 必须产生相同 key")
	}
}

// 资源路径匹配：方法约束、URL 解码、ID 归一化。
func TestWAInteractionResourcePathMatch(t *testing.T) {
	cases := []struct {
		name, method, target string
		wantID               string
	}{
		{"GET 资源", "GET", "/v1beta/interactions/ix%5F1", "ix_1"},
		{"DELETE 资源", "DELETE", "/interactions/abc", "abc"},
		{"POST cancel", "POST", "/v1beta/interactions/abc/cancel", "abc"},
		{"PATCH cancel 拒绝", "PATCH", "/v1beta/interactions/abc/cancel", ""},
		{"PUT 资源拒绝", "PUT", "/v1beta/interactions/abc", ""},
		{"POST 非 cancel 拒绝", "POST", "/v1beta/interactions/abc/other", ""},
		{"空 ID 拒绝", "GET", "/v1beta/interactions//cancel", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.target, nil)
			if got := ResourceIDFromRequest(request); got != tc.wantID {
				t.Fatalf("ResourceIDFromRequest = %q，期望 %q", got, tc.wantID)
			}
			if matched := IsInteractionResourceRequest(request); (tc.wantID != "") != matched {
				t.Fatalf("IsInteractionResourceRequest = %v", matched)
			}
		})
	}
	// 解码失败保留原值（直接测解码 helper，避免 httptest 拒绝非法转义）。
	if got := decodePathSegment("abc%zz"); got != "abc%zz" {
		t.Fatalf("decodePathSegment 非法转义应保留原值 = %q", got)
	}
	if id := ResourceIDFromRequest(nil); id != "" {
		t.Fatalf("nil 请求应返回空: %q", id)
	}
	if !IsInteractionCreateRequest(httptest.NewRequest("POST", "/V1BETA/Interactions", nil)) {
		t.Fatal("create 请求判定大小写不敏感")
	}
	if IsInteractionCreateRequest(httptest.NewRequest("PUT", "/v1beta/interactions", nil)) {
		t.Fatal("非 POST 不应判定 create")
	}
	if IsInteractionCreateRequest(httptest.NewRequest("POST", "/v1beta/interactions/abc", nil)) {
		t.Fatal("带 ID 的资源路径不是 create")
	}
	if IsInteractionCreateRequest(nil) {
		t.Fatal("nil 请求不应判定 create")
	}
}

func TestWANormalizeInteractionID(t *testing.T) {
	longOK := strings.Repeat("好", interactionIDMaxLength) // 512 个 rune，恰好达到上限
	tooLong := longOK + "好"
	cases := []struct {
		in   any
		want string
	}{
		{"  ix  ", "ix"},
		{"", ""},
		{"   ", ""},
		{"a/b", ""},
		{"a\\b", ""},
		{"a\x01b", ""},
		{"a\x7fb", ""},
		{tooLong, ""},
		{longOK, longOK},
		{123, ""},
		{nil, ""},
	}
	for index, tc := range cases {
		if got := normalizeInteractionID(tc.in); got != tc.want {
			t.Fatalf("normalizeInteractionID 用例 %d = %q，期望 %q", index, got, tc.want)
		}
	}
}

// InteractionIDFromResponseBody：整 JSON 优先，其次逐行 SSE data。
func TestWAInteractionIDFromResponseBody(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"interaction.id", `{"interaction":{"id":" ix1 "}}`, "ix1"},
		{"interaction_id", `{"interaction_id":"ix2"}`, "ix2"},
		{"根 id", `{"id":"ix3"}`, "ix3"},
		{"SSE data 行", "data: {\"interaction_id\":\"ix4\"}\ndata: [DONE]\n", "ix4"},
		// SSE 负载按 allowRootID=false 解析：根级 id 不采信（只认
		// interaction.id / interaction_id）。
		{"SSE 根 id 不采信", "data: {\"id\":\"ix5\"}\n", ""},
		{"SSE interaction.id", "data: {\"interaction\":{\"id\":\"ix6\"}}\n", "ix6"},
		{"非 JSON 整体", "plain", ""},
		{"空串", "  ", ""},
		{"数组负载", "[]", ""},
		{"SSE 数据数组负载", "data: []\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InteractionIDFromResponseBody(tc.body); got != tc.want {
				t.Fatalf("InteractionIDFromResponseBody = %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestWAInteractionIDFromParsedResponse(t *testing.T) {
	if got := InteractionIDFromParsedResponse("not-map"); got != "" {
		t.Fatalf("非 map = %q", got)
	}
	if got := InteractionIDFromParsedResponse(map[string]any{"interaction": "scalar"}); got != "" {
		t.Fatalf("interaction 非对象 = %q", got)
	}
	value := map[string]any{"interaction": map[string]any{"id": " ix9 "}}
	if got := InteractionIDFromParsedResponse(value); got != "ix9" {
		t.Fatalf("interaction.id = %q", got)
	}
	// allowRootID=false 的流式数据负载：无 interaction_id 时返回空。
	delete(value["interaction"].(map[string]any), "id")
	if got := InteractionIDFromParsedResponse(value); got != "" {
		t.Fatalf("缺 ID = %q", got)
	}
}

// InteractionIDFromJSONPrefix：在截断 JSON 前缀字节流中解析顶层 id。
func TestWAInteractionIDFromJSONPrefix(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"id 在前", `{"id":"ix1","usage":{`, "ix1"},
		{"前导空白", "\n\t {\"id\":\"ix2\"", "ix2"},
		{"转义 id", `{"id":"ix\/3"`, ""}, // \/ 解码为 /，被禁止字符规则拒绝
		{"非对象开头", `[1,2`, ""},
		{"空对象", `{}`, ""},
		{"值截断", `{"id":`, ""},
		{"id 值截断", `{"id":"abc`, ""},
		{"键非法", `{123`, ""},
		{"缺冒号", `{"id" "x"`, ""},
		{"先跳过其他键", `{"model":"gemini","x":{"nested":"}"},"id":"ix6"`, "ix6"},
		{"跳过数组值", `{"history":[1,2,{"a":}],"id":"ix7"`, "ix7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := InteractionIDFromJSONPrefix([]byte(tc.raw))
			if got != tc.want {
				t.Fatalf("InteractionIDFromJSONPrefix(%q) = %q，期望 %q", tc.raw, got, tc.want)
			}
		})
	}
	// 含 "/" 的 id 被 normalizeInteractionID 拒绝（禁止路径分隔符）。
	if got := InteractionIDFromJSONPrefix([]byte(`{"id":"a/b","x":1}`)); got != "" {
		t.Fatalf("含斜杠 id 应拒绝: %q", got)
	}
	if got := InteractionIDFromJSONPrefix([]byte(`{"id":"ok","trailing":{"deep":[1,2`)); got != "ok" {
		t.Fatalf("嵌套截断值应可跳过: %q", got)
	}
	if got := InteractionIDFromJSONPrefix([]byte(`{"id":`)); got != "" {
		t.Fatalf("截断 id 值 = %q", got)
	}
}
