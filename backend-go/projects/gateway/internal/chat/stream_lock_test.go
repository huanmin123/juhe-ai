package chat

// 会话锁定（PLAN-20261012T121500000Z D5）与联网开关（D3）表驱动单测：
//  - 路由预检：锁定会话发送不同模型 → 400 chat_model_locked（不派发上游）；
//  - 未锁定会话首轮接受后写入 last_model（后续轮次不可变）；
//  - AcceptTurn 事务内复核：锁定模型不同 → ModelLockedError；绑定账户与预检
//    读取值不一致 → chat_conversation_binding_changed；
//  - /clear 不触碰 last_model；
//  - enableWebSearch：responses 直连且目录声明 → 注入 hosted web_search 工具
//    透传；其余形态 → 422 chat_web_search_unsupported。

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestStreamRouteModelLockPreflight(t *testing.T) {
	t.Run("锁定会话换模型 → 400 chat_model_locked 且不派发", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_lock_route", routeTestOwner)
		bindStreamConversation(t, env, "conv_lock_route")
		response := env.streamPost("conv_lock_route", routeTestOwner, streamPayload("cmid-lock-1", "问题", "gpt-5-mini"))
		if response.status != http.StatusBadRequest || response.code() != "chat_model_locked" {
			t.Fatalf("locked model = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatal("锁定拒绝不得派发上游")
		}
	})
	t.Run("锁定会话同模型可发送", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_lock_route", routeTestOwner)
		bindStreamConversation(t, env, "conv_lock_route")
		env.executor.steps = []scriptStep{{
			match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return sseResponse(chatCompletionsSSE("回答", false))
			},
		}}
		response := env.streamPost("conv_lock_route", routeTestOwner, streamPayload("cmid-lock-2", "问题", "gpt-5"))
		if response.status != http.StatusOK {
			t.Fatalf("same model = %d %s", response.status, response.rawString())
		}
	})
	t.Run("未锁定会话首轮写入 last_model 且后续不可变", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_lock_route", routeTestOwner)
		bindStreamConversation(t, env, "conv_lock_route")
		if _, err := env.fixture.db.Exec(`UPDATE chat_conversations SET last_model = NULL WHERE id = 'conv_lock_route'`); err != nil {
			t.Fatal(err)
		}
		env.executor.steps = []scriptStep{{
			match: func(call dispatchCall) bool { return call.Path == "/v1/chat/completions" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return sseResponse(chatCompletionsSSE("首轮", false))
			},
		}}
		first := env.streamPost("conv_lock_route", routeTestOwner, streamPayload("cmid-lock-3", "问题", "gpt-5-mini"))
		if first.status != http.StatusOK {
			t.Fatalf("first turn = %d %s", first.status, first.rawString())
		}
		var locked string
		if err := env.fixture.db.QueryRow(`SELECT last_model FROM chat_conversations WHERE id = 'conv_lock_route'`).Scan(&locked); err != nil || locked != "gpt-5-mini" {
			t.Fatalf("last_model = %q err=%v, want gpt-5-mini", locked, err)
		}
		switched := env.streamPost("conv_lock_route", routeTestOwner, streamPayload("cmid-lock-4", "换模型", "gpt-5"))
		if switched.status != http.StatusBadRequest || switched.code() != "chat_model_locked" {
			t.Fatalf("second model = %d %s", switched.status, switched.rawString())
		}
	})
	t.Run("/clear 不触碰 last_model", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_lock_clear", routeTestOwner)
		cleared, err := env.fixture.store.ClearConversation(ClearConversationInput{
			ConversationID: "conv_lock_clear", SystemAccountID: routeTestOwner, Now: env.fixture.nowISO,
		})
		if err != nil || cleared == nil {
			t.Fatalf("clear = %+v err=%v", cleared, err)
		}
		if cleared.LastModel == nil || *cleared.LastModel != "gpt-5" {
			t.Fatalf("clear 后 lastModel = %v, want gpt-5 保持", cleared.LastModel)
		}
	})
}

func TestAcceptTurnLockRecheck(t *testing.T) {
	t.Run("锁定模型不同 → ModelLockedError 携带锁定模型", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_lock_store", routeTestOwner)
		input := acceptTurnInputW10D(fixture, "conv_lock_store", "cmid-lock-a")
		input.Model = "gpt-5-mini"
		_, err := fixture.store.AcceptTurn(input)
		var locked *ModelLockedError
		if !errors.As(err, &locked) || locked.LockedModel != "gpt-5" {
			t.Fatalf("err = %v, want ModelLockedError(gpt-5)", err)
		}
	})
	t.Run("绑定账户与预检读取值不一致 → chat_conversation_binding_changed", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_lock_store", routeTestOwner)
		input := acceptTurnInputW10D(fixture, "conv_lock_store", "cmid-lock-b")
		input.ExpectedBindAccountID = "account-other"
		_, err := fixture.store.AcceptTurn(input)
		var conflict *ConflictError
		if !errors.As(err, &conflict) || conflict.Code != ConflictConversationBindingChanged {
			t.Fatalf("err = %v, want ConflictConversationBindingChanged", err)
		}
		if string(conflict.Code) != "chat_conversation_binding_changed" {
			t.Fatalf("code = %s", string(conflict.Code))
		}
	})
	t.Run("预检一致时接受轮次且 last_model 保持首轮值", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_lock_store", routeTestOwner)
		input := acceptTurnInputW10D(fixture, "conv_lock_store", "cmid-lock-c")
		input.ExpectedBindAccountID = "account-1"
		turn, err := fixture.store.AcceptTurn(input)
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		if turn.TurnID == "" {
			t.Fatal("turnID 为空")
		}
		var locked string
		if err := fixture.db.QueryRow(`SELECT last_model FROM chat_conversations WHERE id = 'conv_lock_store'`).Scan(&locked); err != nil || locked != "gpt-5" {
			t.Fatalf("last_model = %q err=%v, want gpt-5", locked, err)
		}
	})
	t.Run("未锁定会话首轮写入后不可被后续轮次改写", func(t *testing.T) {
		fixture, _ := newFaultChatFixtureW10D(t)
		fixture.createConversation("conv_lock_store", routeTestOwner)
		if _, err := fixture.db.Exec(`UPDATE chat_conversations SET last_model = NULL WHERE id = 'conv_lock_store'`); err != nil {
			t.Fatal(err)
		}
		first := acceptTurnInputW10D(fixture, "conv_lock_store", "cmid-lock-d1")
		first.Model = "deepseek-chat"
		if _, err := fixture.store.AcceptTurn(first); err != nil {
			t.Fatalf("first accept: %v", err)
		}
		second := acceptTurnInputW10D(fixture, "conv_lock_store", "cmid-lock-d2")
		second.Model = "gpt-5"
		_, err := fixture.store.AcceptTurn(second)
		var locked *ModelLockedError
		if !errors.As(err, &locked) || locked.LockedModel != "deepseek-chat" {
			t.Fatalf("err = %v, want ModelLockedError(deepseek-chat)", err)
		}
	})
}

// responsesSearchCatalog 提供 gpt（responses 直连）账户视图与可选 web_search
// 声明的目录（联网开关矩阵夹具）。
type responsesSearchCatalog struct {
	declareWebSearch bool
}

func (c responsesSearchCatalog) account() ChatTransportAccount {
	enabled := true
	return ChatTransportAccount{
		ID: "account-1", Type: "api_key", ProviderCode: "gpt",
		SupportedEndpointModes: []string{"responses_sse"},
		ModelMappings: []ChatTransportModelMapping{{
			Enabled: &enabled, SourceModel: "gpt-5",
		}},
	}
}

func (c responsesSearchCatalog) ListAccountsForGroup(groupID, systemAccountID, requestedModel, endpointFamily string) []ChatTransportAccount {
	return []ChatTransportAccount{c.account()}
}

func (c responsesSearchCatalog) ListChatPinnedAccountsForGroup(groupID, systemAccountID, accountID string) []ChatTransportAccount {
	return []ChatTransportAccount{c.account()}
}

func (c responsesSearchCatalog) ListProviderCatalog(providerCode, systemAccountID string) []ProviderModelCatalogItem {
	tools := map[string][]string{"chat_completions": {"function_calling"}}
	if c.declareWebSearch {
		tools["responses"] = []string{"web_search"}
	}
	return []ProviderModelCatalogItem{{
		Model: "gpt-5", ProviderCode: "gpt",
		SupportedAPIProtocols: []string{"responses"},
		InputModalities:       []string{"text"},
		OutputModalities:      []string{"text"},
		SupportedToolsByProtocol: tools,
	}}
}

func TestEnableWebSearchSwitchMatrix(t *testing.T) {
	buildEnv := func(t *testing.T, declareWebSearch bool) *generationEnv {
		env := newGenerationEnv(t)
		env.deps.ModelCatalog = responsesSearchCatalog{declareWebSearch: declareWebSearch}
		env.fixture.createConversation("conv_web_search", routeTestOwner)
		bindStreamConversation(t, env, "conv_web_search")
		return env
	}

	t.Run("responses 直连且目录声明 → 注入 hosted web_search", func(t *testing.T) {
		env := buildEnv(t, true)
		env.executor.steps = []scriptStep{{
			match: func(call dispatchCall) bool { return call.Path == "/v1/responses" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return sseResponse(responsesTextSSE("联网回答"))
			},
		}}
		payload := `{"clientMessageId":"cmid-web-1","content":"搜一下","model":"gpt-5","enableWebSearch":true}`
		response := env.streamPost("conv_web_search", routeTestOwner, payload)
		if response.status != http.StatusOK {
			t.Fatalf("web search stream = %d %s", response.status, response.rawString())
		}
		calls := env.executor.calls
		if len(calls) == 0 || calls[0].Path != "/v1/responses" {
			t.Fatalf("calls = %+v", calls)
		}
		if !strings.Contains(calls[0].Body, `"tools":[{"type":"web_search"}]`) {
			t.Fatalf("tools missing in upstream body: %s", calls[0].Body)
		}
	})
	t.Run("responses 直连但目录未声明 → 422 chat_web_search_unsupported", func(t *testing.T) {
		env := buildEnv(t, false)
		response := env.streamPost("conv_web_search", routeTestOwner,
			`{"clientMessageId":"cmid-web-2","content":"搜一下","model":"gpt-5","enableWebSearch":true}`)
		if response.status != http.StatusUnprocessableEntity || response.code() != "chat_web_search_unsupported" {
			t.Fatalf("undeclared = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatal("拒绝后不得派发上游")
		}
	})
	t.Run("chat 直连账户传 true → 422", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.fixture.createConversation("conv_web_search", routeTestOwner)
		bindStreamConversation(t, env, "conv_web_search")
		response := env.streamPost("conv_web_search", routeTestOwner,
			`{"clientMessageId":"cmid-web-3","content":"搜一下","model":"gpt-5","enableWebSearch":true}`)
		if response.status != http.StatusUnprocessableEntity || response.code() != "chat_web_search_unsupported" {
			t.Fatalf("chat direct = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatal("拒绝后不得派发上游")
		}
	})
	t.Run("默认不携带 enableWebSearch 不注入工具", func(t *testing.T) {
		env := buildEnv(t, true)
		env.executor.steps = []scriptStep{{
			match: func(call dispatchCall) bool { return call.Path == "/v1/responses" },
			respond: func(call dispatchCall) *GenerationDispatchResponse {
				return sseResponse(responsesTextSSE("普通回答"))
			},
		}}
		response := env.streamPost("conv_web_search", routeTestOwner, streamPayload("cmid-web-4", "普通问题", "gpt-5"))
		if response.status != http.StatusOK {
			t.Fatalf("plain stream = %d %s", response.status, response.rawString())
		}
		if strings.Contains(env.executor.calls[0].Body, `"tools"`) {
			t.Fatalf("tools should be absent: %s", env.executor.calls[0].Body)
		}
	})
}
