package main

// G20 phase-3 chat API key provision port: the purpose='chat' key lifecycle
// the /__aisys__/api/my-chat family needs (Node
// storage/chat-api-key.repository.ts findChatApiKeySecretAsync +
// storage/api-key.repository.ts ensureChatApiKeyForSystemAccountAsync and its
// collaborators ensureDefaultRouteStrategiesForSystemAccountAsync /
// defaultGptRouteStrategyForSystemAccountAsync / nextDefaultApiKeyNameAsync /
// chatApiKeyIdForSystemAccountAsync). Sealed plaintext, key hashing and key
// generation reuse the apikeys package (Node storage/crypto.ts mirrors).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/chat"
)

// chatAPIKeyProvider implements chat.ChatAPIKeyProvider over the business
// database.
type chatAPIKeyProvider struct {
	db       *sql.DB
	postgres bool
	secret   string
	now      func() time.Time
	newID    func(prefix string) string
}

func newChatAPIKeyProvider(db *sql.DB, postgres bool, secret string) *chatAPIKeyProvider {
	return &chatAPIKeyProvider{db: db, postgres: postgres, secret: secret, now: time.Now, newID: newCompositionID}
}

func (p *chatAPIKeyProvider) table(name string) string {
	if p.postgres {
		return "juhe_business." + name
	}
	return name
}

func (p *chatAPIKeyProvider) bind(query string) string {
	if !p.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprint(index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

// EnsureChatAPIKey mirrors ensureChatApiKeyForSystemAccountAsync: the default
// route strategies are ensured first, then the purpose='chat' key is created
// against the GPT default strategy with the Node duplicate-race recovery.
func (p *chatAPIKeyProvider) EnsureChatAPIKey(ownerID string) (string, error) {
	timestamp := p.now().UTC().Format(chainTimeLayout)
	if err := p.ensureDefaultRouteStrategies(ownerID, timestamp); err != nil {
		return "", err
	}
	existing, err := p.chatApiKeyIdForSystemAccount(ownerID)
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}
	routeStrategy, err := p.defaultGptRouteStrategyForSystemAccount(ownerID)
	if err != nil {
		return "", err
	}
	if routeStrategy == nil {
		return "", errors.New("创建 AI 对话 API Key 前必须先创建 GPT 默认策略路由")
	}
	apiKeyID := p.newID("key")
	key := apikeys.NewAPIKey()
	name, err := p.nextDefaultApiKeyName(ownerID, "AI 对话 API Key")
	if err != nil {
		return "", err
	}
	sealed, err := apikeys.EncryptJSON(p.secret, map[string]string{"key": key})
	if err != nil {
		return "", err
	}
	description := fmt.Sprintf("AI 对话专用 API Key，默认绑定%s，可在 API Key 页面修改策略路由。", routeStrategy.name)
	_, err = p.db.Exec(p.bind(fmt.Sprintf(`INSERT INTO %s (
			id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
			key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json, availability_schedule_json,
			availability_schedule_next_check_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 0, 'chat', NULL, NULL, NULL, NULL, ?, ?)`, p.table("api_keys"))),
		apiKeyID, ownerID, routeStrategy.id, name, description,
		apikeys.HashSecret(key), key[:8], key[len(key)-8:],
		sealed, timestamp, timestamp)
	if err != nil {
		// Node race recovery: a concurrent ensure won the chat-key unique
		// indexes (idx_api_keys_chat_purpose_unique / owner-name unique).
		raced, raceErr := p.chatApiKeyIdForSystemAccount(ownerID)
		if raceErr == nil && raced != "" && (isDuplicateChatAPIKeyError(err) || isDuplicateAPIKeyNameError(err)) {
			return raced, nil
		}
		return "", err
	}
	return apiKeyID, nil
}

// FindChatAPIKey mirrors findChatApiKeySecretAsync: the active, unexpired
// row with its decrypted plaintext key.
func (p *chatAPIKeyProvider) FindChatAPIKey(keyID, ownerID string) (*chat.ChatAPIKeyRecord, error) {
	now := p.now().UTC().Format(chainTimeLayout)
	var id, name, status string
	var secretEncrypted string
	var expiresAt sql.NullString
	query := fmt.Sprintf(`SELECT id, name, status, key_secret_encrypted, expires_at
		FROM %s WHERE id = ? AND system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1`, p.table("api_keys"))
	err := p.db.QueryRowContext(context.Background(), p.bind(query), keyID, ownerID, now).
		Scan(&id, &name, &status, &secretEncrypted, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Key string `json:"key"`
	}
	if err := apikeys.DecryptJSON(p.secret, secretEncrypted, &envelope); err != nil {
		return nil, fmt.Errorf("API Key %s 的密钥数据无效", id)
	}
	if envelope.Key == "" {
		return nil, fmt.Errorf("API Key %s 的密钥数据无效", id)
	}
	return &chat.ChatAPIKeyRecord{ID: id, Name: name, Secret: envelope.Key, Status: status}, nil
}

// chatApiKeyIdForSystemAccount mirrors chatApiKeyIdForSystemAccountAsync.
func (p *chatAPIKeyProvider) chatApiKeyIdForSystemAccount(ownerID string) (string, error) {
	var id string
	err := p.db.QueryRow(p.bind(fmt.Sprintf(`SELECT id FROM %s WHERE system_account_id = ? AND purpose = 'chat' LIMIT 1`, p.table("api_keys"))), ownerID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// chatGPTStrategy mirrors the defaultGptRouteStrategyForSystemAccountAsync row.
type chatGPTStrategy struct {
	id   string
	name string
}

// defaultGptRouteStrategyForSystemAccount mirrors
// defaultGptRouteStrategyForSystemAccountAsync.
func (p *chatAPIKeyProvider) defaultGptRouteStrategyForSystemAccount(ownerID string) (*chatGPTStrategy, error) {
	query := fmt.Sprintf(`SELECT route_strategies.id, route_strategies.name
		FROM %[1]s route_strategies
		INNER JOIN %[1]s route_strategy_groups
			ON route_strategy_groups.route_strategy_id = route_strategies.id
			AND route_strategy_groups.system_account_id = route_strategies.system_account_id
			AND route_strategy_groups.status = 'active'
		INNER JOIN %[2]s groups
			ON groups.id = route_strategy_groups.group_id
			AND groups.system_account_id = route_strategy_groups.system_account_id
			AND groups.enabled = 1
			AND groups.is_default = 1
		WHERE route_strategies.system_account_id = ?
			AND route_strategies.status = 'active'
			AND route_strategies.is_default = 1
			AND groups.provider_code = ?
		ORDER BY route_strategies.created_at ASC, route_strategies.id ASC
		LIMIT 1`, p.table("route_strategies"), p.table("groups"))
	var strategy chatGPTStrategy
	err := p.db.QueryRow(p.bind(query), ownerID, "gpt").Scan(&strategy.id, &strategy.name)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &strategy, nil
}

// ensureDefaultRouteStrategies mirrors ensureDefaultRouteStrategiesForSystemAccountAsync.
func (p *chatAPIKeyProvider) ensureDefaultRouteStrategies(ownerID, timestamp string) error {
	groups, err := p.defaultRouteStrategyGroups(ownerID)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return errors.New("创建默认策略路由前必须先创建默认分组")
	}
	for _, group := range groups {
		existing, err := p.defaultRouteStrategyIDForGroup(ownerID, group.id)
		if err != nil {
			return err
		}
		if existing != "" {
			continue
		}
		routeStrategyID := p.newID("route_strategy")
		baseName := chainDefaultRouteStrategyNameForGroup(group.name)
		name, err := p.nextDefaultRouteStrategyName(ownerID, baseName)
		if err != nil {
			return err
		}
		description := fmt.Sprintf("系统默认普通路由，绑定%s。", group.name)
		_, err = p.db.Exec(p.bind(fmt.Sprintf(`INSERT INTO %s (
				id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at
			) VALUES (?, ?, ?, ?, 'normal', 'active', 1, NULL, ?, ?)`, p.table("route_strategies"))),
			routeStrategyID, ownerID, name, description, timestamp, timestamp)
		if err != nil {
			raced, raceErr := p.defaultRouteStrategyIDForGroup(ownerID, group.id)
			if raceErr == nil && raced != "" && isDuplicateRouteStrategyNameError(err) {
				continue
			}
			return err
		}
		_, err = p.db.Exec(p.bind(fmt.Sprintf(`INSERT INTO %s (
				id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
			) VALUES (?, ?, ?, ?, 1, 1, 'active', ?, ?)`, p.table("route_strategy_groups"))),
			p.newID("rsg"), routeStrategyID, ownerID, group.id, timestamp, timestamp)
		if err != nil {
			return err
		}
	}
	return nil
}

// chatDefaultGroup mirrors the RouteStrategyBindableGroupRow subset.
type chatDefaultGroup struct {
	id   string
	name string
}

// defaultRouteStrategyGroups mirrors defaultRouteStrategyGroupsForSystemAccountAsync.
func (p *chatAPIKeyProvider) defaultRouteStrategyGroups(ownerID string) ([]chatDefaultGroup, error) {
	query := fmt.Sprintf(`SELECT id, name, provider_code FROM %s WHERE system_account_id = ? AND is_default = 1 ORDER BY created_at ASC, id ASC`, p.table("groups"))
	rows, err := p.db.Query(p.bind(query), ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chatDefaultGroup{}
	for rows.Next() {
		var id, name, providerCode string
		if err := rows.Scan(&id, &name, &providerCode); err != nil {
			return nil, err
		}
		if strings.EqualFold(strings.TrimSpace(providerCode), "hybrid") {
			continue
		}
		out = append(out, chatDefaultGroup{id: id, name: name})
	}
	return out, rows.Err()
}

// defaultRouteStrategyIDForGroup mirrors defaultRouteStrategyIdForGroupAsync.
func (p *chatAPIKeyProvider) defaultRouteStrategyIDForGroup(ownerID, groupID string) (string, error) {
	query := fmt.Sprintf(`SELECT route_strategies.id
		FROM %[1]s route_strategies
		INNER JOIN %[2]s route_strategy_groups
			ON route_strategy_groups.route_strategy_id = route_strategies.id
			AND route_strategy_groups.system_account_id = route_strategies.system_account_id
		WHERE route_strategies.system_account_id = ?
			AND route_strategies.is_default = 1
			AND route_strategy_groups.group_id = ?
		ORDER BY route_strategies.updated_at DESC, route_strategies.id ASC
		LIMIT 1`, p.table("route_strategies"), p.table("route_strategy_groups"))
	var id string
	err := p.db.QueryRow(p.bind(query), ownerID, groupID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// nextDefaultApiKeyName mirrors nextDefaultApiKeyNameAsync.
func (p *chatAPIKeyProvider) nextDefaultApiKeyName(ownerID, baseName string) (string, error) {
	query := fmt.Sprintf(`SELECT name FROM %s WHERE system_account_id = ? AND (name = ? OR name LIKE ? ESCAPE '\\')`, p.table("api_keys"))
	rows, err := p.db.Query(p.bind(query), ownerID, baseName, baseName+" %")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		names = append(names, name.String)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return chainNextDefaultNameFromExisting(names, baseName), nil
}

// nextDefaultRouteStrategyName mirrors nextDefaultRouteStrategyNameAsync.
func (p *chatAPIKeyProvider) nextDefaultRouteStrategyName(ownerID, baseName string) (string, error) {
	query := fmt.Sprintf(`SELECT name FROM %s WHERE system_account_id = ? AND (name = ? OR name LIKE ? ESCAPE '\\')`, p.table("route_strategies"))
	rows, err := p.db.Query(p.bind(query), ownerID, baseName, baseName+" %")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		names = append(names, name.String)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return chainNextDefaultNameFromExisting(names, baseName), nil
}

// chainNextDefaultNameFromExisting mirrors
// nextDefaultApiKeyNameFromExisting / nextDefaultRouteStrategyNameFromExisting.
func chainNextDefaultNameFromExisting(names []string, baseName string) string {
	existing := map[string]bool{}
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed != "" {
			existing[trimmed] = true
		}
	}
	if !existing[baseName] {
		return baseName
	}
	for index := 2; index <= 1000; index++ {
		candidate := fmt.Sprintf("%s %d", baseName, index)
		if !existing[candidate] {
			return candidate
		}
	}
	return fmt.Sprintf("%s %d", baseName, time.Now().UnixMilli())
}

// chainDefaultRouteStrategyNameForGroup mirrors defaultRouteStrategyNameForGroup.
func chainDefaultRouteStrategyNameForGroup(groupName string) string {
	name := strings.TrimSpace(groupName)
	if name == "" {
		return "默认路由"
	}
	if suffix := "分组"; strings.HasSuffix(name, suffix) {
		return strings.TrimSuffix(name, suffix) + "路由"
	}
	return name
}

// isDuplicateAPIKeyNameError mirrors isDuplicateApiKeyNameError.
func isDuplicateAPIKeyNameError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "idx_api_keys_owner_name_unique") ||
		strings.Contains(message, "idx_api_keys_owner_name_unique_lower") ||
		strings.Contains(message, "UNIQUE constraint failed: api_keys.system_account_id, api_keys.name")
}

// isDuplicateChatAPIKeyError mirrors isDuplicateChatApiKeyError.
func isDuplicateChatAPIKeyError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "idx_api_keys_chat_purpose_unique") ||
		strings.Contains(message, "UNIQUE constraint failed: api_keys.system_account_id")
}

// isDuplicateRouteStrategyNameError mirrors isDuplicateRouteStrategyNameError.
func isDuplicateRouteStrategyNameError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "idx_route_strategies_owner_name_unique") ||
		strings.Contains(message, "idx_route_strategies_owner_name_unique_lower") ||
		strings.Contains(message, "UNIQUE constraint failed: route_strategies.system_account_id, route_strategies.name")
}
