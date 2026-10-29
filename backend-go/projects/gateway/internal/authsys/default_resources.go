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

// defaultResourceGroupSeed mirrors the narrowed DEFAULT_BUILT_IN_GROUPS
// contract (2026-10-07 默认资源收口; 2026-10-17 设计 §8 去供应商化): account
// creation provisions exactly one default group with no provider dimension —
// the identity is (system_account_id, name) and every owner keeps at most one
// is_default=1 group. Other groups are created explicitly by the operator.
type defaultResourceGroupSeed struct {
	name        string
	description string
}

// defaultResourceGroupSeeds 对齐 maintenance seed（pgSeedGroups / 内置分组
// INSERT）：组名「默认分组」、空描述；ID 仍由 newID("grp") 派生，与 seed 的
// grp_default_gpt_sys_admin 字面 ID 各自独立、派生链不受影响。
var defaultResourceGroupSeeds = []defaultResourceGroupSeed{
	{name: "默认分组"},
}

const (
	// defaultRouteStrategyName mirrors DEFAULT_ROUTE_STRATEGY_NAME.
	defaultRouteStrategyName = "默认路由"
	// missingDefaultGroupError mirrors the Node ensureDefaultRouteStrategies
	// failure message.
	missingDefaultGroupError = "创建默认策略路由前必须先创建默认分组"
	// builtinSchedulingPreferenceID 与 apikeys.BuiltinSchedulingPreferenceID、
	// maintenance schema.BuiltinSchedulingPreferenceID 与 maintenance
	// accountdefaultresources 同源锚点，改动即四处同改：调度偏好 P1 契约
	// 「缺省 → 内置固定 ID」，默认 Key 与 Store.Create 同形显式落内置行
	// （本文件初版早于 P1，曾漏列导致默认 Key 绑定落 NULL，随存量补齐命令
	// accountdefaultresources 同批回正）。
	builtinSchedulingPreferenceID = "schpref-builtin-chat-cost-first"
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

// EnsureDefaultResources runs inside the account-create transaction and
// provisions the narrowed default family (2026-10-07 默认资源收口; 2026-10-17
// 设计 §8 去供应商化): ensureDefaultGroups (the single 默认分组) ->
// ensureDefaultRouteStrategies -> ensureDefaultAPIKeys. AI 问答不创建 API Key
// （2026-10-24 免 Key 直调改造）：历史 chat-session ensure 链
// (cmd/juhe-ai-gateway chain_chat_keys.go EnsureChatAPIKey) 已整体删除，
// 本函数与其它资源创建路径都不再补建 chat Key。
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
	return e.ensureDefaultAPIKeys(ctx, tx, accountID, nowRFC3339)
}

// ensureDefaultGroups mirrors the maintenance built-in group seed (设计 §8
// 去供应商化): for the built-in seed, skip when the owner already has any
// is_default=1 group (每 owner 至多一个默认组，唯一部分索引
// idx_groups_owner_default_unique 保证), otherwise INSERT with enabled=1
// is_default=1 and the new-schema column set (no provider_code; group_type
// stays at its schema default).
func (e *SQLDefaultResources) ensureDefaultGroups(ctx context.Context, tx *sql.Tx, accountID, nowText string) error {
	for _, seed := range defaultResourceGroupSeeds {
		var existing string
		err := tx.QueryRowContext(ctx, e.store.bind(`SELECT id FROM `+e.store.table("groups")+` WHERE system_account_id = ? AND is_default = 1 ORDER BY updated_at DESC, id ASC LIMIT 1`), accountID).Scan(&existing)
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
		if _, err := tx.ExecContext(ctx, e.store.bind(`INSERT INTO `+e.store.table("groups")+` (id, system_account_id, name, description, enabled, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, 1, 1, ?, ?)`),
			id, accountID, seed.name, seed.description, nowText, nowText); err != nil {
			return err
		}
	}
	return nil
}

// ensureDefaultRouteStrategies mirrors the maintenance default-groups select
// (设计 §8): default groups are exactly the owner's is_default=1 rows ordered
// by created_at ASC, id ASC — there is no provider dimension and no hybrid
// exclusion anymore; per group an idempotent route strategy + binding pair
// with the Node description and deduplicated name.
func (e *SQLDefaultResources) ensureDefaultRouteStrategies(ctx context.Context, tx *sql.Tx, accountID, nowText string) error {
	rows, err := tx.QueryContext(ctx, e.store.bind(`SELECT id, name FROM `+e.store.table("groups")+` WHERE system_account_id = ? AND is_default = 1 ORDER BY created_at ASC, id ASC`), accountID)
	if err != nil {
		return err
	}
	type bindableGroup struct{ id, name string }
	groups := []bindableGroup{}
	for rows.Next() {
		var group bindableGroup
		if err := rows.Scan(&group.id, &group.name); err != nil {
			rows.Close()
			return err
		}
		groups = append(groups, group)
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

// ensureDefaultAPIKeys mirrors the maintenance default API key seed (设计 §8):
// one default API key per default route strategy, skipping strategies that
// already carry an is_default=1 key. Groups have no provider dimension, so
// there is no hybrid exclusion filter.
func (e *SQLDefaultResources) ensureDefaultAPIKeys(ctx context.Context, tx *sql.Tx, accountID, nowText string) error {
	rows, err := tx.QueryContext(ctx, e.store.bind(`SELECT route_strategies.id, route_strategies.name FROM `+e.store.table("route_strategies")+` route_strategies INNER JOIN `+e.store.table("route_strategy_groups")+` route_strategy_groups ON route_strategy_groups.route_strategy_id = route_strategies.id AND route_strategy_groups.system_account_id = route_strategies.system_account_id INNER JOIN `+e.store.table("groups")+` groups ON groups.id = route_strategy_groups.group_id AND groups.system_account_id = route_strategy_groups.system_account_id WHERE route_strategies.system_account_id = ? AND route_strategies.is_default = 1 ORDER BY route_strategies.created_at ASC, route_strategies.id ASC`), accountID)
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

// insertAPIKey mirrors the Node default INSERT: the plaintext is sealed
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
	_, err = tx.ExecContext(ctx, e.store.bind(`INSERT INTO `+e.store.table("api_keys")+` (id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, expires_at, quota_limits_json, availability_schedule_json, availability_schedule_next_check_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, NULL, NULL, NULL, NULL, ?, ?)`),
		id, accountID, routeID, name, description, hex.EncodeToString(sum[:]), secret[:8], secret[len(secret)-8:], sealed, isDefault, purpose, builtinSchedulingPreferenceID, nowText, nowText)
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
