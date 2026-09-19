// Package accountstransfer is the import/export & batch-edit subdomain of the
// accounts slice (REFACTOR-0005 阶段 C): the M09 import preview/confirm
// pipeline (native + Sub2API + NewAPI/One-API + CLIProxyAPI/YAML source
// adapters), the account export document builder, the batch edit
// context/update family and the batch post-commit side-effect chain (dispatch
// revision family advance, lookup/stats/gateway invalidation). The HTTP
// surface (m09_routes.go), the write-path normalization (write-side
// credentials/endpoint-mode normalization), the provider model catalog and the
// schedule evaluation logic stay in the accounts facade, which consumes this
// package through the forwarded Store methods (transfer_subdomain_bridge.go).
//
// 依赖方向约束：accountstransfer 只 import accountscore（中立类型层），不
// import accounts 门面；Store 能力经 accountscore.StoreBase 窄端口由门面装配
// 注入，跨域能力（写面 Create/replace*、列表分页取 id、供应商目录断言、凭据
// 归一化、可用性时间计划求值、上游地址安全策略）经 Deps 函数端口注入（门面
// 每次调用以活字段构造 Service，兼容测试克隆 Store 后替换字段的语义）。
package accountstransfer

import (
	"context"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// Deps bundles the cross-domain function ports the transfer family needs from
// the facade (each adapter closes over the live Store, so composition-root
// tests that clone a Store and swap fields keep observing the clone).
type Deps struct {
	// Writer is the narrow write port (facade write.go/tags.go surface): the
	// shared create path (Store.Create; the transfer executor projects the
	// created id/status) plus the satellite replace helpers inside the caller's
	// transaction.
	Writer AccountWriter
	// ListExportPage mirrors Store.ListPage narrowed to the export collection
	// loop: request-order visible account ids plus the pagination flag.
	ListExportPage func(ctx context.Context, access accountscore.AccessScope, options accountscore.ListOptions) (ids []string, hasMore bool, err error)
	// AssertSupportedModelsInProviderCatalog mirrors
	// Store.assertAccountSupportedModelsInProviderCatalog
	// (model_mapping_protocol_matrix.go).
	AssertSupportedModelsInProviderCatalog func(ctx context.Context, q accountscore.Queryer, models []string, providerCode, systemAccountID string, predicate accountscore.ProtocolPredicate) error
	// AssertModelMappingsInProviderCatalog mirrors
	// Store.assertAccountModelMappingsInProviderCatalog
	// (model_catalog_validation.go).
	AssertModelMappingsInProviderCatalog func(ctx context.Context, q accountscore.Queryer, providerCode, systemAccountID string, predicate accountscore.ProtocolPredicate, mappings []accountscore.ModelMapping, supportedEndpointModes []string) error
	// AssertGptRequestOverridesSupported mirrors
	// Store.assertAccountGptRequestOverridesSupported
	// (model_catalog_validation.go).
	AssertGptRequestOverridesSupported func(ctx context.Context, providerCode, accountType string, credentials accountscore.Credentials, supportedModels []string, systemAccountID string) error
	// NormalizeCredentialsForWrite mirrors NormalizeAccountCredentialsForWrite
	// (credentials_normalize.go stays a facade surface per 设计 v2).
	NormalizeCredentialsForWrite func(accountType string, value accountscore.Credentials, defaults *accountscore.EndpointModeDefaultContext) (accountscore.Credentials, error)
	// NormalizeOpenAIAccountClientCompatibility mirrors
	// normalizeOpenAIAccountClientCompatibility (client_compatibility.go).
	NormalizeOpenAIAccountClientCompatibility func(providerCode, accountType, value string, profile accountscore.ProtocolPredicate) (string, error)
	// ResolveHealthCheckEndpointMode mirrors resolveHealthCheckEndpointMode
	// (endpoint_modes.go).
	ResolveHealthCheckEndpointMode func(value *string, providerCode, providerProtocolProfileID string, enabledModes []string, modelSupportsImages *bool) (string, error)
	// AssertEndpointModesCompatible mirrors assertEndpointModesCompatible
	// (endpoint_modes.go).
	AssertEndpointModesCompatible func(providerCode, accountType, clientCompatibility string, predicate accountscore.ProtocolPredicate, modes []string) error
	// NormalizeErrorHandlingRules mirrors normalizeAccountErrorHandlingRules
	// (error_policy.go).
	NormalizeErrorHandlingRules func(value any) ([]any, error)
	// NormalizeResponseInspectionRules mirrors
	// normalizeAccountResponseInspectionRules (response_inspection.go).
	NormalizeResponseInspectionRules func(value any) ([]any, error)
	// NormalizeTagNames mirrors normalizeAccountTagNamesInput (write.go).
	NormalizeTagNames func(value any) ([]string, error)
	// NormalizeSupportedModels mirrors normalizeSupportedModelsInput
	// (write.go).
	NormalizeSupportedModels func(value any) ([]string, error)
	// AssertSupportedModelsRequired mirrors assertSupportedModelsRequired
	// (write.go).
	AssertSupportedModelsRequired func(models []string) error
	// AssertSafeUpstreamBaseURL mirrors assertSafeUpstreamBaseURL
	// (upstream_base_url.go): the runtime-config-backed upstream security
	// policy behind the source adapters.
	AssertSafeUpstreamBaseURL func(value string) error
	// StatusQueryValue mirrors statusQueryValue (routes.go list query parser).
	StatusQueryValue func(value string) string
	// SchedulableQueryValue mirrors schedulableQueryValue (routes.go).
	SchedulableQueryValue func(value string) string
	// NewResourceID mirrors the facade free function newID(prefix) (crypto.go):
	// the import resource creators bypass the store-injected newID clock
	// (阶段 B 忠实度先例), so the port closes over the free function.
	NewResourceID func(prefix string) string
	// NormalizeSchedule mirrors NormalizeSchedule (schedule.go stays a facade
	// surface; the availability schedule types are accountscore values).
	NormalizeSchedule func(input any) (*accountscore.AvailabilitySchedule, error)
	// ParseScheduleJSON mirrors ParseScheduleJSON (schedule.go).
	ParseScheduleJSON func(raw string) (*accountscore.AvailabilitySchedule, error)
	// ScheduleJSON mirrors ScheduleJSON (schedule.go).
	ScheduleJSON func(schedule *accountscore.AvailabilitySchedule) (string, bool)
	// ScheduleStatus mirrors ScheduleStatus (schedule.go).
	ScheduleStatus func(schedule *accountscore.AvailabilitySchedule, now time.Time) (string, bool)
	// NextScheduleCheckAt mirrors NextScheduleCheckAt (schedule.go).
	NextScheduleCheckAt func(schedule *accountscore.AvailabilitySchedule, now time.Time) (string, bool)
	// CacheInvalidator is the nil-safe post-commit invalidation port of the
	// batch edit (batch_effects.go); the facade passes its live Store field so
	// SetCacheInvalidator wiring keeps working.
	CacheInvalidator CacheInvalidator
}

// AccountWriter is the narrow write port the transfer family consumes on the
// facade write surface (REFACTOR-0005 阶段 C：transfer→Create 经窄写接口收敛).
type AccountWriter interface {
	// CreateAccount mirrors Store.Create; the executor only consumes the
	// created id and status (the facade adapter projects CreateResult).
	CreateAccount(ctx context.Context, input accountscore.CreateInput, access accountscore.AccessScope) (accountID, status string, err error)
	// ReplaceAccountSupportedModels mirrors
	// Store.replaceAccountSupportedModels (write.go).
	ReplaceAccountSupportedModels(ctx context.Context, q accountscore.Queryer, accountID, providerCode string, models []string, nowISO string) error
	// ReplaceAccountModelMappings mirrors
	// Store.replaceAccountModelMappings (write.go).
	ReplaceAccountModelMappings(ctx context.Context, q accountscore.Queryer, accountID, providerCode string, mappings []accountscore.ModelMapping, nowISO string) error
	// ReplaceTags mirrors Store.replaceAccountTags (tags.go); the batch call
	// site ignores the returned tag summaries.
	ReplaceTags(ctx context.Context, q accountscore.Queryer, accountID, systemAccountID string, tagNames []string, nowISO string) error
}

// Service carries the import/export & batch-edit subdomain state: the injected
// Store capability port plus the cross-domain function ports.
type Service struct {
	store accountscore.StoreBase
	deps  Deps
}

// New builds the subdomain service over the injected Store capability port.
func New(store accountscore.StoreBase, deps Deps) *Service {
	return &Service{store: store, deps: deps}
}

// ---- transfer-local generic helpers（门面同名小工具的镜像副本：纯泛用工具，
// 无域逻辑，行为恒等；其余小工具随迁文件自带） ----

func textString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// textListQuery mirrors the facade routes.go helper: split comma separated
// query values into a trimmed list.
func textListQuery(values []string) []string {
	out := []string{}
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}
