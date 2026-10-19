// t30_catalog_models_test.go 钉 latestCatalogModels 切 models 单表（供应商与
// 上游协议面重构设计 §3：provider_model_catalog 已并入 models，旧目录行
// scope 固定迁为 builtin）后的取数语义：
//   - builtin + vendor_code 窗口 + status='active'（与写入面
//     resolveEntryModelID 身份门同键，默认值全部可过建户写面断言）；
//   - 图像/向量排除经 supported_endpoint_modes_json 逆映射等价旧 mode 谓词；
//   - release_date 降序、空值压后、LIMIT 截断；
//   - 目录臂无行时回落供应商档案 default_supported_models（旧行为语义保持）。
package oauthmgmt

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

// supportedModelSet 读账户落库的支持模型名集合（account_supported_models ×
// models.upstream_model）。
func supportedModelSet(t *testing.T, env *testEnv, accountID string) map[string]bool {
	t.Helper()
	rows, err := env.db.Query(`SELECT m.upstream_model FROM account_supported_models asm
		JOIN models m ON m.id = asm.model_id WHERE asm.account_id = ?`, accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			t.Fatal(err)
		}
		set[model] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return set
}

func TestT30LatestCatalogModelsFromModelsTable(t *testing.T) {
	env := newTestEnv(t)
	now := "2026-01-01T00:00:00.000Z"
	rows := []struct {
		name, release, status, modes string
	}{
		{"t30-new", "2026-06-01", "active", `["chat_json","chat_sse"]`},
		{"t30-mid", "2025-01-15", "active", `["responses_json"]`},
		{"t30-nodate", "", "active", `["chat_json"]`},
		{"t30-img", "2026-09-01", "active", `["images_json"]`},
		{"t30-embed", "2026-09-02", "active", `["embed_content","count_tokens"]`},
		{"t30-off", "2026-09-03", "disabled", `["chat_json"]`},
	}
	for _, row := range rows {
		env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
			supported_endpoint_modes_json, release_date, created_at, updated_at)
			VALUES (?, 't30vendor', ?, ?, 'builtin', ?, ?, ?, ?, ?)`,
			"mod-t30-"+row.name, row.name, row.name, row.status, row.modes, row.release, now, now)
	}
	models, err := env.store.latestCatalogModels(context.Background(), "t30vendor", 5)
	if err != nil {
		t.Fatal(err)
	}
	// release_date 降序：t30-new > t30-mid > 空日期组；图像/向量 mode 与
	// disabled 行排除；空日期行压后。
	want := []string{"t30-new", "t30-mid", "t30-nodate"}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("目录取数: got %v want %v", models, want)
	}
}

func TestT30CreateAccountDefaultsFollowModelsCatalog(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	now := "2026-01-01T00:00:00.000Z"
	for _, row := range []struct {
		id, name, release, status, modes string
	}{
		{"mod-t30-cat-a", "t30-cat-a", "2026-06-01", "active", `["chat_json"]`},
		{"mod-t30-cat-b", "t30-cat-b", "2025-01-01", "active", `["chat_json"]`},
		{"mod-t30-cat-img", "t30-cat-img", "2026-09-01", "active", `["images_json"]`},
		{"mod-t30-cat-off", "t30-cat-off", "2026-09-02", "disabled", `["chat_json"]`},
	} {
		env.exec(t, `INSERT INTO models (id, vendor_code, public_model, upstream_model, scope, status,
			supported_endpoint_modes_json, release_date, created_at, updated_at)
			VALUES (?, 'gpt', ?, ?, 'builtin', ?, ?, ?, ?, ?)`,
			row.id, row.name, row.name, row.status, row.modes, row.release, now, now)
	}
	// gpt 目录行集：seedOAuthCatalog 的 gpt-4o-mini/gpt-5/m1/m2（无
	// release_date）+ 上面两行 active 带日期行。期望目录臂取最新 5 个对话模
	// 型：带日期两行在前，空日期组按 upstream_model ASC 补足
	// [gpt-4o-mini, gpt-5, m1]；m2（第 6 名）截断，图像/停用行排除。
	env.exchanger.respond = staticToken(openAITokenPayload("t30-catalog"))
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"T30 目录默认账户"}`)
	if code != http.StatusCreated {
		t.Fatalf("目录默认建户: %d %v", code, payload)
	}
	accountID := dataMap(t, payload)["id"].(string)
	got := supportedModelSet(t, env, accountID)
	want := map[string]bool{"t30-cat-a": true, "t30-cat-b": true, "gpt-4o-mini": true, "gpt-5": true, "m1": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("目录臂默认支持模型: got %v want %v", got, want)
	}
}

func TestT30CreateAccountFallsBackToProfileDefaultsWhenBuiltinEmpty(t *testing.T) {
	env := newTestEnv(t)
	env.login(t, "root", "root-pass", "super_admin")
	// gpt 的 builtin 目录行整体转 global：写面身份门（owner-visible）仍解析
	// 档案默认模型，目录臂 builtin 窗口为空 → 回落
	// profile.default_supported_models（seedOAuthCatalog gpt 行 =
	// ["gpt-4o-mini","gpt-5"]）。
	env.exec(t, `UPDATE models SET scope = 'global' WHERE vendor_code = 'gpt'`)
	env.exchanger.respond = staticToken(openAITokenPayload("t30-fallback"))
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/openai-oauth/create-from-refresh-token",
		`{"refreshToken":"rt","providerProtocolProfileId":"profile_gpt_openai_v1","name":"T30 回落账户"}`)
	if code != http.StatusCreated {
		t.Fatalf("回落建户: %d %v", code, payload)
	}
	accountID := dataMap(t, payload)["id"].(string)
	got := supportedModelSet(t, env, accountID)
	want := map[string]bool{"gpt-4o-mini": true, "gpt-5": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("档案默认回落: got %v want %v", got, want)
	}
}
