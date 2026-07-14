package publicaccounts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
)

const accountRequestOverridesModelsRequiredMessage = "请求覆盖要求账户至少配置一个支持模型"

type accountRequestOverrides struct {
	serviceTier     string
	reasoningEffort string
}

func (o accountRequestOverrides) configured() bool {
	return o.serviceTier != "" || o.reasoningEffort != ""
}

func preflightAccountRequestOverrides(
	credentials map[string]any,
) (accountRequestOverrides, error) {
	serviceTier, err := readAccountRequestOverride(
		credentials,
		"service_tier_override",
	)
	if err != nil {
		return accountRequestOverrides{}, err
	}
	reasoningEffort, err := readAccountRequestOverride(
		credentials,
		"reasoning_effort_override",
	)
	if err != nil {
		return accountRequestOverrides{}, err
	}
	return accountRequestOverrides{
		serviceTier:     serviceTier,
		reasoningEffort: reasoningEffort,
	}, nil
}

func (s *Service) validateAccountRequestOverridesInProviderCatalog(
	ctx context.Context,
	systemAccountID string,
	providerCode string,
	accountType string,
	overrides accountRequestOverrides,
	models []string,
) error {
	if !overrides.configured() {
		return nil
	}

	providerCode = strings.TrimSpace(providerCode)
	if err := validateAccountRequestOverrideProvider(providerCode, overrides); err != nil {
		return err
	}
	if s.providerModels == nil {
		return errors.New(providerModelsRequiredMessage)
	}
	catalog, err := s.providerModels.Models(ctx, managementprovidermodels.ModelListInput{
		ProviderCode:    providerCode,
		SystemAccountID: strings.TrimSpace(systemAccountID),
		IncludeInactive: false,
		IncludeUnpriced: true,
	})
	if err != nil {
		return err
	}
	models = uniqueAccountRequestOverrideModels(models)
	if len(models) == 0 {
		return fmt.Errorf(
			"%w: %s",
			ErrInvalidSupportedModels,
			accountRequestOverridesModelsRequiredMessage,
		)
	}

	catalogByModel := make(map[string]managementprovidermodels.ModelCatalogItem, len(catalog))
	for _, item := range catalog {
		model := strings.TrimSpace(item.Model)
		if model != "" {
			catalogByModel[model] = item
		}
	}
	modelItems := make([]managementprovidermodels.ModelCatalogItem, 0, len(models))
	missingModels := make([]string, 0)
	for _, model := range models {
		item, ok := catalogByModel[model]
		if !ok {
			missingModels = append(missingModels, model)
			continue
		}
		modelItems = append(modelItems, item)
	}
	if len(missingModels) > 0 {
		return fmt.Errorf(
			"%w: 模型目录缺少账户支持模型：%s",
			ErrInvalidSupportedModels,
			strings.Join(missingModels, "、"),
		)
	}

	if overrides.serviceTier != "" {
		if providerCode == "gpt" && strings.TrimSpace(accountType) == "oauth" && overrides.serviceTier == "flex" {
			return fmt.Errorf(
				"%w: OpenAI OAuth 账户不支持 Flex 服务等级覆盖",
				ErrInvalidCredentials,
			)
		}
		for _, item := range modelItems {
			supported := overrides.serviceTier == "default" && len(item.SupportedServiceTiers) > 0
			if overrides.serviceTier != "default" {
				supported = stringListContains(item.SupportedServiceTiers, overrides.serviceTier)
			}
			if !supported {
				label := "服务等级覆盖"
				if overrides.serviceTier != "default" {
					label = "服务等级 " + overrides.serviceTier
				}
				return fmt.Errorf("%w: 账户全部支持模型必须共同支持%s", ErrInvalidSupportedModels, label)
			}
		}
	}
	if overrides.reasoningEffort != "" {
		for _, item := range modelItems {
			if !stringListContains(item.SupportedReasoningEfforts, overrides.reasoningEffort) {
				return fmt.Errorf(
					"%w: 账户全部支持模型必须共同支持思考级别 %s",
					ErrInvalidSupportedModels,
					overrides.reasoningEffort,
				)
			}
		}
	}
	return nil
}

func readAccountRequestOverride(
	credentials map[string]any,
	key string,
) (string, error) {
	raw, exists := credentials[key]
	if !exists || raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if ok && value == "" {
		return "", nil
	}
	if !ok || !validAccountRequestOverrideToken(value) {
		return "", fmt.Errorf("%w: 账户请求覆盖字段 %s 无效", ErrInvalidCredentials, key)
	}
	return value, nil
}

func validateAccountRequestOverrideProvider(providerCode string, overrides accountRequestOverrides) error {
	switch providerCode {
	case "gpt", "openai", "anthropic", "gemini":
	default:
		return fmt.Errorf(
			"%w: 供应商 %s 没有可确认的账户请求覆盖 wire 映射",
			ErrInvalidCredentials,
			providerCode,
		)
	}
	if providerCode == "gemini" && overrides.serviceTier != "" {
		return fmt.Errorf(
			"%w: Gemini 原生请求没有可确认的服务等级 wire 字段，不能保存账户服务等级覆盖",
			ErrInvalidCredentials,
		)
	}
	return nil
}

func validAccountRequestOverrideToken(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') {
			continue
		}
		if index > 0 && (char == '.' || char == '_' || char == '-') {
			continue
		}
		return false
	}
	return true
}

func uniqueAccountRequestOverrideModels(values []string) []string {
	models := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		model := strings.TrimSpace(value)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	return models
}
