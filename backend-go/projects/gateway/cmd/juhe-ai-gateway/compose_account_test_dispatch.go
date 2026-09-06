package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
)

// jobsAccountTestDispatchBridge 是 accounts.TestDispatchEffects 的生产装配：
// 组合根把手动账号测试的派发/取消从 gateway 进程桥接到 jobs 进程的
// internal-api loopback 端点（POST /__aiinternal__/v1/account-test/dispatch
// 与 /v1/account-test/cancel，jobs/internal/internalapi/dispatch.go）。
//
// Node 原型是 worker IPC（background-ipc.ts sendAccountTestTasksToWorker /
// sendAccountTestCancelToWorker：db-service 把任务 ID 发给 ops worker，worker
// 不在线时返回 false，路由随后把任务置败并回 503）。Go 双进程拓扑下该通道
// 换成 loopback HTTP + HMAC：
//   - 签名域 `juhe-ai:account-test-dispatch:v1\n` + 原始请求体，
//     HMAC-SHA256，头部 X-Juhe-Ai-Signature: v1=<hex>（jobs 侧
//     internalapi.CreateAccountTestDispatchSignature 的逐字节镜像——gateway
//     模块不允许 import jobs 模块，故此处在组合根复刻同一契约，双方由
//     golden 向量测试钉死）；
//   - 请求体 {"version":1,"taskId":"<id>"}（jobs parseTaskID 契约：恰好两个
//     键，version=1，taskId 非空）；
//   - 仅接受 127.0.0.1/::1 来源，因此默认回环地址
//     http://127.0.0.1:3305（jobs health/internal-api 监听默认值）；
//   - 非 202 视为 worker 不可用（返回 false），与 Node worker-unavailable
//     路径一致：路由把任务置败并回 503。
type jobsAccountTestDispatchBridge struct {
	baseURL string
	secret  string
	client  *http.Client
}

// accountTestDispatchSignatureDomain mirrors jobs internalapi
// AccountTestDispatchSignatureDomain（含尾部 \n）。
const accountTestDispatchSignatureDomain = "juhe-ai:account-test-dispatch:v1\n"

// accountTestDispatchPath / accountTestCancelPath mirror jobs internalapi
// route paths (Node accountTestDispatchInternalPrefix + v1 route).
const (
	accountTestDispatchPath = "/__aiinternal__/v1/account-test/dispatch"
	accountTestCancelPath   = "/__aiinternal__/v1/account-test/cancel"
)

// newJobsAccountTestDispatchBridge assembles the production dispatch bridge.
// A nil client installs a 5s-timeout transport client; jobs 不可达时派发按
// 不可用处理（路由 503 契约），组合根不因 jobs 缺席而失败。
func newJobsAccountTestDispatchBridge(baseURL, secret string, client *http.Client) accounts.TestDispatchEffects {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &jobsAccountTestDispatchBridge{baseURL: trimmed, secret: secret, client: client}
}

// accountTestDispatchRequest mirrors the jobs parseTaskID wire contract:
// exactly {"version":1,"taskId":"<id>"}.
type accountTestDispatchRequest struct {
	Version int    `json:"version"`
	TaskID  string `json:"taskId"`
}

// signAccountTestDispatch mirrors jobs internalapi
// CreateAccountTestDispatchSignature byte-for-byte.
func signAccountTestDispatch(secret string, rawBody []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(accountTestDispatchSignatureDomain))
	_, _ = mac.Write(rawBody)
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

// DispatchAccountTestTasks mirrors dispatchAccountTestTasks →
// sendAccountTestTasksToWorker：空集合直接成功；逐任务 POST 派发端点，
// 任一任务未被接受即返回 false（路由会把该批任务置败并回 503；当前路由
// 每批只携带一个任务 ID，与 Node 单消息单批语义等价）。
func (b *jobsAccountTestDispatchBridge) DispatchAccountTestTasks(ctx context.Context, taskIDs []string) bool {
	normalized := make([]string, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		if trimmed := strings.TrimSpace(taskID); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	if len(normalized) == 0 {
		return true
	}
	accepted := true
	for _, taskID := range normalized {
		if !b.postAccountTestInternal(ctx, accountTestDispatchPath, taskID, true) {
			accepted = false
		}
	}
	return accepted
}

// DispatchAccountTestCancel mirrors dispatchAccountTestCancel：
// fire-and-forget（Node 返回值被路由忽略；失败仅告警，不阻塞取消响应）。
func (b *jobsAccountTestDispatchBridge) DispatchAccountTestCancel(taskID string) {
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !b.postAccountTestInternal(ctx, accountTestCancelPath, trimmed, false) {
		slog.Warn("账号测试取消派发未送达 jobs internal-api",
			"event", "account_test_cancel_dispatch_unavailable", "taskId", trimmed)
	}
}

// postAccountTestInternal performs one signed POST; requireAccepted 区分
// 派发（202 才算接受）与取消（尽力而为，任何 HTTP 结束都不重试）。
func (b *jobsAccountTestDispatchBridge) postAccountTestInternal(ctx context.Context, path, taskID string, requireAccepted bool) bool {
	rawBody, err := json.Marshal(accountTestDispatchRequest{Version: 1, TaskID: taskID})
	if err != nil {
		slog.Warn("账号测试派发请求体编码失败", "event", "account_test_dispatch_encode_failed", "error", err)
		return false
	}
	endpoint := b.baseURL + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		slog.Warn("账号测试派发请求构造失败", "event", "account_test_dispatch_request_failed", "endpoint", endpoint, "error", err)
		return false
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Juhe-Ai-Signature", signAccountTestDispatch(b.secret, rawBody))
	response, err := b.client.Do(request)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("账号测试派发未送达 jobs internal-api",
				"event", "account_test_dispatch_unavailable", "endpoint", endpoint, "error", err)
		}
		return false
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusAccepted {
		slog.Warn("jobs internal-api 拒绝账号测试派发",
			"event", "account_test_dispatch_rejected", "endpoint", endpoint,
			"statusCode", response.StatusCode)
		return false
	}
	return true
}
