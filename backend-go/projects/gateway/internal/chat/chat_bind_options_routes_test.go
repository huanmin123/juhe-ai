package chat

// 新建会话绑定下拉（GET /conversation-bind-options）的路由级覆盖：登录态
// 200 信封精确形状、查询端口未接线的 500 臂、未登录 401 臂。Mock 风格与
// chat_bind_modes_test.go 一致。

import (
	"net/http"
	"strings"
	"testing"
)

// mockGroupOptionsLookup 返回确定性的启用分组最小摘要。
type mockGroupOptionsLookup struct{}

func (mockGroupOptionsLookup) ListChatGroupOptions() ([]ChatBindOption, error) {
	return []ChatBindOption{{ID: "g1", Name: "分组一"}, {ID: "g2", Name: "分组二"}}, nil
}

// mockAccountOptionsLookup 返回确定性的可绑定账户最小摘要。
type mockAccountOptionsLookup struct{}

func (mockAccountOptionsLookup) ListChatAccountOptions() ([]ChatBindOption, error) {
	return []ChatBindOption{{ID: "a1", Name: "账户一"}}, nil
}

func TestConversationBindOptionsRoutes(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.GroupOptionsLookup = mockGroupOptionsLookup{}
	env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
	response := env.do("GET", "/__aisys__/api/my-chat/conversation-bind-options", routeTestOwner, "")
	if response.status != http.StatusOK {
		t.Fatalf("bind options = %d %s", response.status, response.rawString())
	}
	// 最小投影信封的精确形状：groups/accounts 恒为数组，元素只含 id/name。
	want := `{"data":{"groups":[{"id":"g1","name":"分组一"},{"id":"g2","name":"分组二"}],"accounts":[{"id":"a1","name":"账户一"}]}}`
	if response.rawString() != want {
		t.Fatalf("bind options body = %s, want %s", response.rawString(), want)
	}
}

func TestConversationBindOptionsUnwiredArms(t *testing.T) {
	prefix := "/__aisys__/api/my-chat"
	cases := []struct {
		name           string
		groupsLookup   ChatGroupOptionsLookup
		accountsLookup ChatAccountOptionsLookup
	}{
		{name: "两个查询端口均未接线"},
		{name: "分组查询端口未接线", accountsLookup: mockAccountOptionsLookup{}},
		{name: "账户查询端口未接线", groupsLookup: mockGroupOptionsLookup{}},
	}
	for _, item := range cases {
		env := newGenerationEnv(t)
		env.deps.GroupOptionsLookup = item.groupsLookup
		env.deps.AccountOptionsLookup = item.accountsLookup
		response := env.do("GET", prefix+"/conversation-bind-options", routeTestOwner, "")
		if response.status != http.StatusInternalServerError || response.code() != "internal_generation_failed" {
			t.Fatalf("%s = %d %s", item.name, response.status, response.rawString())
		}
		// DomainError 明细沿 writeChatRouteError 的既有 500 语义附带在"详情"中。
		if !strings.Contains(response.message(), "绑定选项列表暂不可用，请稍后重试") {
			t.Fatalf("%s message = %q, want 含 %q", item.name, response.message(), "绑定选项列表暂不可用，请稍后重试")
		}
	}
}

func TestConversationBindOptionsUnauthenticated(t *testing.T) {
	env := newGenerationEnv(t)
	env.deps.GroupOptionsLookup = mockGroupOptionsLookup{}
	env.deps.AccountOptionsLookup = mockAccountOptionsLookup{}
	unauthorized := env.do("GET", "/__aisys__/api/my-chat/conversation-bind-options", "", "")
	if unauthorized.status != http.StatusUnauthorized || unauthorized.message() != "请先登录" {
		t.Fatalf("未登录 = %d %s", unauthorized.status, unauthorized.rawString())
	}
}
