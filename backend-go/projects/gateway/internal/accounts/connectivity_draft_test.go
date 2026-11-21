package accounts

import (
	"context"
	"testing"
)

// TestPrepareConnectionTestDraftOmitsHealthTrio 锁定连接测试草稿契约：媒体域
// 草稿允许只提交连接配置与支持模型，健康检查模型/形态由快照准备阶段派生；
// connectionPresetCode 随草稿透传供注册表查找探测规格。
func TestPrepareConnectionTestDraftOmitsHealthTrio(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	access := AccessScope{ViewerID: adminID, IsAdmin: true}

	draft, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), &TestDraftAccountInput{
		ProviderCode:         "gpt",
		ConnectionPresetCode: "gpt_openai_v1",
		Name:                 "连接测试草稿",
		Type:                 "api_key",
		GroupID:              "grp-default-" + adminID,
		Credentials: Credentials{
			"api_key":                  "sk-connection-draft",
			"base_url":                 "https://api.openai.com/v1",
			"supported_endpoint_modes": []any{"audio_speech"},
		},
		SupportedModels: []string{"gpt-4o-mini"},
	}, access, "")
	if err != nil {
		t.Fatalf("连接测试草稿应允许省略健康三件套：%v", err)
	}
	if draft.HealthCheckModel != "gpt-4o-mini" {
		t.Fatalf("检查模型应派生首个支持模型：%q", draft.HealthCheckModel)
	}
	if draft.HealthCheckEndpointMode != "audio_speech" {
		t.Fatalf("检查形态应从声明集派生：%q", draft.HealthCheckEndpointMode)
	}
	if draft.ConnectionPresetCode == nil || *draft.ConnectionPresetCode != "gpt_openai_v1" {
		t.Fatalf("连接 preset 必须写入快照：%+v", draft.ConnectionPresetCode)
	}
}

// TestPrepareModelTestDraftStillRequiresHealthTrio 锁定模型测试草稿仍要求
// 检查模型/形态，避免“连接测试放宽”误伤对话账户。
func TestPrepareModelTestDraftStillRequiresHealthTrio(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	access := AccessScope{ViewerID: adminID, IsAdmin: true}

	_, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), &TestDraftAccountInput{
		ProviderCode: "gpt",
		Name:         "模型测试草稿",
		Type:         "api_key",
		GroupID:      "grp-default-" + adminID,
		Credentials: Credentials{
			"api_key":                  "sk-model-draft",
			"base_url":                 "https://api.openai.com/v1",
			"supported_endpoint_modes": []any{"chat_json"},
		},
		SupportedModels: []string{"gpt-4o-mini"},
	}, access, "")
	if err == nil {
		t.Fatal("模型测试草稿缺检查模型/形态应拒绝")
	}
}
