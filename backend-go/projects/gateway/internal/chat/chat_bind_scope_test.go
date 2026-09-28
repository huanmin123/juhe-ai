package chat

// AI 问答会话绑定数据范围（ChatBindScope）的路由级 enforcement 覆盖：创建
// 时绑定范围外分组/账户 → 与"不存在"同型 400；发送与模型列表对存量越权绑
// 定（scope 校验返回 nil）→ 既有"会话绑定的分组不存在或已删除"降级路径；
// 同一会话在 admin scope 下照常可用（IsAdmin 驱动范围口径）。

import (
	"net/http"
	"testing"
)

// outOfScopeGroupLookup 在指定 scope 下把目标分组解析为不可见（nil，模拟普
// 通用户对越权分组的存量绑定），其余走 mockGroupLookup 既有行为。
type outOfScopeGroupLookup struct {
	mockGroupLookup
	deny ChatBindScope
	id   string
}

func (m outOfScopeGroupLookup) FindChatGroup(scope ChatBindScope, groupID string) (*ChatGroupRef, error) {
	if scope == m.deny && groupID == m.id {
		return nil, nil
	}
	return m.mockGroupLookup.FindChatGroup(scope, groupID)
}

// outOfScopeAccountLookup 在指定 scope 下把目标账户解析为不可见（nil），其
// 余走 mockAccountLookup 既有行为。
type outOfScopeAccountLookup struct {
	mockAccountLookup
	deny ChatBindScope
	id   string
}

func (m outOfScopeAccountLookup) FindChatAccount(scope ChatBindScope, accountID string) (*ChatAccountRef, error) {
	if scope == m.deny && accountID == m.id {
		return nil, nil
	}
	return m.mockAccountLookup.FindChatAccount(scope, accountID)
}

// TestCreateConversationBindScopeEnforced：创建时绑定对象在请求者范围外
// （Find 对该 scope 返回 nil）→ 与"不存在"同型 400 文案。
func TestCreateConversationBindScopeEnforced(t *testing.T) {
	userScope := ChatBindScope{ViewerID: routeTestOwner, IsAdmin: false}
	prefix := "/__aisys__/api/my-chat"

	t.Run("group 范围外 400 绑定的分组不存在", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.GroupLookup = outOfScopeGroupLookup{deny: userScope, id: "group-b"}
		response := env.do("POST", prefix+"/conversations", routeTestOwner, `{"bindMode":"group","groupId":"group-b"}`)
		if response.status != http.StatusBadRequest || response.code() != "chat_invalid_request" || response.message() != "绑定的分组不存在" {
			t.Fatalf("范围外分组创建 = %d %s", response.status, response.rawString())
		}
	})

	t.Run("account 范围外 400 绑定的账户不存在", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.AccountLookup = outOfScopeAccountLookup{deny: userScope, id: "account-1"}
		response := env.do("POST", prefix+"/conversations", routeTestOwner, `{"bindMode":"account","accountId":"account-1"}`)
		if response.status != http.StatusBadRequest || response.code() != "chat_invalid_request" || response.message() != "绑定的账户不存在" {
			t.Fatalf("范围外账户创建 = %d %s", response.status, response.rawString())
		}
	})
}

// TestOutOfScopeBindingConversationDegrades：存量越权绑定会话在数据范围收
// 紧后自然变为不可发送/不可列模型，与"对象不存在/停用"同型降级；admin
// scope 下同一会话照常可用。
func TestOutOfScopeBindingConversationDegrades(t *testing.T) {
	userScope := ChatBindScope{ViewerID: routeTestOwner, IsAdmin: false}
	prefix := "/__aisys__/api/my-chat"

	t.Run("模型列表 400 会话绑定的分组不存在或已删除", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.GroupLookup = outOfScopeGroupLookup{deny: userScope, id: "group-b"}
		env.deps.ModelCatalog = &bindModeCatalog{}
		createBoundConversation(t, env.fixture, "scope_conv_g", routeTestOwner, CreateConversationInput{
			BindMode: BindModeGroup, BindGroupID: "group-b", BindGroupNameSnapshot: "分组 B",
		})
		list := env.do("GET", prefix+"/conversations/scope_conv_g/models", routeTestOwner, "")
		if list.status != http.StatusBadRequest || list.code() != "chat_invalid_request" || list.message() != "会话绑定的分组不存在或已删除" {
			t.Fatalf("越权分组模型列表 = %d %s", list.status, list.rawString())
		}
	})

	t.Run("发送 400 会话绑定的分组不存在或已删除且不派发上游", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.GroupLookup = outOfScopeGroupLookup{deny: userScope, id: "group-b"}
		env.deps.ModelCatalog = &bindModeCatalog{}
		createBoundConversation(t, env.fixture, "scope_stream_g", routeTestOwner, CreateConversationInput{
			BindMode: BindModeGroup, BindGroupID: "group-b", BindGroupNameSnapshot: "分组 B",
		})
		response := env.streamPost("scope_stream_g", routeTestOwner, streamPayload("cmid-scope", "问题", "claude-x"))
		if response.status != http.StatusBadRequest || response.message() != "会话绑定的分组不存在或已删除" {
			t.Fatalf("越权分组发送 = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatalf("越权绑定不得派发上游")
		}
	})

	t.Run("发送 400 会话绑定的账户不存在或已删除", func(t *testing.T) {
		env := newGenerationEnv(t)
		env.deps.AccountLookup = outOfScopeAccountLookup{deny: userScope, id: "account-1"}
		env.deps.ModelCatalog = &bindModeCatalog{}
		createBoundConversation(t, env.fixture, "scope_stream_a", routeTestOwner, CreateConversationInput{
			BindMode: BindModeAccount, BindAccountID: "account-1", BindAccountNameSnapshot: "账户 account-1",
		})
		response := env.streamPost("scope_stream_a", routeTestOwner, streamPayload("cmid-scope-a", "问题", "gpt-5"))
		if response.status != http.StatusBadRequest || response.message() != "会话绑定的账户不存在或已删除" {
			t.Fatalf("越权账户发送 = %d %s", response.status, response.rawString())
		}
		if env.executor.callCount() != 0 {
			t.Fatalf("越权绑定不得派发上游")
		}
	})

	t.Run("admin scope 下同一越权会话照常可用", func(t *testing.T) {
		env := newBindOptionsEnvWithRole(t, "admin")
		env.deps.GroupLookup = outOfScopeGroupLookup{deny: userScope, id: "group-b"}
		env.deps.ModelCatalog = mockModelCatalog{}
		createBoundConversation(t, env.fixture, "scope_conv_admin", routeTestOwner, CreateConversationInput{
			BindMode: BindModeGroup, BindGroupID: "group-b", BindGroupNameSnapshot: "分组 B",
		})
		list := env.do("GET", prefix+"/conversations/scope_conv_admin/models", routeTestOwner, "")
		if list.status != http.StatusOK {
			t.Fatalf("admin 模型列表 = %d %s", list.status, list.rawString())
		}
	})
}
