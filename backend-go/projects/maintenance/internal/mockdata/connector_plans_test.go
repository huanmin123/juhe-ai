package mockdata

// connectorPlansFor 的五域退役钉值（AI账户上游接口能力设计 §3.2，revision 30
// 清洗终态）：五域连接 plan 不带 modes（连接行不写 account_endpoint_modes，
// mock 造数后 upgrade 无可清洗行），健康检查形态按域常量回落；chat 域 plan
// 维持 lane ∩ preset 词表播种（既有语义）。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func connectorPlansTestSeed(id, slug, domain string) *mockAccountSeed {
	seed := &mockAccountSeed{
		id:   id,
		slug: slug,
		typ:  contracts.AccountTypeAPIKey,
		// preset/vendor 留空走默认组合 gpt × api_key × gpt_openai_v1（registry
		// 对 chat/video/audio/image/embedding 域均有注册行）。
		domain: domain,
	}
	if domain == contracts.DomainRerank {
		// rerank 全平台仅 qwen DashScope 行注册（registry §1.1）。
		seed.vendor = contracts.VendorQwen
		seed.preset = contracts.PresetQwenDashScope
	}
	return seed
}

func TestConnectorPlansForRetiredDomainsOmitModes(t *testing.T) {
	for _, domain := range []string{
		contracts.DomainImage, contracts.DomainVideo, contracts.DomainAudio,
		contracts.DomainEmbedding, contracts.DomainRerank,
	} {
		seed := connectorPlansTestSeed("acc-"+domain, domain, domain)
		plans, err := connectorPlansFor(seed)
		if err != nil {
			t.Fatalf("域 %s 的连接计划应成功：%v", domain, err)
		}
		if len(plans) != 1 {
			t.Fatalf("域 %s 应产出单连接 plan：%d", domain, len(plans))
		}
		if plans[0].domain != domain {
			t.Fatalf("plan 域应与 seed 一致：%s != %s", plans[0].domain, domain)
		}
		if len(plans[0].modes) != 0 {
			t.Fatalf("五域 plan 不应携带 modes（revision 30 清洗终态）：%s %+v", domain, plans[0].modes)
		}
		if mode := preferredHealthModeForPlan(plans[0]); mode == "" {
			t.Fatalf("五域 plan 健康检查形态应落域常量记录值：%s", domain)
		} else if domain == contracts.DomainImage && mode != contracts.ModeImagesJSON {
			t.Fatalf("image 域健康检查形态应为 images_json：%s", mode)
		}
	}
}

func TestConnectorPlansForChatDomainKeepsModes(t *testing.T) {
	seed := connectorPlansTestSeed("acc-chat", "chat", contracts.DomainChat)
	plans, err := connectorPlansFor(seed)
	if err != nil {
		t.Fatalf("chat 域连接计划应成功：%v", err)
	}
	if len(plans) != 1 || len(plans[0].modes) == 0 {
		t.Fatalf("chat 域 plan 应维持 lane ∩ 词表播种：%+v", plans)
	}
	if mode := preferredHealthModeForPlan(plans[0]); mode != contracts.ModeResponsesSSE && mode != contracts.ModeChatJSON {
		t.Fatalf("chat 域健康检查形态应从 mode 交集选型：%s", mode)
	}
}

// TestAccountModesDeclaration 钉住 mock 账户凭据声明键（上游接口能力设计 §3）：
// chat 域取首个 chat plan 形态集（非空、推导落 chat），五域退役域恒 nil
//（凭据不落键，与 revision 30 清洗终态一致）。
func TestAccountModesDeclaration(t *testing.T) {
	chatSeed := connectorPlansTestSeed("acc-chat", "chat", contracts.DomainChat)
	chatPlans, err := connectorPlansFor(chatSeed)
	if err != nil {
		t.Fatalf("chat plan: %v", err)
	}
	declaration := accountModesDeclaration(chatPlans)
	if len(declaration) == 0 {
		t.Fatalf("chat 域 mock 账户凭据必须携带声明键（m2/v2 核验判 modes_missing）")
	}
	for _, mode := range declaration {
		if mode == "embeddings_json" || mode == "rerank_json" {
			t.Fatalf("chat 声明集不得与向量/重排混合：%v", declaration)
		}
	}
	for _, domain := range []string{
		contracts.DomainImage, contracts.DomainVideo, contracts.DomainAudio,
		contracts.DomainEmbedding, contracts.DomainRerank,
	} {
		seed := connectorPlansTestSeed("acc-"+domain, domain, domain)
		plans, err := connectorPlansFor(seed)
		if err != nil {
			t.Fatalf("域 %s plan: %v", domain, err)
		}
		if got := accountModesDeclaration(plans); got != nil {
			t.Fatalf("五域退役域 %s 凭据不得落声明键，got %v", domain, got)
		}
	}
	if got := accountModesDeclaration(nil); got != nil {
		t.Fatalf("无 plan 应返回 nil，got %v", got)
	}
}
