// Public welfare-account family (list/add/update/del), ported from
// external-public-account-push.service.ts (the addPublicWelfareAccount /
// updatePublicWelfareAccount / deletePublicWelfareAccount /
// listPublicWelfareAccounts branches) plus the payload/sanitize layers and the
// account write operation logs (recordPublicWelfareAccountWriteOperation /
// DeleteOperation). The Go composition keeps one transaction per store call;
// the configRevision CAS retry loop (3 attempts, PublicAccountUpdateConflict
// -> 409) mirrors the Node async path.
package aipublic

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

// accountListQuery mirrors accountListQuerySchema (strict).
type accountListQuery struct {
	TargetUsername  string
	TargetGroupName string
	HasGroupName    bool
	ProviderCode    string
	HasProvider     bool
	ProfileID       string
	HasProfileID    bool
	GroupID         string
	HasGroupID      bool
	Keyword         string
	HasKeyword      bool
	Type            string
	HasType         bool
	Status          string
	HasStatus       bool
	Schedulable     string
	HasSchedulable  bool
	Page            int
	HasPage         bool
	PageSize        int
	HasPageSize     bool
}

var accountSchedulableOptions = []string{"all", "enabled", "disabled", "cooling"}

func parseAccountListQuery(values url.Values) (*accountListQuery, string) {
	unknown := strictObjectKeys(valuesAsMap(values),
		"targetUsername", "targetGroupName", "providerCode", "providerProtocolProfileId",
		"groupId", "keyword", "type", "status", "schedulable", "page", "pageSize")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	query := &accountListQuery{}
	username, issue := parseQueryString(values, "targetUsername", true, 2, 80)
	if issue != "" {
		return nil, issue
	}
	query.TargetUsername = username
	groupName, has, issue := parseOptionalQueryString(values, "targetGroupName", 1, 80)
	if issue != "" {
		return nil, issue
	}
	query.TargetGroupName, query.HasGroupName = groupName, has
	providerCode, has, issue := parseOptionalQueryString(values, "providerCode", 1, 60)
	if issue != "" {
		return nil, issue
	}
	query.ProviderCode, query.HasProvider = providerCode, has
	profileID, has, issue := parseOptionalQueryString(values, "providerProtocolProfileId", 1, 120)
	if issue != "" {
		return nil, issue
	}
	query.ProfileID, query.HasProfileID = profileID, has
	groupID, has, issue := parseOptionalQueryString(values, "groupId", 1, 120)
	if issue != "" {
		return nil, issue
	}
	query.GroupID, query.HasGroupID = groupID, has
	keyword, has, issue := parseOptionalQueryString(values, "keyword", 0, 120)
	if issue != "" {
		return nil, issue
	}
	query.Keyword, query.HasKeyword = keyword, has
	accountType, has, issue := parseOptionalQueryString(values, "type", 0, 60)
	if issue != "" {
		return nil, issue
	}
	query.Type, query.HasType = accountType, has
	status, has, issue := parseOptionalQueryString(values, "status", 0, 200)
	if issue != "" {
		return nil, issue
	}
	query.Status, query.HasStatus = status, has
	schedulable, has, issue := parseOptionalQueryEnum(values, "schedulable", accountSchedulableOptions)
	if issue != "" {
		return nil, issue
	}
	query.Schedulable, query.HasSchedulable = schedulable, has
	page, has, issue := parseOptionalQueryInt(values, "page", 1, 0)
	if issue != "" {
		return nil, issue
	}
	query.Page, query.HasPage = page, has
	pageSize, has, issue := parseOptionalQueryInt(values, "pageSize", 1, 100)
	if issue != "" {
		return nil, issue
	}
	query.PageSize, query.HasPageSize = pageSize, has
	return query, ""
}

func (d *Deps) listAccounts(w http.ResponseWriter, r *http.Request) {
	query, issue := parseAccountListQuery(r.URL.Query())
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		page, pageSize := d.accountPaging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
		d.mockAccountList(w, map[string]string{
			"targetUsername":            query.TargetUsername,
			"targetGroupName":           query.TargetGroupName,
			"providerCode":              query.ProviderCode,
			"providerProtocolProfileId": query.ProfileID,
			"groupId":                   query.GroupID,
			"keyword":                   query.Keyword,
		}, page, pageSize)
		return
	}
	target, err := d.requirePublicTarget(r.Context(), query.TargetUsername)
	if err != nil {
		d.writeServiceError(w, err, "账号列表读取失败")
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	profileFilter, err := d.resolveOptionalProfileID(r.Context(), query.ProviderCode, query.HasProvider, query.ProfileID, query.HasProfileID)
	if err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	groupFilter, err := d.resolveAccountListGroupID(r.Context(), target.SystemAccountID, query.ProviderCode, query.GroupID, query.HasGroupID, query.TargetGroupName, query.HasGroupName)
	if err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	page, pageSize := d.accountPaging(query.HasPage, query.Page, query.HasPageSize, query.PageSize)
	options := accounts.ListOptions{
		Page: page, PageSize: pageSize,
		Keyword: query.Keyword, ProviderCode: query.ProviderCode,
		GroupID: groupFilter,
	}
	if profileFilter != nil {
		options.ProviderProtocolProfileID = *profileFilter
	}
	if query.HasType {
		options.Type = query.Type
	}
	if query.HasStatus {
		options.Status = query.Status
	}
	if query.HasSchedulable {
		options.Schedulable = query.Schedulable
	}
	result, err := d.AiAccounts.ListPage(r.Context(), accounts.AccessScope{ViewerID: target.SystemAccountID}, options)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.ID)
	}
	models, err := d.loadAccountSupportedModels(r.Context(), ids)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	items := make([]PublicAccountListItem, 0, len(result.Items))
	for index := range result.Items {
		items = append(items, sanitizeAccountItem(&result.Items[index], models[result.Items[index].ID]))
	}
	d.writeStatsEnvelope(w, map[string]any{
		"target":         target.Public,
		"page":           result.Page,
		"pageSize":       result.PageSize,
		"pageUpperBound": result.Total,
		"hasMore":        result.HasMore,
		"items":          items,
	})
}

func (d *Deps) accountPaging(hasPage bool, page int, hasPageSize bool, pageSize int) (int, int) {
	// normalizeAccountListOptions defaults pageSize 50; the public schema caps
	// explicit sizes at 100.
	currentPage := 1
	if hasPage && page > 0 {
		currentPage = page
	}
	size := 50
	if hasPageSize && pageSize > 0 {
		size = pageSize
	}
	return currentPage, size
}

// resolveOptionalProfileID mirrors resolveOptionalProviderProtocolProfileIdAsync.
func (d *Deps) resolveOptionalProfileID(ctx context.Context, providerCode string, hasProvider bool, profileID string, hasProfileID bool) (*string, error) {
	if !hasProvider && !hasProfileID {
		return nil, nil
	}
	if !hasProvider {
		return nil, errors.New("按协议档案查询时必须提供 providerCode")
	}
	if !hasProfileID || profileID == "" {
		return nil, nil
	}
	profile, err := d.requireProviderProfile(ctx, providerCode, profileID)
	if err != nil {
		return nil, err
	}
	return &profile.ID, nil
}

// resolveAccountListGroupID mirrors resolveAccountListGroupIdAsync: groupId
// wins, then targetGroupName (+providerCode), then the not-found sentinel.
func (d *Deps) resolveAccountListGroupID(ctx context.Context, ownerID, providerCode string, groupID string, hasGroupID bool, groupName string, hasGroupName bool) (string, error) {
	if hasGroupID {
		return groupID, nil
	}
	if !hasGroupName || groupName == "" {
		return "", nil
	}
	if providerCode == "" {
		return "", errors.New("按目标分组名称查询账号时必须提供 providerCode")
	}
	existing, err := d.findExistingTargetGroup(ctx, ownerID, providerCode, groupName)
	if err != nil {
		return "", err
	}
	if existing == nil {
		return "__public_group_not_found__", nil
	}
	return existing.ID, nil
}

// loadAccountSupportedModels mirrors loadSupportedModelsByAccountIds.
func (d *Deps) loadAccountSupportedModels(ctx context.Context, ids []string) (map[string][]string, error) {
	result := map[string][]string{}
	if len(ids) == 0 {
		return result, nil
	}
	args := make([]any, 0, len(ids))
	placeholders := ""
	for i, id := range ids {
		if i > 0 {
			placeholders += ", "
		}
		placeholders += "?"
		args = append(args, id)
	}
	rows, err := d.db().QueryContext(ctx, d.bind(`SELECT account_id, model FROM `+d.table("account_supported_models")+`
		WHERE account_id IN (`+placeholders+`)
		ORDER BY account_id ASC, model ASC`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var accountID, model string
		if err := rows.Scan(&accountID, &model); err != nil {
			return nil, err
		}
		result[accountID] = append(result[accountID], model)
	}
	return result, rows.Err()
}

// accountAddBody mirrors accountPushSchema (strict).
type accountAddBody struct {
	TargetUsername       string
	TargetDisplayName    *string
	TargetGroupName      string
	ProviderCode         string
	ProfileID            string
	Name                 string
	AccountType          string
	BaseURL              string
	APIKey               string
	SupportedModels      *[]string
	Status               string
	HasStatus            bool
	ConcurrencyLimit     *int
	Priority             *int
	AvailabilitySchedule any
	HasSchedule          bool
	Notes                *string
}

func parseAccountAddBody(body map[string]any) (*accountAddBody, string) {
	allowed := []string{"targetUsername", "targetDisplayName", "targetGroupName", "providerCode",
		"providerProtocolProfileId", "name", "type", "baseUrl", "apiKey", "supportedModels",
		"status", "concurrencyLimit", "priority", "availabilitySchedule", "notes"}
	unknown := strictObjectKeys(body, allowed...)
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	parsed := &accountAddBody{}
	username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
	if issue != "" {
		return nil, issue
	}
	parsed.TargetUsername = username
	if bodyHas(body, "targetDisplayName") {
		displayName, issue := optionalTrimmedBody(body, "targetDisplayName", 1, 80)
		if issue != "" {
			return nil, issue
		}
		parsed.TargetDisplayName = displayName
	}
	groupName, issue := requiredTrimmedBody(body, "targetGroupName", 1, 80)
	if issue != "" {
		return nil, issue
	}
	parsed.TargetGroupName = groupName
	providerCode, issue := requiredTrimmedBody(body, "providerCode", 1, 60)
	if issue != "" {
		return nil, issue
	}
	parsed.ProviderCode = providerCode
	profileID, issue := requiredTrimmedBody(body, "providerProtocolProfileId", 1, 120)
	if issue != "" {
		return nil, issue
	}
	parsed.ProfileID = profileID
	name, issue := requiredTrimmedBody(body, "name", 1, 120)
	if issue != "" {
		return nil, issue
	}
	parsed.Name = name
	if bodyHas(body, "type") {
		value, present := body["type"]
		if !present || value == nil {
			return nil, "公开账号接口仅支持 API Key 账户"
		}
		text, isString := value.(string)
		if !isString || text != "api_key" {
			return nil, "公开账号接口仅支持 API Key 账户"
		}
		parsed.AccountType = text
	}
	baseURL, issue := requiredTrimmedBody(body, "baseUrl", 1, 500)
	if issue != "" {
		return nil, issue
	}
	parsed.BaseURL = baseURL
	apiKey, issue := requiredTrimmedBody(body, "apiKey", 1, 1000)
	if issue != "" {
		return nil, issue
	}
	parsed.APIKey = apiKey
	if bodyHas(body, "supportedModels") {
		raw := body["supportedModels"]
		items, isList := raw.([]any)
		if !isList {
			return nil, zodInvalidType("array", raw)
		}
		if len(items) > 500 {
			return nil, "Array must contain at most 500 element(s)"
		}
		values := make([]string, 0, len(items))
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return nil, zodInvalidType("string", item)
			}
			trimmed := trimSpaces(text)
			if runeLen(trimmed) < 1 || runeLen(trimmed) > 120 {
				if runeLen(trimmed) < 1 {
					return nil, zodStringMin(1)
				}
				return nil, zodStringMax(120)
			}
			values = append(values, trimmed)
		}
		parsed.SupportedModels = &values
	}
	status, hasStatus, issue := bodyOptionalEnumField(body, "status", []string{"active", "disabled"})
	if issue != "" {
		return nil, issue
	}
	parsed.Status, parsed.HasStatus = status, hasStatus
	if bodyHas(body, "concurrencyLimit") && body["concurrencyLimit"] != nil {
		limit, _, issue := bodyOptionalInt(body["concurrencyLimit"], true, 1, 100000)
		if issue != "" {
			return nil, issue
		}
		parsed.ConcurrencyLimit = &limit
	}
	if bodyHas(body, "priority") && body["priority"] != nil {
		priority, _, issue := bodyOptionalInt(body["priority"], true, 0, 100000)
		if issue != "" {
			return nil, issue
		}
		parsed.Priority = &priority
	}
	if bodyHas(body, "availabilitySchedule") {
		parsed.AvailabilitySchedule, parsed.HasSchedule = body["availabilitySchedule"], true
	}
	if bodyHas(body, "notes") {
		notes, issue := optionalTrimmedBody(body, "notes", 0, 1000)
		if issue != "" {
			return nil, issue
		}
		parsed.Notes = notes
	}
	return parsed, ""
}

func trimSpaces(value string) string {
	out := []rune(value)
	start, end := 0, len(out)
	for start < end && isSpaceRune(out[start]) {
		start++
	}
	for end > start && isSpaceRune(out[end-1]) {
		end--
	}
	return string(out[start:end])
}

func isSpaceRune(char rune) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r' || char == '\v' || char == '\f'
}

func (d *Deps) addAccount(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, issue := parseAccountAddBody(body)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockAccountPush(w, body)
		return
	}
	// Node order: provider + profile + type validation before any write.
	profile, err := d.requireProviderProfile(r.Context(), parsed.ProviderCode, parsed.ProfileID)
	if err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	if err := assertSupportedPushAccountType(parsed.AccountType, profile); err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	// Node renders 账户支持模型不能为空 from inside createAccount's
	// transaction; the Go composition runs one transaction per store call, so
	// precheck here to keep a failed push from leaving the auto-created
	// target user/group behind.
	if parsed.SupportedModels == nil || len(normalizedStringList(*parsed.SupportedModels)) == 0 {
		kernel.WriteBadRequest(w, "账户支持模型不能为空，请至少选择一个该 Base URL 支持的模型")
		return
	}
	target, err := d.ensureTargetSystemAccount(r.Context(), parsed.TargetUsername, parsed.TargetDisplayName)
	if err != nil {
		kernel.WriteBadRequest(w, serviceMessage(err, "账号新增失败"))
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	group, groupCreated, err := d.ensureTargetGroup(r.Context(), target.SystemAccountID, parsed.ProviderCode, parsed.TargetGroupName)
	if err != nil {
		kernel.WriteBadRequest(w, serviceMessage(err, "账号新增失败"))
		return
	}
	// Duplicate check: same owner + provider + profile + group + name.
	if existing := d.findDuplicateAccount(r.Context(), target.SystemAccountID, parsed.ProviderCode, profile.ID, group.ID, parsed.Name); existing {
		kernelWriteError(w, http.StatusConflict, "账号已存在："+parsed.Name)
		return
	}
	input := accounts.CreateInput{
		ProviderCode:              parsed.ProviderCode,
		ProviderProtocolProfileID: profile.ID,
		Name:                      parsed.Name,
		AccountType:               "api_key",
		Credentials:               accounts.Credentials{"api_key": parsed.APIKey, "base_url": parsed.BaseURL},
		GroupID:                   strPtr(group.ID),
		AvailabilitySchedule:      nil,
	}
	if parsed.SupportedModels != nil {
		input.SupportedModels = normalizedStringList(*parsed.SupportedModels)
	}
	// Node payload: status = input.status === 'disabled' ? 'disabled' :
	// 'pending_test', schedulable = false.
	if parsed.HasStatus && parsed.Status == "disabled" {
		input.Status = accounts.CreationStatus{Status: "disabled", Schedulable: false}
	} else {
		input.Status = accounts.CreationStatus{Status: "pending_test", Schedulable: false}
	}
	input.ConcurrencyLimit = parsed.ConcurrencyLimit
	input.Priority = parsed.Priority
	if parsed.HasSchedule {
		input.AvailabilitySchedule = parsed.AvailabilitySchedule
	}
	input.Notes = parsed.Notes
	created, err := d.AiAccounts.Create(r.Context(), input, accounts.AccessScope{ViewerID: target.SystemAccountID})
	if err != nil {
		d.writeServiceError(w, err, "账号新增失败")
		return
	}
	item := d.readAccountItem(r, created.ID, target.SystemAccountID)
	summary := PublicAccountSummary{}
	var itemValue *accounts.ListItem
	if item != nil {
		itemValue = item
		models, _ := d.loadAccountSupportedModels(r.Context(), []string{created.ID})
		listItem := sanitizeAccountItem(item, models[created.ID])
		summary = listItem.PublicAccountSummary
	}
	groupID := group.ID
	_ = groupID
	d.recordAccountWriteLog(r, context, "account_add", "external_integrations.public_account_add",
		"新增", summary.ID, summary.Name, target, groupCreated,
		map[string]any{"status": summary.Status, "schedulable": summary.Schedulable})
	d.writeStatsCreated(w, map[string]any{
		"action": "created",
		"target": PublicGroupTarget{
			PublicTarget: target.Public,
			GroupID:      group.ID, GroupName: group.Name, GroupCreated: groupCreated,
		},
		"account": summary,
	})
	_ = itemValue
}

// findDuplicateAccount mirrors findTargetAccount: paged keyword lookup plus
// provider/profile/name equality.
func (d *Deps) findDuplicateAccount(ctx context.Context, ownerID, providerCode, profileID, groupID, name string) bool {
	result, err := d.AiAccounts.ListPage(ctx, accounts.AccessScope{ViewerID: ownerID}, accounts.ListOptions{
		Page: 1, PageSize: 20, Keyword: name,
		ProviderCode: providerCode, ProviderProtocolProfileID: profileID, GroupID: groupID,
	})
	if err != nil {
		return false
	}
	for index := range result.Items {
		item := result.Items[index]
		if item.ProviderCode == providerCode && item.ProviderProtocolProfileID == profileID && sameText(item.Name, name) {
			return true
		}
	}
	return false
}

// readAccountItem re-reads one account row for the response projection.
func (d *Deps) readAccountItem(r *http.Request, accountID, ownerID string) *accounts.ListItem {
	result, err := d.AiAccounts.ListPage(r.Context(), accounts.AccessScope{ViewerID: ownerID}, accounts.ListOptions{IDs: []string{accountID}, Page: 1, PageSize: 1})
	if err != nil {
		return nil
	}
	for index := range result.Items {
		if result.Items[index].ID == accountID {
			return &result.Items[index]
		}
	}
	return nil
}

// accountUpdateBody mirrors accountUpdateSchema (strict + refine).
type accountUpdateBody struct {
	AccountID            string
	TargetUsername       string
	HasTarget            bool
	TargetGroupName      string
	HasGroupName         bool
	ProviderCode         string
	HasProvider          bool
	ProfileID            string
	HasProfileID         bool
	Name                 *string
	HasName              bool
	AccountType          string
	HasType              bool
	BaseURL              *string
	HasBaseURL           bool
	APIKey               *string
	HasAPIKey            bool
	SupportedModels      *[]string
	HasSupportedModels   bool
	Status               string
	HasStatus            bool
	ConcurrencyLimit     *int
	HasConcurrencyLimit  bool
	Priority             *int
	HasPriority          bool
	AvailabilitySchedule any
	HasSchedule          bool
	Notes                *string
	HasNotes             bool
}

var accountUpdateMutableFields = []string{"name", "baseUrl", "apiKey", "supportedModels", "status",
	"concurrencyLimit", "priority", "availabilitySchedule", "notes"}

func parseAccountUpdateBody(body map[string]any) (*accountUpdateBody, string) {
	allowed := []string{"accountId", "targetUsername", "targetGroupName", "providerCode",
		"providerProtocolProfileId", "name", "type", "baseUrl", "apiKey", "supportedModels",
		"status", "concurrencyLimit", "priority", "availabilitySchedule", "notes"}
	unknown := strictObjectKeys(body, allowed...)
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	parsed := &accountUpdateBody{}
	accountID, issue := requiredTrimmedBody(body, "accountId", 1, 120)
	if issue != "" {
		return nil, issue
	}
	parsed.AccountID = accountID
	if bodyHas(body, "targetUsername") {
		username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
		if issue != "" {
			return nil, issue
		}
		parsed.TargetUsername, parsed.HasTarget = username, true
	}
	if bodyHas(body, "targetGroupName") {
		groupName, issue := optionalTrimmedBody(body, "targetGroupName", 1, 80)
		if issue != "" {
			return nil, issue
		}
		if groupName != nil {
			parsed.TargetGroupName, parsed.HasGroupName = *groupName, true
		}
	}
	if bodyHas(body, "providerCode") {
		providerCode, issue := optionalTrimmedBody(body, "providerCode", 1, 60)
		if issue != "" {
			return nil, issue
		}
		if providerCode != nil {
			parsed.ProviderCode, parsed.HasProvider = *providerCode, true
		}
	}
	if bodyHas(body, "providerProtocolProfileId") {
		profileID, issue := optionalTrimmedBody(body, "providerProtocolProfileId", 1, 120)
		if issue != "" {
			return nil, issue
		}
		if profileID != nil {
			parsed.ProfileID, parsed.HasProfileID = *profileID, true
		}
	}
	if bodyHas(body, "name") {
		name, issue := optionalTrimmedBody(body, "name", 1, 120)
		if issue != "" {
			return nil, issue
		}
		parsed.Name, parsed.HasName = name, true
	}
	if bodyHas(body, "type") {
		value := body["type"]
		text, isString := value.(string)
		if !isString || text != "api_key" {
			return nil, "公开账号接口仅支持 API Key 账户"
		}
		parsed.AccountType, parsed.HasType = text, true
	}
	if bodyHas(body, "baseUrl") {
		baseURL, issue := optionalTrimmedBody(body, "baseUrl", 1, 500)
		if issue != "" {
			return nil, issue
		}
		parsed.BaseURL, parsed.HasBaseURL = baseURL, true
	}
	if bodyHas(body, "apiKey") {
		apiKey, issue := optionalTrimmedBody(body, "apiKey", 1, 1000)
		if issue != "" {
			return nil, issue
		}
		parsed.APIKey, parsed.HasAPIKey = apiKey, true
	}
	if bodyHas(body, "supportedModels") {
		raw := body["supportedModels"]
		items, isList := raw.([]any)
		if !isList {
			return nil, zodInvalidType("array", raw)
		}
		if len(items) > 500 {
			return nil, "Array must contain at most 500 element(s)"
		}
		values := make([]string, 0, len(items))
		for _, item := range items {
			text, isString := item.(string)
			if !isString {
				return nil, zodInvalidType("string", item)
			}
			trimmed := trimSpaces(text)
			if runeLen(trimmed) < 1 || runeLen(trimmed) > 120 {
				if runeLen(trimmed) < 1 {
					return nil, zodStringMin(1)
				}
				return nil, zodStringMax(120)
			}
			values = append(values, trimmed)
		}
		parsed.SupportedModels, parsed.HasSupportedModels = &values, true
	}
	if bodyHas(body, "status") {
		status, _, issue := bodyOptionalEnumField(body, "status", []string{"active", "disabled"})
		if issue != "" {
			return nil, issue
		}
		parsed.Status, parsed.HasStatus = status, true
	}
	if bodyHas(body, "concurrencyLimit") {
		if body["concurrencyLimit"] == nil {
			return nil, zodInvalidType("number", nil)
		}
		limit, _, issue := bodyOptionalInt(body["concurrencyLimit"], true, 1, 100000)
		if issue != "" {
			return nil, issue
		}
		parsed.ConcurrencyLimit, parsed.HasConcurrencyLimit = &limit, true
	}
	if bodyHas(body, "priority") {
		if body["priority"] == nil {
			return nil, zodInvalidType("number", nil)
		}
		priority, _, issue := bodyOptionalInt(body["priority"], true, 0, 100000)
		if issue != "" {
			return nil, issue
		}
		parsed.Priority, parsed.HasPriority = &priority, true
	}
	if bodyHas(body, "availabilitySchedule") {
		parsed.AvailabilitySchedule, parsed.HasSchedule = body["availabilitySchedule"], true
	}
	if bodyHas(body, "notes") {
		notes, issue := optionalTrimmedBody(body, "notes", 0, 1000)
		if issue != "" {
			return nil, issue
		}
		parsed.Notes, parsed.HasNotes = notes, true
	}
	if !hasAnyField(body, accountUpdateMutableFields) {
		return nil, "账号修改至少提供一个要修改的字段"
	}
	return parsed, ""
}

const publicAccountConcurrentChangeMessage = "账号配置已发生并发变更，请重试"

func (d *Deps) updateAccount(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, issue := parseAccountUpdateBody(body)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockAccountPush(w, body)
		return
	}
	owner, err := d.findAccountOwnerByID(r.Context(), parsed.AccountID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if owner == nil {
		d.writeNotFoundEnvelope(w, "账号不存在")
		return
	}
	target, targetErr := d.resolveOwnedTarget(r.Context(), parsed.TargetUsername, parsed.HasTarget, owner)
	if targetErr != nil || target == nil {
		d.writeNotFoundEnvelope(w, "账号不存在")
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	access := accounts.AccessScope{ViewerID: target.SystemAccountID}
	basic, err := d.AiAccounts.FindEditBasicDetail(r.Context(), parsed.AccountID, access)
	if err != nil {
		kernel.WriteBadRequest(w, serviceMessage(err, "账号修改失败"))
		return
	}
	if basic == nil {
		d.writeNotFoundEnvelope(w, "账号不存在")
		return
	}
	if basic.Type != "api_key" {
		kernel.WriteBadRequest(w, "公开账号修改仅支持 API Key 账户")
		return
	}
	profile, err := d.requireProviderProfile(r.Context(), basic.ProviderCode, basic.ProviderProtocolProfileID)
	if err != nil {
		kernel.WriteBadRequest(w, err.Error())
		return
	}
	if parsed.HasType {
		if err := assertSupportedPushAccountType(parsed.AccountType, profile); err != nil {
			kernel.WriteBadRequest(w, err.Error())
			return
		}
	}
	group, groupErr := d.resolveAccountGroupFilter(r, parsed, basic, access)
	if groupErr != nil {
		kernel.WriteBadRequest(w, groupErr.Error())
		return
	}
	input := accounts.PatchInput{ExpectedConfigRevision: basic.ConfigRevision}
	if parsed.HasName && parsed.Name != nil {
		input.Name = parsed.Name
	}
	if parsed.HasAPIKey || parsed.HasBaseURL {
		credentials := accounts.Credentials{}
		for key, value := range basic.Credentials {
			credentials[key] = value
		}
		apiKey := valueOrEmpty(parsed.APIKey, credentials["api_key"])
		baseURL := valueOrEmpty(parsed.BaseURL, credentials["base_url"])
		if baseURL == "" {
			kernel.WriteBadRequest(w, "Base URL 不能为空")
			return
		}
		if apiKey == "" {
			kernel.WriteBadRequest(w, "API Key 不能为空")
			return
		}
		credentials["api_key"] = apiKey
		credentials["base_url"] = baseURL
		input.Credentials = credentials
		input.CredentialsPresent = true
	}
	if parsed.HasSupportedModels {
		input.SupportedModels = normalizedStringList(*parsed.SupportedModels)
		input.SupportedModelsPresent = true
	}
	if parsed.HasStatus {
		status := "active"
		schedulable := true
		if parsed.Status == "disabled" {
			status = "disabled"
			schedulable = false
		}
		input.Status = &status
		input.Schedulable = &schedulable
	}
	if parsed.HasConcurrencyLimit {
		input.ConcurrencyLimit = parsed.ConcurrencyLimit
	}
	if parsed.HasPriority {
		priority := 0
		if parsed.Priority != nil {
			priority = *parsed.Priority
		}
		input.Priority = &priority
	}
	if parsed.HasNotes {
		input.Notes = parsed.Notes
	}
	if parsed.HasSchedule {
		input.AvailabilitySchedule = parsed.AvailabilitySchedule
		input.AvailabilitySchedulePresent = true
	}
	// 3-attempt CAS retry loop (retryPublicAccountUpdateAfterConfigConflict).
	var changed *accounts.PatchResult
	for attempt := 1; attempt <= 3; attempt++ {
		result, patchErr := d.AiAccounts.Patch(r.Context(), parsed.AccountID, input, access)
		if patchErr == nil {
			changed = result
			break
		}
		var revision *accounts.RevisionConflictError
		if !errors.As(patchErr, &revision) {
			kernelWriteBadRequest(w, serviceMessage(patchErr, "账号修改失败"))
			return
		}
		if attempt == 3 {
			kernelWriteError(w, http.StatusConflict, publicAccountConcurrentChangeMessage)
			return
		}
		// Re-read the current revision like the Node retry path.
		reloaded, readErr := d.AiAccounts.FindEditBasicDetail(r.Context(), parsed.AccountID, access)
		if readErr != nil || reloaded == nil {
			kernelWriteBadRequest(w, serviceMessage(readErr, "账号修改失败"))
			return
		}
		basic = reloaded
		input.ExpectedConfigRevision = basic.ConfigRevision
	}
	if changed == nil {
		d.writeNotFoundEnvelope(w, "账号不存在")
		return
	}
	item := d.readAccountItem(r, parsed.AccountID, target.SystemAccountID)
	summary := PublicAccountSummary{}
	if item != nil {
		models, _ := d.loadAccountSupportedModels(r.Context(), []string{parsed.AccountID})
		summary = sanitizeAccountItem(item, models[parsed.AccountID]).PublicAccountSummary
	}
	d.recordAccountWriteLog(r, context, "account_update", "external_integrations.public_account_update",
		"修改", summary.ID, summary.Name, target, false,
		map[string]any{"status": summary.Status, "schedulable": summary.Schedulable})
	groupID := ""
	groupName := ""
	if group != nil {
		groupID = group.ID
		groupName = group.Name
	} else if summary.BoundGroupID != nil {
		groupID = *summary.BoundGroupID
	}
	if groupName == "" && summary.BoundGroupName != nil {
		groupName = *summary.BoundGroupName
	}
	d.writeStatsEnvelope(w, map[string]any{
		"action": "updated",
		"target": PublicGroupTarget{
			PublicTarget: target.Public,
			GroupID:      groupID, GroupName: groupName, GroupCreated: false,
		},
		"account": summary,
	})
}

// resolveAccountGroupFilter mirrors resolvePublicAccountGroupFilterAsync.
func (d *Deps) resolveAccountGroupFilter(r *http.Request, parsed *accountUpdateBody, basic *accounts.EditBasicDetail, access accounts.AccessScope) (*accounts.ListItem, error) {
	if parsed.HasProvider && parsed.ProviderCode != basic.ProviderCode {
		return nil, errors.New("账号不存在")
	}
	profileCode := basic.ProviderCode
	if parsed.HasProvider && parsed.ProviderCode != "" {
		profileCode = parsed.ProviderCode
	}
	profileID, err := d.resolveOptionalProfileID(r.Context(), profileCode, true, parsed.ProfileID, parsed.HasProfileID)
	if err != nil {
		return nil, err
	}
	if profileID != nil && *profileID != basic.ProviderProtocolProfileID {
		return nil, errors.New("账号不存在")
	}
	if basic.ProviderProtocolProfileID == "" {
		return nil, errors.New("账号 providerProtocolProfileId 不能为空")
	}
	if !parsed.HasGroupName || parsed.TargetGroupName == "" {
		if basic.BoundGroupID == nil {
			return nil, nil
		}
		return d.readAccountGroupItem(r, access, *basic.BoundGroupID)
	}
	existing, err := d.findExistingTargetGroup(r.Context(), access.ViewerID, basic.ProviderCode, parsed.TargetGroupName)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, errors.New("账号不存在")
	}
	// The account must actually belong to that group.
	result, err := d.AiAccounts.ListPage(r.Context(), access, accounts.ListOptions{
		IDs: []string{basic.ID}, Page: 1, PageSize: 1,
		ProviderCode: basic.ProviderCode, ProviderProtocolProfileID: basic.ProviderProtocolProfileID,
		GroupID: existing.ID,
	})
	if err != nil {
		return nil, err
	}
	found := false
	for index := range result.Items {
		if result.Items[index].ID == basic.ID {
			found = true
		}
	}
	if !found {
		return nil, errors.New("账号不存在")
	}
	return d.readAccountGroupItem(r, access, existing.ID)
}

func (d *Deps) readAccountGroupItem(r *http.Request, access accounts.AccessScope, groupID string) (*accounts.ListItem, error) {
	// Node resolves the group summary via findGroupSummary; the public target
	// only needs id/name, both carried by the bound-group projection.
	result, err := d.AiAccounts.ListPage(r.Context(), access, accounts.ListOptions{GroupID: groupID, Page: 1, PageSize: 1})
	if err != nil {
		return nil, err
	}
	if len(result.Items) == 0 {
		// The group exists even when no account is bound; return an empty
		// projection carrier.
		return &accounts.ListItem{BoundGroupID: strPtr(groupID)}, nil
	}
	item := &result.Items[0]
	return item, nil
}

func valueOrEmpty(override *string, current any) string {
	if override != nil {
		return *override
	}
	if text, isString := current.(string); isString {
		return trimSpaces(text)
	}
	return ""
}

// accountDeleteBody mirrors accountDeleteSchema (strict).
type accountDeleteBody struct {
	AccountID       string
	TargetUsername  string
	HasTarget       bool
	TargetGroupName string
	HasGroupName    bool
	ProviderCode    string
	HasProvider     bool
	ProfileID       string
	HasProfileID    bool
}

func parseAccountDeleteBody(body map[string]any) (*accountDeleteBody, string) {
	unknown := strictObjectKeys(body, "accountId", "targetUsername", "targetGroupName", "providerCode", "providerProtocolProfileId")
	if unknown != nil {
		return nil, zodUnrecognizedKeys(unknown...)
	}
	parsed := &accountDeleteBody{}
	accountID, issue := requiredTrimmedBody(body, "accountId", 1, 120)
	if issue != "" {
		return nil, issue
	}
	parsed.AccountID = accountID
	if bodyHas(body, "targetUsername") {
		username, issue := requiredTrimmedBody(body, "targetUsername", 2, 80)
		if issue != "" {
			return nil, issue
		}
		parsed.TargetUsername, parsed.HasTarget = username, true
	}
	if bodyHas(body, "targetGroupName") {
		groupName, issue := optionalTrimmedBody(body, "targetGroupName", 1, 80)
		if issue != "" {
			return nil, issue
		}
		if groupName != nil {
			parsed.TargetGroupName, parsed.HasGroupName = *groupName, true
		}
	}
	if bodyHas(body, "providerCode") {
		providerCode, issue := optionalTrimmedBody(body, "providerCode", 1, 60)
		if issue != "" {
			return nil, issue
		}
		if providerCode != nil {
			parsed.ProviderCode, parsed.HasProvider = *providerCode, true
		}
	}
	if bodyHas(body, "providerProtocolProfileId") {
		profileID, issue := optionalTrimmedBody(body, "providerProtocolProfileId", 1, 120)
		if issue != "" {
			return nil, issue
		}
		if profileID != nil {
			parsed.ProfileID, parsed.HasProfileID = *profileID, true
		}
	}
	return parsed, ""
}

func (d *Deps) deleteAccount(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	parsed, issue := parseAccountDeleteBody(body)
	if issue != "" {
		kernel.WriteBadRequest(w, issue)
		return
	}
	context := AuthContextFrom(r)
	if context.IsTestToken {
		d.mockAccountDelete(w, body)
		return
	}
	owner, err := d.findAccountOwnerByID(r.Context(), parsed.AccountID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if owner == nil {
		d.writeAccountNotFoundDelete(w, parsed)
		return
	}
	target, targetErr := d.resolveOwnedTarget(r.Context(), parsed.TargetUsername, parsed.HasTarget, owner)
	if targetErr != nil || target == nil {
		d.writeAccountNotFoundDelete(w, parsed)
		return
	}
	if target.Account.Status != "active" {
		kernel.WriteBadRequest(w, "目标用户已停用："+target.Account.Username)
		return
	}
	access := accounts.AccessScope{ViewerID: target.SystemAccountID}
	basic, err := d.AiAccounts.FindEditBasicDetail(r.Context(), parsed.AccountID, access)
	if err != nil {
		kernel.WriteBadRequest(w, serviceMessage(err, "账号删除失败"))
		return
	}
	if basic == nil {
		d.writeAccountNotFoundDelete(w, parsed)
		return
	}
	if basic.Type != "api_key" {
		kernel.WriteBadRequest(w, "公开账号删除仅支持 API Key 账户")
		return
	}
	if _, err := d.resolveAccountGroupFilter(r, &accountUpdateBody{
		TargetUsername: parsed.TargetUsername, HasTarget: parsed.HasTarget,
		TargetGroupName: parsed.TargetGroupName, HasGroupName: parsed.HasGroupName,
		ProviderCode: parsed.ProviderCode, HasProvider: parsed.HasProvider,
		ProfileID: parsed.ProfileID, HasProfileID: parsed.HasProfileID,
	}, basic, access); err != nil {
		kernelWriteBadRequest(w, err.Error())
		return
	}
	deleted, err := d.AiAccounts.Delete(r.Context(), parsed.AccountID, access)
	if err != nil {
		kernelWriteBadRequest(w, serviceMessage(err, "账号删除失败"))
		return
	}
	if !deleted {
		kernelWriteBadRequest(w, "目标账号无法删除，可能正在作为授权实例使用")
		return
	}
	models, _ := d.loadAccountSupportedModels(r.Context(), []string{parsed.AccountID})
	summary := sanitizeAccountItem(&accounts.ListItem{
		ID: basic.ID, Name: basic.Name, ProviderCode: basic.ProviderCode,
		ProviderProtocolProfileID: basic.ProviderProtocolProfileID,
		ProtocolCode:              basic.ProtocolCode, ProtocolVersion: basic.ProtocolVersion,
		Type: basic.Type, ClientCompatibility: basic.ClientCompatibility,
		Status: basic.Status, BoundGroupID: basic.BoundGroupID, BoundGroupName: basic.BoundGroupName,
	}, models[parsed.AccountID]).PublicAccountSummary
	d.recordAccountDeleteLog(r, context, summary.ID, summary.Name, target, parsed)
	groupID := ""
	groupName := parsed.TargetGroupName
	if basic.BoundGroupID != nil {
		groupID = *basic.BoundGroupID
	}
	if groupName == "" && basic.BoundGroupName != nil {
		groupName = *basic.BoundGroupName
	}
	d.writeStatsEnvelope(w, map[string]any{
		"action": "deleted",
		"target": PublicGroupTarget{
			PublicTarget: target.Public,
			GroupID:      groupID, GroupName: groupName, GroupCreated: false,
		},
		"account": summary,
	})
}

// writeAccountNotFoundDelete mirrors notFoundAccountDeleteResponse (200).
func (d *Deps) writeAccountNotFoundDelete(w http.ResponseWriter, parsed *accountDeleteBody) {
	target := PublicGroupTarget{
		PublicTarget: emptyTarget(parsed.TargetUsername),
		GroupName:    parsed.TargetGroupName,
	}
	d.writeStatsEnvelope(w, map[string]any{
		"action": "not_found", "target": target, "account": nil,
	})
}
