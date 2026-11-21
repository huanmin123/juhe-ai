package accounts

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountstest"
	capabilitydomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
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

// TestPrepareRetiredDomainDraftConnectionTest 锁定五域退役草稿测试契约
// （AI账户上游接口能力设计 §3/§5）：五域表单草稿不声明接口能力、不携带检查
// 模型——能力域经 draft 入参透传后，prepare 链走写面退役臂（凭据剥声明键、
// 不播种默认集），草稿域直接取入参域判定连接测试，不再从播种后的 chat 默认
// 集反推（修复前恒判模型测试并撞「检查模型不能为空」400）。
func TestPrepareRetiredDomainDraftConnectionTest(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	access := AccessScope{ViewerID: adminID, IsAdmin: true}

	draft, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), &TestDraftAccountInput{
		ProviderCode:     "gpt",
		CapabilityDomain: "image",
		Name:             "图像域草稿",
		Type:             "api_key",
		GroupID:          "grp-default-" + adminID,
		Credentials: Credentials{
			"api_key":  "sk-image-draft",
			"base_url": "https://api.openai.com/v1",
		},
		SupportedModels: []string{"gpt-image-1"},
	}, access, "")
	if err != nil {
		t.Fatalf("五域草稿（无声明、无检查模型）应按连接测试放行：%v", err)
	}
	if draft.CapabilityDomain != "image" {
		t.Fatalf("快照应携带入参能力域：%q", draft.CapabilityDomain)
	}
	if draft.HealthCheckModel != "gpt-image-1" {
		t.Fatalf("检查模型应派生首个支持模型：%q", draft.HealthCheckModel)
	}
	if draft.HealthCheckEndpointMode != "images_json" {
		t.Fatalf("检查形态应落域常量默认（images_json）：%q", draft.HealthCheckEndpointMode)
	}
	if _, present := draft.Credentials["supported_endpoint_modes"]; present {
		t.Fatal("退役五域草稿凭据不应落 supported_endpoint_modes 声明键")
	}
}

// TestPrepareRetiredDomainDraftExplicitModeWithinLane 锁定五域草稿显式健康
// 检查形态的成员校验集：声明面退役后凭据声明集恒空，显式值改按域 lane 探测
// 形态集校验——lane 内放行、lane 外拒绝。
func TestPrepareRetiredDomainDraftExplicitModeWithinLane(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	access := AccessScope{ViewerID: adminID, IsAdmin: true}
	base := TestDraftAccountInput{
		ProviderCode:     "gpt",
		CapabilityDomain: "audio",
		Name:             "音频域草稿",
		Type:             "api_key",
		GroupID:          "grp-default-" + adminID,
		Credentials: Credentials{
			"api_key":  "sk-audio-draft",
			"base_url": "https://api.openai.com/v1",
		},
		SupportedModels: []string{"gpt-4o-audio"},
	}

	inLane := base
	inLane.HealthCheckEndpointMode = "audio_speech"
	draft, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), &inLane, access, "")
	if err != nil {
		t.Fatalf("域 lane 内的显式检查形态应放行：%v", err)
	}
	if draft.HealthCheckEndpointMode != "audio_speech" || draft.CapabilityDomain != "audio" {
		t.Fatalf("快照应保留显式形态与入参域：%q %q", draft.HealthCheckEndpointMode, draft.CapabilityDomain)
	}

	outOfLane := base
	outOfLane.HealthCheckEndpointMode = "chat_json"
	if _, err := env.store.prepareAccountDraftTestSnapshot(context.Background(), &outOfLane, access, ""); err == nil {
		t.Fatal("域 lane 外的显式检查形态应拒绝")
	}
}

// TestDraftOptionsContextForRetiredDomainIsConnectionKind 锁定 test-draft-options
// 的 testKind 派生：五域草稿上下文优先消费快照显式能力域（判定连接测试），
// 修复前从凭据 modes 反推恒 chat 而误报 model；无域键的 chat 声明快照维持
// DeriveDomain 既有语义（对照，行为不变）。
func TestDraftOptionsContextForRetiredDomainIsConnectionKind(t *testing.T) {
	retired := manualTestCapabilitiesContextFromDraft(&TestDraftSnapshot{
		ID:                   "acctdraft-retired",
		OwnerSystemAccountID: "sys-owner",
		GroupID:              "grp-1",
		ProviderCode:         "gpt",
		Name:                 "图像域草稿",
		Type:                 "api_key",
		Credentials:          map[string]any{},
		CapabilityDomain:     "image",
	})
	if retired.CapabilityDomain != "image" {
		t.Fatalf("草稿上下文应携带快照能力域：%q", retired.CapabilityDomain)
	}
	if accountstest.ManualTestKind(retired) != capabilitydomain.TestKindConnection {
		t.Fatal("五域草稿 testKind 应为 connection（修复前误报 model）")
	}

	legacy := manualTestCapabilitiesContextFromDraft(&TestDraftSnapshot{
		ID:                   "acctdraft-legacy",
		OwnerSystemAccountID: "sys-owner",
		GroupID:              "grp-1",
		ProviderCode:         "gpt",
		Name:                 "对话草稿",
		Type:                 "api_key",
		Credentials:          map[string]any{"supported_endpoint_modes": []any{"chat_json"}},
	})
	if legacy.CapabilityDomain != "chat" {
		t.Fatalf("无域键快照应维持 modes 反推语义：%q", legacy.CapabilityDomain)
	}
	if accountstest.ManualTestKind(legacy) != capabilitydomain.TestKindModel {
		t.Fatal("chat 声明草稿 testKind 应保持 model")
	}
}

// TestParseTestDraftInputCapabilityDomainGate 锁定解析层域通道：合法六值域
// 接受、词表外值拒绝（fail closed）、空串/缺失按 chat 缺省。
func TestParseTestDraftInputCapabilityDomainGate(t *testing.T) {
	base := func(domain any) map[string]any {
		record := map[string]any{
			"providerCode": "gpt",
			"name":         "草稿",
			"type":         "api_key",
			"groupId":      "grp-1",
		}
		if domain != nil {
			record["capabilityDomain"] = domain
		}
		return record
	}

	input, message := parseTestDraftAccountInput(base("video"))
	if message != "" || input.CapabilityDomain != "video" {
		t.Fatalf("合法域应接受：%q %q", message, input.CapabilityDomain)
	}
	input, message = parseTestDraftAccountInput(base(""))
	if message != "" || input.CapabilityDomain != "" {
		t.Fatalf("空串应按 chat 缺省（不落值）：%q %q", message, input.CapabilityDomain)
	}
	input, message = parseTestDraftAccountInput(base(nil))
	if message != "" || input.CapabilityDomain != "" {
		t.Fatalf("显式 null 应按 chat 缺省：%q %q", message, input.CapabilityDomain)
	}
	if _, message = parseTestDraftAccountInput(base("agent")); message == "" {
		t.Fatal("词表外域应在解析层拒绝")
	}
}

// TestSavedDraftTestSnapshotUsesAccountRowDomain 锁定 saved 草稿域权威：账户
// 行 capability_domain 覆写入参（域创建时确定不可更改 D3），行值五域 + 草稿
// 缺三件套仍按连接测试放行——修复前入参无域通道时按 chat 播种默认集，五域
// 账户「表单配置测试」恒撞「检查模型不能为空」400。
func TestSavedDraftTestSnapshotUsesAccountRowDomain(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	ownerID := env.login(t, "retired-domain-owner", "retired-domain-pass", "user")
	env.seedAccount(t, "acc-retired-domain", ownerID, "音频域账户", "active")
	env.exec(t, `UPDATE accounts SET capability_domain = 'audio' WHERE id = 'acc-retired-domain'`)
	access := AccessScope{ViewerID: adminID, IsAdmin: true}
	view, err := env.store.FindAccountForTestView(context.Background(), access, "acc-retired-domain")
	if err != nil || view == nil {
		t.Fatalf("已保存账户测试视图应返回：%v %v", view, err)
	}

	draft, err := env.store.savedAccountDraftTestSnapshot(context.Background(), view, &TestDraftAccountInput{
		ProviderCode: "gpt",
		Name:         "音频域账户",
		Type:         "api_key",
		GroupID:      "grp-default-" + adminID,
		Credentials: Credentials{
			"api_key":  "sk-retired-domain",
			"base_url": "https://api.openai.com/v1",
		},
		SupportedModels: []string{"gpt-4o-audio"},
	}, access)
	if err != nil {
		t.Fatalf("五域 saved 草稿（无声明、无检查模型）应按账户行域放行：%v", err)
	}
	if draft.CapabilityDomain != "audio" {
		t.Fatalf("快照应按账户行 capability_domain 落域：%q", draft.CapabilityDomain)
	}
	if draft.HealthCheckModel != "gpt-4o-audio" || draft.HealthCheckEndpointMode != "audio_speech" {
		t.Fatalf("连接测试草稿应宽松派生检查模型/形态：%q %q", draft.HealthCheckModel, draft.HealthCheckEndpointMode)
	}
}
