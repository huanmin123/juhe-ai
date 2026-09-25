// 波次 w14q：速度优先/电路恢复探针闭包成功臂。此前 w9h/w12c 只覆盖了
// 任务失败分支（31.8%/76.5%）；这里用本地 httptest 上游（base_url 指向
// 127.0.0.1）驱动 ProbeAccountView 真实成功路径，不发起外部请求。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accountbalance"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
)

// w14qSeedProbeHTTPAccount 在 probe fixture 上补一个指向本地 httptest 上游的
// chat_json 账户（凭据 base_url 直接写回环地址，探针不出本机）。
func w14qSeedProbeHTTPAccount(t *testing.T, handle *proberepoStoreHandle, upstream string) string {
	t.Helper()
	credentials, err := json.Marshal(map[string]any{"api_key": "sk-w14q", "base_url": upstream})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := accountbalance.EncryptV1Envelope(wgBalanceSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		// seedProbeCoreTables 的账户形状缺 LoadProbeView 读取的代理列。
		`ALTER TABLE accounts ADD COLUMN proxy_profile_id TEXT`,
		`INSERT INTO groups (id, system_account_id, provider_code) VALUES ('g-w14q', 'sys-w14q', 'openai')`,
		`INSERT INTO group_accounts (group_id, system_account_id, account_id) VALUES ('g-w14q', 'sys-w14q', 'acc-w14q')`,
		`INSERT INTO accounts (id, system_account_id, name, type, status, schedulable, provider_code, health_check_model, health_check_endpoint_mode, credentials_encrypted, config_revision, dispatch_revision)
		 VALUES ('acc-w14q', 'sys-w14q', 'w14q', 'api_key', 'active', 1, 'openai', 'gpt-w14q', 'chat_json', ?, 1, 2)`,
	}
	for index, statement := range statements {
		var execErr error
		if index == len(statements)-1 {
			_, execErr = handle.db.Exec(statement, envelope)
		} else {
			_, execErr = handle.db.Exec(statement)
		}
		if execErr != nil {
			t.Fatal(execErr)
		}
	}
	return "acc-w14q"
}

func w14qProbeStoreFixture(t *testing.T) (*proberepoStoreHandle, *accountprobe.Service) {
	t.Helper()
	store, handle := w9hProbeStoreFixture(t)
	service, err := accountprobe.NewService(accountprobe.Options{Source: store, Secret: wgBalanceSecret, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	return handle, service
}

// TestW14QSpeedFirstProbeFuncSuccessPath 覆盖速度优先探针闭包的成功路径：
// 真实完成上游请求 → 快照成功 + TransportProbeOutcomeFromResult 证据分类。
func TestW14QSpeedFirstProbeFuncSuccessPath(t *testing.T) {
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	if seenPath != "" {
		t.Fatal("上游不应在测试前被访问")
	}
	handle, service := w14qProbeStoreFixture(t)
	w14qSeedProbeHTTPAccount(t, handle, server.URL)
	closure := speedFirstProbeFunc(service)
	snapshot, outcome := closure(context.Background(), nil, opsjobs.ProbeCandidate{
		AccountID: "acc-w14q",
		Scope:     opsjobs.ProbeScope{GroupID: "g-w14q", SystemAccountID: "sys-w14q"},
	}, &opsjobs.ProbeAccountRef{AccountID: "acc-w14q", GroupID: "g-w14q"})
	if !snapshot.Success {
		t.Fatalf("本地成功上游必须产生成功快照: %+v", snapshot)
	}
	if outcome.Kind == opsjobs.ProbeOutcomeUnknown && outcome.FailureKind == opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("成功路径不得分类为任务失败: %+v", outcome)
	}
	if seenPath != "/v1/chat/completions" {
		t.Fatalf("上游路径=%q", seenPath)
	}
}

// TestW14QCircuitRecoveryTransportProbeSuccess 覆盖电路恢复传输探针的
// 真实上游成功/失败分类链（HasRealUpstreamAttempt + UpstreamCompleted）。
func TestW14QCircuitRecoveryTransportProbeSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"juhe"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	handle, service := w14qProbeStoreFixture(t)
	w14qSeedProbeHTTPAccount(t, handle, server.URL)
	// 零值 scope 无 modelBucket → 不钉住（回退健康检查模型，既有行为）。
	outcome := circuitRecoveryTransportProbe(context.Background(), service,
		circuitRecoveryProbeRequest(
			opsjobs.RecoveryRuntimeIdentity{Kind: "owner", AccountID: "acc-w14q", SystemAccountID: "sys-w14q"},
			opsjobs.CircuitState{}, "g-w14q", "sys-w14q"))
	if outcome.Kind == opsjobs.ProbeOutcomeUnknown && outcome.FailureKind == opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("成功上游不得分类为任务失败: %+v", outcome)
	}
	// 上游 HTTP 失败（非 2xx）→ 真实上游尝试未完成成功的失败证据。
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"boom"}}`, http.StatusBadGateway)
	}))
	defer broken.Close()
	handle2, service2 := w14qProbeStoreFixture(t)
	w14qSeedProbeHTTPAccount(t, handle2, broken.URL)
	failureOutcome := circuitRecoveryTransportProbe(context.Background(), service2,
		circuitRecoveryProbeRequest(
			opsjobs.RecoveryRuntimeIdentity{Kind: "owner", AccountID: "acc-w14q", SystemAccountID: "sys-w14q"},
			opsjobs.CircuitState{}, "g-w14q", "sys-w14q"))
	if failureOutcome.Kind == opsjobs.ProbeOutcomeUnknown && failureOutcome.FailureKind == opsjobs.ProbeFailureTaskFailure {
		t.Fatalf("502 上游是真实失败证据，不得分类为任务失败: %+v", failureOutcome)
	}
	// HTTP 往返真实完成（502）→ framing 完成但语义失败。
	if failureOutcome.Kind != opsjobs.ProbeOutcomeFramingComplete {
		t.Fatalf("502 上游必须分类为 framing 完成: %+v", failureOutcome)
	}
	if failureOutcome.SemanticSuccess != nil && *failureOutcome.SemanticSuccess {
		t.Fatalf("502 上游不得语义成功: %+v", failureOutcome)
	}
}
