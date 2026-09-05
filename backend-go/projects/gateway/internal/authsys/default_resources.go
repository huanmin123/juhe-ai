package authsys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

// defaultResourceGroupSeed mirrors DEFAULT_BUILT_IN_GROUPS
// (storage/schema-defaults.ts:105-114) in order.
type defaultResourceGroupSeed struct {
	name        string
	provider    string
	description string
}

var defaultResourceGroupSeeds = []defaultResourceGroupSeed{
	{name: "默认 OpenAI 兼容分组", provider: "openai"},
	{name: "默认 GPT 分组", provider: "gpt"},
	{name: "默认 xAI 分组", provider: "xai"},
	{name: "默认 DeepSeek 分组", provider: "deepseek"},
	{name: "默认 Anthropic 分组", provider: "anthropic"},
	{name: "默认 Gemini 分组", provider: "gemini"},
	{name: "默认 GLM 分组", provider: "glm"},
	{name: "默认混合供应商分组", provider: "hybrid", description: "混合供应商账户保存真实上游凭据和 Base URL，允许账户内配置跨协议入口映射"},
}

const (
	hybridProviderCode = "hybrid"
	gptVendorCode      = "gpt"

	// defaultRouteStrategyName mirrors DEFAULT_ROUTE_STRATEGY_NAME.
	defaultRouteStrategyName = "默认路由"
	// defaultChatAPIKeyName mirrors the ensureChatApiKey base name.
	defaultChatAPIKeyName = "AI 对话 API Key"
	// missingGPTRouteError mirrors the Node ensureChatApiKey failure message.
	missingGPTRouteError = "创建 AI 对话 API Key 前必须先创建 GPT 默认策略路由"
	// missingDefaultGroupError mirrors the Node ensureDefaultRouteStrategies
	// failure message.
	missingDefaultGroupError = "创建默认策略路由前必须先创建默认分组"
)

// SQLDefaultResources implements DefaultResourceEnsurer with the same
// statement semantics as the Node repository ensure* functions
// (system-accounts.repository.ts:1350-1378, route-strategy.repository.ts:884-921,
// api-key.repository.ts:1691-1811). It runs inside the account-create
// transaction; a nil sealer fails closed before any INSERT.
type SQLDefaultResources struct {
	store  *AccountStore
	sealer SecretSealer
}

// NewSQLDefaultResources binds the ensurer to the store's dual-mode SQL
// dialect and the Node-compatible secret sealer.
func NewSQLDefaultResources(store *AccountStore, sealer SecretSealer) *SQLDefaultResources {
	return &SQLDefaultResources{store: store, sealer: sealer}
}

// EnsureDefaultResources mirrors createSystemAccountWithPasswordHashInClientAsync
// lines 629-632: ensureDefaultBuiltInGroups -> ensureDefaultRouteStrategies ->
// ensureDefaultApiKeys -> ensureChatApiKey.
func (e *SQLDefaultResources) EnsureDefaultResources(ctx context.Context, tx *sql.Tx, accountID, nowRFC3339 string) error {
	if e == nil || e.store == nil {
		return errors.New("default resource ensurer is not initialized")
	}
	if e.sealer == nil {
		return errors.New("system account default API key secret sealer is required")
	}
	if err := e.ensureDefaultGroups(ctx, tx, accountID, nowRFC3339); err != nil {
		return err
	}
	if err := e.ensureDefaultRouteStrategies(ctx, tx, accountID, nowRFC3339); err != nil {
		return err
	}
	if err := e.ensureDefaultAPIKeys(ctx, tx, accountID, nowRFC3339); err != nil {
		return err
	}
	return e.ensureChatAPIKey(ctx, tx, accountID, nowRFC3339)
}

// ensureDefaultGroups mirrors ensureDefaultBuiltInGroupsForSystemAccountAsync:
// for every built-in seed, skip when a default group for the provider already
// exists, otherwise INSERT with enabled=1 is_default=1 (group_type stays at
// its schema default 'personal', exactly like the Node statement).
func (e *SQLDefaultResources) ensureDefaultGroups(ctx context.Context, tx *sql.Tx, accountID, nowText string) error {
	for _, seed := range defaultResourceGroupSeeds {
		var existing string
		err := tx.QueryRowContext(ctx, e.store.bind(`SELECT id FROM `+e.store.table("groups")+` WHERE system_account_id = ? AND provider_code = ? AND is_default = 1 ORDER BY updated_at DESC, id ASC LIMIT 1`), accountID, seed.provider).Scan(&existing)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id, err := newID("grp")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, e.store.bind(`INSERT INTO `+e.store.table("groups")+` (id, system_account_id, name, provider_code, description, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)`),
			id, accountID, seed.name, seed.provider, seed.description, nowText, nowText); err != nil {
			return err
		}
	}
	return nil
}

// ensureDefaultRouteStrategies mirrors ensureDefaultRouteStrategiesForSystemAccountAsync:
// default groups ordered by created_at ASC, id ASC minus the hybrid provider;
// per group an idempotent route strategy + binding pair with the Node
// description and deduplicated name.
func (e *SQLDefaultResources) ensureDefaultRouteStrategies(ctx context.Context, tx *sql.Tx, accountID, nowText string) error {
	rows, err := tx.QueryContext(ctx, e.store.bind(`SELECT id, provider_code, name FROM `+e.store.table("groups")+` WHERE system_account_id = ? AND is_default = 1 ORDER BY created_at ASC, id ASC`), accountID)
	if err != nil {
		return err
	}
	type bindableGroup struct{ id, provider, name string }
	groups := []bindableGroup{}
	for rows.Next() {
		var group bindableGroup
		if err := rows.Scan(&group.id, &group.provider, &group.name); err != nil {
			rows.Close()
			return err
		}
		if group.provider != hybridProviderCode {
			groups = append(groups, group)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(groups) == 0 {
		return errors.New(missingDefaultGroupError)
	}
	for _, group := range groups {
		existing, err := e.defaultRouteStrategyIDForGroup(ctx, tx, accountID, group.id)
		if err != nil {
			return err
		}
		if existing != "" {
			continue
		}
		baseName := defaultRouteStrategyNameForGroup(group.name)
		name, err := e.nextDefaultResourceName(ctx, tx, "route_strategies", accountID, baseName)
		if err != nil {
			return err
		}
		id, err := newID("route_strategy")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, e.store.bind(`INSERT INTO `+e.store.table("route_strategies")+` (id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at) VALUES (?, ?, ?, ?, 'normal', 'active', 1, NULL, ?, ?)`),
			id, accountID, name, "系统默认普通路由，绑定"+group.name+"。", nowText, nowText); err != nil {
			return err
		}
		bindingID, err := newID("rsg")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, e.store.bind(`INSERT INTO `+e.store.table("route_strategy_groups")+` (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 1, 'active', ?, ?)`),
			bindingID, id, accountID, group.id, nowText, nowText); err != nil {
			return err
		}
	}
	return nil
}

// defaultRouteStrategyIDForGroup mirrors defaultRouteStrategyIdForGroupAsync.
func (e *SQLDefaultResources) defaultRouteStrategyIDForGroup(ctx context.Context, tx *sql.Tx, accountID, groupID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, e.store.bind(`SELECT route_strategies.id FROM `+e.store.table("route_strategies")+` route_strategies INNER JOIN `+e.store.table("route_strategy_groups")+` route_strategy_groups ON route_strategy_groups.route_strategy_id = route_strategies.id AND route_strategy_groups.system_account_id = route_strategies.system_account_id WHERE route_strategies.system_account_id = ? AND route_strategies.is_default = 1 AND route_strategy_groups.group_id = ? ORDER BY route_strategies.updated_at DESC, route_strategies.id ASC LIMIT 1`), accountID, groupID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ensureDefaultAPIKeys mirrors ensureDefaultApiKeysForSystemAccountAsync:
// one default API key per default route strategy (hybrid excluded), skipping
// strategies that already carry an is_default=1 key.
func (e *SQLDefaultResources) ensureDefaultAPIKeys(ctx context.Context, tx *sql.Tx, accountID, nowText string) error {
	rows, err := tx.QueryContext(ctx, e.store.bind(`SELECT route_strategies.id, route_strategies.name FROM `+e.store.table("route_strategies")+` route_strategies INNER JOIN `+e.store.table("route_strategy_groups")+` route_strategy_groups ON route_strategy_groups.route_strategy_id = route_strategies.id AND route_strategy_groups.system_account_id = route_strategies.system_account_id INNER JOIN `+e.store.table("groups")+` groups ON groups.id = route_strategy_groups.group_id AND groups.system_account_id = route_strategy_groups.system_account_id WHERE route_strategies.system_account_id = ? AND route_strategies.is_default = 1 AND groups.provider_code <> ? ORDER BY route_strategies.created_at ASC, route_strategies.id ASC`), accountID, hybridProviderCode)
	if err != nil {
		return err
	}
	type defaultRoute struct{ id, name string }
	routes := []defaultRoute{}
	for rows.Next() {
		var route defaultRoute
		if err := rows.Scan(&route.id, &route.name); err != nil {
			rows.Close()
			return err
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, route := range routes {
		var existing string
		err := tx.QueryRowContext(ctx, e.store.bind(`SELECT id FROM `+e.store.table("api_keys")+` WHERE route_strategy_id = ? AND is_default = 1 ORDER BY created_at ASC, id ASC LIMIT 1`), route.id).Scan(&existing)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		baseName := defaultAPIKeyNameForRouteStrategy(route.name)
		name, err := e.nextDefaultResourceName(ctx, tx, "api_keys", accountID, baseName)
		if err != nil {
			return err
		}
		if err := e.insertAPIKey(ctx, tx, accountID, route.id, name, "系统默认 API Key，绑定"+route.name+"。", "general", 1, nowText); err != nil {
			return err
		}
	}
	return nil
}

// ensureChatAPIKey mirrors ensureChatApiKeyForSystemAccountAsync: at most one
// purpose='chat' key per account, bound to the default active GPT route.
func (e *SQLDefaultResources) ensureChatAPIKey(ctx context.Context, tx *sql.Tx, accountID, nowText string) error {
	var existing string
	err := tx.QueryRowContext(ctx, e.store.bind(`SELECT id FROM `+e.store.table("api_keys")+` WHERE system_account_id = ? AND purpose = 'chat' LIMIT 1`), accountID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var routeID, routeName string
	err = tx.QueryRowContext(ctx, e.store.bind(`SELECT route_strategies.id, route_strategies.name FROM `+e.store.table("route_strategies")+` route_strategies INNER JOIN `+e.store.table("route_strategy_groups")+` route_strategy_groups ON route_strategy_groups.route_strategy_id = route_strategies.id AND route_strategy_groups.system_account_id = route_strategies.system_account_id AND route_strategy_groups.status = 'active' INNER JOIN `+e.store.table("groups")+` groups ON groups.id = route_strategy_groups.group_id AND groups.system_account_id = route_strategy_groups.system_account_id AND groups.enabled = 1 AND groups.is_default = 1 WHERE route_strategies.system_account_id = ? AND route_strategies.status = 'active' AND route_strategies.is_default = 1 AND groups.provider_code = ? ORDER BY route_strategies.created_at ASC, route_strategies.id ASC LIMIT 1`), accountID, gptVendorCode).Scan(&routeID, &routeName)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New(missingGPTRouteError)
	}
	if err != nil {
		return err
	}
	name, err := e.nextDefaultResourceName(ctx, tx, "api_keys", accountID, defaultChatAPIKeyName)
	if err != nil {
		return err
	}
	return e.insertAPIKey(ctx, tx, accountID, routeID, name, "AI 对话专用 API Key，默认绑定"+routeName+"，可在 API Key 页面修改策略路由。", "chat", 0, nowText)
}

// insertAPIKey mirrors the Node default/chat INSERT: the plaintext is sealed
// through the injected Node-compatible envelope, the lookup hash is the
// sha256 hex of the plaintext, and purpose/is_default follow the caller.
func (e *SQLDefaultResources) insertAPIKey(ctx context.Context, tx *sql.Tx, accountID, routeID, name, description, purpose string, isDefault int, nowText string) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	secret := "sk-" + hex.EncodeToString(raw)
	sealed, err := e.sealer.SealSecret(ctx, secret)
	if err != nil {
		return err
	}
	if strings.TrimSpace(sealed) == "" {
		return errors.New("system account default API key secret sealer returned an empty envelope")
	}
	sum := sha256.Sum256([]byte(secret))
	id, err := newID("key")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, e.store.bind(`INSERT INTO `+e.store.table("api_keys")+` (id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json, availability_schedule_json, availability_schedule_next_check_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, NULL, NULL, NULL, NULL, ?, ?)`),
		id, accountID, routeID, name, description, hex.EncodeToString(sum[:]), secret[:8], secret[len(secret)-8:], sealed, isDefault, purpose, nowText, nowText)
	return err
}

// nextDefaultResourceName mirrors nextDefaultRouteStrategyNameAsync /
// nextDefaultApiKeyNameAsync: the base name, else the first free "<base> N"
// with N in 2..1000, else "<base> <unix-millis>".
func (e *SQLDefaultResources) nextDefaultResourceName(ctx context.Context, tx *sql.Tx, table, accountID, baseName string) (string, error) {
	rows, err := tx.QueryContext(ctx, e.store.bind(`SELECT name FROM `+e.store.table(table)+` WHERE system_account_id = ? AND (name = ? OR name LIKE ? ESCAPE '\')`), accountID, baseName, baseName+" %")
	if err != nil {
		return "", err
	}
	existing := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return "", err
		}
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			existing[trimmed] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	if _, taken := existing[baseName]; !taken {
		return baseName, nil
	}
	for index := 2; index <= 1000; index++ {
		candidate := baseName + " " + itoa(index)
		if _, taken := existing[candidate]; !taken {
			return candidate, nil
		}
	}
	return baseName + " " + itoa(int(e.store.now().UnixMilli())), nil
}

// defaultRouteStrategyNameForGroup mirrors defaultRouteStrategyNameForGroup:
// the trailing 分组 becomes 路由, an empty name falls back to 默认路由.
func defaultRouteStrategyNameForGroup(groupName string) string {
	name := strings.TrimSpace(groupName)
	if name == "" {
		return defaultRouteStrategyName
	}
	if suffix := "分组"; strings.HasSuffix(name, suffix) {
		return strings.TrimSuffix(name, suffix) + "路由"
	}
	return name
}

// defaultAPIKeyNameForRouteStrategy mirrors defaultApiKeyNameForRouteStrategy:
// the trailing 路由 becomes API Key.
func defaultAPIKeyNameForRouteStrategy(routeName string) string {
	if suffix := "路由"; strings.HasSuffix(routeName, suffix) {
		return strings.TrimSuffix(routeName, suffix) + "API Key"
	}
	return routeName
}
