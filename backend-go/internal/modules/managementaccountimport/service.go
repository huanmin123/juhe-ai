package managementaccountimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	ProtocolType    = "juhe-ai-account-import"
	ProtocolVersion = 1
	MaxAccounts     = 50
	MaxProxies      = 20
)

var ErrInvalid = errors.New("账户导入参数无效")

type CredentialCodec interface {
	EncryptJSON(map[string]any) (string, error)
}

type Options struct {
	Store           port.ManagementAccountImporter
	CredentialCodec CredentialCodec
	Now             func() time.Time
}

type Service struct {
	store port.ManagementAccountImporter
	codec CredentialCodec
	now   func() time.Time
}

type OptionsInput struct {
	CreateMissingGroups  *bool `json:"createMissingGroups,omitempty"`
	CreateMissingProxies *bool `json:"createMissingProxies,omitempty"`
	SkipDuplicates       *bool `json:"skipDuplicates,omitempty"`
}

type ImportSummaryCounts struct {
	Total  int `json:"total"`
	Create int `json:"create"`
	Reuse  int `json:"reuse,omitempty"`
	Skip   int `json:"skip"`
	Failed int `json:"failed"`
}

type Summary struct {
	Accounts ImportSummaryCounts `json:"accounts"`
	Proxies  ImportSummaryCounts `json:"proxies"`
	Groups   ImportSummaryCounts `json:"groups"`
}

type Item struct {
	Index     int      `json:"index"`
	Ref       string   `json:"ref,omitempty"`
	Name      string   `json:"name,omitempty"`
	Action    string   `json:"action"`
	Messages  []string `json:"messages"`
	Warnings  []string `json:"warnings"`
	AccountID string   `json:"accountId,omitempty"`
	ProxyID   string   `json:"proxyProfileId,omitempty"`
}

type Result struct {
	Type      string   `json:"type"`
	Version   int      `json:"version"`
	Mode      string   `json:"mode"`
	CanImport bool     `json:"canImport"`
	Imported  int      `json:"imported"`
	Summary   Summary  `json:"summary"`
	Accounts  []Item   `json:"accounts"`
	Proxies   []Item   `json:"proxies"`
	Messages  []string `json:"messages"`
}

type document struct {
	Type     string            `json:"type"`
	Version  int               `json:"version"`
	Proxies  []proxyDocument   `json:"proxies,omitempty"`
	Accounts []accountDocument `json:"accounts"`
}

type proxyDocument struct {
	Ref         string `json:"ref"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	Description string `json:"description,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type accountDocument struct {
	Ref                                 string           `json:"ref,omitempty"`
	Name                                string           `json:"name"`
	ProviderCode                        string           `json:"providerCode"`
	ProviderProtocolProfileID           string           `json:"providerProtocolProfileId"`
	Type                                string           `json:"type"`
	Status                              string           `json:"status"`
	GroupID                             string           `json:"groupId,omitempty"`
	GroupName                           string           `json:"groupName,omitempty"`
	ProxyRef                            string           `json:"proxyRef,omitempty"`
	ProxyProfileID                      string           `json:"proxyProfileId,omitempty"`
	ConcurrencyLimit                    int              `json:"concurrencyLimit,omitempty"`
	Priority                            int              `json:"priority,omitempty"`
	SuperPriorityEnabled                bool             `json:"superPriorityEnabled,omitempty"`
	FallbackEnabled                     bool             `json:"fallbackEnabled,omitempty"`
	SupportedModels                     []string         `json:"supportedModels,omitempty"`
	HealthCheckModel                    string           `json:"healthCheckModel,omitempty"`
	HealthCheckEndpointMode             string           `json:"healthCheckEndpointMode,omitempty"`
	TemporaryUnavailableContinuousProbe *bool            `json:"temporaryUnavailableContinuousProbeEnabled,omitempty"`
	ModelMappings                       []map[string]any `json:"modelMappings,omitempty"`
	Tags                                []string         `json:"tags,omitempty"`
	AccountExpiresAt                    string           `json:"accountExpiresAt,omitempty"`
	AvailabilitySchedule                map[string]any   `json:"availabilitySchedule,omitempty"`
	Credentials                         map[string]any   `json:"credentials"`
	Notes                               string           `json:"notes,omitempty"`
}

func NewService(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opts.Store, codec: opts.CredentialCodec, now: now}
}

func (s *Service) Preview(ctx context.Context, raw []byte, options OptionsInput) (Result, error) {
	input, result, err := s.prepare(raw, options, "")
	_ = ctx
	_ = input
	return result, err
}

func (s *Service) Confirm(ctx context.Context, raw []byte, options OptionsInput, systemAccountID string) (Result, error) {
	if s.store == nil || s.codec == nil || strings.TrimSpace(systemAccountID) == "" {
		return Result{}, ErrInvalid
	}
	input, result, err := s.prepare(raw, options, strings.TrimSpace(systemAccountID))
	result.Mode = "import"
	if err != nil || !result.CanImport {
		return result, err
	}
	stored, err := s.store.Import(ctx, input)
	if err != nil {
		return Result{}, fmt.Errorf("导入账户事务失败: %w", err)
	}
	result.Imported = stored.Imported
	result.Summary.Accounts.Create = stored.Imported
	result.Summary.Accounts.Skip = stored.Skipped
	result.CanImport = false
	return result, nil
}

func (s *Service) prepare(raw []byte, options OptionsInput, owner string) (port.ManagementAccountImportInput, Result, error) {
	result := Result{Type: ProtocolType, Version: ProtocolVersion, Mode: "preview", Accounts: []Item{}, Proxies: []Item{}, Messages: []string{}}
	var doc document
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return port.ManagementAccountImportInput{}, result, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return port.ManagementAccountImportInput{}, result, ErrInvalid
	}
	if doc.Type != ProtocolType || doc.Version != ProtocolVersion {
		return port.ManagementAccountImportInput{}, result, fmt.Errorf("%w: type 必须是 %s 且 version 必须是 %d", ErrInvalid, ProtocolType, ProtocolVersion)
	}
	if len(doc.Accounts) == 0 || len(doc.Accounts) > MaxAccounts || len(doc.Proxies) > MaxProxies {
		return port.ManagementAccountImportInput{}, result, ErrInvalid
	}

	now := s.now().UTC()
	input := port.ManagementAccountImportInput{SystemAccountID: owner, Options: normalizedOptions(options), Now: now}
	proxyRefs := map[string]struct{}{}
	for index, proxy := range doc.Proxies {
		item := Item{Index: index + 1, Ref: strings.TrimSpace(proxy.Ref), Name: strings.TrimSpace(proxy.Name), Action: "create", Messages: []string{}, Warnings: []string{}}
		if item.Ref == "" || item.Name == "" || strings.TrimSpace(proxy.Type) == "" || strings.TrimSpace(proxy.Host) == "" || proxy.Port < 1 || proxy.Port > 65535 {
			item.Action = "failed"
			item.Messages = append(item.Messages, "代理配置无效")
		}
		if _, exists := proxyRefs[item.Ref]; exists {
			item.Action = "failed"
			item.Messages = append(item.Messages, "代理 ref 重复")
		}
		proxyRefs[item.Ref] = struct{}{}
		passwordEncrypted := ""
		if proxy.Password != "" && s.codec != nil {
			passwordEncrypted, _ = s.codec.EncryptJSON(map[string]any{"password": proxy.Password})
		}
		enabled := true
		if proxy.Enabled != nil {
			enabled = *proxy.Enabled
		}
		proxyID := "proxy_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		input.Proxies = append(input.Proxies, port.ManagementAccountImportProxy{Ref: item.Ref, ID: proxyID, Name: item.Name, Type: strings.TrimSpace(proxy.Type), Host: strings.TrimSpace(proxy.Host), Port: proxy.Port, Username: strings.TrimSpace(proxy.Username), PasswordEncrypted: passwordEncrypted, Description: strings.TrimSpace(proxy.Description), Enabled: enabled})
		item.ProxyID = proxyID
		result.Proxies = append(result.Proxies, item)
	}

	for index, account := range doc.Accounts {
		item := Item{Index: index + 1, Ref: strings.TrimSpace(account.Ref), Name: strings.TrimSpace(account.Name), Action: "create", Messages: []string{}, Warnings: []string{}}
		if item.Name == "" || strings.TrimSpace(account.ProviderCode) == "" || strings.TrimSpace(account.ProviderProtocolProfileID) == "" || strings.TrimSpace(account.Type) == "" || strings.TrimSpace(account.Status) == "" || len(account.Credentials) == 0 {
			item.Action = "failed"
			item.Messages = append(item.Messages, "账户基础字段或凭据无效")
		}
		if account.SuperPriorityEnabled && account.FallbackEnabled {
			item.Action = "failed"
			item.Messages = append(item.Messages, "超级优先和降级备用不能同时启用")
		}
		if account.ProxyRef != "" {
			if _, ok := proxyRefs[strings.TrimSpace(account.ProxyRef)]; !ok && strings.TrimSpace(account.ProxyProfileID) == "" {
				item.Action = "failed"
				item.Messages = append(item.Messages, "账户引用的代理不存在")
			}
		}
		if len(account.ModelMappings) > 0 || len(account.Tags) > 0 {
			item.Warnings = append(item.Warnings, "第一轮暂未写入 modelMappings 和 tags")
		}
		encrypted := ""
		if s.codec != nil && len(account.Credentials) > 0 {
			var encryptErr error
			encrypted, encryptErr = s.codec.EncryptJSON(account.Credentials)
			if encryptErr != nil {
				return port.ManagementAccountImportInput{}, result, fmt.Errorf("加密第 %d 个账户凭据: %w", index+1, encryptErr)
			}
		}
		credentialJSON, _ := json.Marshal(account.Credentials)
		fingerprint := sha256.Sum256(credentialJSON)
		concurrency := account.ConcurrencyLimit
		if concurrency == 0 {
			concurrency = 20
		}
		endpoint := strings.TrimSpace(account.HealthCheckEndpointMode)
		if endpoint == "" {
			endpoint = defaultEndpoint(account.ProviderCode)
		}
		continuousProbe := true
		if account.TemporaryUnavailableContinuousProbe != nil {
			continuousProbe = *account.TemporaryUnavailableContinuousProbe
		}
		var expires *time.Time
		if strings.TrimSpace(account.AccountExpiresAt) != "" {
			value, parseErr := time.Parse(time.RFC3339, account.AccountExpiresAt)
			if parseErr != nil {
				item.Action = "failed"
				item.Messages = append(item.Messages, "accountExpiresAt 无效")
			} else {
				expires = &value
			}
		}
		var schedule *string
		if account.AvailabilitySchedule != nil {
			encoded, _ := json.Marshal(account.AvailabilitySchedule)
			value := string(encoded)
			schedule = &value
		}
		accountID := "acct_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		input.Accounts = append(input.Accounts, port.ManagementAccountImportAccount{Index: index + 1, Ref: item.Ref, ID: accountID, Name: item.Name, ProviderCode: strings.TrimSpace(account.ProviderCode), ProviderProtocolProfileID: strings.TrimSpace(account.ProviderProtocolProfileID), Type: strings.TrimSpace(account.Type), Status: strings.TrimSpace(account.Status), CredentialsEncrypted: encrypted, CredentialFingerprint: hex.EncodeToString(fingerprint[:]), GroupID: strings.TrimSpace(account.GroupID), GroupName: strings.TrimSpace(account.GroupName), ProxyRef: strings.TrimSpace(account.ProxyRef), ProxyProfileID: strings.TrimSpace(account.ProxyProfileID), ConcurrencyLimit: concurrency, Priority: account.Priority, SuperPriorityEnabled: account.SuperPriorityEnabled, FallbackEnabled: account.FallbackEnabled, SupportedModels: normalizedStrings(account.SupportedModels), HealthCheckModel: strings.TrimSpace(account.HealthCheckModel), HealthCheckEndpointMode: endpoint, TemporaryUnavailableContinuousProbe: continuousProbe, AccountExpiresAt: expires, AvailabilityScheduleJSON: schedule, Notes: optionalText(account.Notes)})
		item.AccountID = accountID
		result.Accounts = append(result.Accounts, item)
	}

	result.Summary.Accounts.Total = len(result.Accounts)
	result.Summary.Proxies.Total = len(result.Proxies)
	for _, item := range append(append([]Item{}, result.Accounts...), result.Proxies...) {
		if item.Action == "failed" {
			if item.AccountID != "" {
				result.Summary.Accounts.Failed++
			} else {
				result.Summary.Proxies.Failed++
			}
		}
	}
	result.Summary.Accounts.Create = len(result.Accounts) - result.Summary.Accounts.Failed
	result.Summary.Proxies.Create = len(result.Proxies) - result.Summary.Proxies.Failed
	result.CanImport = result.Summary.Accounts.Failed == 0 && result.Summary.Proxies.Failed == 0 && result.Summary.Accounts.Create > 0
	return input, result, nil
}

func normalizedOptions(input OptionsInput) port.ManagementAccountImportOptions {
	return port.ManagementAccountImportOptions{CreateMissingGroups: input.CreateMissingGroups == nil || *input.CreateMissingGroups, CreateMissingProxies: input.CreateMissingProxies == nil || *input.CreateMissingProxies, SkipDuplicates: input.SkipDuplicates == nil || *input.SkipDuplicates}
}

func normalizedStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func optionalText(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func defaultEndpoint(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return "messages_json"
	case "gemini":
		return "generate_content_json"
	default:
		return "chat_json"
	}
}
