package modelcheckruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
)

// w14i_projection_arms_test.go 覆盖 run 的剩余可注入错误臂：
//   - validateRequest 拒绝臂（缺 Model 字段）；
//   - 失败路径的 ProjectOutcome 错误臂（resolver 失败前关闭 dataset）；
//   - 成功路径的 ProjectOutcome 错误臂（probe_completed 进度事件中关闭
//     dataset，投影阶段连接已关闭）。
//
// w14i 波次不可达清单（均已核对）：
//   - 成功/失败路径的 UpdateQualityDecision 错误臂：ProjectOutcome 与
//     UpdateQualityDecision 之间无注入点，dataset 关闭会先让前者失败，
//     无独立故障通道。
//   - len(items)==0 的 payload.Item 回退：executor 的 OutcomePayload.Items
//     恒 >=1，回退不可达。
//   - marshalEvidence/marshalRequestSummary/qualityDecisionJSON/digestJSON
//     的编码失败回退：入参均为包内受控纯数据结构，json.Marshal 恒成功。

func TestW14iRunRejectsInvalidRequest(t *testing.T) {
	// 契约：缺 Model 的请求必须在 validateRequest 处 fail-closed。
	durable, dataset, _, now := openRuntimeStores(t)
	defer durable.Close()
	defer dataset.Close()
	service := newRuntimeService(durable, dataset, "http://unused.invalid", now)
	request := runtimeRequest(now)
	request.Model = ""
	result, err := service.Run(context.Background(), request)
	if !errors.Is(err, ErrInvalidRequest) || result.RunID != "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestW14iFailurePathProjectsOutcomeErrorJoined(t *testing.T) {
	// 契约：执行失败且 dataset 投影也不可用时，run 返回执行错误与投影
	// 错误的合并错误。
	durable, dataset, _, now := openRuntimeStores(t)
	defer durable.Close()
	service := newRuntimeService(durable, dataset, "http://unused.invalid", now)
	service.Resolver = func(context.Context, modelcheckexecutor.ResolutionRequest) (modelcheckexecutor.ResolvedTarget, error) {
		// claim 之后 resolver 才运行，此时 CreateRun 已完成；先关闭
		// dataset，让失败路径的 ProjectOutcome 也失败。
		_ = dataset.Close()
		return modelcheckexecutor.ResolvedTarget{}, errors.New("w14i resolver boom")
	}
	result, err := service.Run(context.Background(), runtimeRequest(now))
	if err == nil || !strings.Contains(err.Error(), "w14i resolver boom") || !strings.Contains(err.Error(), "project model check failure") {
		t.Fatalf("应返回合并错误: %v", err)
	}
	if result.RunStatus != "" {
		t.Fatalf("投影失败时应返回空结果: %#v", result)
	}
}

func TestW14iSuccessPathProjectOutcomeError(t *testing.T) {
	// 契约：探针全部成功但 dataset 投影连接失效时，run 必须上抛投影错误。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-sol","output_text":"OK-MODEL-CHECK"}`))
	}))
	defer server.Close()
	durable, dataset, _, now := openRuntimeStores(t)
	defer durable.Close()
	service := newRuntimeService(durable, dataset, server.URL, now)
	_, err := service.RunWithProgress(context.Background(), runtimeRequest(now), func(event ProgressEvent) {
		if event.Type == "probe_completed" {
			// 探针执行阶段不使用 dataset；首个探针事件后关闭它，
			// 使终态投影失败。
			_ = dataset.Close()
		}
	})
	if err == nil || !strings.Contains(err.Error(), "project model check outcome") {
		t.Fatalf("应上抛投影错误: %v", err)
	}
}
