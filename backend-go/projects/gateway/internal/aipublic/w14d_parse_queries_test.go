// w14d_parse_queries_test.go pins the previously uncovered branches of the
// aipublic list-query and body parsers across the account, strategy, group
// and api-key domains: strict-key rejection, the required targetUsername
// chain, every optional filter arm, the paging normalizers and the
// add/update body schemas including the "at least one field" refinements.
package aipublic

import (
	"net/url"
	"strings"
	"testing"
)

// w14dQuery builds url.Values from key/value pairs.
func w14dQuery(pairs ...string) url.Values {
	values := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		values.Set(pairs[i], pairs[i+1])
	}
	return values
}

func TestW14dParseAccountListQuery(t *testing.T) {
	// The happy path fills every optional filter.
	query, issue := parseAccountListQuery(w14dQuery("targetUsername", " alice ",
		"targetGroupName", "g", "providerCode", "gpt", "providerProtocolProfileId", "p1",
		"groupId", "grp", "keyword", "kw", "type", "chat", "status", "active",
		"schedulable", "enabled", "page", "2", "pageSize", "50"))
	if issue != "" {
		t.Fatalf("happy path: %q", issue)
	}
	if query.TargetUsername != "alice" || !query.HasGroupName || query.TargetGroupName != "g" ||
		!query.HasProvider || query.ProviderCode != "gpt" || !query.HasProfileID ||
		!query.HasGroupID || !query.HasKeyword || query.Keyword != "kw" || !query.HasType ||
		!query.HasStatus || query.Schedulable != "enabled" || query.Page != 2 || query.PageSize != 50 {
		t.Fatalf("parsed query: %+v", query)
	}
	// Unknown keys.
	_, issue = parseAccountListQuery(w14dQuery("targetUsername", "a", "bogus", "1"))
	if issue != "Unrecognized key(s) in object: bogus" {
		t.Fatalf("unknown key: %q", issue)
	}
	// Required username (absent, short).
	_, issue = parseAccountListQuery(w14dQuery())
	if issue != "Required" {
		t.Fatalf("required: %q", issue)
	}
	_, issue = parseAccountListQuery(w14dQuery("targetUsername", "x"))
	if issue != zodStringMin(2) {
		t.Fatalf("short username: %q", issue)
	}
	// Per-filter issue arms.
	_, issue = parseAccountListQuery(w14dQuery("targetUsername", "alice", "targetGroupName", " "))
	if issue != zodStringMin(1) {
		t.Fatalf("group name: %q", issue)
	}
	_, issue = parseAccountListQuery(w14dQuery("targetUsername", "alice", "keyword", strings.Repeat("k", 121)))
	if issue != zodStringMax(120) {
		t.Fatalf("keyword max: %q", issue)
	}
	_, issue = parseAccountListQuery(w14dQuery("targetUsername", "alice", "schedulable", "nope"))
	if issue != zodEnumMessage(accountSchedulableOptions, "nope") {
		t.Fatalf("schedulable: %q", issue)
	}
	_, issue = parseAccountListQuery(w14dQuery("targetUsername", "alice", "page", "0"))
	if issue != zodNumberMin(1) {
		t.Fatalf("page min: %q", issue)
	}
	_, issue = parseAccountListQuery(w14dQuery("targetUsername", "alice", "pageSize", "101"))
	if issue != zodNumberMax(100) {
		t.Fatalf("pageSize max: %q", issue)
	}
	_, issue = parseAccountListQuery(w14dQuery("targetUsername", "alice", "page", "abc"))
	if issue != "Expected number, received nan" {
		t.Fatalf("page nan: %q", issue)
	}
}

func TestW14dParseStrategyListQuery(t *testing.T) {
	query, issue := parseStrategyListQuery(w14dQuery("targetUsername", "alice", "keyword", "k",
		"mode", "hybrid_smart", "status", "active", "page", "1", "pageSize", "10"))
	if issue != "" {
		t.Fatalf("happy path: %q", issue)
	}
	if query.Mode != "hybrid_smart" || query.Status != "active" || !query.HasMode || !query.HasStatus {
		t.Fatalf("parsed: %+v", query)
	}
	_, issue = parseStrategyListQuery(w14dQuery("targetUsername", "alice", "mode", "mystery"))
	if issue != zodEnumMessage(strategyModeOptions, "mystery") {
		t.Fatalf("mode: %q", issue)
	}
	_, issue = parseStrategyListQuery(w14dQuery("targetUsername", "alice", "status", "mystery"))
	if issue != zodEnumMessage(strategyStatusOptions, "mystery") {
		t.Fatalf("status: %q", issue)
	}
	_, issue = parseStrategyListQuery(w14dQuery("targetUsername", "alice", "pageSize", "200"))
	if issue != zodNumberMax(100) {
		t.Fatalf("pageSize: %q", issue)
	}
}

func TestW14dParseGroupAndApiKeyListQueries(t *testing.T) {
	groups, issue := parseGroupListQuery(w14dQuery("targetUsername", "alice", "keyword", "k", "providerCode", "gpt"))
	if issue != "" {
		t.Fatalf("groups: %q", issue)
	}
	if groups.Keyword != "k" || groups.ProviderCode != "gpt" {
		t.Fatalf("groups parsed: %+v", groups)
	}
	_, issue = parseGroupListQuery(w14dQuery("targetUsername", "alice", "providerCode", strings.Repeat("p", 61)))
	if issue != zodStringMax(60) {
		t.Fatalf("groups provider max: %q", issue)
	}

	keys, issue := parseApiKeyListQuery(w14dQuery("targetUsername", "alice", "routeStrategyId", "s1",
		"keyword", "k", "status", "all", "page", "3", "pageSize", "25"))
	if issue != "" {
		t.Fatalf("api keys: %q", issue)
	}
	if keys.RouteStrategyID != "s1" || keys.Status != "all" || keys.Page != 3 || keys.PageSize != 25 {
		t.Fatalf("keys parsed: %+v", keys)
	}
	_, issue = parseApiKeyListQuery(w14dQuery("targetUsername", "alice", "status", "mystery"))
	if issue != zodEnumMessage([]string{"active", "disabled", "all"}, "mystery") {
		t.Fatalf("keys status: %q", issue)
	}
}

func TestW14dParseStrategyBodySchemas(t *testing.T) {
	// parseStrategyAddBody: required fields and the bindings list.
	parsed, issue := parseStrategyAddBody(map[string]any{
		"targetUsername": " alice ", "name": "strategy", "mode": "normal",
		"groupBindings":  []any{map[string]any{"groupId": "g1"}},
	})
	if issue != "" {
		t.Fatalf("strategy add: %q", issue)
	}
	if parsed.TargetUsername != "alice" || parsed.Name != "strategy" || parsed.Mode != "normal" ||
		len(parsed.Bindings) != 1 {
		t.Fatalf("strategy add parsed: %+v", parsed)
	}
	_, issue = parseStrategyAddBody(map[string]any{})
	if issue != "Required" {
		t.Fatalf("strategy add required: %q", issue)
	}
	_, issue = parseStrategyAddBody(map[string]any{"targetUsername": "x", "name": "n"})
	if issue != zodStringMin(2) {
		t.Fatalf("strategy add short username: %q", issue)
	}
	_, issue = parseStrategyAddBody(map[string]any{"bogus": 1})
	if issue != "Unrecognized key(s) in object: bogus" {
		t.Fatalf("strategy add unknown: %q", issue)
	}

	// parseStrategyBindings list arms.
	bindings, issue := parseStrategyBindings(map[string]any{
		"groupBindings": []any{map[string]any{"groupId": " g1 ", "priority": float64(2)}},
	})
	if issue != "" || len(bindings) != 1 || bindings[0].GroupID != "g1" || *bindings[0].Priority != 2 {
		t.Fatalf("bindings: %+v %q", bindings, issue)
	}
	if _, issue = parseStrategyBindings(map[string]any{"groupBindings": "x"}); issue == "" {
		t.Fatal("non-array bindings must fail")
	}
	if _, issue = parseStrategyBindings(map[string]any{"groupBindings": []any{}}); issue == "" {
		t.Fatal("empty bindings must fail")
	}
	if _, issue = parseStrategyBindings(map[string]any{}); issue != "Required" {
		t.Fatalf("absent bindings issue: %q", issue)
	}

	// parseStrategyUpdateBody requires at least one mutable field.
	if _, issue = parseStrategyUpdateBody(map[string]any{"routeStrategyId": "s1"}); issue == "" {
		t.Fatal("empty update must fail")
	}
	parsedUpdate, issue := parseStrategyUpdateBody(map[string]any{"routeStrategyId": " s1 ", "name": "n", "targetUsername": "alice"})
	if issue != "" || parsedUpdate.RouteStrategyID != "s1" || parsedUpdate.Name == nil || *parsedUpdate.Name != "n" {
		t.Fatalf("strategy update: %+v %q", parsedUpdate, issue)
	}
}

func TestW14dParseGroupBodySchemas(t *testing.T) {
	add, issue := parseGroupAddBody(map[string]any{
		"targetUsername": " alice ", "name": "group", "providerCode": " gpt ",
		"description": "d", "enabled": true, "groupType": "personal",
	})
	if issue != "" {
		t.Fatalf("group add: %q", issue)
	}
	if add.TargetUsername != "alice" || add.ProviderCode != "gpt" || add.Description == nil ||
		add.Enabled == nil || !*add.Enabled || add.GroupType != "personal" {
		t.Fatalf("group add parsed: %+v", add)
	}
	_, issue = parseGroupAddBody(map[string]any{
		"targetUsername": "alice", "name": "n", "providerCode": "gpt", "groupType": "mystery",
	})
	if issue != zodEnumMessage([]string{"personal", "high_concurrency"}, "mystery") {
		t.Fatalf("group add enum: %q", issue)
	}

	// Update: at least one mutable field is required.
	if _, issue = parseGroupUpdateBody(map[string]any{"groupId": "g"}); issue != "分组修改至少提供一个要修改的字段" {
		t.Fatalf("group update refine: %q", issue)
	}
	update, issue := parseGroupUpdateBody(map[string]any{
		"groupId": " g ", "name": "n", "providerCode": "p", "description": nil,
		"enabled": false, "groupType": "high_concurrency", "targetUsername": "alice",
	})
	if issue != "" {
		t.Fatalf("group update: %q", issue)
	}
	if update.GroupID != "g" || update.Name == nil || *update.Name != "n" || update.ProviderCode == nil ||
		update.Description != nil || update.Enabled == nil || update.GroupType == nil || !update.HasGroupType ||
		update.TargetUsername != "alice" {
		t.Fatalf("group update parsed: %+v", update)
	}
}

func TestW14dParseApiKeyBodySchemas(t *testing.T) {
	add, issue := parseApiKeyAddBody(map[string]any{
		"targetUsername": " alice ", "name": "key", "routeStrategyId": " s1 ",
		"description": nil, "status": "active", "expiresAt": "2030-01-01",
		"quotaLimits": map[string]any{"rpm": 1.0}, "availabilitySchedule": []any{},
	})
	if issue != "" {
		t.Fatalf("key add: %q", issue)
	}
	if add.TargetUsername != "alice" || add.Name != "key" || add.RouteStrategyID != "s1" ||
		add.Description != nil || !add.HasDescription || add.Status != "active" ||
		add.ExpiresAt != "2030-01-01" || !add.HasQuotaLimits || !add.HasSchedule {
		t.Fatalf("key add parsed: %+v", add)
	}
	_, issue = parseApiKeyAddBody(map[string]any{"targetUsername": "alice"})
	if issue != "Required" {
		t.Fatalf("key add required: %q", issue)
	}

	// Update: at least one mutable field is required.
	if _, issue = parseApiKeyUpdateBody(map[string]any{"apiKeyId": "k"}); issue != "API Key 修改至少提供一个要修改的字段" {
		t.Fatalf("key update refine: %q", issue)
	}
	update, issue := parseApiKeyUpdateBody(map[string]any{
		"apiKeyId": " k ", "targetUsername": " alice ", "description": "d",
		"routeStrategyId": " s9 ", "status": "disabled",
	})
	if issue != "" {
		t.Fatalf("key update: %q", issue)
	}
	if update.ApiKeyID != "k" || update.TargetUsername != "alice" ||
		update.Description == nil || !update.HasStrategy || update.RouteStrategyID != "s9" ||
		update.Status != "disabled" || !update.HasStatus {
		t.Fatalf("key update parsed: %+v", update)
	}
	// A blank routeStrategyId fails its min-1 optional string check.
	if _, issue = parseApiKeyUpdateBody(map[string]any{
		"apiKeyId": "k", "routeStrategyId": " ",
	}); issue != zodStringMin(1) {
		t.Fatalf("blank strategy id issue: %q", issue)
	}
}

func TestW14dPagingNormalizers(t *testing.T) {
	deps := &Deps{}
	for _, paging := range []func(bool, int, bool, int) (int, int){
		deps.paging, deps.mockPaging, deps.accountPaging, deps.strategyPaging, deps.apiKeyPaging,
	} {
		if page, pageSize := paging(false, 0, false, 0); page != 1 || pageSize == 0 {
			t.Fatalf("defaults: %d %d", page, pageSize)
		}
		if page, pageSize := paging(true, -5, true, 0); page != 1 || pageSize <= 0 {
			t.Fatalf("defaults on non-positive input: %d %d", page, pageSize)
		}
		if page, pageSize := paging(true, 3, true, 40); page != 3 || pageSize != 40 {
			t.Fatalf("explicit: %d %d", page, pageSize)
		}
	}
}

func TestW14dGroupHelpers(t *testing.T) {
	if got := textPrefixUpperBound("abc"); got != "abd" {
		t.Fatalf("upper bound: %s", got)
	}
	// The empty prefix still yields a one-rune upper bound.
	if got := textPrefixUpperBound(""); len(got) == 0 {
		t.Fatalf("empty upper bound: %q", got)
	}
	if got := pagedTotalUpperBound(2, 20, 20, true); got <= 0 {
		t.Fatalf("total bound: %d", got)
	}
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatal("boolToInt")
	}
	if got := groupTypeOr("personal", true); got != "personal" {
		t.Fatalf("groupTypeOr present: %s", got)
	}
	if got := groupTypeOr("", false); got == "" {
		t.Fatalf("groupTypeOr fallback: %s", got)
	}
	if strPtr("v") == nil || *strPtr("v") != "v" {
		t.Fatal("strPtr")
	}
}
