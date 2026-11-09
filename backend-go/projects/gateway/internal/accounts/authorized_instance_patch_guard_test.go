package accounts

// SEC-003 回归：授权实例行的来源侧字段在通用 PATCH 入口即被拒绝，使用侧
// 字段保持可写。此函数是 Patch 事务内的唯一判定点（patch.go 在 revision
// CAS 之后调用），纯函数测试覆盖全部字段分组，不需要数据库。

import "testing"

func TestAuthorizedInstanceSourceFieldMessage(t *testing.T) {
	text := "x"
	boolean := true
	models := []string{"gpt-6.1-sol"}
	mappings := []ModelMapping{{}}

	sourceCases := []struct {
		name  string
		input PatchInput
	}{
		{"credentials", PatchInput{Credentials: Credentials{"api_key": "sk"}, CredentialsPresent: true}},
		{"credentialsPatch", PatchInput{Credentials: Credentials{"api_key": nil}, CredentialsPresent: true, CredentialsPatch: true}},
		{"connections", PatchInput{Connections: []ConnectionInput{{}}, ConnectionsPresent: true}},
		{"supportedModels", PatchInput{SupportedModels: models, SupportedModelsPresent: true}},
		{"modelMappings", PatchInput{ModelMappings: mappings, ModelMappingsPresent: true}},
		{"healthCheckModel", PatchInput{HealthCheckModel: &text}},
		{"healthCheckEndpointMode", PatchInput{HealthCheckEndpointMode: &text}},
		{"healthCheckConnectionPresetCode", PatchInput{HealthCheckConnectionPresetCode: &text}},
		{"proxyProfileId", PatchInput{ProxyProfileID: &text, ProxyProfileIDPresent: true}},
		{"proxyPoolId", PatchInput{ProxyPoolID: &text, ProxyPoolIDPresent: true}},
		{"accountExpiresAt", PatchInput{AccountExpiresAt: &text, AccountExpiresAtPresent: true}},
		{"availabilitySchedule", PatchInput{AvailabilitySchedule: map[string]any{}, AvailabilitySchedulePresent: true}},
		{"balanceQueryEnabled", PatchInput{BalanceQueryEnabled: &boolean}},
		{"balanceQueryConfig", PatchInput{BalanceQueryConfigCanonical: &text, BalanceQueryConfigPresent: true}},
		{"temporaryUnavailableContinuousProbeEnabled", PatchInput{TemporaryUnavailableContinuousProbeEnabled: &boolean}},
	}
	for _, testCase := range sourceCases {
		t.Run(testCase.name, func(t *testing.T) {
			if message := authorizedInstanceSourceFieldMessage(testCase.input); message == "" {
				t.Fatalf("%s must be rejected on an authorized instance", testCase.name)
			}
		})
	}

	// 使用侧字段：名称/备注/状态/标签/清失败态/本地分组绑定/域校验入参；
	// expeditedRecoveryEnabled（特供）按《AI账户特供快速恢复通道设计》
	// §3.11/§4 是实例本地字段、名额计入被授权方，必须保持可写。
	useSide := PatchInput{
		Name:                     &text,
		Notes:                    &text,
		Status:                   &text,
		Tags:                     []string{"tag"},
		TagsPresent:              true,
		ClearFailureState:        true,
		GroupID:                  &text,
		GroupIDPresent:           true,
		CapabilityDomain:         &text,
		ExpeditedRecoveryEnabled: &boolean,
		ExpectedConfigRevision:   1,
	}
	if message := authorizedInstanceSourceFieldMessage(useSide); message != "" {
		t.Fatalf("use-side fields must stay writable, got %q", message)
	}
}
