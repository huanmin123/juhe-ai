// 管理面 PATCH 对既有媒体模板行的接口级回归：读侧 mode 推导放行后，
// Gemini generate_content 图像行（chat 投影）与空 endpoint modes 的自定义
// 媒体行仍必须可编辑价格与日期，且不能放开新建路径的严格校验。
package providers

import (
	"net/http"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

func TestProvidersPatchExistingProjectedMediaPricingRows(t *testing.T) {
	env := newTestEnv(t)
	env.seedCatalog(t)
	env.seedBuiltinModel(t, builtinModelSeed{
		ID: "cat-gemini-image", ProviderCode: "gemini", Model: "gemini-3.1-flash-image",
		Protocols: `["generate_content","stream_generate_content"]`,
	})
	env.exec(t, `UPDATE models SET media_pricing_json = ? WHERE id = 'cat-gemini-image'`, testImageMediaPricing)
	env.login(t, "root", "root-pass", "super_admin")

	// Gemini 内置图像行：endpoint modes 投影为 chat，日期/价格编辑必须放行。
	code, patched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gemini/models/cat-gemini-image",
		`{"expectedUpdatedAt":"`+env.builtinUpdatedAtOf(t, "cat-gemini-image")+`","releaseDate":"2025-10-01"}`)
	if code != http.StatusOK {
		t.Fatalf("patch projected built-in releaseDate: %d %v", code, patched)
	}
	code, repriced := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gemini/models/cat-gemini-image",
		`{"expectedUpdatedAt":"`+env.builtinUpdatedAtOf(t, "cat-gemini-image")+`","mediaPricing":`+testImageMediaPricing+`}`)
	if code != http.StatusOK {
		t.Fatalf("patch projected built-in mediaPricing: %d %v", code, repriced)
	}
	var retained string
	if err := env.db.QueryRow(`SELECT media_pricing_json FROM models WHERE id = 'cat-gemini-image'`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained == "" {
		t.Fatal("built-in media pricing cleared by date/pricing patch")
	}

	// 新建同供应商图像模型未提交 mediaPricing：Gemini 图像行的 chat 投影
	// 不能阻止它作为自动模板候选（按模板 unit 还原媒体类型）。
	code, copied := env.do(t, http.MethodPost, "/__aisys__/api/providers/gemini/models",
		`{"model":"gemini-new-image","mode":"image","supportedApiProtocols":["images"],"releaseDate":"2026-01-01"}`)
	if code != http.StatusCreated {
		t.Fatalf("create image model from projected template: %d %v", code, copied)
	}
	var copiedRaw string
	if err := env.db.QueryRow(`SELECT media_pricing_json FROM models WHERE public_model = 'gemini-new-image'`).Scan(&copiedRaw); err != nil {
		t.Fatal(err)
	}
	copiedPricing := parseMediaPricingForTest(t, copiedRaw)
	if copiedPricing.Unit != mediapricing.UnitImage || copiedPricing.BaseUsd != 0.04 {
		t.Fatalf("copied media pricing = %+v", copiedPricing)
	}

	// 空 endpoint modes 的自定义媒体行（迁移/historical 行）同理。
	env.seedCustomModel(t, customModelSeed{
		ID: "cu-unknown-mode", ProviderCode: "gemini", Model: "custom-image-empty-modes",
		Scope: "global", Protocols: `[]`,
	})
	env.exec(t, `UPDATE models SET media_pricing_json = ? WHERE id = 'cu-unknown-mode'`, testImageMediaPricing)
	code, customPatched := env.do(t, http.MethodPatch, "/__aisys__/api/providers/gemini/models/cu-unknown-mode",
		`{"expectedUpdatedAt":"`+env.updatedAtOf(t, "cu-unknown-mode")+`","mediaPricing":`+testImageMediaPricing+`}`)
	if code != http.StatusOK {
		t.Fatalf("patch empty-mode custom mediaPricing: %d %v", code, customPatched)
	}

	// 新建路径仍严格：空 mode + image 模板必须 400。
	code, created := env.do(t, http.MethodPost, "/__aisys__/api/providers/gemini/models",
		`{"model":"new-empty-mode-image","mediaPricing":`+testImageMediaPricing+`}`)
	if code != http.StatusBadRequest {
		t.Fatalf("create empty-mode media pricing must be rejected: %d %v", code, created)
	}
}
