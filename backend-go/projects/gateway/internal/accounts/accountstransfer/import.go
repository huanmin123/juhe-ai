package accountstransfer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
	"strings"
)

// M09 import slice: the POST /accounts/import/preview + /accounts/import/confirm
// family ported from backend/src/modules/accounts/account-import.routes.ts and
// the account-import*.ts pipeline (root validation, proxy/group/account
// planning, duplicate marking and the resource/account creators). Preview
// returns the plan without touching the database; confirm executes the plan
// with the same create semantics as POST /accounts (Store.Create). The
// credential normalization service, client-compatibility/endpoint-mode
// compatibility asserts, the gpt request-override validation and the pending
// health-check worker dispatch stay with the companion slices (the credential
// record is sealed as provided, exactly like the M08 create path).

// ImportOptions mirrors AccountImportOptions; nil pointers take the Node
// defaults (all three enabled).
type ImportOptions struct {
	CreateMissingGroups  *bool `json:"createMissingGroups,omitempty"`
	CreateMissingProxies *bool `json:"createMissingProxies,omitempty"`
	SkipDuplicates       *bool `json:"skipDuplicates,omitempty"`
}

func (o ImportOptions) resolve() (groups, proxies, duplicates bool) {
	groups = o.CreateMissingGroups == nil || *o.CreateMissingGroups
	proxies = o.CreateMissingProxies == nil || *o.CreateMissingProxies
	duplicates = o.SkipDuplicates == nil || *o.SkipDuplicates
	return groups, proxies, duplicates
}

// ImportAccountsSummary mirrors the accounts block of AccountImportSummary.
type ImportAccountsSummary struct {
	Total  int `json:"total"`
	Create int `json:"create"`
	Skip   int `json:"skip"`
	Failed int `json:"failed"`
}

// ImportProxiesSummary mirrors the proxies block of AccountImportSummary.
type ImportProxiesSummary struct {
	Total  int `json:"total"`
	Create int `json:"create"`
	Reuse  int `json:"reuse"`
	Skip   int `json:"skip"`
	Failed int `json:"failed"`
}

// ImportGroupsSummary mirrors the groups block of AccountImportSummary.
type ImportGroupsSummary struct {
	Create int `json:"create"`
	Reuse  int `json:"reuse"`
	Failed int `json:"failed"`
}

// ImportSummary mirrors AccountImportSummary.
type ImportSummary struct {
	Accounts ImportAccountsSummary `json:"accounts"`
	Proxies  ImportProxiesSummary  `json:"proxies"`
	Groups   ImportGroupsSummary   `json:"groups"`
}

// ImportItem mirrors AccountImportItem.
type ImportItem struct {
	Index                     int      `json:"index"`
	Ref                       *string  `json:"ref,omitempty"`
	Name                      *string  `json:"name,omitempty"`
	ProviderCode              *string  `json:"providerCode,omitempty"`
	ProviderProtocolProfileID *string  `json:"providerProtocolProfileId,omitempty"`
	ProtocolCode              *string  `json:"protocolCode,omitempty"`
	ProtocolVersion           *string  `json:"protocolVersion,omitempty"`
	AccountType               *string  `json:"accountType,omitempty"`
	GroupName                 *string  `json:"groupName,omitempty"`
	GroupID                   *string  `json:"groupId,omitempty"`
	ProxyRef                  *string  `json:"proxyRef,omitempty"`
	Action                    string   `json:"action"`
	Messages                  []string `json:"messages"`
	Warnings                  []string `json:"warnings"`
	AccountID                 *string  `json:"accountId,omitempty"`
}

// ImportProxyItem mirrors AccountImportProxyItem.
type ImportProxyItem struct {
	Index          int      `json:"index"`
	Ref            *string  `json:"ref,omitempty"`
	Name           *string  `json:"name,omitempty"`
	Action         string   `json:"action"`
	Messages       []string `json:"messages"`
	Warnings       []string `json:"warnings"`
	ProxyProfileID *string  `json:"proxyProfileId,omitempty"`
}

// ImportResult mirrors AccountImportResult.
type ImportResult struct {
	Type      string              `json:"type"`
	Version   int                 `json:"version"`
	Mode      string              `json:"mode"`
	CanImport bool                `json:"canImport"`
	Imported  bool                `json:"imported"`
	Summary   ImportSummary       `json:"summary"`
	Accounts  []ImportItem        `json:"accounts"`
	Proxies   []ImportProxyItem   `json:"proxies"`
	Messages  []string            `json:"messages"`
	Source    ImportSourceSummary `json:"source"`
}

const (
	ImportActionCreate = "create"
	ImportActionReuse  = "reuse"
	ImportActionSkip   = "skip"
	ImportActionFailed = "failed"
)

// ---- request payload (accountImportRequestSchema.strict()) ----

type ImportRequest struct {
	Data       any
	SourceMode string
	Options    ImportOptions
}

func ParseImportBody(body map[string]any) (ImportRequest, bool) {
	for key := range body {
		switch key {
		case "data", "sourceMode", "options":
		default:
			return ImportRequest{}, false
		}
	}
	request := ImportRequest{}
	if value, exists := body["data"]; exists {
		request.Data = value
	}
	if value, exists := body["sourceMode"]; exists && value != nil {
		text, ok := value.(string)
		if !ok || !ImportSourceModes[text] {
			return ImportRequest{}, false
		}
		request.SourceMode = text
	}
	if value, exists := body["options"]; exists && value != nil {
		record, ok := value.(map[string]any)
		if !ok {
			return ImportRequest{}, false
		}
		for key := range record {
			switch key {
			case "createMissingGroups", "createMissingProxies", "skipDuplicates":
			default:
				return ImportRequest{}, false
			}
		}
		if raw, exists := record["createMissingGroups"]; exists && raw != nil {
			enabled, ok := raw.(bool)
			if !ok {
				return ImportRequest{}, false
			}
			request.Options.CreateMissingGroups = &enabled
		}
		if raw, exists := record["createMissingProxies"]; exists && raw != nil {
			enabled, ok := raw.(bool)
			if !ok {
				return ImportRequest{}, false
			}
			request.Options.CreateMissingProxies = &enabled
		}
		if raw, exists := record["skipDuplicates"]; exists && raw != nil {
			enabled, ok := raw.(bool)
			if !ok {
				return ImportRequest{}, false
			}
			request.Options.SkipDuplicates = &enabled
		}
	}
	return request, true
}

// ---- providers ----

type importProviderProfile struct {
	id              string
	providerCode    string
	name            string
	enabled         bool
	protocolCode    string
	protocolVersion string
	accountTypes    []string
}

type ImportProvider struct {
	code                   string
	enabled                bool
	defaultSupportedModels []string
	profiles               map[string]*importProviderProfile
}

func (s *Service) LoadImportProviders(ctx context.Context) (map[string]*ImportProvider, error) {
	providers := map[string]*ImportProvider{}
	rows, err := s.store.DB().QueryContext(ctx, s.store.Bind(`SELECT code, enabled, default_supported_models_json
		FROM `+s.store.Table("providers")))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var code string
		var enabled int64
		var defaultsJSON string
		if err := rows.Scan(&code, &enabled, &defaultsJSON); err != nil {
			rows.Close()
			return nil, err
		}
		provider := &ImportProvider{code: code, enabled: enabled == 1, profiles: map[string]*importProviderProfile{}}
		if strings.TrimSpace(defaultsJSON) != "" {
			_ = json.Unmarshal([]byte(defaultsJSON), &provider.defaultSupportedModels)
		}
		providers[code] = provider
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	profileRows, err := s.store.DB().QueryContext(ctx, s.store.Bind(`SELECT id, provider_code, name, enabled,
			protocol_code, protocol_version, account_types_json
		FROM `+s.store.Table("provider_protocol_profiles")))
	if err != nil {
		return nil, err
	}
	defer profileRows.Close()
	for profileRows.Next() {
		var id, providerCode, name, protocolCode, protocolVersion, accountTypesJSON string
		var enabled int64
		if err := profileRows.Scan(&id, &providerCode, &name, &enabled, &protocolCode, &protocolVersion, &accountTypesJSON); err != nil {
			return nil, err
		}
		provider, ok := providers[providerCode]
		if !ok {
			continue
		}
		profile := &importProviderProfile{
			id: id, providerCode: providerCode, name: name, enabled: enabled == 1,
			protocolCode: protocolCode, protocolVersion: protocolVersion, accountTypes: []string{},
		}
		if strings.TrimSpace(accountTypesJSON) != "" {
			_ = json.Unmarshal([]byte(accountTypesJSON), &profile.accountTypes)
		}
		provider.profiles[id] = profile
	}
	return providers, profileRows.Err()
}

// ---- plan model ----

// ImportGroupCreatePlan mirrors the planned group creation (facade test
// literals consume the exported fields).
type ImportGroupCreatePlan struct {
	ProviderCode string
	Name         string
}

type normalizedImportProxy struct {
	index       int
	ref         string
	name        string
	proxyType   string
	host        string
	port        int
	username    string
	password    string
	description string
	enabled     bool
}

type importProxyPlan struct {
	source         normalizedImportProxy
	item           ImportProxyItem
	proxyProfileID string
}

// NormalizedImportAccount mirrors the normalized import account row (facade
// test literals consume the exported message plumbing).
type NormalizedImportAccount struct {
	index                     int
	ref                       string
	name                      string
	providerCode              string
	providerProtocolProfileID string
	protocolCode              string
	protocolVersion           string
	accountType               string
	status                    string
	credentials               accountscore.Credentials
	groupID                   string
	groupName                 string
	proxyRef                  string
	proxyProfileID            string
	concurrencyLimit          int
	priority                  int
	superPriorityEnabled      *bool
	fallbackEnabled           *bool
	supportedModels           []string
	healthCheckModel          string
	healthCheckEndpointMode   string
	modelMappings             []accountscore.ModelMapping
	tags                      []string
	accountExpiresAt          string
	availabilityScheduleRaw   any
	notes                     string

	// Messages aliases the plan item's message list (Node aliases
	// item.messages into the normalized source).
	Messages *[]string
}

// Push appends one plan message (a nil message list stays a no-op).
func (a *NormalizedImportAccount) Push(message string) {
	if a.Messages == nil {
		return
	}
	*a.Messages = append(*a.Messages, message)
}

type importAccountPlan struct {
	source         NormalizedImportAccount
	item           ImportItem
	groupID        string
	proxyProfileID string
}

// ImportPlan mirrors the full import plan (facade test literals consume the
// exported options field).
type ImportPlan struct {
	result             *ImportResult
	accounts           []importAccountPlan
	proxies            []importProxyPlan
	groupIDsByKey      map[string]string
	groupNamesToCreate map[string]ImportGroupCreatePlan
	Options            ImportOptions
}

// importPlanContext carries the shared lookup state (providers, options,
// access) across the planning stages.
type importPlanContext struct {
	store       *Service
	access      accountscore.AccessScope
	targetOwner string
	providers   map[string]*ImportProvider
	options     ImportOptions

	groupLookup map[string]*importGroupOption
	proxyLookup map[string]*ImportProxyOption
}

func (c *importPlanContext) createMissingGroups() bool {
	enabled, _, _ := c.options.resolve()
	return enabled
}

func (c *importPlanContext) createMissingProxies() bool {
	_, enabled, _ := c.options.resolve()
	return enabled
}

func (c *importPlanContext) skipDuplicates() bool {
	_, _, enabled := c.options.resolve()
	return enabled
}

type importGroupOption struct {
	id           string
	name         string
	providerCode string
}

type ImportProxyOption struct {
	id      string
	name    string
	enabled bool
}

func importTargetOwner(access accountscore.AccessScope) (string, error) {
	// importTargetSystemAccountId: manageableSystemAccountId ?? the caller id.
	owner := access.ManageableID()
	if owner == "" {
		owner = access.ViewerID
	}
	if strings.TrimSpace(owner) == "" {
		return "", &accountscore.ValidationError{Message: "缺少系统账户上下文"}
	}
	return owner, nil
}

// PreviewImport mirrors previewAccountImportAsync: the adapted source document
// is validated and planned without any write.
func (s *Service) PreviewImport(ctx context.Context, data any, sourceMode string, options ImportOptions, access accountscore.AccessScope) (*ImportResult, error) {
	ctx = accountscore.EnsureCtx(ctx)
	plan, err := s.buildImportPlan(ctx, data, sourceMode, options, access)
	if err != nil {
		return nil, err
	}
	return plan.result, nil
}

// ExecuteImport mirrors executeAccountImportAsync: plan, then execute the
// resource/account creators. Per-item failures are rendered into the result;
// store-level failures surface as errors (the route maps them to 400).
func (s *Service) ExecuteImport(ctx context.Context, data any, sourceMode string, options ImportOptions, access accountscore.AccessScope) (*ImportResult, error) {
	ctx = accountscore.EnsureCtx(ctx)
	plan, err := s.buildImportPlan(ctx, data, sourceMode, options, access)
	if err != nil {
		return nil, err
	}
	result := plan.result
	result.Mode = "import"
	if !result.CanImport {
		return result, nil
	}
	if err := s.executeImportPlan(ctx, plan, access); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) buildImportPlan(ctx context.Context, data any, sourceMode string, rawOptions ImportOptions, access accountscore.AccessScope) (*ImportPlan, error) {
	if strings.TrimSpace(sourceMode) == "" {
		sourceMode = ImportSourceNative
	}
	if !ImportSourceModes[sourceMode] {
		return nil, &accountscore.ValidationError{Message: "账户导入参数无效"}
	}
	owner, err := importTargetOwner(access)
	if err != nil {
		return nil, err
	}
	providers, err := s.LoadImportProviders(ctx)
	if err != nil {
		return nil, err
	}

	result := &ImportResult{
		Type: AccountImportProtocolType, Version: AccountImportProtocolVersion, Mode: "preview",
		Accounts: []ImportItem{}, Proxies: []ImportProxyItem{}, Messages: []string{},
	}
	emptyPlan := &ImportPlan{
		result: result, groupIDsByKey: map[string]string{},
		groupNamesToCreate: map[string]ImportGroupCreatePlan{}, Options: rawOptions,
	}

	adaptedData, source := AdaptImportSource(data, sourceMode, s.deps.AssertSafeUpstreamBaseURL)
	result.Source = source

	validationMessages := []string{}
	rawAccounts, rawProxies, ok := validateImportRoot(adaptedData, &validationMessages)
	if !ok {
		result.Messages = append(result.Messages, validationMessages...)
		return emptyPlan, nil
	}

	planCtx := &importPlanContext{
		store: s, access: access, targetOwner: owner, providers: providers,
		options: rawOptions,
	}
	proxyPlans, proxyByRef := s.planImportProxies(ctx, rawProxies, planCtx)

	groupIDsByKey := map[string]string{}
	groupNamesToCreate := map[string]ImportGroupCreatePlan{}
	accounts := make([]importAccountPlan, 0, len(rawAccounts))
	for index, raw := range rawAccounts {
		plan, err := s.planImportAccount(ctx, raw, index+1, planCtx, proxyByRef, groupIDsByKey, groupNamesToCreate)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, plan)
	}
	markDuplicateImportItems(accounts, planCtx.skipDuplicates())

	result.Proxies = renderImportProxyItems(proxyPlans)
	result.Accounts = renderImportAccountItems(accounts)
	result.Summary = buildImportSummary(result.Accounts, result.Proxies, groupNamesToCreate)
	result.CanImport = result.Summary.Accounts.Failed == 0 &&
		result.Summary.Proxies.Failed == 0 &&
		result.Summary.Accounts.Create > 0

	return &ImportPlan{
		result: result, accounts: accounts, proxies: proxyPlans,
		groupIDsByKey: groupIDsByKey, groupNamesToCreate: groupNamesToCreate,
		Options: rawOptions,
	}, nil
}

// ---- root validation (account-import-root-validation.ts) ----

var importRootKeys = map[string]bool{"type": true, "version": true, "proxies": true, "accounts": true}

func validateImportRoot(data any, messages *[]string) (rawAccounts, rawProxies []any, ok bool) {
	record, isRecord := data.(map[string]any)
	if !isRecord {
		*messages = append(*messages, "导入内容必须是 JSON 对象")
		return nil, nil, false
	}
	appendUnknownImportFields(record, importRootKeys, "导入内容", messages)
	if text, isText := record["type"].(string); !isText || text != AccountImportProtocolType {
		*messages = append(*messages, "type 必须是 "+AccountImportProtocolType)
	}
	if version, isNumber := record["version"].(float64); !isNumber || version != float64(AccountImportProtocolVersion) {
		*messages = append(*messages, fmt.Sprintf("version 必须是 %d", AccountImportProtocolVersion))
	}
	if len(*messages) > 0 {
		return nil, nil, false
	}

	rawProxies, _ = record["proxies"].([]any)
	if _, exists := record["proxies"]; exists {
		if _, isArray := record["proxies"].([]any); !isArray {
			*messages = append(*messages, "proxies 必须是数组")
		}
	}
	rawAccounts, _ = record["accounts"].([]any)
	if _, exists := record["accounts"]; exists {
		if _, isArray := record["accounts"].([]any); !isArray {
			*messages = append(*messages, "accounts 必须是数组")
		}
	}
	if len(*messages) > 0 {
		return nil, nil, false
	}
	if len(rawAccounts) == 0 {
		*messages = append(*messages, "accounts 至少需要 1 条账户")
		return nil, nil, false
	}
	if len(rawAccounts) > MaxImportedAccounts {
		*messages = append(*messages, fmt.Sprintf("accounts 单次最多导入 %d 条", MaxImportedAccounts))
		return nil, nil, false
	}
	if len(rawProxies) > MaxImportedProxies {
		*messages = append(*messages, fmt.Sprintf("proxies 单次最多导入 %d 条", MaxImportedProxies))
		return nil, nil, false
	}
	return rawAccounts, rawProxies, true
}

func appendUnknownImportFields(record map[string]any, allowed map[string]bool, label string, messages *[]string) {
	unknown := []string{}
	for key := range record {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	accountscore.SortStrings(unknown)
	if len(unknown) > 0 {
		*messages = append(*messages, label+"包含未知字段："+strings.Join(unknown, "、"))
	}
}

// ---- field parser (account-import-field-parser.ts) ----

func ImportOptionalTextField(record map[string]any, key, label string, messages *[]string) string {
	value, exists := record[key]
	if !exists {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		*messages = append(*messages, label+"必须是字符串")
		return ""
	}
	return strings.TrimSpace(text)
}

func ImportOptionalBooleanField(record map[string]any, key, label string, messages *[]string) *bool {
	value, exists := record[key]
	if !exists {
		return nil
	}
	enabled, ok := value.(bool)
	if !ok {
		*messages = append(*messages, label+"必须是布尔值")
		return nil
	}
	return &enabled
}

func ImportOptionalPositiveIntegerField(record map[string]any, key, label string, messages *[]string) int {
	value, exists := record[key]
	if !exists {
		return 0
	}
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) {
		*messages = append(*messages, label+"必须是整数")
		return 0
	}
	if int(number) <= 0 {
		*messages = append(*messages, label+"必须是大于 0 的整数")
		return 0
	}
	return int(number)
}

func ImportOptionalNonNegativeIntegerField(record map[string]any, key, label string, messages *[]string) int {
	value, exists := record[key]
	if !exists {
		return -1
	}
	number, ok := value.(float64)
	if !ok || number != float64(int64(number)) {
		*messages = append(*messages, label+"必须是整数")
		return -1
	}
	if number < 0 {
		*messages = append(*messages, label+"必须是大于等于 0 的整数")
		return -1
	}
	return int(number)
}

func ImportOptionalStringArrayField(record map[string]any, key, label string, messages *[]string) []string {
	value, exists := record[key]
	if !exists {
		return nil
	}
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		*messages = append(*messages, label+"必须是非空字符串数组")
		return nil
	}
	items := []string{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			*messages = append(*messages, label+"必须是非空字符串数组")
			return nil
		}
		items = append(items, strings.TrimSpace(text))
	}
	return items
}

func ImportOptionalHealthCheckEndpointModeField(record map[string]any, key, label string, messages *[]string) string {
	value, exists := record[key]
	if !exists {
		return ""
	}
	text, ok := value.(string)
	if ok && accountscore.AccountHealthCheckEndpointModes[text] {
		return text
	}
	*messages = append(*messages, label+"必须是支持的 JSON 或 Streaming 请求形态")
	return ""
}

func ImportOptionalTagsField(record map[string]any, key, label string, messages *[]string) []string {
	value, exists := record[key]
	if !exists {
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		*messages = append(*messages, label+"必须是字符串数组")
		return nil
	}
	items := []string{}
	seen := map[string]bool{}
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			*messages = append(*messages, label+"必须是字符串数组")
			return nil
		}
		tagName := strings.Join(strings.Fields(text), " ")
		if tagName == "" {
			continue
		}
		if len([]rune(tagName)) > accountscore.MaxTagNameLength {
			*messages = append(*messages, label+"单个标签不能超过 40 个字符")
			return nil
		}
		if seen[tagName] {
			continue
		}
		seen[tagName] = true
		items = append(items, tagName)
	}
	if len(items) > accountscore.MaxTagsPerAccount {
		*messages = append(*messages, label+"单个账户最多配置 24 个标签")
		return nil
	}
	return items
}

func ImportOptionalDateTimeField(record map[string]any, key, label string, messages *[]string) string {
	value, exists := record[key]
	if !exists {
		return ""
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		*messages = append(*messages, label+"必须是有效时间字符串")
		return ""
	}
	canonical, valid := accountscore.CanonicalRFC3339(text)
	if !valid {
		*messages = append(*messages, label+"必须是有效时间字符串")
		return ""
	}
	return canonical
}

func ImportModelMappingsField(record map[string]any, key, label string, messages *[]string) []accountscore.ModelMapping {
	value, exists := record[key]
	if !exists {
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		*messages = append(*messages, label+"必须是模型映射数组")
		return nil
	}
	output := []accountscore.ModelMapping{}
	seenSources := map[string]bool{}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			*messages = append(*messages, label+"条目必须是对象")
			return nil
		}
		sourceModel := ImportMappingText(entry["sourceModel"])
		sourceFamily := ImportMappingSourceFamily(entry["sourceEndpointFamily"])
		upstreamModel := ImportMappingText(entry["upstreamModel"])
		upstreamFamily := ImportMappingUpstreamFamily(entry["upstreamEndpointFamily"])
		if sourceModel == "" || sourceFamily == "" || upstreamModel == "" || upstreamFamily == "" {
			*messages = append(*messages, label+"条目必须包含 sourceModel、sourceEndpointFamily、upstreamModel 和 upstreamEndpointFamily")
			return nil
		}
		if sourceModel == upstreamModel && sourceFamily == upstreamFamily {
			continue
		}
		sourceKey := sourceFamily + "\n" + sourceModel
		if seenSources[sourceKey] {
			*messages = append(*messages, label+"不能重复配置同一个 sourceModel 和 sourceEndpointFamily："+sourceModel+" / "+sourceFamily)
			return nil
		}
		seenSources[sourceKey] = true
		enabled := true
		if raw, ok := entry["enabled"].(bool); ok {
			enabled = raw
		}
		output = append(output, accountscore.ModelMapping{
			SourceModel: sourceModel, SourceEndpointFamily: sourceFamily,
			UpstreamModel: upstreamModel, UpstreamEndpointFamily: upstreamFamily,
			Enabled: &enabled,
		})
	}
	return output
}

func ImportMappingText(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func ImportMappingSourceFamily(value any) string {
	switch text := value.(type) {
	case string:
		switch text {
		case "chat_completions", "responses", "messages", "generate_content", "stream_generate_content":
			return text
		}
	}
	return ""
}

func ImportMappingUpstreamFamily(value any) string {
	switch text := value.(type) {
	case string:
		switch text {
		case "chat_completions", "responses", "messages", "generate_content":
			return text
		}
	}
	return ""
}

func NormalizeImportStatus(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	switch text {
	case "active", "pending_test", "disabled":
		return text
	default:
		return ""
	}
}

// ---- proxy planning (account-import-proxy-plan.ts) ----

var importProxyKeys = map[string]bool{
	"ref": true, "name": true, "type": true, "host": true, "port": true,
	"username": true, "password": true, "description": true, "enabled": true,
}

func NormalizeImportProxyType(value any) string {
	text := strings.ToLower(sourceText(value))
	switch text {
	case "http", "https", "socks5", "socks5h":
		return text
	default:
		return ""
	}
}

func (s *Service) planImportProxies(ctx context.Context, rawProxies []any, planCtx *importPlanContext) ([]importProxyPlan, map[string]*importProxyPlan) {
	plans := make([]importProxyPlan, 0, len(rawProxies))
	for index, raw := range rawProxies {
		plans = append(plans, s.planImportProxy(ctx, raw, index+1, planCtx))
	}
	proxyByRef := map[string]*importProxyPlan{}
	for index := range plans {
		ref := plans[index].source.ref
		if ref == "" {
			continue
		}
		if _, taken := proxyByRef[ref]; taken {
			plans[index].item.Action = ImportActionFailed
			plans[index].item.Messages = append(plans[index].item.Messages, "代理 ref 重复："+ref)
			continue
		}
		proxyByRef[ref] = &plans[index]
	}
	return plans, proxyByRef
}

func (s *Service) planImportProxy(ctx context.Context, value any, index int, planCtx *importPlanContext) importProxyPlan {
	plan := importProxyPlan{
		source: normalizedImportProxy{index: index, enabled: true},
		item:   ImportProxyItem{Index: index, Action: ImportActionCreate, Messages: []string{}, Warnings: []string{}},
	}
	record, ok := value.(map[string]any)
	if !ok {
		plan.item.Action = ImportActionFailed
		plan.item.Messages = append(plan.item.Messages, "代理配置必须是对象")
		return plan
	}
	messages := &plan.item.Messages
	appendUnknownImportFields(record, importProxyKeys, "代理配置", messages)
	plan.source.ref = ImportOptionalTextField(record, "ref", "代理 ref", messages)
	plan.source.name = ImportOptionalTextField(record, "name", "代理名称", messages)
	proxyTypeInput := ImportOptionalTextField(record, "type", "代理 type", messages)
	if proxyTypeInput != "" {
		plan.source.proxyType = NormalizeImportProxyType(proxyTypeInput)
		if plan.source.proxyType == "" {
			plan.item.Messages = append(plan.item.Messages, "代理 type 不支持："+proxyTypeInput)
		}
	}
	plan.source.host = ImportOptionalTextField(record, "host", "代理 host", messages)
	plan.source.port = ImportOptionalPositiveIntegerField(record, "port", "代理 port", messages)
	plan.source.username = ImportOptionalTextField(record, "username", "代理 username", messages)
	plan.source.password = ImportOptionalTextField(record, "password", "代理 password", messages)
	plan.source.description = ImportOptionalTextField(record, "description", "代理 description", messages)
	if enabled := ImportOptionalBooleanField(record, "enabled", "代理 enabled", messages); enabled != nil {
		plan.source.enabled = *enabled
	}
	plan.item.Ref = TextPointerOrNil(plan.source.ref)
	plan.item.Name = TextPointerOrNil(plan.source.name)

	if plan.source.ref == "" {
		plan.item.Messages = append(plan.item.Messages, "代理 ref 不能为空")
	}
	if plan.source.name == "" {
		plan.item.Messages = append(plan.item.Messages, "代理名称不能为空")
	}
	if proxyTypeInput == "" {
		plan.item.Messages = append(plan.item.Messages, "代理 type 不能为空")
	}
	if plan.source.host == "" {
		plan.item.Messages = append(plan.item.Messages, "代理 host 不能为空")
	}
	if plan.source.port < 1 || plan.source.port > 65535 {
		plan.item.Messages = append(plan.item.Messages, "代理 port 必须是 1 到 65535 的整数")
	}
	existing := s.findImportProxyOptionByName(ctx, plan.source.name, planCtx)
	if existing != nil {
		plan.item.Action = ImportActionReuse
		plan.item.ProxyProfileID = &existing.id
	} else if !canCreateImportProxy(planCtx, &plan.item) {
		if len(plan.item.Messages) > 0 {
			plan.item.Action = ImportActionFailed
		} else {
			plan.item.Action = ImportActionSkip
		}
	}
	if len(plan.item.Messages) > 0 {
		plan.item.Action = ImportActionFailed
	}
	if plan.item.ProxyProfileID != nil {
		plan.proxyProfileID = *plan.item.ProxyProfileID
	}
	return plan
}

func canCreateImportProxy(planCtx *importPlanContext, item *ImportProxyItem) bool {
	if !planCtx.createMissingProxies() {
		item.Warnings = append(item.Warnings, "当前导入选项未启用代理创建")
		return false
	}
	if !planCtx.access.IsAdmin {
		item.Messages = append(item.Messages, "用户侧导入不能创建代理，请由管理员先创建代理")
		return false
	}
	return true
}

// findImportProxyOptionByName mirrors findProxyOptionByName: enabled proxies
// only, exact trimmed-name match.
func (s *Service) findImportProxyOptionByName(ctx context.Context, name string, planCtx *importPlanContext) *ImportProxyOption {
	key := strings.TrimSpace(name)
	if key == "" {
		return nil
	}
	// planCtx 可能为 nil（createImportProxy 的重复名回退以 nil 调用），
	// 既不能解引用其缓存，也不投递查询结果到缓存。
	if planCtx != nil && planCtx.proxyLookup != nil {
		if existing, ok := planCtx.proxyLookup[key]; ok {
			return existing
		}
	}
	var id string
	var enabled int64
	err := s.store.DB().QueryRowContext(ctx, s.store.Bind(`SELECT id, enabled FROM `+s.store.Table("proxy_profiles")+`
		WHERE name = ? AND enabled = 1
		ORDER BY updated_at DESC, id ASC
		LIMIT 1`), key).Scan(&id, &enabled)
	var option *ImportProxyOption
	if err == nil {
		option = &ImportProxyOption{id: id, name: key, enabled: enabled == 1}
	} else if !errors.Is(err, sql.ErrNoRows) {
		option = nil
	}
	if planCtx == nil {
		return option
	}
	if planCtx.proxyLookup == nil {
		planCtx.proxyLookup = map[string]*ImportProxyOption{}
	}
	planCtx.proxyLookup[key] = option
	return option
}

// FindImportProxyByID mirrors findProxy: global id lookup, enabled is checked
// by the caller.
func (s *Service) FindImportProxyByID(ctx context.Context, id string) (*ImportProxyOption, error) {
	var row struct {
		id      string
		name    string
		enabled int64
	}
	err := s.store.DB().QueryRowContext(ctx, s.store.Bind(`SELECT id, name, enabled FROM `+s.store.Table("proxy_profiles")+`
		WHERE id = ? LIMIT 1`), id).Scan(&row.id, &row.name, &row.enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ImportProxyOption{id: row.id, name: row.name, enabled: row.enabled == 1}, nil
}

// ---- account planning (account-import-account-plan.ts) ----

var importAccountKeys = map[string]bool{
	"ref": true, "name": true, "providerCode": true, "providerProtocolProfileId": true,
	"type": true, "status": true, "credentials": true, "groupId": true, "groupName": true,
	"proxyRef": true, "proxyProfileId": true, "concurrencyLimit": true, "priority": true,
	"superPriorityEnabled": true, "fallbackEnabled": true, "supportedModels": true,
	"healthCheckModel": true, "healthCheckEndpointMode": true,
	"temporaryUnavailableContinuousProbeEnabled": true, "modelMappings": true, "tags": true,
	"accountExpiresAt": true, "availabilitySchedule": true, "notes": true,
}

func (s *Service) planImportAccount(ctx context.Context, value any, index int, planCtx *importPlanContext,
	proxyByRef map[string]*importProxyPlan, groupIDsByKey map[string]string, groupNamesToCreate map[string]ImportGroupCreatePlan,
) (importAccountPlan, error) {
	plan := importAccountPlan{
		item: ImportItem{Index: index, Action: ImportActionCreate, Messages: []string{}, Warnings: []string{}},
	}
	source := &plan.source
	source.index = index
	source.accountType = "api_key"
	source.status = "active"
	source.credentials = accountscore.Credentials{}
	source.priority = -1
	source.Messages = &plan.item.Messages

	record, ok := value.(map[string]any)
	if !ok {
		plan.item.Action = ImportActionFailed
		plan.item.Messages = append(plan.item.Messages, "账户配置必须是对象")
		return plan, nil
	}
	messages := &plan.item.Messages
	appendUnknownImportFields(record, importAccountKeys, "账户配置", messages)
	source.ref = ImportOptionalTextField(record, "ref", "账户 ref", messages)
	source.name = ImportOptionalTextField(record, "name", "账户名称", messages)
	source.providerCode = ImportOptionalTextField(record, "providerCode", "账户 providerCode", messages)
	source.providerProtocolProfileID = ImportOptionalTextField(record, "providerProtocolProfileId", "账户 providerProtocolProfileId", messages)
	if source.providerCode == "" {
		source.Push("账户 providerCode 不能为空")
	}
	if typeInput := ImportOptionalTextField(record, "type", "账户 type", messages); typeInput != "" {
		source.accountType = typeInput
	} else {
		source.Push("账户 type 不能为空")
	}
	if rawStatus := ImportOptionalTextField(record, "status", "账户 status", messages); rawStatus != "" {
		if normalized := NormalizeImportStatus(rawStatus); normalized != "" {
			source.status = normalized
		} else {
			source.Push("账户状态不支持：" + rawStatus)
		}
	} else {
		source.Push("账户 status 不能为空")
	}
	source.groupID = ImportOptionalTextField(record, "groupId", "账户 groupId", messages)
	source.groupName = ImportOptionalTextField(record, "groupName", "账户 groupName", messages)
	source.proxyRef = ImportOptionalTextField(record, "proxyRef", "账户 proxyRef", messages)
	source.proxyProfileID = ImportOptionalTextField(record, "proxyProfileId", "账户 proxyProfileId", messages)
	source.concurrencyLimit = ImportOptionalPositiveIntegerField(record, "concurrencyLimit", "账户 concurrencyLimit", messages)
	source.priority = ImportOptionalNonNegativeIntegerField(record, "priority", "账户 priority", messages)
	source.superPriorityEnabled = ImportOptionalBooleanField(record, "superPriorityEnabled", "账户 superPriorityEnabled", messages)
	source.fallbackEnabled = ImportOptionalBooleanField(record, "fallbackEnabled", "账户 fallbackEnabled", messages)
	source.supportedModels = ImportOptionalStringArrayField(record, "supportedModels", "账户 supportedModels", messages)
	source.healthCheckModel = ImportOptionalTextField(record, "healthCheckModel", "账户 healthCheckModel", messages)
	source.healthCheckEndpointMode = ImportOptionalHealthCheckEndpointModeField(record, "healthCheckEndpointMode", "账户 healthCheckEndpointMode", messages)
	// Parsed for validation parity only; the column default owns the effective
	// value in this slice.
	ImportOptionalBooleanField(record, "temporaryUnavailableContinuousProbeEnabled", "账户 temporaryUnavailableContinuousProbeEnabled", messages)
	source.modelMappings = ImportModelMappingsField(record, "modelMappings", "账户 modelMappings", messages)
	source.tags = ImportOptionalTagsField(record, "tags", "账户 tags", messages)
	source.accountExpiresAt = ImportOptionalDateTimeField(record, "accountExpiresAt", "账户 accountExpiresAt", messages)
	if rawSchedule, exists := record["availabilitySchedule"]; exists {
		source.availabilityScheduleRaw = rawSchedule
		if _, err := s.deps.NormalizeSchedule(rawSchedule); err != nil {
			source.Push(AccountScheduleError(err).Error())
		}
	}
	source.notes = ImportOptionalTextField(record, "notes", "账户 notes", messages)
	if credentials, ok := record["credentials"].(map[string]any); ok {
		source.credentials = accountscore.Credentials(credentials)
	}

	applyImportAccountProtocolProfileDefaults(source, planCtx.providers)

	plan.item.Ref = TextPointerOrNil(source.ref)
	plan.item.Name = TextPointerOrNil(source.name)
	plan.item.ProviderCode = TextPointerOrNil(source.providerCode)
	plan.item.GroupName = TextPointerOrNil(source.groupName)
	plan.item.GroupID = TextPointerOrNil(source.groupID)
	plan.item.ProxyRef = TextPointerOrNil(source.proxyRef)

	validateImportProviderAndBasics(source, planCtx.providers)
	plan.item.ProviderProtocolProfileID = TextPointerOrNil(source.providerProtocolProfileID)
	plan.item.ProtocolCode = TextPointerOrNil(source.protocolCode)
	plan.item.ProtocolVersion = TextPointerOrNil(source.protocolVersion)
	plan.item.AccountType = TextPointerOrNil(source.accountType)

	// Credential normalization (account-import-account-plan.ts:257-277): the
	// normalized record feeds Store.Create; a failure lands as a per-account
	// message and marks the item failed, exactly like the Node plan helper.
	s.normalizeImportAccountCredentials(source)

	// The endpoint-mode compatibility asserts stay with the model-validation
	// companion slice; the gpt request-override catalog validation and the
	// mapping catalog checks landed with the model-catalog-validation slice
	// (model_catalog_validation.go).

	if err := s.validateImportModelCatalogFields(ctx, source, planCtx); err != nil {
		return plan, err
	}
	s.validateImportGptRequestOverrides(ctx, source, planCtx)
	if err := s.resolveImportAccountGroup(ctx, &plan, planCtx, groupIDsByKey, groupNamesToCreate); err != nil {
		return plan, err
	}
	if err := s.resolveImportAccountProxy(ctx, &plan, proxyByRef); err != nil {
		return plan, err
	}
	if len(plan.item.Messages) > 0 {
		plan.item.Action = ImportActionFailed
	}
	return plan, nil
}

// applyImportAccountProtocolProfileDefaults mirrors
// applyImportAccountProtocolProfileDefaults: the requested profile is resolved
// against the provider and the protocol columns are pinned.
func applyImportAccountProtocolProfileDefaults(source *NormalizedImportAccount, providers map[string]*ImportProvider) {
	provider := providers[source.providerCode]
	if provider == nil {
		return
	}
	requestedProfileID := strings.TrimSpace(source.providerProtocolProfileID)
	if requestedProfileID == "" {
		source.Push("账户 providerProtocolProfileId 不能为空")
		return
	}
	profile := provider.profiles[requestedProfileID]
	if profile == nil || profile.providerCode != source.providerCode {
		return
	}
	source.providerProtocolProfileID = profile.id
	source.protocolCode = profile.protocolCode
	source.protocolVersion = profile.protocolVersion
}

// normalizeImportAccountCredentials mirrors the credential normalization block
// of account-import-account-plan.ts: normalize the raw credentials with the
// resolved provider profile context, push the failure as a per-account message
// and keep the raw record untouched (Store.Create re-normalizes for the write).
func (s *Service) normalizeImportAccountCredentials(source *NormalizedImportAccount) {
	clientCompatibility, err := s.deps.NormalizeOpenAIAccountClientCompatibility(
		source.providerCode,
		source.accountType,
		"",
		accountscore.ProtocolPredicate{
			ProviderCode:              source.providerCode,
			ProtocolCode:              source.protocolCode,
			ProtocolVersion:           source.protocolVersion,
			ProviderProtocolProfileID: source.providerProtocolProfileID,
		},
	)
	if err != nil {
		source.Push(err.Error())
		return
	}
	normalized, err := s.deps.NormalizeCredentialsForWrite(source.accountType, source.credentials, &accountscore.EndpointModeDefaultContext{
		ProviderCode:              source.providerCode,
		AccountType:               source.accountType,
		ClientCompatibility:       clientCompatibility,
		ProviderProtocolProfileID: source.providerProtocolProfileID,
		ProtocolCode:              source.protocolCode,
		ProtocolVersion:           source.protocolVersion,
	})
	if err != nil {
		source.Push(err.Error())
		return
	}
	source.credentials = normalized
}

// validateImportProviderAndBasics mirrors validateImportProviderAndBasics.
func validateImportProviderAndBasics(source *NormalizedImportAccount, providers map[string]*ImportProvider) {
	if source.name == "" {
		source.Push("账户名称不能为空")
	}
	provider := providers[source.providerCode]
	if provider == nil {
		source.Push("不支持的供应商：" + source.providerCode)
	} else if !provider.enabled {
		source.Push("供应商已停用：" + source.providerCode)
	} else if profile := resolveImportAccountProtocolProfile(source, provider); profile != nil && !accountscore.ContainsString(profile.accountTypes, source.accountType) {
		source.Push("供应商协议档案 " + profile.name + " 不支持账户类型 " + source.accountType)
	}
	if source.status != "active" && source.status != "pending_test" && source.status != "disabled" {
		source.Push("账户状态仅支持 active、pending_test 或 disabled")
	}
	if source.concurrencyLimit != 0 && source.concurrencyLimit < 1 {
		source.Push("concurrencyLimit 必须大于 0")
	}
	if source.accountExpiresAt != "" {
		if _, valid := accountscore.CanonicalRFC3339(source.accountExpiresAt); !valid {
			source.Push("accountExpiresAt 必须是有效时间字符串")
		}
	}
}

// resolveImportAccountProtocolProfile mirrors resolveImportAccountProtocolProfile.
func resolveImportAccountProtocolProfile(source *NormalizedImportAccount, provider *ImportProvider) *importProviderProfile {
	requestedProfileID := strings.TrimSpace(source.providerProtocolProfileID)
	if requestedProfileID == "" {
		source.Push("账户 providerProtocolProfileId 不能为空")
		return nil
	}
	profile := provider.profiles[requestedProfileID]
	if profile == nil {
		source.Push("供应商 " + source.providerCode + " 未配置协议档案")
		return nil
	}
	if profile.providerCode != source.providerCode {
		source.Push("协议档案 " + profile.id + " 不属于供应商 " + source.providerCode)
		return nil
	}
	if !profile.enabled {
		source.Push("供应商协议档案已停用：" + profile.name)
		return nil
	}
	source.providerProtocolProfileID = profile.id
	source.protocolCode = profile.protocolCode
	source.protocolVersion = profile.protocolVersion
	return profile
}

// validateImportModelCatalogFields mirrors validateAccountModelCatalogFields
// restricted to the slice-owned normalization subset: provider default models,
// the required supported-model set, the health check model membership and the
// mapping upstream allowlist. The mapping catalog checks (the
// normalizeAccountModelMappingsForProviderAsync catalog segment) ride the
// model-catalog-validation slice; failures land as per-account messages.
func (s *Service) validateImportModelCatalogFields(ctx context.Context, source *NormalizedImportAccount, planCtx *importPlanContext) error {
	if source.providerCode == "" || planCtx.providers[source.providerCode] == nil || planCtx.targetOwner == "" {
		return nil
	}
	provider := planCtx.providers[source.providerCode]
	input := source.supportedModels
	if len(input) == 0 {
		input = provider.defaultSupportedModels
	}
	models, err := s.deps.NormalizeSupportedModels(anySliceOrNil(input))
	if err != nil {
		source.Push(err.Error())
		return nil
	}
	source.supportedModels = models
	if err := s.deps.AssertSupportedModelsRequired(source.supportedModels); err != nil {
		source.Push(err.Error())
		return nil
	}
	if source.healthCheckModel != "" && !accountscore.ContainsString(source.supportedModels, source.healthCheckModel) {
		source.Push("账户 healthCheckModel 必须属于 supportedModels")
	}
	if err := AssertMappingUpstreamsAllowed(source.modelMappings, source.supportedModels); err != nil {
		source.Push(err.Error())
	}
	// Supported-model catalog assertion (归档 patch 链
	// normalizeAccountSupportedModelsForProviderAsync :1520 的同语义导入段；
	// 归档 import plan 的调用方文件已被裁剪，语义对齐 patch/create 链：支持模型
	// 必须落在当前供应商模型目录中且声明支持当前协议档案，hybrid 供应商直通，
	// filterIncompatibleDefaults=false 严格拒绝)。owner scope 与映射断言同源
	// （planCtx.targetOwner），失败作为 per-account message 收集。
	if err := s.deps.AssertSupportedModelsInProviderCatalog(ctx, s.store.DB(), source.supportedModels, source.providerCode, planCtx.targetOwner, accountscore.ProtocolPredicate{
		ProviderCode:    source.providerCode,
		ProtocolCode:    source.protocolCode,
		ProtocolVersion: source.protocolVersion,
	}); err != nil {
		source.Push(err.Error())
	}
	if err := s.deps.AssertModelMappingsInProviderCatalog(ctx, s.store.DB(), source.providerCode, planCtx.targetOwner, accountscore.ProtocolPredicate{
		ProviderCode:    source.providerCode,
		ProtocolCode:    source.protocolCode,
		ProtocolVersion: source.protocolVersion,
	}, source.modelMappings, StoredEndpointModes(source.credentials["supported_endpoint_modes"])); err != nil {
		source.Push(err.Error())
	}
	return nil
}

// validateImportGptRequestOverrides mirrors
// validateImportAccountGptRequestOverridesAsync
// (account-import-account-plan.ts): the catalog-backed assertion with the
// import fallback to the provider default supported models; failures land as
// per-account messages exactly like the Node plan helper.
func (s *Service) validateImportGptRequestOverrides(ctx context.Context, source *NormalizedImportAccount, planCtx *importPlanContext) {
	supportedModels := source.supportedModels
	if len(supportedModels) == 0 {
		if provider := planCtx.providers[source.providerCode]; provider != nil {
			supportedModels = provider.defaultSupportedModels
		}
	}
	if err := s.deps.AssertGptRequestOverridesSupported(ctx,
		source.providerCode,
		source.accountType,
		source.credentials,
		supportedModels,
		planCtx.access.EffectiveViewerID(),
	); err != nil {
		source.Push(err.Error())
	}
}

// resolveImportAccountGroup mirrors resolveAccountGroupAsync.
func (s *Service) resolveImportAccountGroup(ctx context.Context, plan *importAccountPlan, planCtx *importPlanContext,
	groupIDsByKey map[string]string, groupNamesToCreate map[string]ImportGroupCreatePlan,
) error {
	source := &plan.source
	item := &plan.item
	if source.groupID != "" && source.groupName != "" {
		item.Warnings = append(item.Warnings, "同时填写 groupId 和 groupName 时优先使用 groupId")
	}
	if source.groupID != "" {
		group, err := s.FindImportGroupByID(ctx, source.groupID, planCtx.access)
		if err != nil {
			return err
		}
		if group == nil {
			item.Messages = append(item.Messages, "分组不存在或无权使用："+source.groupID)
			return nil
		}
		if group.providerCode != source.providerCode {
			item.Messages = append(item.Messages, "分组供应商与账户供应商不一致："+group.name.String)
			return nil
		}
		plan.groupID = group.id
		return nil
	}
	if source.groupName == "" {
		item.Messages = append(item.Messages, "账户 groupId 或 groupName 必填")
		return nil
	}
	key := accountImportGroupKey(source.providerCode, source.groupName)
	if existing, ok := groupIDsByKey[key]; ok {
		plan.groupID = existing
		return nil
	}
	group, err := s.findImportGroupOptionByName(ctx, source.providerCode, source.groupName, planCtx)
	if err != nil {
		return err
	}
	if group != nil {
		groupIDsByKey[key] = group.id
		plan.groupID = group.id
		return nil
	}
	if !planCtx.createMissingGroups() {
		item.Messages = append(item.Messages, "分组不存在："+source.groupName)
		return nil
	}
	groupNamesToCreate[key] = ImportGroupCreatePlan{ProviderCode: source.providerCode, Name: source.groupName}
	return nil
}

// resolveImportAccountProxy mirrors resolveAccountProxyAsync.
func (s *Service) resolveImportAccountProxy(ctx context.Context, plan *importAccountPlan, proxyByRef map[string]*importProxyPlan) error {
	source := &plan.source
	item := &plan.item
	if source.proxyRef != "" && source.proxyProfileID != "" {
		item.Messages = append(item.Messages, "proxyRef 和 proxyProfileId 只能填写一个")
		return nil
	}
	if source.proxyProfileID != "" {
		proxy, err := s.FindImportProxyByID(ctx, source.proxyProfileID)
		if err != nil {
			return err
		}
		if proxy == nil {
			item.Messages = append(item.Messages, "代理不存在："+source.proxyProfileID)
			return nil
		}
		if !proxy.enabled {
			item.Messages = append(item.Messages, "代理已停用："+proxy.name)
			return nil
		}
		plan.proxyProfileID = proxy.id
		return nil
	}
	if source.proxyRef == "" {
		return nil
	}
	if planned, ok := proxyByRef[source.proxyRef]; ok {
		if planned.item.Action == ImportActionFailed {
			item.Messages = append(item.Messages, "代理引用不可用："+source.proxyRef)
		}
		if planned.item.Action == ImportActionSkip {
			item.Messages = append(item.Messages, "代理引用未创建："+source.proxyRef)
		}
		plan.proxyProfileID = planned.proxyProfileID
		return nil
	}
	proxy, err := s.FindImportProxyByID(ctx, source.proxyRef)
	if err != nil {
		return err
	}
	if proxy == nil {
		item.Messages = append(item.Messages, "代理引用不存在："+source.proxyRef)
		return nil
	}
	if !proxy.enabled {
		item.Messages = append(item.Messages, "代理已停用："+proxy.name)
		return nil
	}
	plan.proxyProfileID = proxy.id
	return nil
}

// GroupReference mirrors the facade write.go GroupReference row projection
// (findGroupSummary subset)；仅在本包 FindImportGroupByID → resolveImportAccountGroup
// 链内使用，不跨桥。
type GroupReference struct {
	id              string
	systemAccountID string
	providerCode    string
	name            sql.NullString
}

// FindImportGroupByID mirrors findGroupSummary for the slice subset: own-group
// visibility plus the unscoped admin view (resource authorizations are a
// companion slice).
func (s *Service) FindImportGroupByID(ctx context.Context, groupID string, access accountscore.AccessScope) (*GroupReference, error) {
	scoped := access.ManageableID()
	query := `SELECT id, system_account_id, provider_code, name FROM ` + s.store.Table("groups") + ` WHERE id = ?`
	args := []any{groupID}
	if scoped != "" {
		query += ` AND system_account_id = ?`
		args = append(args, scoped)
	}
	query += ` LIMIT 1`
	var row GroupReference
	err := s.store.DB().QueryRowContext(ctx, s.store.Bind(query), args...).Scan(&row.id, &row.systemAccountID, &row.providerCode, &row.name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// findImportGroupOptionByName mirrors findGroupOptionByNameAsync: the
// manageable owner's groups, exact provider + name match.
func (s *Service) findImportGroupOptionByName(ctx context.Context, providerCode, name string, planCtx *importPlanContext) (*importGroupOption, error) {
	key := accountImportGroupKey(providerCode, name)
	if planCtx != nil && planCtx.groupLookup != nil {
		if existing, ok := planCtx.groupLookup[key]; ok {
			return existing, nil
		}
	}
	normalized := strings.TrimSpace(name)
	owner := ""
	if planCtx != nil {
		owner = planCtx.targetOwner
	}
	var id string
	err := s.store.DB().QueryRowContext(ctx, s.store.Bind(`SELECT id FROM `+s.store.Table("groups")+`
		WHERE system_account_id = ? AND provider_code = ? AND name = ?
		ORDER BY updated_at DESC, id ASC
		LIMIT 1`), owner, providerCode, normalized).Scan(&id)
	var option *importGroupOption
	if err == nil {
		option = &importGroupOption{id: id, name: normalized, providerCode: providerCode}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if planCtx != nil {
		if planCtx.groupLookup == nil {
			planCtx.groupLookup = map[string]*importGroupOption{}
		}
		planCtx.groupLookup[key] = option
	}
	return option, nil
}

// accountImportGroupKey mirrors accountImportGroupKey.
func accountImportGroupKey(providerCode, name string) string {
	return strings.ToLower(strings.TrimSpace(providerCode)) + ":" + strings.TrimSpace(name)
}

// markDuplicateImportItems mirrors markDuplicateAccountImportItems.
func markDuplicateImportItems(accounts []importAccountPlan, skipDuplicates bool) {
	seenName := map[string]int{}
	for index := range accounts {
		plan := &accounts[index]
		if plan.item.Action == ImportActionFailed {
			continue
		}
		nameKey := strings.TrimSpace(plan.source.name)
		if first, ok := seenName[nameKey]; ok {
			if skipDuplicates {
				plan.item.Action = ImportActionSkip
			} else {
				plan.item.Action = ImportActionFailed
			}
			plan.item.Messages = append(plan.item.Messages, fmt.Sprintf("与第 %d 条账户名称重复", first))
		} else {
			seenName[nameKey] = plan.source.index
		}
	}
}

// buildImportSummary mirrors buildAccountImportSummary.
func buildImportSummary(accounts []ImportItem, proxies []ImportProxyItem, groupsToCreate map[string]ImportGroupCreatePlan) ImportSummary {
	groupRefs := map[string]bool{}
	for _, item := range accounts {
		if item.Action == ImportActionFailed {
			continue
		}
		if item.GroupID != nil {
			groupRefs["id:"+*item.GroupID] = true
		} else if item.GroupName != nil {
			provider := ""
			if item.ProviderCode != nil {
				provider = *item.ProviderCode
			}
			groupRefs[accountImportGroupKey(provider, *item.GroupName)] = true
		}
	}
	summary := ImportSummary{
		Accounts: ImportAccountsSummary{Total: len(accounts)},
		Proxies:  ImportProxiesSummary{Total: len(proxies)},
		Groups:   ImportGroupsSummary{Create: len(groupsToCreate)},
	}
	for _, item := range accounts {
		switch item.Action {
		case ImportActionCreate:
			summary.Accounts.Create++
		case ImportActionSkip:
			summary.Accounts.Skip++
		case ImportActionFailed:
			summary.Accounts.Failed++
		}
	}
	for _, item := range proxies {
		switch item.Action {
		case ImportActionCreate:
			summary.Proxies.Create++
		case ImportActionReuse:
			summary.Proxies.Reuse++
		case ImportActionSkip:
			summary.Proxies.Skip++
		case ImportActionFailed:
			summary.Proxies.Failed++
		}
	}
	summary.Groups.Reuse = accountscore.MaxInt(0, len(groupRefs)-len(groupsToCreate))
	return summary
}

func renderImportAccountItems(accounts []importAccountPlan) []ImportItem {
	items := make([]ImportItem, 0, len(accounts))
	for index := range accounts {
		items = append(items, accounts[index].item)
	}
	return items
}

func renderImportProxyItems(proxies []importProxyPlan) []ImportProxyItem {
	items := make([]ImportProxyItem, 0, len(proxies))
	for index := range proxies {
		items = append(items, proxies[index].item)
	}
	return items
}

func TextPointerOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// anySliceOrNil mirrors the facade write.go helper: nil for empty input,
// widened []any otherwise.
func anySliceOrNil(values []string) any {
	if len(values) == 0 {
		return nil
	}
	return accountscore.AnySlice(values)
}

// ---- execution (account-import-executor.ts + resource/account creators) ----

func (s *Service) executeImportPlan(ctx context.Context, plan *ImportPlan, access accountscore.AccessScope) error {
	now := s.store.Now()
	nowISO := accountscore.IsoMillis(now)
	owner, err := access.OwnerID()
	if err != nil {
		return err
	}

	// 1. Proxies: create planned rows, reuse on duplicate names, map refs.
	createdProxyByRef := map[string]string{}
	for index := range plan.proxies {
		proxy := &plan.proxies[index]
		if proxy.item.Action == ImportActionReuse && proxy.proxyProfileID != "" {
			createdProxyByRef[proxy.source.ref] = proxy.proxyProfileID
			continue
		}
		if proxy.item.Action != ImportActionCreate {
			continue
		}
		proxyID, reuse, err := s.createImportProxy(ctx, proxy, owner, nowISO)
		if err != nil {
			proxy.item.Action = ImportActionFailed
			proxy.item.Messages = []string{err.Error()}
			plan.result.Summary.Proxies.Create--
			plan.result.Summary.Proxies.Failed++
			continue
		}
		proxy.proxyProfileID = proxyID
		proxy.item.ProxyProfileID = &proxyID
		if reuse {
			proxy.item.Action = ImportActionReuse
			proxy.item.Messages = []string{"代理名称已存在，已复用现有代理"}
			plan.result.Summary.Proxies.Create--
			plan.result.Summary.Proxies.Reuse++
		} else {
			proxy.item.Messages = []string{"已创建代理"}
		}
		createdProxyByRef[proxy.source.ref] = proxyID
	}

	// 2. Proxy refs created during this run resolve onto their accounts.
	for index := range plan.accounts {
		account := &plan.accounts[index]
		if account.item.Action != ImportActionCreate || account.source.proxyRef == "" || account.proxyProfileID != "" {
			continue
		}
		if proxyID, ok := createdProxyByRef[account.source.proxyRef]; ok {
			account.proxyProfileID = proxyID
		}
	}

	// 3. Accounts whose proxy failed to create fail before any write
	// (failAccountsWithUnresolvedImportProxy).
	proxyPlanByRef := map[string]bool{}
	for index := range plan.proxies {
		proxyPlanByRef[plan.proxies[index].source.ref] = true
	}
	for index := range plan.accounts {
		account := &plan.accounts[index]
		if account.item.Action != ImportActionCreate || account.source.proxyRef == "" || account.proxyProfileID != "" {
			continue
		}
		if !proxyPlanByRef[account.source.proxyRef] {
			continue
		}
		account.item.Action = ImportActionFailed
		account.item.Messages = []string{"代理创建失败，账户未导入：" + account.source.proxyRef}
		plan.result.Summary.Accounts.Create--
		plan.result.Summary.Accounts.Failed++
	}

	// 4. Groups: create planned rows, reuse on duplicate names.
	for key, group := range plan.groupNamesToCreate {
		groupID, reuse, err := s.CreateImportGroup(ctx, group, owner, nowISO)
		if err != nil {
			if reuse {
				plan.groupIDsByKey[key] = groupID
				plan.result.Summary.Groups.Create--
				plan.result.Summary.Groups.Reuse++
				continue
			}
			plan.result.Summary.Groups.Create--
			plan.result.Summary.Groups.Failed++
			for index := range plan.accounts {
				account := &plan.accounts[index]
				if account.source.groupName != "" && account.source.providerCode != "" &&
					accountImportGroupKey(account.source.providerCode, account.source.groupName) == key &&
					account.item.Action == ImportActionCreate {
					account.item.Action = ImportActionFailed
					account.item.Messages = []string{err.Error()}
					plan.result.Summary.Accounts.Create--
					plan.result.Summary.Accounts.Failed++
				}
			}
			continue
		}
		plan.groupIDsByKey[key] = groupID
	}

	// 5. Accounts: create through the shared create path.
	for index := range plan.accounts {
		account := &plan.accounts[index]
		if account.item.Action != ImportActionCreate {
			continue
		}
		groupID := account.groupID
		if groupID == "" && account.source.groupName != "" && account.source.providerCode != "" {
			groupID = plan.groupIDsByKey[accountImportGroupKey(account.source.providerCode, account.source.groupName)]
		}
		input := ImportCreateInput(&account.source, groupID, account.proxyProfileID)
		createdID, createdStatus, err := s.deps.Writer.CreateAccount(ctx, input, access)
		if err != nil {
			if accountscore.DuplicateAccountNameError(err, account.source.name) != nil && PlanSkipDuplicates(plan) {
				account.item.Action = ImportActionSkip
				account.item.Messages = []string{err.Error()}
				plan.result.Summary.Accounts.Create--
				plan.result.Summary.Accounts.Skip++
				continue
			}
			account.item.Action = ImportActionFailed
			account.item.Messages = []string{err.Error()}
			plan.result.Summary.Accounts.Create--
			plan.result.Summary.Accounts.Failed++
			continue
		}
		account.item.AccountID = &createdID
		if createdStatus == "pending_test" {
			account.item.Messages = []string{"已创建账户，等待后台健康检查通过后参与调度"}
		} else {
			account.item.Messages = []string{"已创建账户"}
		}
	}

	plan.result.Accounts = renderImportAccountItems(plan.accounts)
	plan.result.Proxies = renderImportProxyItems(plan.proxies)
	plan.result.Imported = true
	plan.result.CanImport = false
	return nil
}

// PlanSkipDuplicates mirrors PlanSkipDuplicates (facade tests call it directly).
func PlanSkipDuplicates(plan *ImportPlan) bool {
	_, _, duplicates := plan.Options.resolve()
	return duplicates
}

// createImportProxy mirrors createPlannedImportProxies' create branch: the
// insert, with the duplicate-name reuse fallback signaled through reuse.
func (s *Service) createImportProxy(ctx context.Context, proxy *importProxyPlan, ownerID, nowISO string) (string, bool, error) {
	proxyID := s.deps.NewResourceID("proxy")
	var sealedPassword sql.NullString
	if proxy.source.password != "" {
		sealed, err := accountscore.EncryptJSON(s.store.Secret(), accountscore.Credentials{"password": proxy.source.password})
		if err != nil {
			return "", false, err
		}
		sealedPassword = sql.NullString{String: sealed, Valid: true}
	}
	nullableText := func(value string) sql.NullString {
		if value == "" {
			return sql.NullString{}
		}
		return sql.NullString{String: value, Valid: true}
	}
	_, err := s.store.DB().ExecContext(ctx, s.store.Bind(`INSERT INTO `+s.store.Table("proxy_profiles")+`
		(id, system_account_id, name, description, type, host, port, username, password_encrypted,
		 enabled, test_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'unknown', ?, ?)`),
		proxyID, ownerID, proxy.source.name, nullableText(proxy.source.description),
		proxy.source.proxyType, proxy.source.host, proxy.source.port,
		nullableText(proxy.source.username), sealedPassword,
		accountscore.BoolInt(proxy.source.enabled), nowISO, nowISO)
	if err == nil {
		return proxyID, false, nil
	}
	if duplicate := DuplicateProxyNameError(err, proxy.source.name); duplicate != nil {
		existing := s.findImportProxyOptionByName(ctx, proxy.source.name, nil)
		if existing != nil {
			return existing.id, true, nil
		}
		return "", false, duplicate
	}
	return "", false, err
}

// CreateImportGroup mirrors createPlannedImportGroups' create branch: the
// insert, with the duplicate-name reuse fallback signaled through reuse.
func (s *Service) CreateImportGroup(ctx context.Context, group ImportGroupCreatePlan, ownerID, nowISO string) (string, bool, error) {
	groupID := s.deps.NewResourceID("grp")
	_, err := s.store.DB().ExecContext(ctx, s.store.Bind(`INSERT INTO `+s.store.Table("groups")+`
		(id, system_account_id, name, provider_code, description, enabled, is_default, group_type,
		 scheduling_policy_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, '由账户导入自动创建', 1, 0, 'personal', NULL, ?, ?)`),
		groupID, ownerID, group.Name, group.ProviderCode, nowISO, nowISO)
	if err == nil {
		return groupID, false, nil
	}
	if duplicate := DuplicateImportGroupNameError(err, group.Name); duplicate != nil {
		existing, lookupErr := s.findImportGroupOptionByName(ctx, group.ProviderCode, group.Name, nil)
		if lookupErr == nil && existing != nil {
			return existing.id, true, nil
		}
		return "", false, duplicate
	}
	return "", false, err
}

// ImportCreateInput mirrors buildAccountImportCreatePayload + the create
// status remap (active imports start as pending_test for the initial health
// check).
func ImportCreateInput(source *NormalizedImportAccount, groupID, proxyProfileID string) accountscore.CreateInput {
	input := accountscore.CreateInput{
		ProviderCode:              source.providerCode,
		ProviderProtocolProfileID: source.providerProtocolProfileID,
		Name:                      source.name,
		AccountType:               source.accountType,
		Credentials:               source.credentials,
		SupportedModels:           source.supportedModels,
		ModelMappings:             source.modelMappings,
		Tags:                      source.tags,
	}
	status := source.status
	if status == "active" {
		status = "pending_test"
	}
	input.Status = accountscore.AccountCreationStatusInput(status)
	if source.concurrencyLimit > 0 {
		limit := source.concurrencyLimit
		input.ConcurrencyLimit = &limit
	}
	if source.priority >= 0 {
		priority := source.priority
		input.Priority = &priority
	}
	if source.superPriorityEnabled != nil {
		input.SuperPriorityEnabled = source.superPriorityEnabled
	}
	if source.fallbackEnabled != nil {
		input.FallbackEnabled = source.fallbackEnabled
	}
	if source.healthCheckModel != "" {
		text := source.healthCheckModel
		input.HealthCheckModel = &text
	}
	if source.healthCheckEndpointMode != "" {
		text := source.healthCheckEndpointMode
		input.HealthCheckEndpointMode = &text
	}
	if groupID != "" {
		input.GroupID = &groupID
	}
	if proxyProfileID != "" {
		input.ProxyProfileID = &proxyProfileID
	}
	if source.accountExpiresAt != "" {
		text := source.accountExpiresAt
		input.AccountExpiresAt = &text
	}
	input.AvailabilitySchedule = source.availabilityScheduleRaw
	if source.notes != "" {
		text := source.notes
		input.Notes = &text
	}
	return input
}

// DuplicateProxyNameError mirrors isDuplicateProxyNameError.
func DuplicateProxyNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "UNIQUE constraint failed") && strings.Contains(message, "proxy_profiles") && strings.Contains(message, "name") {
		return &accountscore.ConflictError{Message: "代理名称已存在：" + name}
	}
	return nil
}

// DuplicateImportGroupNameError mirrors isDuplicateGroupNameError.
func DuplicateImportGroupNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "idx_groups_owner_provider_name_unique") ||
		strings.Contains(message, "idx_groups_owner_provider_name_unique_lower") ||
		strings.Contains(message, "UNIQUE constraint failed: groups.system_account_id, groups.provider_code, groups.name") ||
		strings.Contains(message, "UNIQUE constraint failed: juhe_business.groups.system_account_id, juhe_business.groups.provider_code, juhe_business.groups.name") {
		return &accountscore.ConflictError{Message: "同一供应商下分组名称已存在：" + name}
	}
	return nil
}
