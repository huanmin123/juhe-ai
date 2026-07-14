package publicaccounts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/modules/managementprovidermodels"
)

const gptRequestOverridesModelsRequiredMessage = "GPT 请求覆盖要求账户至少配置一个支持模型"

type gptRequestOverrides struct {
	serviceTier     string
	reasoningEffort string
}

func (o gptRequestOverrides) configured() bool {
	return o.serviceTier != "" || o.reasoningEffort != ""
}

func preflightGPTRequestOverrides(
	providerCode string,
	credentials map[string]any,
) (gptRequestOverrides, error) {
	serviceTier, err := readGPTRequestOverride(
		credentials,
		"service_tier_override",
		isGPTServiceTierOverride,
	)
	if err != nil {
		return gptRequestOverrides{}, err
	}
	reasoningEffort, err := readGPTRequestOverride(
		credentials,
		"reasoning_effort_override",
		isGPTReasoningEffortOverride,
	)
	if err != nil {
		return gptRequestOverrides{}, err
	}
	overrides := gptRequestOverrides{
		serviceTier:     serviceTier,
		reasoningEffort: reasoningEffort,
	}
	if !overrides.configured() {
		return overrides, nil
	}
	if strings.TrimSpace(providerCode) != "gpt" {
		return gptRequestOverrides{}, fmt.Errorf(
			"%w: 只有 GPT 账户支持服务等级和思考级别覆盖",
			ErrInvalidCredentials,
		)
	}
	return overrides, nil
}

func (s *Service) validateGPTRequestOverridesInProviderCatalog(
	ctx context.Context,
	systemAccountID string,
	providerCode string,
	overrides gptRequestOverrides,
	models []string,
) error {
	if !overrides.configured() {
		return nil
	}

	providerCode = strings.TrimSpace(providerCode)
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
	models = uniqueGPTRequestOverrideModels(models)
	if len(models) == 0 {
		return fmt.Errorf(
			"%w: %s",
			ErrInvalidSupportedModels,
			gptRequestOverridesModelsRequiredMessage,
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

func readGPTRequestOverride(
	credentials map[string]any,
	key string,
	valid func(string) bool,
) (string, error) {
	raw, exists := credentials[key]
	if !exists || raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if ok && value == "" {
		return "", nil
	}
	if !ok || !valid(value) {
		return "", fmt.Errorf("%w: GPT 账户请求覆盖字段 %s 无效", ErrInvalidCredentials, key)
	}
	return value, nil
}

func uniqueGPTRequestOverrideModels(values []string) []string {
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

func isGPTServiceTierOverride(value string) bool {
	switch value {
	case "default", "priority", "flex":
		return true
	default:
		return false
	}
}

func isGPTReasoningEffortOverride(value string) bool {
	switch value {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}
