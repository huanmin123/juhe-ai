package main

// 手动账号测试派发装配测试（test_effects.go 收口）：
//  1. 桥接契约测试：jobsAccountTestDispatchBridge 对 jobs internal-api
//     loopback 契约的逐字节行为（路径、方法、Content-Type、HMAC 签名域、
//     请求体形状、202/503/传输失败的接受语义、空集合直通）；
//  2. 生效断言测试：composeSystemAPI 之上以生产同款构造把桥接装配到
//     accounts.Store，真实 CreateTestTask 后走路由同款派发调用，断言 jobs
//     端点收到该任务——装配断线（未接线）时该端口为 nil，测试直接钉住
//     接线后的端口可达性；
//  3. 组合根源码断言：compose.go 必须包含 SetTestDispatchEffects 装配行
//     （先于该装配行的账户 store 构造），复用 revoker 断言的既有先例。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
)

type capturedDispatch struct {
	path        string
	method      string
	contentType string
	signature   string
	body        []byte
}

// newDispatchCaptureServer records one signed dispatch request per call and
// answers with the configured status.
func newDispatchCaptureServer(t *testing.T, status int, captured *[]capturedDispatch) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read dispatch body: %v", err)
		}
		*captured = append(*captured, capturedDispatch{
			path:        r.URL.Path,
			method:      r.Method,
			contentType: r.Header.Get("Content-Type"),
			signature:   r.Header.Get("X-Juhe-Ai-Signature"),
			body:        body,
		})
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server
}

func dispatchTestSecret() string { return "compose-test-secret" }

// TestJobsAccountTestDispatchBridgeContract pins the jobs internalapi wire
// contract: path, method, JSON content type, the exact request body shape and
// the HMAC-SHA256 signature over `juhe-ai:account-test-dispatch:v1\n` + raw
// body (jobs CreateAccountTestDispatchSignature mirror, golden vector).
func TestJobsAccountTestDispatchBridgeContract(t *testing.T) {
	captured := &[]capturedDispatch{}
	server := newDispatchCaptureServer(t, http.StatusAccepted, captured)
	secret := dispatchTestSecret()
	bridge := newJobsAccountTestDispatchBridge(server.URL, secret, server.Client())

	if !bridge.DispatchAccountTestTasks(context.Background(), []string{"task-1"}) {
		t.Fatal("202 dispatch must report accepted")
	}
	if len(*captured) != 1 {
		t.Fatalf("expected one dispatch call, got %d", len(*captured))
	}
	call := (*captured)[0]
	if call.method != http.MethodPost {
		t.Fatalf("method: %s", call.method)
	}
	if call.path != "/__aiinternal__/v1/account-test/dispatch" {
		t.Fatalf("path: %s", call.path)
	}
	if !strings.HasPrefix(call.contentType, "application/json") {
		t.Fatalf("content type: %q", call.contentType)
	}
	// parseTaskID 契约：恰好 {"version":1,"taskId":"<id>"}。
	if string(call.body) != `{"version":1,"taskId":"task-1"}` {
		t.Fatalf("body: %s", call.body)
	}
	// 签名域逐字节钉死（含尾部 \n）：域或算法漂移会让 jobs 侧 401。
	// 契约算法 = HMAC-SHA256(secret, domain + rawBody)。
	mac := hmac.New(sha256.New, []byte(dispatchTestSecret()))
	_, _ = mac.Write([]byte("juhe-ai:account-test-dispatch:v1\n"))
	_, _ = mac.Write(call.body)
	if call.signature != "v1="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("signature not HMAC(secret, domain+body): %s", call.signature)
	}
	// golden 向量：与 jobs internalapi.CreateAccountTestDispatchSignature
	// 同算法同域的同输入结果（域/体任一侧漂移即失配）。
	if golden := signAccountTestDispatch(secret, []byte(`{"version":1,"taskId":"task-1"}`)); golden != "v1=865db4bc0d999a0479d60bc15048003c4109b973d5fb1f53b042fa090ea467a2" {
		t.Fatalf("golden signature drifted: %s", golden)
	}
}

// TestJobsAccountTestDispatchBridgeUnavailable pins the worker-unavailable
// semantics: non-202 answers, unreachable jobs and cancel fire-and-forget.
func TestJobsAccountTestDispatchBridgeUnavailable(t *testing.T) {
	captured := &[]capturedDispatch{}
	server := newDispatchCaptureServer(t, http.StatusServiceUnavailable, captured)
	bridge := newJobsAccountTestDispatchBridge(server.URL, dispatchTestSecret(), server.Client())

	if bridge.DispatchAccountTestTasks(context.Background(), []string{"task-1"}) {
		t.Fatal("503 dispatch must report unavailable")
	}
	if len(*captured) != 1 {
		t.Fatalf("expected one dispatch attempt, got %d", len(*captured))
	}

	// 无服务端：连接失败按不可用处理（路由随后任务置败 + 503）。
	deadBridge := newJobsAccountTestDispatchBridge("http://127.0.0.1:1", dispatchTestSecret(), nil)
	if deadBridge.DispatchAccountTestTasks(context.Background(), []string{"task-1"}) {
		t.Fatal("unreachable jobs must report unavailable")
	}
	// 空集合 / 空白 ID：直通成功，不发起调用（Node normalizedIds 空即 true）。
	before := len(*captured)
	if !bridge.DispatchAccountTestTasks(context.Background(), []string{" ", ""}) {
		t.Fatal("blank-only batch must report accepted")
	}
	if len(*captured) != before {
		t.Fatal("blank-only batch must not call jobs")
	}
	// 取消：fire-and-forget，任何结果都不 panic/阻塞。
	bridge.DispatchAccountTestCancel("")
	deadBridge.DispatchAccountTestCancel("task-1")
}

// TestComposeWiringAccountTestDispatchBridge is the effectiveness assertion:
// the production-same construction over a composed system API actually hands
// a real test task to the jobs internal-api endpoint.
func TestComposeWiringAccountTestDispatchBridge(t *testing.T) {
	cfg := composeTestConfig(t)
	cfg.Secret = dispatchTestSecret()
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditConfig)
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()

	captured := &[]capturedDispatch{}
	server := newDispatchCaptureServer(t, http.StatusAccepted, captured)
	bridge := newJobsAccountTestDispatchBridge(server.URL, cfg.Secret, server.Client())

	accountStore, err := accounts.NewStore(composed.DB, false, cfg.Secret, time.Now, newCompositionID)
	if err != nil {
		t.Fatalf("accounts store: %v", err)
	}
	// 生产同款装配行（compose.go 同调用）。
	accountStore.SetTestDispatchEffects(bridge)

	task, err := accountStore.CreateTestTask(context.Background(), accounts.TestTaskCreateInput{
		AccountID:    "acc-dispatch-e2e",
		AccountName:  "dispatch-e2e",
		ProviderCode: "gpt",
		AccountType:  "api_key",
		Diagnostics:  "full",
		Model:        "gpt-4o-mini",
		Access:       accounts.AccessScope{ViewerID: "sys_admin", IsAdmin: true},
	})
	if err != nil {
		t.Fatalf("create test task: %v", err)
	}
	// 路由同款派发调用（test_dispatch_routes.go：effects.DispatchAccountTestTasks）。
	if !accountStore.TestDispatchEffects().DispatchAccountTestTasks(context.Background(), []string{task.ID}) {
		t.Fatal("wired dispatch port must report accepted")
	}
	if len(*captured) != 1 {
		t.Fatalf("expected exactly one signed dispatch, got %d", len(*captured))
	}
	call := (*captured)[0]
	if call.path != "/__aiinternal__/v1/account-test/dispatch" {
		t.Fatalf("dispatch path: %s", call.path)
	}
	var payload struct {
		Version int    `json:"version"`
		TaskID  string `json:"taskId"`
	}
	if err := json.Unmarshal(call.body, &payload); err != nil {
		t.Fatalf("decode dispatch body: %v", err)
	}
	if payload.Version != 1 || payload.TaskID != task.ID {
		t.Fatalf("dispatch payload: %+v", payload)
	}
	expected := signAccountTestDispatch(cfg.Secret, call.body)
	if call.signature != expected {
		t.Fatalf("dispatch signature mismatch: %s want %s", call.signature, expected)
	}
}

// TestComposeSystemAPIWiresAccountTestDispatch pins the composition-root
// wiring line (assembly 断线零容忍：源码级断言复用 revoker 先例).
func TestComposeSystemAPIWiresAccountTestDispatch(t *testing.T) {
	source, err := os.ReadFile("compose.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	needle := "accountStore.SetTestDispatchEffects(newJobsAccountTestDispatchBridge(cfg.JobsInternalURL, cfg.Secret, nil))"
	if !strings.Contains(text, needle) {
		t.Fatalf("compose root must wire the manual account test dispatch bridge: %s", needle)
	}
	accountStorePos := strings.Index(text, "accountStore, err := accounts.NewStore")
	wirePos := strings.Index(text, needle)
	if accountStorePos < 0 || wirePos < accountStorePos {
		t.Fatalf("account test dispatch bridge must be wired after account store construction")
	}
}
