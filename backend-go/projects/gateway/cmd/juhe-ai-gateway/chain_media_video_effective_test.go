package main

// 视频创建计划的计费快照换算测试（P1）：GLM 档位时长、Veo 默认 8s 与两档
// 分辨率必须按 adapter 实际出站值进入 request_snapshot_json，而不是请求原值。

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaymedia"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func TestChainMediaParamValidationErrorMapsNestedMetadataConflict(t *testing.T) {
	err := chainMediaParamValidationError(fmt.Errorf("%w: parameters.size", gatewaymedia.ErrMetadataReservedField))
	var validation *gatewaypreauth.GatewayRequestValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("应为 GatewayRequestValidationError: %v", err)
	}
	if validation.Code != "metadata_conflict" || validation.StatusCode != 400 {
		t.Fatalf("nested metadata 冲突映射 = %d/%s, want 400/metadata_conflict", validation.StatusCode, validation.Code)
	}
	if validation.Message == "" {
		t.Fatal("错误消息不得为空")
	}
}

func TestChainVideoCreatePlanRejectsAbsentNormativeMetadataPaths(t *testing.T) {
	for name, tc := range map[string]struct {
		body   map[string]any
		vendor string
	}{
		"qwen size": {
			body: map[string]any{"model": "wan2.6-t2v", "prompt": "p",
				"metadata": map[string]any{"parameters": map[string]any{"size": "1920*1080"}}},
			vendor: "qwen",
		},
		"veo resolution": {
			body: map[string]any{"model": "veo-3.0-generate-preview", "prompt": "p",
				"metadata": map[string]any{"parameters": map[string]any{"resolution": "1080p"}}},
			vendor: "gemini",
		},
		"xai duration": {
			body: map[string]any{"model": "grok-imagine-video-1.5", "prompt": "p",
				"metadata": map[string]any{"duration": float64(10)}},
			vendor: "xai",
		},
	} {
		_, err := chainVideoCreatePlanOf(tc.body, nil, gatewaydispatch.AccountCandidate{VendorCode: tc.vendor})
		var gatewayErr *gatewaypreauth.GatewayRequestValidationError
		if !errors.As(err, &gatewayErr) || gatewayErr.Code != "metadata_conflict" || gatewayErr.StatusCode != 400 {
			t.Fatalf("%s 应映射 400 metadata_conflict: %v", name, err)
		}
		if !strings.Contains(gatewayErr.Message, "parameters") && !strings.Contains(gatewayErr.Message, "duration") {
			t.Fatalf("%s 错误消息应指明冲突路径: %v", name, gatewayErr.Message)
		}
	}
}

func TestChainVideoCreatePlanEffectiveMetering(t *testing.T) {
	glmPlan, err := chainVideoCreatePlanOf(map[string]any{
		"model":   "cogvideox-3",
		"prompt":  "p",
		"seconds": float64(8),
	}, nil, gatewaydispatch.AccountCandidate{VendorCode: "glm"})
	if err != nil {
		t.Fatalf("glm plan: %v", err)
	}
	glmSnapshot := glmPlan.snapshotFields()
	if glmSnapshot.Seconds == nil || *glmSnapshot.Seconds != 10 {
		t.Fatalf("glm snapshot seconds = %v, want 10（出站档位）", glmSnapshot.Seconds)
	}

	veoPlan, err := chainVideoCreatePlanOf(map[string]any{
		"model":  "veo-3.0-generate-preview",
		"prompt": "p",
		"size":   "1024x1024",
	}, nil, gatewaydispatch.AccountCandidate{VendorCode: "gemini"})
	if err != nil {
		t.Fatalf("veo plan: %v", err)
	}
	veoSnapshot := veoPlan.snapshotFields()
	if veoSnapshot.Seconds == nil || *veoSnapshot.Seconds != 8 {
		t.Fatalf("veo snapshot seconds = %v, want default 8", veoSnapshot.Seconds)
	}
	if veoSnapshot.ResolutionTier != "720p" {
		t.Fatalf("veo snapshot tier = %q, want 720p", veoSnapshot.ResolutionTier)
	}

	// Veo 无模型默认时长时，未生效的请求 seconds 不得进入快照（否则响应
	// metadata 与计费会误用用户未生效值）。
	unknownPlan, err := chainVideoCreatePlanOf(map[string]any{
		"model":   "veo-unknown",
		"prompt":  "p",
		"seconds": float64(5),
	}, nil, gatewaydispatch.AccountCandidate{VendorCode: "gemini"})
	if err != nil {
		t.Fatalf("veo unknown plan: %v", err)
	}
	if seconds := unknownPlan.snapshotFields().Seconds; seconds != nil {
		t.Fatalf("ignored seconds must not enter snapshot: %v", *seconds)
	}
}
