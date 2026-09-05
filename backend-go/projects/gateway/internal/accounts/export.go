package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// M09 export slice: the POST /accounts/export family ported from
// backend/src/modules/accounts/account-export.routes.ts,
// account-export-request.ts and account-export.service.ts. Export documents
// are the native import protocol (juhe-ai-account-import v1); credentials are
// included for exportable owner accounts exactly like Node (the document is
// the sanctioned credential portability surface).

const (
	accountImportProtocolType    = "juhe-ai-account-import"
	accountImportProtocolVersion = 1
	accountExportMaxAccounts     = 500
	accountExportListPageSize    = 200
)

// ExportProxy mirrors AccountExportProxy.
type ExportProxy struct {
	Ref         string  `json:"ref"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	Host        string  `json:"host"`
	Port        int     `json:"port"`
	Username    *string `json:"username,omitempty"`
	Password    *string `json:"password,omitempty"`
	Description *string `json:"description,omitempty"`
	Enabled     bool    `json:"enabled"`
}

// ExportAccount mirrors AccountExportAccount.
type ExportAccount struct {
	Ref                                        string                `json:"ref"`
	Name                                       string                `json:"name"`
	ProviderCode                               string                `json:"providerCode"`
	ProviderProtocolProfileID                  *string               `json:"providerProtocolProfileId,omitempty"`
	Type                                       string                `json:"type"`
	Status                                     string                `json:"status"`
	GroupID                                    *string               `json:"groupId,omitempty"`
	GroupName                                  *string               `json:"groupName,omitempty"`
	ProxyRef                                   *string               `json:"proxyRef,omitempty"`
	ConcurrencyLimit                           *int                  `json:"concurrencyLimit,omitempty"`
	Priority                                   *int                  `json:"priority,omitempty"`
	SuperPriorityEnabled                       *bool                 `json:"superPriorityEnabled,omitempty"`
	FallbackEnabled                            *bool                 `json:"fallbackEnabled,omitempty"`
	SupportedModels                            []string              `json:"supportedModels,omitempty"`
	HealthCheckModel                           *string               `json:"healthCheckModel,omitempty"`
	HealthCheckEndpointMode                    string                `json:"healthCheckEndpointMode"`
	TemporaryUnavailableContinuousProbeEnabled *bool                 `json:"temporaryUnavailableContinuousProbeEnabled,omitempty"`
	ModelMappings                              []ModelMapping        `json:"modelMappings,omitempty"`
	Tags                                       []string              `json:"tags,omitempty"`
	AccountExpiresAt                           *string               `json:"accountExpiresAt,omitempty"`
	AvailabilitySchedule                       *AvailabilitySchedule `json:"availabilitySchedule,omitempty"`
	Credentials                                Credentials           `json:"credentials"`
	Notes                                      *string               `json:"notes,omitempty"`
}

// ExportDocument mirrors AccountExportDocument.
type ExportDocument struct {
	Type     string          `json:"type"`
	Version  int             `json:"version"`
	Proxies  []ExportProxy   `json:"proxies,omitempty"`
	Accounts []ExportAccount `json:"accounts"`
}

// ExportSummary mirrors the result summary block.
type ExportSummary struct {
	Accounts        int   `json:"accounts"`
	Proxies         int   `json:"proxies"`
	SkippedAccounts int   `json:"skippedAccounts"`
	MatchedAccounts *int  `json:"matchedAccounts,omitempty"`
	Truncated       *bool `json:"truncated,omitempty"`
}

// ExportResult mirrors AccountExportResult.
type ExportResult struct {
	Document ExportDocument `json:"document"`
	Summary  ExportSummary  `json:"summary"`
}

// ExportOptions carries the normalized id selection plus the filter-match
// bookkeeping (matchedAccounts flows into the summary + log).
type ExportOptions struct {
	AccountIDs      []string
	MatchedAccounts *int
}

var apiKeyExportCredentialKeys = []string{
	"api_key", "api_keys", "api_key_strategy", "api_key_weights", "base_url",
	"supported_endpoint_modes", "service_tier_override", "reasoning_effort_override",
	"error_handling_rules", "response_inspection_rules", "quota_recovery_policy",
}

var oauthExportCredentialKeys = []string{
	"refresh_token", "access_token", "expires_at", "client_id", "id_token", "base_url",
	"supported_endpoint_modes", "service_tier_override", "reasoning_effort_override",
	"account_id", "email", "chatgpt_user_id", "plan_type",
	"error_handling_rules", "response_inspection_rules", "quota_recovery_policy",
}

// ExportAccounts mirrors exportAccountsAsImportDocumentAsync: the requested
// ids resolve to scope-visible owner rows in request order, skipped rows are
// counted, and proxy references are materialized once per profile.
func (s *Store) ExportAccounts(ctx context.Context, options ExportOptions, access AccessScope) (*ExportResult, error) {
	ctx = ensureCtx(ctx)
	ids := normalizeExportAccountIDs(options.AccountIDs)
	rows, err := s.loadExportRows(ctx, ids, access)
	if err != nil {
		return nil, err
	}
	byID := map[string]*exportAccountRow{}
	for index := range rows {
		byID[rows[index].id] = &rows[index]
	}
	ordered := make([]*exportAccountRow, 0, len(ids))
	for _, id := range ids {
		if row := byID[id]; row != nil {
			ordered = append(ordered, row)
		}
	}
	if len(ordered) == 0 {
		return nil, &ValidationError{Message: "没有可导出的自有 AI 账户"}
	}

	result := &ExportResult{}
	result.Document.Type = accountImportProtocolType
	result.Document.Version = accountImportProtocolVersion
	result.Document.Accounts = []ExportAccount{}
	proxyRefs := map[string]string{}
	for _, row := range ordered {
		exported, err := s.buildExportAccount(ctx, row, proxyRefs, &result.Document.Proxies)
		if err != nil {
			return nil, err
		}
		result.Document.Accounts = append(result.Document.Accounts, exported)
	}
	result.Summary.Accounts = len(result.Document.Accounts)
	result.Summary.Proxies = len(result.Document.Proxies)
	result.Summary.SkippedAccounts = len(ids) - len(result.Document.Accounts)
	if result.Summary.SkippedAccounts < 0 {
		result.Summary.SkippedAccounts = 0
	}
	result.Summary.MatchedAccounts = options.MatchedAccounts
	return result, nil
}

func normalizeExportAccountIDs(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// exportAccountRow is the per-account projection the export needs.
type exportAccountRow struct {
	id                        string
	providerCode              string
	providerProtocolProfileID string
	name                      string
	notes                     sql.NullString
	accountType               string
	status                    string
	schedulable               bool
	credentialsEncrypted      string
	healthCheckModel          string
	healthCheckEndpointMode   string
	proxyProfileID            sql.NullString
	availabilitySchedule      sql.NullString
	accountExpiresAt          sql.NullString
	concurrencyLimit          int
	priority                  int
	superPriorityEnabled      bool
	fallbackEnabled           bool
	continuousProbeEnabled    bool
	boundGroupID              sql.NullString
	boundGroupName            sql.NullString
}

// loadExportRows mirrors findAccountSummaryAsync + isExportableOwnerAccount:
// scope-checked non-authorization rows only; authorization instances are
// silently skipped (counted as skippedAccounts).
func (s *Store) loadExportRows(ctx context.Context, ids []string, access AccessScope) ([]exportAccountRow, error) {
	if len(ids) == 0 {
		return nil, &ValidationError{Message: "请选择要导出的 AI 账户"}
	}
	if len(ids) > accountExportMaxAccounts {
		return nil, &ValidationError{Message: fmt.Sprintf("单次最多导出 %d 个 AI 账户", accountExportMaxAccounts)}
	}
	scopeClause := ""
	args := anySlice(ids)
	if scoped := access.manageableID(); scoped != "" {
		scopeClause = " AND accounts.system_account_id = ?"
		args = append(args, scoped)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT accounts.id, accounts.provider_code,
			accounts.provider_protocol_profile_id, accounts.name, accounts.notes, accounts.type,
			accounts.status, accounts.schedulable, accounts.credentials_encrypted,
			accounts.health_check_model, accounts.health_check_endpoint_mode, accounts.proxy_profile_id,
			accounts.availability_schedule_json, accounts.account_expires_at, accounts.concurrency_limit,
			accounts.priority, accounts.super_priority_enabled, accounts.fallback_enabled,
			accounts.temporary_unavailable_continuous_probe_enabled,
			accounts.authorization_instance_authorization_id, accounts.authorization_instance_source_account_id,
			group_bindings.group_id AS bound_group_id, bound_groups.name AS bound_group_name
		FROM `+s.table("accounts")+` accounts
		LEFT JOIN `+s.table("group_accounts")+` group_bindings
			ON group_bindings.account_id = accounts.id
			AND group_bindings.system_account_id = accounts.system_account_id
			AND group_bindings.enabled = 1
		LEFT JOIN `+s.table("groups")+` bound_groups ON bound_groups.id = group_bindings.group_id
		WHERE accounts.id IN (`+placeholders(len(ids))+`)
			AND accounts.deleted_at IS NULL`+scopeClause+`
		ORDER BY accounts.id ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []exportAccountRow{}
	for rows.Next() {
		var row exportAccountRow
		var schedulable, superPriority, fallback, continuousProbe int64
		var authorizationID, sourceAccountID sql.NullString
		if err := rows.Scan(&row.id, &row.providerCode, &row.providerProtocolProfileID, &row.name,
			&row.notes, &row.accountType, &row.status, &schedulable, &row.credentialsEncrypted,
			&row.healthCheckModel, &row.healthCheckEndpointMode, &row.proxyProfileID,
			&row.availabilitySchedule, &row.accountExpiresAt, &row.concurrencyLimit,
			&row.priority, &superPriority, &fallback, &continuousProbe,
			&authorizationID, &sourceAccountID, &row.boundGroupID, &row.boundGroupName); err != nil {
			return nil, err
		}
		// isExportableOwnerAccount: authorization instances never export.
		if authorizationID.Valid && authorizationID.String != "" {
			continue
		}
		if sourceAccountID.Valid && sourceAccountID.String != "" {
			continue
		}
		row.schedulable = schedulable == 1
		row.superPriorityEnabled = superPriority == 1
		row.fallbackEnabled = fallback == 1
		row.continuousProbeEnabled = continuousProbe == 1
		out = append(out, row)
	}
	return out, rows.Err()
}

// buildExportAccount mirrors buildExportAccount + exportAccount's proxy-ref
// materialization.
func (s *Store) buildExportAccount(ctx context.Context, row *exportAccountRow, proxyRefs map[string]string, proxies *[]ExportProxy) (ExportAccount, error) {
	status := exportAccountStatus(row)
	exported := ExportAccount{
		Ref:                       row.id,
		Name:                      row.name,
		ProviderCode:              row.providerCode,
		ProviderProtocolProfileID: strPtrOrNil(row.providerProtocolProfileID),
		Type:                      row.accountType,
		Status:                    status,
		HealthCheckEndpointMode:   row.healthCheckEndpointMode,
	}
	credentials := Credentials{}
	if strings.TrimSpace(row.credentialsEncrypted) != "" {
		if err := DecryptJSON(s.secret, row.credentialsEncrypted, &credentials); err != nil {
			return ExportAccount{}, err
		}
	}
	exported.Credentials = exportCredentials(row.accountType, credentials)
	if row.boundGroupName.Valid && row.boundGroupName.String != "" {
		exported.GroupName = &row.boundGroupName.String
	} else if row.boundGroupID.Valid && row.boundGroupID.String != "" {
		exported.GroupID = &row.boundGroupID.String
	}
	if row.proxyProfileID.Valid && row.proxyProfileID.String != "" {
		if ref, err := s.exportProxyRef(ctx, row.proxyProfileID.String, proxyRefs, proxies); err != nil {
			return ExportAccount{}, err
		} else if ref != nil {
			exported.ProxyRef = ref
		}
	}
	if row.concurrencyLimit > 0 {
		limit := row.concurrencyLimit
		exported.ConcurrencyLimit = &limit
	}
	if row.priority >= 0 {
		priority := row.priority
		exported.Priority = &priority
	}
	if status == "active" {
		if row.superPriorityEnabled {
			exported.SuperPriorityEnabled = boolPtr(true)
		}
		if row.fallbackEnabled {
			exported.FallbackEnabled = boolPtr(true)
		}
	}
	models := []string{}
	modelRows, err := s.db.QueryContext(ctx, s.bind(`SELECT model FROM `+s.table("account_supported_models")+`
		WHERE account_id = ? ORDER BY model ASC`), row.id)
	if err != nil {
		return ExportAccount{}, err
	}
	for modelRows.Next() {
		var model string
		if err := modelRows.Scan(&model); err != nil {
			modelRows.Close()
			return ExportAccount{}, err
		}
		models = append(models, model)
	}
	modelRows.Close()
	if err := modelRows.Err(); err != nil {
		return ExportAccount{}, err
	}
	if len(models) > 0 {
		exported.SupportedModels = models
	}
	if trimmed := strings.TrimSpace(row.healthCheckModel); trimmed != "" {
		exported.HealthCheckModel = &trimmed
	}
	if !row.continuousProbeEnabled {
		exported.TemporaryUnavailableContinuousProbeEnabled = boolPtr(false)
	}
	mappings, err := s.loadBatchModelMappings(ctx, s.db, []string{row.id})
	if err != nil {
		return ExportAccount{}, err
	}
	if list := mappings[row.id]; len(list) > 0 {
		exported.ModelMappings = list
	}
	tagNames, err := s.exportTagNames(ctx, row.id)
	if err != nil {
		return ExportAccount{}, err
	}
	if len(tagNames) > 0 {
		exported.Tags = tagNames
	}
	if row.accountExpiresAt.Valid && row.accountExpiresAt.String != "" {
		text := row.accountExpiresAt.String
		exported.AccountExpiresAt = &text
	}
	if schedule, err := ParseScheduleJSON(row.availabilitySchedule.String); err == nil && schedule != nil {
		exported.AvailabilitySchedule = schedule
	}
	exported.Notes = nullPtrString(row.notes)
	return exported, nil
}

func (s *Store) exportTagNames(ctx context.Context, accountID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT account_tags.name
		FROM `+s.table("account_tag_bindings")+` account_tag_bindings
		INNER JOIN `+s.table("account_tags")+` account_tags
			ON account_tags.id = account_tag_bindings.tag_id
		WHERE account_tag_bindings.account_id = ?
		ORDER BY account_tags.name ASC, account_tags.id ASC`), accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names, rows.Err()
}

// exportAccountStatus mirrors exportAccountStatus.
func exportAccountStatus(row *exportAccountRow) string {
	if row.status == "pending_test" {
		return "pending_test"
	}
	if row.status == "active" && row.schedulable {
		return "active"
	}
	return "disabled"
}

// exportCredentials mirrors exportCredentials: the credential whitelist per
// account type with only present, defined keys.
func exportCredentials(accountType string, credentials Credentials) Credentials {
	keys := credentials.orderedKeys()
	switch accountType {
	case "api_key":
		keys = apiKeyExportCredentialKeys
	case "oauth":
		keys = oauthExportCredentialKeys
	}
	out := Credentials{}
	for _, key := range keys {
		if value, ok := credentials[key]; ok && value != nil {
			out[key] = value
		}
	}
	return out
}

// orderedKeys keeps map iteration deterministic for the "other types" branch.
func (c Credentials) orderedKeys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

// exportProxyRef mirrors exportProxyRef: one shared ref per enabled profile.
func (s *Store) exportProxyRef(ctx context.Context, proxyProfileID string, proxyRefs map[string]string, proxies *[]ExportProxy) (*string, error) {
	if existing, ok := proxyRefs[proxyProfileID]; ok {
		return &existing, nil
	}
	var row struct {
		name        string
		proxyType   string
		host        string
		port        int
		username    sql.NullString
		password    sql.NullString
		description sql.NullString
		enabled     int64
	}
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT name, type, host, port, username, password_encrypted,
			description, enabled FROM `+s.table("proxy_profiles")+` WHERE id = ? LIMIT 1`), proxyProfileID).
		Scan(&row.name, &row.proxyType, &row.host, &row.port, &row.username, &row.password,
			&row.description, &row.enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.enabled != 1 {
		return nil, nil
	}
	ref := "proxy-" + proxyProfileID
	proxy := ExportProxy{
		Ref: ref, Name: row.name, Type: row.proxyType, Host: row.host, Port: row.port,
		Username: nullPtrString(row.username), Description: nullPtrString(row.description),
		Enabled: true,
	}
	if row.password.Valid && strings.TrimSpace(row.password.String) != "" {
		var secret Credentials
		if err := DecryptJSON(s.secret, row.password.String, &secret); err == nil {
			if password, ok := secret["password"].(string); ok && password != "" {
				proxy.Password = &password
			}
		}
	}
	proxyRefs[proxyProfileID] = ref
	*proxies = append(*proxies, proxy)
	return &ref, nil
}

func strPtrOrNil(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// exportBody is the parsed POST /accounts/export request (either shape).
type exportBody struct {
	byIDs      bool
	accountIDs []string
	filters    map[string]any
}

// parseExportBody parses the POST /accounts/export request body with the
// accountExportRequestSchema union contract: strict {accountIds} or strict
// {filters}; a body carrying both keys fails both strict branches and renders
// the shared 400 (the registered deferral is closed).
func parseExportBody(body map[string]any) (exportBody, bool) {
	for key := range body {
		switch key {
		case "accountIds", "filters":
		default:
			return exportBody{}, false
		}
	}
	_, hasAccountIDs := body["accountIds"]
	_, hasFilters := body["filters"]
	if hasAccountIDs && hasFilters {
		// accountExportByIdsRequestSchema / accountExportByFiltersRequestSchema
		// are both .strict(): a body with both keys matches neither branch.
		return exportBody{}, false
	}
	if raw, ok := body["accountIds"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return exportBody{}, false
		}
		if len(list) < 1 || len(list) > accountExportMaxAccounts {
			return exportBody{}, false
		}
		ids := []string{}
		for _, item := range list {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return exportBody{}, false
			}
			ids = append(ids, text)
		}
		return exportBody{byIDs: true, accountIDs: ids}, true
	}
	rawFilters, ok := body["filters"].(map[string]any)
	if !ok {
		return exportBody{}, false
	}
	allowed := map[string]bool{
		"sorts": true, "keyword": true, "providerCode": true, "groupId": true,
		"tagIds": true, "type": true, "status": true, "schedulable": true,
	}
	for key := range rawFilters {
		if !allowed[key] {
			return exportBody{}, false
		}
	}
	if text, ok := rawFilters["keyword"]; ok && text != nil {
		if _, ok := text.(string); !ok {
			return exportBody{}, false
		}
	}
	if text, ok := rawFilters["providerCode"]; ok && text != nil {
		if _, ok := text.(string); !ok {
			return exportBody{}, false
		}
	}
	if text, ok := rawFilters["groupId"]; ok && text != nil {
		if _, ok := text.(string); !ok {
			return exportBody{}, false
		}
	}
	if text, ok := rawFilters["type"]; ok && text != nil {
		if _, ok := text.(string); !ok {
			return exportBody{}, false
		}
	}
	if text, ok := rawFilters["schedulable"]; ok && text != nil {
		value, _ := text.(string)
		switch value {
		case "all", "enabled", "disabled", "cooling", "":
		default:
			return exportBody{}, false
		}
	}
	return exportBody{filters: rawFilters}, true
}

// exportFiltersOptions mirrors accountExportListOptions: the filter subset the
// shared management list already implements.
func exportFiltersOptions(filters map[string]any, page int) ListOptions {
	text := func(key string) string {
		if value, ok := filters[key].(string); ok {
			return strings.TrimSpace(value)
		}
		return ""
	}
	allText := func(key string) string {
		value := text(key)
		if value != "" && value != "all" {
			return value
		}
		return ""
	}
	options := ListOptions{
		Page:         page,
		PageSize:     accountExportListPageSize,
		Keyword:      text("keyword"),
		ProviderCode: allText("providerCode"),
		GroupID:      text("groupId"),
		Type:         allText("type"),
		Status:       statusQueryValue(textValueOrJoin(filters["status"])),
		Schedulable:  schedulableQueryValue(textValueOrJoin(filters["schedulable"])),
		TagIDs:       textListOrSingle(filters["tagIds"]),
	}
	if sorts, ok := filters["sorts"].([]any); ok {
		for _, item := range sorts {
			record, ok := item.(map[string]any)
			if !ok {
				continue
			}
			field, _ := record["field"].(string)
			order, _ := record["order"].(string)
			if accountListSortFields[field] && (order == "asc" || order == "desc") {
				options.Sorts = append(options.Sorts, ListSort{Field: field, Order: order})
			}
		}
	}
	return options
}

func textValueOrJoin(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []any:
		parts := []string{}
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}

func textListOrSingle(value any) []string {
	switch typed := value.(type) {
	case string:
		return textListQuery([]string{typed})
	case []any:
		out := []string{}
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

// CollectExportIDs mirrors collectAccountExportIdsAsync: page the filtered
// list until exhausted, enforcing the 500-account export ceiling.
func (s *Store) CollectExportIDs(ctx context.Context, filters map[string]any, access AccessScope) ([]string, error) {
	accountIDs := []string{}
	page := 1
	for {
		result, err := s.ListPage(ctx, access, exportFiltersOptions(filters, page))
		if err != nil {
			return nil, err
		}
		for _, item := range result.Items {
			accountIDs = append(accountIDs, item.ID)
		}
		if len(accountIDs) > accountExportMaxAccounts {
			return nil, &ValidationError{Message: fmt.Sprintf("当前筛选匹配 %d 个 AI 账户，超过单次导出上限 %d 个，请先筛选或分批次导出", len(accountIDs), accountExportMaxAccounts)}
		}
		if !result.HasMore {
			break
		}
		page++
	}
	return accountIDs, nil
}
