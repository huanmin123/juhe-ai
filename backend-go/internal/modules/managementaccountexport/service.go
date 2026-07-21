package managementaccountexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	ProtocolType    = "juhe-ai-account-import"
	ProtocolVersion = 1
	exportBatchSize = 20
)

var (
	ErrInvalidRequest = errors.New("账户导出参数无效")
	ErrNoAccounts     = errors.New("没有可导出的自有 AI 账户")
)

type Sort struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

type Filters struct {
	Sorts        []Sort   `json:"sorts,omitempty"`
	Keyword      string   `json:"keyword,omitempty"`
	ProviderCode string   `json:"providerCode,omitempty"`
	GroupID      string   `json:"groupId,omitempty"`
	TagIDs       []string `json:"tagIds,omitempty"`
	Type         string   `json:"type,omitempty"`
	Status       []string `json:"status,omitempty"`
	Schedulable  string   `json:"schedulable,omitempty"`
}

type Input struct {
	SystemAccountID string
	AccountIDs      []string
	Filters         *Filters
}

type Proxy struct {
	Ref         string `json:"ref"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
}

type Account struct {
	Ref                                 string           `json:"ref"`
	Name                                string           `json:"name"`
	ProviderCode                        string           `json:"providerCode"`
	ProviderProtocolProfileID           string           `json:"providerProtocolProfileId,omitempty"`
	Type                                string           `json:"type"`
	Status                              string           `json:"status"`
	GroupID                             string           `json:"groupId,omitempty"`
	GroupName                           string           `json:"groupName,omitempty"`
	ProxyRef                            string           `json:"proxyRef,omitempty"`
	ConcurrencyLimit                    int              `json:"concurrencyLimit,omitempty"`
	Priority                            int              `json:"priority,omitempty"`
	SuperPriorityEnabled                bool             `json:"superPriorityEnabled,omitempty"`
	FallbackEnabled                     bool             `json:"fallbackEnabled,omitempty"`
	SupportedModels                     []string         `json:"supportedModels,omitempty"`
	HealthCheckModel                    string           `json:"healthCheckModel,omitempty"`
	HealthCheckEndpointMode             string           `json:"healthCheckEndpointMode"`
	TemporaryUnavailableContinuousProbe *bool            `json:"temporaryUnavailableContinuousProbeEnabled,omitempty"`
	ModelMappings                       []map[string]any `json:"modelMappings,omitempty"`
	Tags                                []string         `json:"tags,omitempty"`
	AccountExpiresAt                    string           `json:"accountExpiresAt,omitempty"`
	AvailabilitySchedule                map[string]any   `json:"availabilitySchedule,omitempty"`
	Credentials                         map[string]any   `json:"credentials"`
	Notes                               string           `json:"notes,omitempty"`
}

type Document struct {
	Type     string    `json:"type"`
	Version  int       `json:"version"`
	Proxies  []Proxy   `json:"proxies,omitempty"`
	Accounts []Account `json:"accounts"`
}

type Summary struct {
	Accounts        int  `json:"accounts"`
	Proxies         int  `json:"proxies"`
	SkippedAccounts int  `json:"skippedAccounts"`
	MatchedAccounts int  `json:"matchedAccounts,omitempty"`
	Truncated       bool `json:"truncated,omitempty"`
}

type Result struct {
	Document Document `json:"document"`
	Summary  Summary  `json:"summary"`
}

type CredentialCodec interface {
	DecryptJSON(value string) (map[string]any, error)
}

type ServiceOptions struct {
	Reader          port.ManagementAccountExportReader
	CredentialCodec CredentialCodec
}

type Service struct {
	reader port.ManagementAccountExportReader
	codec  CredentialCodec
}

func NewService(opts ServiceOptions) *Service {
	return &Service{reader: opts.Reader, codec: opts.CredentialCodec}
}

func (s *Service) Write(ctx context.Context, writer io.Writer, input Input) (Summary, error) {
	if writer == nil || s.reader == nil || s.codec == nil {
		return Summary{}, fmt.Errorf("账户导出服务未配置")
	}
	normalized, err := normalizeInput(input)
	if err != nil {
		return Summary{}, err
	}
	first, err := s.reader.ListManagementAccountExportBatch(ctx, exportStoreInput(normalized), "", exportBatchSize)
	if err != nil {
		return Summary{}, err
	}
	if len(first.Items) == 0 {
		return Summary{}, ErrNoAccounts
	}

	matched := first.Matched
	encoder := json.NewEncoder(writer)
	if _, err := io.WriteString(writer, `{"data":{"document":{"type":"`+ProtocolType+`","version":1,"accounts":[`); err != nil {
		return Summary{}, fmt.Errorf("写入账户导出结果失败: %w", err)
	}
	proxyRefs := map[string]string{}
	proxies := make([]Proxy, 0)
	accounts := 0
	skipped := 0
	afterID := ""
	writePage := func(page port.ManagementAccountExportPage) error {
		for _, item := range page.Items {
			account, err := s.convertAccount(item, proxyRefs, &proxies)
			if err != nil {
				return err
			}
			if accounts > 0 {
				if _, err := io.WriteString(writer, ","); err != nil {
					return err
				}
			}
			if err := encoder.Encode(account); err != nil {
				return err
			}
			accounts++
			afterID = item.ID
		}
		return nil
	}
	if err := writePage(first); err != nil {
		return Summary{}, fmt.Errorf("写入账户导出结果失败: %w", err)
	}
	for first.HasMore && accounts < port.ManagementAccountExportMaxAccounts {
		page, err := s.reader.ListManagementAccountExportBatch(ctx, exportStoreInput(normalized), afterID, exportBatchSize)
		if err != nil {
			return Summary{}, err
		}
		if len(page.Items) == 0 || page.NextID == afterID {
			break
		}
		if err := writePage(page); err != nil {
			return Summary{}, fmt.Errorf("写入账户导出结果失败: %w", err)
		}
		first = page
	}
	if input.Filters == nil {
		skipped = len(normalized.AccountIDs) - accounts
	}
	if matched == 0 {
		matched = len(normalized.AccountIDs)
	}
	truncated := input.Filters != nil && matched > port.ManagementAccountExportMaxAccounts
	if _, err := io.WriteString(writer, `],"proxies":`); err != nil {
		return Summary{}, fmt.Errorf("写入账户导出结果失败: %w", err)
	}
	if err := encoder.Encode(proxies); err != nil {
		return Summary{}, fmt.Errorf("写入账户导出结果失败: %w", err)
	}
	summary := Summary{Accounts: accounts, Proxies: len(proxies), SkippedAccounts: skipped, MatchedAccounts: matched, Truncated: truncated}
	if _, err := io.WriteString(writer, `},"summary":`); err != nil {
		return Summary{}, fmt.Errorf("写入账户导出结果失败: %w", err)
	}
	if err := encoder.Encode(summary); err != nil {
		return Summary{}, fmt.Errorf("写入账户导出结果失败: %w", err)
	}
	if _, err := io.WriteString(writer, `}}`); err != nil {
		return Summary{}, fmt.Errorf("写入账户导出结果失败: %w", err)
	}
	return summary, nil
}

func exportStoreInput(input Input) port.ManagementAccountExportInput {
	filter := port.ManagementAccountExportFilter{}
	if input.Filters != nil {
		filter = port.ManagementAccountExportFilter{Keyword: strings.TrimSpace(input.Filters.Keyword), ProviderCode: strings.TrimSpace(input.Filters.ProviderCode), GroupID: strings.TrimSpace(input.Filters.GroupID), TagIDs: input.Filters.TagIDs, Type: strings.TrimSpace(input.Filters.Type), Statuses: input.Filters.Status, Schedulable: input.Filters.Schedulable}
	}
	return port.ManagementAccountExportInput{SystemAccountID: input.SystemAccountID, AccountIDs: input.AccountIDs, Filter: filter}
}

func (s *Service) Export(ctx context.Context, input Input) (Result, error) {
	if s.reader == nil || s.codec == nil {
		return Result{}, fmt.Errorf("账户导出服务未配置")
	}
	normalized, err := normalizeInput(input)
	if err != nil {
		return Result{}, err
	}
	items, matched, truncated, err := s.load(ctx, normalized)
	if err != nil {
		return Result{}, err
	}
	if len(items) == 0 {
		return Result{}, ErrNoAccounts
	}
	proxies := make([]Proxy, 0)
	proxyRefs := map[string]string{}
	accounts := make([]Account, 0, len(items))
	for _, item := range items {
		account, err := s.convertAccount(item, proxyRefs, &proxies)
		if err != nil {
			return Result{}, err
		}
		accounts = append(accounts, account)
	}
	skipped := 0
	if input.Filters == nil {
		skipped = len(normalized.AccountIDs) - len(accounts)
	}
	result := Result{
		Document: Document{Type: ProtocolType, Version: ProtocolVersion, Proxies: proxies, Accounts: accounts},
		Summary:  Summary{Accounts: len(accounts), Proxies: len(proxies), SkippedAccounts: skipped, MatchedAccounts: matched, Truncated: truncated},
	}
	return result, nil
}

func normalizeInput(input Input) (Input, error) {
	ids := uniqueStrings(input.AccountIDs)
	if len(ids) > port.ManagementAccountExportMaxAccounts {
		return Input{}, fmt.Errorf("单次最多导出 %d 个 AI 账户", port.ManagementAccountExportMaxAccounts)
	}
	if len(ids) == 0 && input.Filters == nil {
		return Input{}, ErrInvalidRequest
	}
	if len(ids) > 0 && input.Filters != nil {
		return Input{}, ErrInvalidRequest
	}
	input.AccountIDs = ids
	input.SystemAccountID = strings.TrimSpace(input.SystemAccountID)
	return input, nil
}

func (s *Service) load(ctx context.Context, input Input) ([]port.ManagementAccountExportAccount, int, bool, error) {
	filter := port.ManagementAccountExportFilter{}
	if input.Filters != nil {
		filter = port.ManagementAccountExportFilter{Keyword: strings.TrimSpace(input.Filters.Keyword), ProviderCode: strings.TrimSpace(input.Filters.ProviderCode), GroupID: strings.TrimSpace(input.Filters.GroupID), TagIDs: input.Filters.TagIDs, Type: strings.TrimSpace(input.Filters.Type), Statuses: input.Filters.Status, Schedulable: input.Filters.Schedulable}
	}
	storeInput := port.ManagementAccountExportInput{SystemAccountID: input.SystemAccountID, AccountIDs: input.AccountIDs, Filter: filter}
	items := make([]port.ManagementAccountExportAccount, 0, port.ManagementAccountExportMaxAccounts)
	afterID := ""
	matched := 0
	for len(items) < port.ManagementAccountExportMaxAccounts {
		page, err := s.reader.ListManagementAccountExportBatch(ctx, storeInput, afterID, min(exportBatchSize, port.ManagementAccountExportMaxAccounts-len(items)))
		if err != nil {
			return nil, 0, false, err
		}
		if afterID == "" {
			matched = page.Matched
		}
		items = append(items, page.Items...)
		if len(page.Items) == 0 || !page.HasMore {
			break
		}
		afterID = page.NextID
	}
	if input.Filters == nil {
		matched = len(input.AccountIDs)
		byID := make(map[string]port.ManagementAccountExportAccount, len(items))
		for _, item := range items {
			byID[item.ID] = item
		}
		ordered := make([]port.ManagementAccountExportAccount, 0, len(items))
		for _, id := range input.AccountIDs {
			if item, ok := byID[id]; ok {
				ordered = append(ordered, item)
			}
		}
		items = ordered
	}
	return items, matched, input.Filters != nil && matched > port.ManagementAccountExportMaxAccounts, nil
}

func (s *Service) convertAccount(item port.ManagementAccountExportAccount, proxyRefs map[string]string, proxies *[]Proxy) (Account, error) {
	credentials, err := s.codec.DecryptJSON(item.CredentialsEncrypted)
	if err != nil {
		return Account{}, fmt.Errorf("解密账户凭据失败: %w", err)
	}
	credentials = exportCredentials(item.Type, credentials)
	status := exportStatus(item.Status, item.Schedulable)
	account := Account{Ref: item.ID, Name: item.Name, ProviderCode: item.ProviderCode, ProviderProtocolProfileID: item.ProviderProtocolProfileID, Type: item.Type, Status: status, GroupID: item.GroupID, GroupName: item.GroupName, ConcurrencyLimit: item.ConcurrencyLimit, Priority: item.Priority, HealthCheckModel: item.HealthCheckModel, HealthCheckEndpointMode: item.HealthCheckEndpointMode, Credentials: credentials, AccountExpiresAt: item.AccountExpiresAt, Notes: item.Notes}
	if account.GroupName != "" {
		account.GroupID = ""
	}
	if status == "active" {
		account.SuperPriorityEnabled = item.SuperPriorityEnabled
		account.FallbackEnabled = item.FallbackEnabled
	}
	if item.TemporaryUnavailableContinuousProbe == false {
		value := false
		account.TemporaryUnavailableContinuousProbe = &value
	}
	if err := decodeExportJSON(item.SupportedModelsJSON, &account.SupportedModels); err != nil {
		return Account{}, err
	}
	if err := decodeExportJSON(item.ModelMappingsJSON, &account.ModelMappings); err != nil {
		return Account{}, err
	}
	if err := decodeExportJSON(item.TagsJSON, &account.Tags); err != nil {
		return Account{}, err
	}
	if err := decodeExportJSON(item.AvailabilityScheduleJSON, &account.AvailabilitySchedule); err != nil {
		return Account{}, err
	}
	if item.ProxyProfileID != "" && item.ProxyEnabled {
		if ref, ok := proxyRefs[item.ProxyProfileID]; ok {
			account.ProxyRef = ref
		} else {
			ref := "proxy-" + item.ProxyProfileID
			proxy := Proxy{Ref: ref, Name: item.ProxyName, Type: item.ProxyType, Host: item.ProxyHost, Port: item.ProxyPort, Username: item.ProxyUsername, Description: item.ProxyDescription, Enabled: true}
			if item.ProxyPasswordEncrypted != "" {
				password, err := s.codec.DecryptJSON(item.ProxyPasswordEncrypted)
				if err == nil {
					if value, ok := password["password"].(string); ok {
						proxy.Password = value
					}
				}
			}
			proxyRefs[item.ProxyProfileID] = ref
			*proxies = append(*proxies, proxy)
			account.ProxyRef = ref
		}
	}
	return account, nil
}

func exportCredentials(accountType string, input map[string]any) map[string]any {
	keys := []string{"api_key", "api_keys", "api_key_strategy", "api_key_weights", "base_url", "supported_endpoint_modes", "service_tier_override", "reasoning_effort_override", "error_handling_rules", "response_inspection_rules"}
	if accountType == "oauth" {
		keys = []string{"refresh_token", "access_token", "expires_at", "client_id", "id_token", "base_url", "supported_endpoint_modes", "service_tier_override", "reasoning_effort_override", "account_id", "email", "chatgpt_user_id", "plan_type", "error_handling_rules", "response_inspection_rules"}
	}
	if accountType != "api_key" && accountType != "oauth" {
		keys = nil
		for key := range input {
			keys = append(keys, key)
		}
	}
	output := map[string]any{}
	for _, key := range keys {
		if value, ok := input[key]; ok {
			output[key] = value
		}
	}
	return output
}

func exportStatus(status string, schedulable bool) string {
	if status == "pending_test" {
		return status
	}
	if status == "active" && schedulable {
		return "active"
	}
	return "disabled"
}
func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	output := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			if _, ok := seen[value]; !ok {
				seen[value] = struct{}{}
				output = append(output, value)
			}
		}
	}
	return output
}
func decodeExportJSON(raw string, target any) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("解析账户导出字段失败: %w", err)
	}
	return nil
}
func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
