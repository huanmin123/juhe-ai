// X05 场景 6：chat。conversation 免请求体创建 → PATCH accountId 绑定
// mock 上游账户（账户唯一绑定契约）→ POST /stream（真实网关链执行器
// 固定派发到绑定账户）→ SSE 事件序列（content_block.* → message.completed
// 终态）→ 资产上传边界。
package acceptance

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAcceptanceChatFlow(t *testing.T) {
	chain := startChainFixture(t)
	admin := chain.admin
	base := chain.fixture.baseURL

	// conversation 创建（chat/routes.go Register：POST /my-chat/conversations
	// 免请求体创建空会话，历史字段兼容忽略；鉴权主体由服务端
	// EnsureChatAPIKey 自动复用 seed chat key）。
	_, created := admin.do(http.MethodPost, "/__aisys__/api/my-chat/conversations", nil, 0)
	conversation := data(created)
	conversationID := str(conversation["id"])
	if conversationID == "" {
		t.Fatalf("conversation create payload wrong: %#v", created)
	}

	// 账户唯一绑定（《AI问答会话账户唯一绑定设计》§5：PATCH accountId 选
	// 账户，未选账户的会话模型列表为空、不能发送）。
	admin.do(http.MethodPatch, "/__aisys__/api/my-chat/conversations/"+conversationID,
		map[string]any{"accountId": chain.accountID}, wantStatus(http.StatusOK))

	// 会话模型列表 = 绑定账户可路由模型（seed 目录模型经 runtime cache 提供）。
	modelDeadline := time.Now().Add(10 * time.Second)
	modelsVisible := false
	for time.Now().Before(modelDeadline) {
		_, modelsPayload := admin.do(http.MethodGet,
			"/__aisys__/api/my-chat/conversations/"+conversationID+"/models", nil)
		if strings.Contains(fmt.Sprintf("%v", modelsPayload), acceptanceModel) {
			modelsVisible = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !modelsVisible {
		t.Fatalf("conversation models never listed %s", acceptanceModel)
	}

	// POST /stream：SSE 事件序列断言。事件类型契约见 chat/
	// generation_runner.go（content_block.started / content_block.delta /
	// message.completed 终态；Node chat/sse_write.go 同名事件流）。
	streamRequest, _ := http.NewRequest(http.MethodPost,
		base+"/__aisys__/api/my-chat/conversations/"+conversationID+"/stream",
		bytes.NewReader([]byte(fmt.Sprintf(
			`{"clientMessageId":"acc-msg-1","content":"你好","model":"%s"}`, acceptanceModel))))
	streamRequest.Header.Set("Content-Type", "application/json")
	streamResponse, err := admin.http.Do(streamRequest)
	if err != nil {
		t.Fatalf("POST stream: %v", err)
	}
	defer streamResponse.Body.Close()
	if streamResponse.StatusCode != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", streamResponse.StatusCode, readAllBody(t, streamResponse))
	}
	if ct := streamResponse.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("stream content-type=%q", ct)
	}
	eventTypes := []string{}
	eventPayloads := strings.Builder{}
	scanner := bufio.NewScanner(streamResponse.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	currentEvent := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			eventTypes = append(eventTypes, currentEvent)
		} else if strings.HasPrefix(line, "data: ") {
			eventPayloads.WriteString(strings.TrimPrefix(line, "data: "))
			eventPayloads.WriteString("\n")
		}
	}
	if len(eventTypes) == 0 {
		t.Fatal("no SSE events received")
	}
	if eventTypes[len(eventTypes)-1] != "message.completed" {
		t.Fatalf("stream must end with message.completed; events=%v", eventTypes)
	}
	hasBlockStarted, hasBlockDelta := false, false
	for _, eventType := range eventTypes {
		if eventType == "content_block.started" {
			hasBlockStarted = true
		}
		if eventType == "content_block.delta" {
			hasBlockDelta = true
		}
	}
	if !hasBlockStarted || !hasBlockDelta {
		t.Fatalf("content_block events missing; events=%v", eventTypes)
	}
	// 上游内容透传到聊天 turn（mock 上游 delta 「验收」+「直通」）。
	if !strings.Contains(eventPayloads.String(), "验收") || !strings.Contains(eventPayloads.String(), "直通") {
		t.Fatalf("upstream content missing in chat stream: %s", eventPayloads.String())
	}

	// 消息落库：GET /conversations/{id}/messages 包含用户输入与回复。
	_, messages := admin.do(http.MethodGet,
		"/__aisys__/api/my-chat/conversations/"+conversationID+"/messages", nil, wantStatus(http.StatusOK))
	if !strings.Contains(fmt.Sprintf("%v", messages), "你好") {
		t.Fatalf("user message missing: %#v", messages)
	}

	// 资产上传边界：非图片内容被拒绝（chat/assets.go 契约），401 边界由
	// 未登录客户端验证。
	unauthorized := newClient(t, base)
	unauthorized.do(http.MethodPost,
		"/__aisys__/api/my-chat/conversations/"+conversationID+"/assets",
		map[string]any{}, wantStatus(http.StatusUnauthorized))
	boundaryRequest, _ := http.NewRequest(http.MethodPost,
		base+"/__aisys__/api/my-chat/conversations/"+conversationID+"/assets", strings.NewReader("not-a-multipart"))
	boundaryRequest.Header.Set("Content-Type", "multipart/form-data; boundary=none")
	boundaryStatus, boundaryPayload := admin.doRequest(boundaryRequest)
	if boundaryStatus < 400 {
		t.Fatalf("malformed asset upload must fail; status=%d payload=%#v", boundaryStatus, boundaryPayload)
	}

	// 会话清理：DELETE → 204/200 契约。
	cleanupStatus, _ := admin.do(http.MethodDelete,
		"/__aisys__/api/my-chat/conversations/"+conversationID, nil)
	if cleanupStatus >= 400 {
		t.Fatalf("conversation delete failed: %d", cleanupStatus)
	}
}
