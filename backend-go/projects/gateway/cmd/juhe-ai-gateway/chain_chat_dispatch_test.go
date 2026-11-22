package main

// /v1 链 full-chain 测试共用基建（原 AI 问答会话绑定调度覆盖通道测试随
// 演练场真实 Key 派发改造退役，通用 mock 上游 / 组链 / spool 读取辅助保留给
// t93_inflight、安全策略等 full-chain 测试）。

import (
	"encoding/json"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// chatDispatchUpstream 是可命中计数的 mock 上游。
type chatDispatchUpstream struct {
	server *httptest.Server
	hits   int
}

func newChatDispatchUpstream(t *testing.T, body string) *chatDispatchUpstream {
	t.Helper()
	upstream := &chatDispatchUpstream{}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// composeChatDispatchChain 组装 full-chain（含 spool 目录），返回 chain 与
// spool 目录。spool 刻意放在 t.TempDir() 之外：异步用量记录的落盘与
// TempDir 的 RemoveAll 存在 Windows unlinkat 竞态（"directory is not
// empty"，停机后仍可能有迟到记录重建目录），改为自有目录 + 尽力清理。
func composeChatDispatchChain(t *testing.T, fixture *chainFixture) (http.Handler, string) {
	t.Helper()
	spoolDir := filepath.Join(os.TempDir(), "juhe-ai-chatdispatch-spool-"+newCompositionID("t"))
	chain, shutdown, err := composeGatewayChain(chainSmokeDeps(t, fixture, gatewaypreauth.SystemClock{}, spoolDir))
	if err != nil {
		t.Fatalf("compose gateway chain: %v", err)
	}
	t.Cleanup(func() {
		shutdown()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if err := os.RemoveAll(spoolDir); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	return chain, spoolDir
}

// chatDispatchRequest 以外部 HTTP 请求形态（真实 Bearer Key）向 /v1 链
// handler 直发一次 /v1/chat/completions（不经 loopback TCP，与
// chatGatewayExecutor.Dispatch 的进程内直调通道同源语义）。
func chatDispatchRequest(t *testing.T, chain http.Handler, fixture *chainFixture) (int, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"你好"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+fixture.apiKeySecret)
	recorder := httptest.NewRecorder()
	chain.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// waitForSpoolRecords 轮询 spool 目录直到出现记录文件并解析为 JSON 列表。
func waitForSpoolRecords(t *testing.T, spoolDir string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// spool 布局：<spoolDir>/<instanceID>/<token>.json（一文件一条记录）。
		instances, err := os.ReadDir(spoolDir)
		if err == nil {
			records := []map[string]any{}
			for _, instance := range instances {
				if !instance.IsDir() {
					continue
				}
				files, err := os.ReadDir(filepath.Join(spoolDir, instance.Name()))
				if err != nil {
					continue
				}
				for _, file := range files {
					if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
						continue
					}
					raw, err := os.ReadFile(filepath.Join(spoolDir, instance.Name(), file.Name()))
					if err != nil {
						continue
					}
					var record map[string]any
					if json.Unmarshal(raw, &record) == nil && len(record) > 0 {
						records = append(records, record)
					}
				}
			}
			if len(records) > 0 {
				return records
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("spool 无用量记录: %s", spoolDir)
	return nil
}

const chatDispatchSmokeBody = `{"id":"chatcmpl-pin","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"CONTENT"},"finish_reason":"stop"}]}`
