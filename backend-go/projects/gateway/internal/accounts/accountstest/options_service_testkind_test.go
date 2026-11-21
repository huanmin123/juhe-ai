package accountstest

// 手工测试上下文与账户列表 testKind 的同源回归：连接行 account_endpoint_modes
// 优先于 credentials.supported_endpoint_modes；连接未配形态时回落凭据形态。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	capabilitydomain "github.com/huanminabc/juhe-ai/backend-go-platform/capabilitydomain"
)

func TestManualTestContextUsesConnectionEndpointModes(t *testing.T) {
	service := &Service{}

	t.Run("连接形态优先于凭据形态", func(t *testing.T) {
		row := &ManualTestContextRow{
			viewAccountID:    "acc-conn-first",
			factAccountID:    "acc-conn-first",
			capabilityDomain: "chat",
			EndpointModes:    []string{"chat_json"},
		}
		credentials := accountscore.Credentials{
			"supported_endpoint_modes": []any{"embeddings_json"},
		}
		context := service.manualTestContextFromRow(row, credentials, nil)
		if len(context.SupportedEndpointModes) != 1 || context.SupportedEndpointModes[0] != "chat_json" {
			t.Fatalf("连接形态应优先：%v", context.SupportedEndpointModes)
		}
		if got := ManualTestKind(context); got != capabilitydomain.TestKindModel {
			t.Fatalf("testKind = %q, want %q", got, capabilitydomain.TestKindModel)
		}
	})

	t.Run("无连接形态回落凭据形态", func(t *testing.T) {
		row := &ManualTestContextRow{
			viewAccountID:    "acc-cred-fallback",
			factAccountID:    "acc-cred-fallback",
			capabilityDomain: "chat",
		}
		credentials := accountscore.Credentials{
			"supported_endpoint_modes": []any{"chat_sse"},
		}
		context := service.manualTestContextFromRow(row, credentials, nil)
		if len(context.SupportedEndpointModes) != 1 || context.SupportedEndpointModes[0] != "chat_sse" {
			t.Fatalf("应回落凭据形态：%v", context.SupportedEndpointModes)
		}
		if got := ManualTestKind(context); got != capabilitydomain.TestKindModel {
			t.Fatalf("testKind = %q, want %q", got, capabilitydomain.TestKindModel)
		}
	})

	t.Run("连接形态仅输入时连接测试", func(t *testing.T) {
		row := &ManualTestContextRow{
			viewAccountID:    "acc-embedding",
			factAccountID:    "acc-embedding",
			capabilityDomain: "chat",
			EndpointModes:    []string{"embeddings_json"},
		}
		context := service.manualTestContextFromRow(row, accountscore.Credentials{}, nil)
		if got := ManualTestKind(context); got != capabilitydomain.TestKindConnection {
			t.Fatalf("testKind = %q, want %q", got, capabilitydomain.TestKindConnection)
		}
	})

	t.Run("媒体域连接形态也恒连接测试", func(t *testing.T) {
		row := &ManualTestContextRow{
			viewAccountID:    "acc-image",
			factAccountID:    "acc-image",
			capabilityDomain: "image",
			EndpointModes:    []string{"chat_json"},
		}
		context := service.manualTestContextFromRow(row, accountscore.Credentials{}, nil)
		if got := ManualTestKind(context); got != capabilitydomain.TestKindConnection {
			t.Fatalf("testKind = %q, want %q", got, capabilitydomain.TestKindConnection)
		}
	})
}
