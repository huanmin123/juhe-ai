package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	importActionCreate = "create"
	importActionReuse  = "reuse"
	importActionSkip   = "skip"
	importActionFailed = "failed"
)

// ---- request payload (accountImportRequestSchema.strict()) ----

type importRequest struct {
	Data       any
	SourceMode string
	Options    ImportOptions
}

func parseImportBody(body map[string]any) (importRequest, bool) {
	for key := range body {
		switch key {
		case "data", "sourceMode", "options":
		default:
			return importRequest{}, false
		}
	}
	request := importRequest{}
	if value, exists := body["data"]; exists {
		request.Data = value
	}
	if value, exists := body["sourceMode"]; exists && value != nil {
		text, ok := value.(string)
		if !ok || !importSourceModes[text] {
			return importRequest{}, false
		}
		request.SourceMode = text
	}
	if value, exists := body["options"]; exists && value != nil {
		record, ok := value.(map[string]any)
		if !ok {
			return importRequest{}, false
		}
		for key := range record {
			switch key {
			case "createMissingGroups", "createMissingProxies", "skipDuplicates":
			default:
				return importRequest{}, false
			}
		}
		if raw, exists := record["createMissingGroups"]; exists && raw != nil {
			enabled, ok := raw.(bool)
			if !ok {
				return importRequest{}, false
			}
			request.Options.CreateMissingGroups = &enabled
		}
		if raw, exists := record["createMissingProxies"]; exists && raw != nil {
			enabled, ok := raw.(bool)
			if !ok {
				return importRequest{}, false
			}
			request.Options.CreateMissingProxies = &enabled
		}
		if raw, exists := record["skipDuplicates"]; exists && raw != nil {
			enabled, ok := raw.(bool)
			if !ok {
				return importRequest{}, false
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

type importProvider struct {
	code                   string
	enabled                bool
	defaultSupportedModels []string
	profiles               map[string]*importProviderProfile
}

func (s *Store) loadImportProviders(ctx context.Context) (map[string]*importProvider, error) {
	providers := map[string]*importProvider{}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT code, enabled, default_supported_models_json
		FROM `+s.table("providers")))
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
		provider := &importProvider{code: code, enabled: enabled == 1, profiles: map[string]*importProviderProfile{}}
		if strings.TrimSpace(defaultsJSON) != "" {
			_ = json.Unmarshal([]byte(defaultsJSON), &provider.defaultSupportedModels)
		}
		providers[code] = provider
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	profileRows, err := s.db.QueryContext(ctx, s.bind(`SELECT id, provider_code, name, enabled,
			protocol_code, protocol_version, account_types_json
		FROM `+s.table("provider_protocol_profiles")))
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

type importGroupCreatePlan struct {
	providerCode string
	name         string
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

type normalizedImportAccount struct {
	index                     int
	ref                       string
	name                      string
	providerCode              string
	providerProtocolProfileID string
	protocolCode              string
	protocolVersion           string
	accountType               string
	status                    string
	credentials               Credentials
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
	modelMappings             []ModelMapping
	tags                      []string
	accountExpiresAt          string
	availabilityScheduleRaw   any
	notes                     string

	// messages aliases the plan item's message list (Node aliases
	// item.messages into the normalized source).
	messages *[]string
}

func (a *normalizedImportAccount) push(message string) {
	if a.messages == nil {
		return
	}
	*a.messages = append(*a.messages, message)
}

type importAccountPlan struct {
	source         normalizedImportAccount
	item           ImportItem
	groupID        string
	proxyProfileID string
}

type importPlan struct {
	result             *ImportResult
	accounts           []importAccountPlan
	proxies            []importProxyPlan
	groupIDsByKey      map[string]string
	groupNamesToCreate map[string]importGroupCreatePlan
	options            ImportOptions
}

// importPlanContext carries the shared lookup state (providers, options,
// access) across the planning stages.
type importPlanContext struct {
	store       *Store
	access      AccessScope
	targetOwner string
	providers   map[string]*importProvider
	options     ImportOptions

	groupLookup map[string]*importGroupOption
	proxyLookup map[string]*importProxyOption
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

type importProxyOption struct {
	id      string
	name    string
	enabled bool
}

func importTargetOwner(access AccessScope) (string, error) {
	// importTargetSystemAccountId: manageableSystemAccountId ?? the caller id.
	owner := access.manageableID()
	if owner == "" {
		owner = access.ViewerID
	}
	if strings.TrimSpace(owner) == "" {
		return "", &ValidationError{Message: "缺少系统账户上下文"}
	}
	return owner, nil
}

// PreviewImport mirrors previewAccountImportAsync: the adapted source document
// is validated and planned without any write.
func (s *Store) PreviewImport(ctx context.Context, data any, sourceMode string, options ImportOptions, access AccessScope) (*ImportResult, error) {
	ctx = ensureCtx(ctx)
	plan, err := s.buildImportPlan(ctx, data, sourceMode, options, access)
	if err != nil {
		return nil, err
	}
	return plan.result, nil
}

// ExecuteImport mirrors executeAccountImportAsync: plan, then execute the
// resource/account creators. Per-item failures are rendered into the result;
// store-level failures surface as errors (the route maps them to 400).
func (s *Store) ExecuteImport(ctx context.Context, data any, sourceMode string, options ImportOptions, access AccessScope) (*ImportResult, error) {
	ctx = ensureCtx(ctx)
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

func (s *Store) buildImportPlan(ctx context.Context, data any, sourceMode string, rawOptions ImportOptions, access AccessScope) (*importPlan, error) {
	if strings.TrimSpace(sourceMode) == "" {
		sourceMode = importSourceNative
	}
	if !importSourceModes[sourceMode] {
		return nil, &ValidationError{Message: "账户导入参数无效"}
	}
	owner, err := importTargetOwner(access)
	if err != nil {
		return nil, err
	}
	providers, err := s.loadImportProviders(ctx)
	if err != nil {
		return nil, err
	}

	result := &ImportResult{
		Type: accountImportProtocolType, Version: accountImportProtocolVersion, Mode: "preview",
		Accounts: []ImportItem{}, Proxies: []ImportProxyItem{}, Messages: []string{},
	}
	emptyPlan := &importPlan{
		result: result, groupIDsByKey: map[string]string{},
		groupNamesToCreate: map[string]importGroupCreatePlan{}, options: rawOptions,
	}

	adaptedData, source := adaptImportSource(data, sourceMode)
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
	groupNamesToCreate := map[string]importGroupCreatePlan{}
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

	return &importPlan{
		result: result, accounts: accounts, proxies: proxyPlans,
		groupIDsByKey: groupIDsByKey, groupNamesToCreate: groupNamesToCreate,
		options: rawOptions,
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
	if text, isText := record["type"].(string); !isText || text != accountImportProtocolType {
		*messages = append(*messages, "type 必须是 "+accountImportProtocolType)
	}
	if version, isNumber := record["version"].(float64); !isNumber || version != float64(accountImportProtocolVersion) {
		*messages = append(*messages, fmt.Sprintf("version 必须是 %d", accountImportProtocolVersion))
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
	if len(rawAccounts) > maxImportedAccounts {
		*messages = append(*messages, fmt.Sprintf("accounts 单次最多导入 %d 条", maxImportedAccounts))
		return nil, nil, false
	}
	if len(rawProxies) > maxImportedProxies {
		*messages = append(*messages, fmt.Sprintf("proxies 单次最多导入 %d 条", maxImportedProxies))
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
	sortStrings(unknown)
	if len(unknown) > 0 {
		*messages = append(*messages, label+"包含未知字段："+strings.Join(unknown, "、"))
	}
}

// ---- field parser (account-import-field-parser.ts) ----

func importOptionalTextField(record map[string]any, key, label string, messages *[]string) string {
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

func importOptionalBooleanField(record map[string]any, key, label string, messages *[]string) *bool {
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

func importOptionalPositiveIntegerField(record map[string]any, key, label string, messages *[]string) int {
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

func importOptionalNonNegativeIntegerField(record map[string]any, key, label string, messages *[]string) int {
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

func importOptionalStringArrayField(record map[string]any, key, label string, messages *[]string) []string {
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

func importOptionalHealthCheckEndpointModeField(record map[string]any, key, label string, messages *[]string) string {
	value, exists := record[key]
	if !exists {
		return ""
	}
	text, ok := value.(string)
	if ok && accountHealthCheckEndpointModes[text] {
		return text
	}
	*messages = append(*messages, label+"必须是支持的 JSON 或 Streaming 请求形态")
	return ""
}

func importOptionalTagsField(record map[string]any, key, label string, messages *[]string) []string {
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
		if len([]rune(tagName)) > maxTagNameLength {
			*messages = append(*messages, label+"单个标签不能超过 40 个字符")
			return nil
		}
		if seen[tagName] {
			continue
		}
		seen[tagName] = true
		items = append(items, tagName)
	}
	if len(items) > maxTagsPerAccount {
		*messages = append(*messages, label+"单个账户最多配置 24 个标签")
		return nil
	}
	return items
}

func importOptionalDateTimeField(record map[string]any, key, label string, messages *[]string) string {
	value, exists := record[key]
	if !exists {
		return ""
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		*messages = append(*messages, label+"必须是有效时间字符串")
		return ""
	}
	canonical, valid := canonicalRFC3339(text)
	if !valid {
		*messages = append(*messages, label+"必须是有效时间字符串")
		return ""
	}
	return canonical
}

func importModelMappingsField(record map[string]any, key, label string, messages *[]string) []ModelMapping {
	value, exists := record[key]
	if !exists {
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		*messages = append(*messages, label+"必须是模型映射数组")
		return nil
	}
	output := []ModelMapping{}
	seenSources := map[string]bool{}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			*messages = append(*messages, label+"条目必须是对象")
			return nil
		}
		sourceModel := importMappingText(entry["sourceModel"])
		sourceFamily := importMappingSourceFamily(entry["sourceEndpointFamily"])
		upstreamModel := importMappingText(entry["upstreamModel"])
		upstreamFamily := importMappingUpstreamFamily(entry["upstreamEndpointFamily"])
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
		output = append(output, ModelMapping{
			SourceModel: sourceModel, SourceEndpointFamily: sourceFamily,
			UpstreamModel: upstreamModel, UpstreamEndpointFamily: upstreamFamily,
			Enabled: &enabled,
		})
	}
	return output
}

func importMappingText(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func importMappingSourceFamily(value any) string {
	switch text := value.(type) {
	case string:
		switch text {
		case "chat_completions", "responses", "messages", "generate_content", "stream_generate_content":
			return text
		}
	}
	return ""
}

func importMappingUpstreamFamily(value any) string {
	switch text := value.(type) {
	case string:
		switch text {
		case "chat_completions", "responses", "messages", "generate_content":
			return text
		}
	}
	return ""
}

func normalizeImportStatus(value any) string {
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

func normalizeImportProxyType(value any) string {
	text := strings.ToLower(sourceText(value))
	switch text {
	case "http", "https", "socks5", "socks5h":
		return text
	default:
		return ""
	}
}

func (s *Store) planImportProxies(ctx context.Context, rawProxies []any, planCtx *importPlanContext) ([]importProxyPlan, map[string]*importProxyPlan) {
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
			plans[index].item.Action = importActionFailed
			plans[index].item.Messages = append(plans[index].item.Messages, "代理 ref 重复："+ref)
			continue
		}
		proxyByRef[ref] = &plans[index]
	}
	return plans, proxyByRef
}

func (s *Store) planImportProxy(ctx context.Context, value any, index int, planCtx *importPlanContext) importProxyPlan {
	plan := importProxyPlan{
		source: normalizedImportProxy{index: index, enabled: true},
		item:   ImportProxyItem{Index: index, Action: importActionCreate, Messages: []string{}, Warnings: []string{}},
	}
	record, ok := value.(map[string]any)
	if !ok {
		plan.item.Action = importActionFailed
		plan.item.Messages = append(plan.item.Messages, "代理配置必须是对象")
		return plan
	}
	messages := &plan.item.Messages
	appendUnknownImportFields(record, importProxyKeys, "代理配置", messages)
	plan.source.ref = importOptionalTextField(record, "ref", "代理 ref", messages)
	plan.source.name = importOptionalTextField(record, "name", "代理名称", messages)
	proxyTypeInput := importOptionalTextField(record, "type", "代理 type", messages)
	if proxyTypeInput != "" {
		plan.source.proxyType = normalizeImportProxyType(proxyTypeInput)
		if plan.source.proxyType == "" {
			plan.item.Messages = append(plan.item.Messages, "代理 type 不支持："+proxyTypeInput)
		}
	}
	plan.source.host = importOptionalTextField(record, "host", "代理 host", messages)
	plan.source.port = importOptionalPositiveIntegerField(record, "port", "代理 port", messages)
	plan.source.username = importOptionalTextField(record, "username", "代理 username", messages)
	plan.source.password = importOptionalTextField(record, "password", "代理 password", messages)
	plan.source.description = importOptionalTextField(record, "description", "代理 description", messages)
	if enabled := importOptionalBooleanField(record, "enabled", "代理 enabled", messages); enabled != nil {
		plan.source.enabled = *enabled
	}
	plan.item.Ref = textPointerOrNil(plan.source.ref)
	plan.item.Name = textPointerOrNil(plan.source.name)

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
		plan.item.Action = importActionReuse
		plan.item.ProxyProfileID = &existing.id
	} else if !canCreateImportProxy(planCtx, &plan.item) {
		if len(plan.item.Messages) > 0 {
			plan.item.Action = importActionFailed
		} else {
			plan.item.Action = importActionSkip
		}
	}
	if len(plan.item.Messages) > 0 {
		plan.item.Action = importActionFailed
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
func (s *Store) findImportProxyOptionByName(ctx context.Context, name string, planCtx *importPlanContext) *importProxyOption {
	key := strings.TrimSpace(name)
	if key == "" {
		return nil
	}
	if planCtx.proxyLookup != nil {
		if existing, ok := planCtx.proxyLookup[key]; ok {
			return existing
		}
	}
	var id string
	var enabled int64
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, enabled FROM `+s.table("proxy_profiles")+`
		WHERE name = ? AND enabled = 1
		ORDER BY updated_at DESC, id ASC
		LIMIT 1`), key).Scan(&id, &enabled)
	var option *importProxyOption
	if err == nil {
		option = &importProxyOption{id: id, name: key, enabled: enabled == 1}
	} else if !errors.Is(err, sql.ErrNoRows) {
		option = nil
	}
	if planCtx == nil {
		return option
	}
	if planCtx.proxyLookup == nil {
		planCtx.proxyLookup = map[string]*importProxyOption{}
	}
	planCtx.proxyLookup[key] = option
	return option
}

// findImportProxyByID mirrors findProxy: global id lookup, enabled is checked
// by the caller.
func (s *Store) findImportProxyByID(ctx context.Context, id string) (*importProxyOption, error) {
	var row struct {
		id      string
		name    string
		enabled int64
	}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id, name, enabled FROM `+s.table("proxy_profiles")+`
		WHERE id = ? LIMIT 1`), id).Scan(&row.id, &row.name, &row.enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &importProxyOption{id: row.id, name: row.name, enabled: row.enabled == 1}, nil
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

func (s *Store) planImportAccount(ctx context.Context, value any, index int, planCtx *importPlanContext,
	proxyByRef map[string]*importProxyPlan, groupIDsByKey map[string]string, groupNamesToCreate map[string]importGroupCreatePlan,
) (importAccountPlan, error) {
	plan := importAccountPlan{
		item: ImportItem{Index: index, Action: importActionCreate, Messages: []string{}, Warnings: []string{}},
	}
	source := &plan.source
	source.index = index
	source.accountType = "api_key"
	source.status = "active"
	source.credentials = Credentials{}
	source.priority = -1
	source.messages = &plan.item.Messages

	record, ok := value.(map[string]any)
	if !ok {
		plan.item.Action = importActionFailed
		plan.item.Messages = append(plan.item.Messages, "账户配置必须是对象")
		return plan, nil
	}
	messages := &plan.item.Messages
	appendUnknownImportFields(record, importAccountKeys, "账户配置", messages)
	source.ref = importOptionalTextField(record, "ref", "账户 ref", messages)
	source.name = importOptionalTextField(record, "name", "账户名称", messages)
	source.providerCode = importOptionalTextField(record, "providerCode", "账户 providerCode", messages)
	source.providerProtocolProfileID = importOptionalTextField(record, "providerProtocolProfileId", "账户 providerProtocolProfileId", messages)
	if source.providerCode == "" {
		source.push("账户 providerCode 不能为空")
	}
	if typeInput := importOptionalTextField(record, "type", "账户 type", messages); typeInput != "" {
		source.accountType = typeInput
	} else {
		source.push("账户 type 不能为空")
	}
	if rawStatus := importOptionalTextField(record, "status", "账户 status", messages); rawStatus != "" {
		if normalized := normalizeImportStatus(rawStatus); normalized != "" {
			source.status = normalized
		} else {
			source.push("账户状态不支持：" + rawStatus)
		}
	} else {
		source.push("账户 status 不能为空")
	}
	source.groupID = importOptionalTextField(record, "groupId", "账户 groupId", messages)
	source.groupName = importOptionalTextField(record, "groupName", "账户 groupName", messages)
	source.proxyRef = importOptionalTextField(record, "proxyRef", "账户 proxyRef", messages)
	source.proxyProfileID = importOptionalTextField(record, "proxyProfileId", "账户 proxyProfileId", messages)
	source.concurrencyLimit = importOptionalPositiveIntegerField(record, "concurrencyLimit", "账户 concurrencyLimit", messages)
	source.priority = importOptionalNonNegativeIntegerField(record, "priority", "账户 priority", messages)
	source.superPriorityEnabled = importOptionalBooleanField(record, "superPriorityEnabled", "账户 superPriorityEnabled", messages)
	source.fallbackEnabled = importOptionalBooleanField(record, "fallbackEnabled", "账户 fallbackEnabled", messages)
	source.supportedModels = importOptionalStringArrayField(record, "supportedModels", "账户 supportedModels", messages)
	source.healthCheckModel = importOptionalTextField(record, "healthCheckModel", "账户 healthCheckModel", messages)
	source.healthCheckEndpointMode = importOptionalHealthCheckEndpointModeField(record, "healthCheckEndpointMode", "账户 healthCheckEndpointMode", messages)
	// Parsed for validation parity only; the column default owns the effective
	// value in this slice.
	importOptionalBooleanField(record, "temporaryUnavailableContinuousProbeEnabled", "账户 temporaryUnavailableContinuousProbeEnabled", messages)
	source.modelMappings = importModelMappingsField(record, "modelMappings", "账户 modelMappings", messages)
	source.tags = importOptionalTagsField(record, "tags", "账户 tags", messages)
	source.accountExpiresAt = importOptionalDateTimeField(record, "accountExpiresAt", "账户 accountExpiresAt", messages)
	if rawSchedule, exists := record["availabilitySchedule"]; exists {
		source.availabilityScheduleRaw = rawSchedule
		if _, err := NormalizeSchedule(rawSchedule); err != nil {
			source.push(accountScheduleError(err).Error())
		}
	}
	source.notes = importOptionalTextField(record, "notes", "账户 notes", messages)
	if credentials, ok := record["credentials"].(map[string]any); ok {
		source.credentials = Credentials(credentials)
	}

	applyImportAccountProtocolProfileDefaults(source, planCtx.providers)

	plan.item.Ref = textPointerOrNil(source.ref)
	plan.item.Name = textPointerOrNil(source.name)
	plan.item.ProviderCode = textPointerOrNil(source.providerCode)
	plan.item.GroupName = textPointerOrNil(source.groupName)
	plan.item.GroupID = textPointerOrNil(source.groupID)
	plan.item.ProxyRef = textPointerOrNil(source.proxyRef)

	validateImportProviderAndBasics(source, planCtx.providers)
	plan.item.ProviderProtocolProfileID = textPointerOrNil(source.providerProtocolProfileID)
	plan.item.ProtocolCode = textPointerOrNil(source.protocolCode)
	plan.item.ProtocolVersion = textPointerOrNil(source.protocolVersion)
	plan.item.AccountType = textPointerOrNil(source.accountType)

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
		plan.item.Action = importActionFailed
	}
	return plan, nil
}

// applyImportAccountProtocolProfileDefaults mirrors
// applyImportAccountProtocolProfileDefaults: the requested profile is resolved
// against the provider and the protocol columns are pinned.
func applyImportAccountProtocolProfileDefaults(source *normalizedImportAccount, providers map[string]*importProvider) {
	provider := providers[source.providerCode]
	if provider == nil {
		return
	}
	requestedProfileID := strings.TrimSpace(source.providerProtocolProfileID)
	if requestedProfileID == "" {
		source.push("账户 providerProtocolProfileId 不能为空")
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
func (s *Store) normalizeImportAccountCredentials(source *normalizedImportAccount) {
	clientCompatibility, err := normalizeOpenAIAccountClientCompatibility(
		source.providerCode,
		source.accountType,
		"",
		protocolProfileRef{
			ProviderCode:              source.providerCode,
			ProtocolCode:              source.protocolCode,
			ProtocolVersion:           source.protocolVersion,
			ProviderProtocolProfileID: source.providerProtocolProfileID,
		},
	)
	if err != nil {
		source.push(err.Error())
		return
	}
	normalized, err := NormalizeAccountCredentialsForWrite(source.accountType, source.credentials, &EndpointModeDefaultContext{
		ProviderCode:              source.providerCode,
		AccountType:               source.accountType,
		ClientCompatibility:       clientCompatibility,
		ProviderProtocolProfileID: source.providerProtocolProfileID,
		ProtocolCode:              source.protocolCode,
		ProtocolVersion:           source.protocolVersion,
	})
	if err != nil {
		source.push(err.Error())
		return
	}
	source.credentials = normalized
}

// validateImportProviderAndBasics mirrors validateImportProviderAndBasics.
func validateImportProviderAndBasics(source *normalizedImportAccount, providers map[string]*importProvider) {
	if source.name == "" {
		source.push("账户名称不能为空")
	}
	provider := providers[source.providerCode]
	if provider == nil {
		source.push("不支持的供应商：" + source.providerCode)
	} else if !provider.enabled {
		source.push("供应商已停用：" + source.providerCode)
	} else if profile := resolveImportAccountProtocolProfile(source, provider); profile != nil && !containsString(profile.accountTypes, source.accountType) {
		source.push("供应商协议档案 " + profile.name + " 不支持账户类型 " + source.accountType)
	}
	if source.status != "active" && source.status != "pending_test" && source.status != "disabled" {
		source.push("账户状态仅支持 active、pending_test 或 disabled")
	}
	if source.concurrencyLimit != 0 && source.concurrencyLimit < 1 {
		source.push("concurrencyLimit 必须大于 0")
	}
	if source.accountExpiresAt != "" {
		if _, valid := canonicalRFC3339(source.accountExpiresAt); !valid {
			source.push("accountExpiresAt 必须是有效时间字符串")
		}
	}
}

// resolveImportAccountProtocolProfile mirrors resolveImportAccountProtocolProfile.
func resolveImportAccountProtocolProfile(source *normalizedImportAccount, provider *importProvider) *importProviderProfile {
	requestedProfileID := strings.TrimSpace(source.providerProtocolProfileID)
	if requestedProfileID == "" {
		source.push("账户 providerProtocolProfileId 不能为空")
		return nil
	}
	profile := provider.profiles[requestedProfileID]
	if profile == nil {
		source.push("供应商 " + source.providerCode + " 未配置协议档案")
		return nil
	}
	if profile.providerCode != source.providerCode {
		source.push("协议档案 " + profile.id + " 不属于供应商 " + source.providerCode)
		return nil
	}
	if !profile.enabled {
		source.push("供应商协议档案已停用：" + profile.name)
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
func (s *Store) validateImportModelCatalogFields(ctx context.Context, source *normalizedImportAccount, planCtx *importPlanContext) error {
	if source.providerCode == "" || planCtx.providers[source.providerCode] == nil || planCtx.targetOwner == "" {
		return nil
	}
	provider := planCtx.providers[source.providerCode]
	input := source.supportedModels
	if len(input) == 0 {
		input = provider.defaultSupportedModels
	}
	models, err := normalizeSupportedModelsInput(anySliceOrNil(input))
	if err != nil {
		source.push(err.Error())
		return nil
	}
	source.supportedModels = models
	if err := assertSupportedModelsRequired(source.supportedModels); err != nil {
		source.push(err.Error())
		return nil
	}
	if source.healthCheckModel != "" && !containsString(source.supportedModels, source.healthCheckModel) {
		source.push("账户 healthCheckModel 必须属于 supportedModels")
	}
	if err := assertMappingUpstreamsAllowed(source.modelMappings, source.supportedModels); err != nil {
		source.push(err.Error())
	}
	if err := s.assertAccountModelMappingsInProviderCatalog(ctx, s.db, source.providerCode, planCtx.targetOwner, protocolPredicateInput{
		providerCode:    source.providerCode,
		protocolCode:    source.protocolCode,
		protocolVersion: source.protocolVersion,
	}, source.modelMappings); err != nil {
		source.push(err.Error())
	}
	return nil
}

// validateImportGptRequestOverrides mirrors
// validateImportAccountGptRequestOverridesAsync
// (account-import-account-plan.ts): the catalog-backed assertion with the
// import fallback to the provider default supported models; failures land as
// per-account messages exactly like the Node plan helper.
func (s *Store) validateImportGptRequestOverrides(ctx context.Context, source *normalizedImportAccount, planCtx *importPlanContext) {
	supportedModels := source.supportedModels
	if len(supportedModels) == 0 {
		if provider := planCtx.providers[source.providerCode]; provider != nil {
			supportedModels = provider.defaultSupportedModels
		}
	}
	if err := s.assertAccountGptRequestOverridesSupported(ctx, accountGptRequestOverridesInput{
		ProviderCode:    source.providerCode,
		AccountType:     source.accountType,
		Credentials:     source.credentials,
		SupportedModels: supportedModels,
		SystemAccountID: planCtx.access.viewerID(),
	}); err != nil {
		source.push(err.Error())
	}
}

// resolveImportAccountGroup mirrors resolveAccountGroupAsync.
func (s *Store) resolveImportAccountGroup(ctx context.Context, plan *importAccountPlan, planCtx *importPlanContext,
	groupIDsByKey map[string]string, groupNamesToCreate map[string]importGroupCreatePlan,
) error {
	source := &plan.source
	item := &plan.item
	if source.groupID != "" && source.groupName != "" {
		item.Warnings = append(item.Warnings, "同时填写 groupId 和 groupName 时优先使用 groupId")
	}
	if source.groupID != "" {
		group, err := s.findImportGroupByID(ctx, source.groupID, planCtx.access)
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
	groupNamesToCreate[key] = importGroupCreatePlan{providerCode: source.providerCode, name: source.groupName}
	return nil
}

// resolveImportAccountProxy mirrors resolveAccountProxyAsync.
func (s *Store) resolveImportAccountProxy(ctx context.Context, plan *importAccountPlan, proxyByRef map[string]*importProxyPlan) error {
	source := &plan.source
	item := &plan.item
	if source.proxyRef != "" && source.proxyProfileID != "" {
		item.Messages = append(item.Messages, "proxyRef 和 proxyProfileId 只能填写一个")
		return nil
	}
	if source.proxyProfileID != "" {
		proxy, err := s.findImportProxyByID(ctx, source.proxyProfileID)
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
		if planned.item.Action == importActionFailed {
			item.Messages = append(item.Messages, "代理引用不可用："+source.proxyRef)
		}
		if planned.item.Action == importActionSkip {
			item.Messages = append(item.Messages, "代理引用未创建："+source.proxyRef)
		}
		plan.proxyProfileID = planned.proxyProfileID
		return nil
	}
	proxy, err := s.findImportProxyByID(ctx, source.proxyRef)
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

// findImportGroupByID mirrors findGroupSummary for the slice subset: own-group
// visibility plus the unscoped admin view (resource authorizations are a
// companion slice).
func (s *Store) findImportGroupByID(ctx context.Context, groupID string, access AccessScope) (*groupReference, error) {
	scoped := access.manageableID()
	query := `SELECT id, system_account_id, provider_code, name FROM ` + s.table("groups") + ` WHERE id = ?`
	args := []any{groupID}
	if scoped != "" {
		query += ` AND system_account_id = ?`
		args = append(args, scoped)
	}
	query += ` LIMIT 1`
	var row groupReference
	err := s.db.QueryRowContext(ctx, s.bind(query), args...).Scan(&row.id, &row.systemAccountID, &row.providerCode, &row.name)
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
func (s *Store) findImportGroupOptionByName(ctx context.Context, providerCode, name string, planCtx *importPlanContext) (*importGroupOption, error) {
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
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id FROM `+s.table("groups")+`
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
		if plan.item.Action == importActionFailed {
			continue
		}
		nameKey := strings.TrimSpace(plan.source.name)
		if first, ok := seenName[nameKey]; ok {
			if skipDuplicates {
				plan.item.Action = importActionSkip
			} else {
				plan.item.Action = importActionFailed
			}
			plan.item.Messages = append(plan.item.Messages, fmt.Sprintf("与第 %d 条账户名称重复", first))
		} else {
			seenName[nameKey] = plan.source.index
		}
	}
}

// buildImportSummary mirrors buildAccountImportSummary.
func buildImportSummary(accounts []ImportItem, proxies []ImportProxyItem, groupsToCreate map[string]importGroupCreatePlan) ImportSummary {
	groupRefs := map[string]bool{}
	for _, item := range accounts {
		if item.Action == importActionFailed {
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
		case importActionCreate:
			summary.Accounts.Create++
		case importActionSkip:
			summary.Accounts.Skip++
		case importActionFailed:
			summary.Accounts.Failed++
		}
	}
	for _, item := range proxies {
		switch item.Action {
		case importActionCreate:
			summary.Proxies.Create++
		case importActionReuse:
			summary.Proxies.Reuse++
		case importActionSkip:
			summary.Proxies.Skip++
		case importActionFailed:
			summary.Proxies.Failed++
		}
	}
	summary.Groups.Reuse = maxInt(0, len(groupRefs)-len(groupsToCreate))
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

func textPointerOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// ---- execution (account-import-executor.ts + resource/account creators) ----

func (s *Store) executeImportPlan(ctx context.Context, plan *importPlan, access AccessScope) error {
	now := s.now()
	nowISO := isoMillis(now)
	owner, err := access.ownerID()
	if err != nil {
		return err
	}

	// 1. Proxies: create planned rows, reuse on duplicate names, map refs.
	createdProxyByRef := map[string]string{}
	for index := range plan.proxies {
		proxy := &plan.proxies[index]
		if proxy.item.Action == importActionReuse && proxy.proxyProfileID != "" {
			createdProxyByRef[proxy.source.ref] = proxy.proxyProfileID
			continue
		}
		if proxy.item.Action != importActionCreate {
			continue
		}
		proxyID, reuse, err := s.createImportProxy(ctx, proxy, owner, nowISO)
		if err != nil {
			proxy.item.Action = importActionFailed
			proxy.item.Messages = []string{err.Error()}
			plan.result.Summary.Proxies.Create--
			plan.result.Summary.Proxies.Failed++
			continue
		}
		proxy.proxyProfileID = proxyID
		proxy.item.ProxyProfileID = &proxyID
		if reuse {
			proxy.item.Action = importActionReuse
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
		if account.item.Action != importActionCreate || account.source.proxyRef == "" || account.proxyProfileID != "" {
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
		if account.item.Action != importActionCreate || account.source.proxyRef == "" || account.proxyProfileID != "" {
			continue
		}
		if !proxyPlanByRef[account.source.proxyRef] {
			continue
		}
		account.item.Action = importActionFailed
		account.item.Messages = []string{"代理创建失败，账户未导入：" + account.source.proxyRef}
		plan.result.Summary.Accounts.Create--
		plan.result.Summary.Accounts.Failed++
	}

	// 4. Groups: create planned rows, reuse on duplicate names.
	for key, group := range plan.groupNamesToCreate {
		groupID, reuse, err := s.createImportGroup(ctx, group, owner, nowISO)
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
					account.item.Action == importActionCreate {
					account.item.Action = importActionFailed
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
		if account.item.Action != importActionCreate {
			continue
		}
		groupID := account.groupID
		if groupID == "" && account.source.groupName != "" && account.source.providerCode != "" {
			groupID = plan.groupIDsByKey[accountImportGroupKey(account.source.providerCode, account.source.groupName)]
		}
		input := importCreateInput(&account.source, groupID, account.proxyProfileID)
		created, err := s.Create(ctx, input, access)
		if err != nil {
			if duplicateAccountNameError(err, account.source.name) != nil && planSkipDuplicates(plan) {
				account.item.Action = importActionSkip
				account.item.Messages = []string{err.Error()}
				plan.result.Summary.Accounts.Create--
				plan.result.Summary.Accounts.Skip++
				continue
			}
			account.item.Action = importActionFailed
			account.item.Messages = []string{err.Error()}
			plan.result.Summary.Accounts.Create--
			plan.result.Summary.Accounts.Failed++
			continue
		}
		account.item.AccountID = &created.ID
		if created.Status == "pending_test" {
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

func planSkipDuplicates(plan *importPlan) bool {
	_, _, duplicates := plan.options.resolve()
	return duplicates
}

// createImportProxy mirrors createPlannedImportProxies' create branch: the
// insert, with the duplicate-name reuse fallback signaled through reuse.
func (s *Store) createImportProxy(ctx context.Context, proxy *importProxyPlan, ownerID, nowISO string) (string, bool, error) {
	proxyID := newID("proxy")
	var sealedPassword sql.NullString
	if proxy.source.password != "" {
		sealed, err := EncryptJSON(s.secret, Credentials{"password": proxy.source.password})
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
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("proxy_profiles")+`
		(id, system_account_id, name, description, type, host, port, username, password_encrypted,
		 enabled, test_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'unknown', ?, ?)`),
		proxyID, ownerID, proxy.source.name, nullableText(proxy.source.description),
		proxy.source.proxyType, proxy.source.host, proxy.source.port,
		nullableText(proxy.source.username), sealedPassword,
		boolInt(proxy.source.enabled), nowISO, nowISO)
	if err == nil {
		return proxyID, false, nil
	}
	if duplicate := duplicateProxyNameError(err, proxy.source.name); duplicate != nil {
		existing := s.findImportProxyOptionByName(ctx, proxy.source.name, nil)
		if existing != nil {
			return existing.id, true, nil
		}
		return "", false, duplicate
	}
	return "", false, err
}

// createImportGroup mirrors createPlannedImportGroups' create branch: the
// insert, with the duplicate-name reuse fallback signaled through reuse.
func (s *Store) createImportGroup(ctx context.Context, group importGroupCreatePlan, ownerID, nowISO string) (string, bool, error) {
	groupID := newID("grp")
	_, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("groups")+`
		(id, system_account_id, name, provider_code, description, enabled, is_default, group_type,
		 scheduling_policy_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, '由账户导入自动创建', 1, 0, 'personal', NULL, ?, ?)`),
		groupID, ownerID, group.name, group.providerCode, nowISO, nowISO)
	if err == nil {
		return groupID, false, nil
	}
	if duplicate := duplicateImportGroupNameError(err, group.name); duplicate != nil {
		existing, lookupErr := s.findImportGroupOptionByName(ctx, group.providerCode, group.name, nil)
		if lookupErr == nil && existing != nil {
			return existing.id, true, nil
		}
		return "", false, duplicate
	}
	return "", false, err
}

// importCreateInput mirrors buildAccountImportCreatePayload + the create
// status remap (active imports start as pending_test for the initial health
// check).
func importCreateInput(source *normalizedImportAccount, groupID, proxyProfileID string) CreateInput {
	input := CreateInput{
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
	input.Status = AccountCreationStatusInput(status)
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

// duplicateProxyNameError mirrors isDuplicateProxyNameError.
func duplicateProxyNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "UNIQUE constraint failed") && strings.Contains(message, "proxy_profiles") && strings.Contains(message, "name") {
		return &ConflictError{Message: "代理名称已存在：" + name}
	}
	return nil
}

// duplicateImportGroupNameError mirrors isDuplicateGroupNameError.
func duplicateImportGroupNameError(err error, name string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "idx_groups_owner_provider_name_unique") ||
		strings.Contains(message, "idx_groups_owner_provider_name_unique_lower") ||
		strings.Contains(message, "UNIQUE constraint failed: groups.system_account_id, groups.provider_code, groups.name") ||
		strings.Contains(message, "UNIQUE constraint failed: juhe_business.groups.system_account_id, juhe_business.groups.provider_code, juhe_business.groups.name") {
		return &ConflictError{Message: "同一供应商下分组名称已存在：" + name}
	}
	return nil
}
