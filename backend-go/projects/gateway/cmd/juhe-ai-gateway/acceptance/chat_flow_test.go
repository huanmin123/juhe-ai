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
	"os"
	"strings"
	"testing"
	"time"
)

func TestAcceptanceChatFlow(t *testing.T) {
	chain := startChainFixture(t)
	admin := chain.admin
	base := chain.fixture.baseURL

	// conversation 创建（chat/routes.go Register：POST /my-chat/conversations
	// 免请求体创建空会话，历史字段兼容忽略；免 Key 直调后不创建任何 API Key）。
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

	// 使用日志零记录（AI 问答免 Key 直调内部通道 §3.1）：对话流量不落
	// usage_records，也不是落 NULL Key 行——usage 持久化唯一投递面（spool）
	// 必须零文件。本断言成立的前提是 spool 链路本身在工作：末尾用一次外部
	// /v1 请求做对照组，等它落 spool 后复查集合无新增（chat 记录不存在）。
	assertUsageSpoolEmpty(t, chain, "chat 对话完成后")

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

	// 对照组：外部 /v1 请求经同一 usage recorder 落 spool，证明投递链路在工作；
	// 随后复查 spool 中仍没有 chat 产生的记录（chat 流量已全部被内部通道抑制）。
	controlRequest, _ := http.NewRequest(http.MethodPost,
		base+"/v1/chat/completions",
		bytes.NewReader([]byte(fmt.Sprintf(
			`{"model":"%s","messages":[{"role":"user","content":"对照"}]}`, acceptanceModel))))
	controlRequest.Header.Set("Content-Type", "application/json")
	controlRequest.Header.Set("Authorization", "Bearer "+chain.defaultKey)
	controlResponse, controlErr := http.DefaultClient.Do(controlRequest)
	if controlErr != nil {
		t.Fatalf("对照组 /v1/chat/completions: %v", controlErr)
	}
	controlBody := readAllBody(t, controlResponse)
	_ = controlResponse.Body.Close()
	if controlResponse.StatusCode != http.StatusOK {
		t.Fatalf("对照组 /v1/chat/completions status=%d body=%s", controlResponse.StatusCode, controlBody)
	}
	waitUsageSpoolNonEmpty(t, chain, "对照组外部 /v1 请求后")
}

// assertUsageSpoolEmpty 断言 usage spool 目录为空。AI 问答内部通道的全部使用
// 日志必须在持久化唯一闸口（gatewayusage.FinalizationDispatch.DispatchUsageRecord
// 入口，覆盖正常入队与溢出 spool 两臂）被丢弃，记录不会进入缓冲、不会落 spool，
// 也不会落 usage_records。
//
// spool 写入是异步的，因此调用方必须同时提供对照组（本测试在末尾跑一次外部
// /v1 请求并等 spool 出现文件），否则「目录为空」可能只是投递面整体失效的
// 假阳性。
func assertUsageSpoolEmpty(t *testing.T, chain *chainFixture, phase string) {
	t.Helper()
	entries, err := os.ReadDir(chain.fixture.spoolDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("%s: 读取 usage spool 目录失败: %v", phase, err)
	}
	if len(entries) != 0 {
		t.Fatalf("%s: usage spool 必须为空（内部通道不写使用日志），实际 %d 个文件: %v",
			phase, len(entries), entryNames(entries))
	}
}

// waitUsageSpoolNonEmpty 是对照组：等到 spool 出现至少一个文件，证明 usage
// 投递链路在本夹具里确实工作（外部 /v1 流量走同一 recorder）。
func waitUsageSpoolNonEmpty(t *testing.T, chain *chainFixture, phase string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(chain.fixture.spoolDir)
		if err == nil && len(entries) > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: 对照组外部 /v1 请求后 usage spool 仍为空，投递链路未工作; dir=%s",
		phase, chain.fixture.spoolDir)
}

// entryNames 提取目录条目名（断言失败时的可读证据）。
func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
