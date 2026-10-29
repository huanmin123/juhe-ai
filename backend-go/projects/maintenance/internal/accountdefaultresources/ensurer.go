// Package accountdefaultresources 为存量系统账户补齐「默认分组 → 默认路由
// 策略（+分组绑定）→ 默认 API Key」三件套（2026-10-07 默认资源收口形态，
// 2026-10-17 设计 §8 去供应商化后的单套种子）。
//
// 背景：gateway authsys.AccountStore.Create 自 2026-10-07 起在建户事务内调
// EnsureDefaultResources（backend-go/projects/gateway/internal/authsys/
// default_resources.go），该收口之前创建的存量账户只有早期链路建出的默认
// 分组，缺默认策略与默认 API Key。本命令对全部存量账户执行同款幂等 ensure
// （缺什么补什么，已齐账户零写入），纯加法 INSERT、不动存量行，可重复执行
// （重跑零写入即幂等自证）。SQL 与 gateway default_resources.go 逐语句同源
// 镜像，改动即两处同改。
package accountdefaultresources

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/idgen"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// builtinSchedulingPreferenceID 与 gateway apikeys.BuiltinSchedulingPreferenceID、
// maintenance schema.BuiltinSchedulingPreferenceID、gateway authsys（本包与
// default_resources.go）同源锚点，改动即四处同改：调度偏好 P1 契约「缺省 →
// 内置固定 ID」，默认 Key 与 Store.Create 同形显式落内置行。
const builtinSchedulingPreferenceID = "schpref-builtin-chat-cost-first"

const (
	defaultGroupName        = "默认分组"
	defaultRouteStrategyFor = "系统默认普通路由，绑定%s。"
	defaultAPIKeyFor        = "系统默认 API Key，绑定%s。"
	missingDefaultGroupErr  = "创建默认策略路由前必须先创建默认分组"
)

// newID 与 gateway authsys.newID 同源（格式 <prefix>_<24hex>，随机段收敛到
// shared/platform/idgen，crypto/rand 极端失败由 idgen 时间熵回退）。
func newID(prefix string) (string, error) {
	return prefix + "_" + idgen.RandomHex(12), nil
}

// Options 是一次补齐运行的入参。
type Options struct {
	// Secret 是默认 API Key 明文的加密封套密钥（JUHE_AI_SECRET 语义，与
	// --secret / gateway 启动 secret 同源）；空值 fail-closed 零写入。
	Secret string
	// Now 是三件套 created_at/updated_at 的时间源；零值取真实当前时间。
	Now time.Time
}

// AccountOutcome 是单个账户的补齐明细（零动作账户不产生条目）。
type AccountOutcome struct {
	AccountID         string `json:"accountId"`
	GroupsEnsured     int    `json:"groupsEnsured"`
	StrategiesEnsured int    `json:"strategiesEnsured"`
	BindingsEnsured   int    `json:"bindingsEnsured"`
	KeysEnsured       int    `json:"keysEnsured"`
}

// Report 是补齐结果（stdout JSON 契约）。
type Report struct {
	AccountsScanned   int              `json:"accountsScanned"`
	AccountsTouched   int              `json:"accountsTouched"`
	GroupsEnsured     int              `json:"groupsEnsured"`
	StrategiesEnsured int              `json:"strategiesEnsured"`
	BindingsEnsured   int              `json:"bindingsEnsured"`
	KeysEnsured       int              `json:"keysEnsured"`
	PerAccount        []AccountOutcome `json:"perAccount"`
}

// Open 校验并打开指向业务库的 PostgreSQL 连接（单连接一次性池；与
// openAccountBalancePostgres 同款 URL 门槛：postgres scheme、主机、数据库、
// 显式角色，不回落应用连接串）。
func Open(rawURL string) (*sql.DB, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil, errors.New("account default resources 需要显式的 postgres/postgresql --dsn URL")
	}
	if parsed.Hostname() == "" || strings.Trim(strings.TrimSpace(parsed.Path), "/") == "" || parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return nil, errors.New("account default resources --dsn 必须包含主机、数据库和显式角色")
	}
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		return nil, fmt.Errorf("打开 account default resources PostgreSQL 连接失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// Run 对全部系统账户执行幂等 ensure，返回补齐报告。单账户失败即终止（已提
// 交账户保持补齐态，重跑收敛剩余账户）；任一写入前 secret 校验 fail-closed。
func Run(ctx context.Context, db *sql.DB, opts Options) (Report, error) {
	report := Report{}
	if strings.TrimSpace(opts.Secret) == "" {
		return report, errors.New("account default resources 需要非空 --secret（JUHE_AI_SECRET 语义）以密封默认 API Key")
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	nowText := now.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	if err := ensureBuiltinSchedulingPreference(ctx, db); err != nil {
		return report, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id FROM juhe_business.system_accounts ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return report, fmt.Errorf("扫描系统账户失败: %w", err)
	}
	accountIDs := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return report, err
		}
		accountIDs = append(accountIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return report, err
	}
	rows.Close()
	report.AccountsScanned = len(accountIDs)
	for _, accountID := range accountIDs {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return report, err
		}
		outcome, ensureErr := ensureAccount(ctx, tx, accountID, nowText, opts.Secret)
		if ensureErr == nil {
			ensureErr = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if ensureErr != nil {
			return report, fmt.Errorf("账户 %s 补齐失败: %w", accountID, ensureErr)
		}
		if outcome.zeroAction() {
			continue
		}
		outcome.AccountID = accountID
		report.AccountsTouched++
		report.GroupsEnsured += outcome.GroupsEnsured
		report.StrategiesEnsured += outcome.StrategiesEnsured
		report.BindingsEnsured += outcome.BindingsEnsured
		report.KeysEnsured += outcome.KeysEnsured
		report.PerAccount = append(report.PerAccount, outcome)
	}
	return report, nil
}

func (o AccountOutcome) zeroAction() bool {
	return o.GroupsEnsured == 0 && o.StrategiesEnsured == 0 && o.BindingsEnsured == 0 && o.KeysEnsured == 0
}

// ensureBuiltinSchedulingPreference 在任何写入前确认内置调度偏好行存在
// （api_keys.scheduling_preference_id 外键目标；生产/标准库由 --seed 预置）。
func ensureBuiltinSchedulingPreference(ctx context.Context, db *sql.DB) error {
	var id string
	err := db.QueryRowContext(ctx, `SELECT id FROM juhe_business.scheduling_preferences WHERE id = $1 AND built_in = 1`, builtinSchedulingPreferenceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("内置调度偏好行不存在（schpref-builtin-chat-cost-first），请先执行 --seed 预置后再补齐")
	}
	return err
}

// ensureAccount 是 gateway EnsureDefaultResources 的存量镜像（单账户事务内
// 三步：默认分组 → 默认路由策略+绑定 → 默认 API Key），每步先查后插、缺什
// 么补什么。
func ensureAccount(ctx context.Context, tx *sql.Tx, accountID, nowText, secret string) (AccountOutcome, error) {
	outcome := AccountOutcome{}
	if err := ensureDefaultGroups(ctx, tx, accountID, nowText, &outcome); err != nil {
		return outcome, err
	}
	if err := ensureDefaultRouteStrategies(ctx, tx, accountID, nowText, &outcome); err != nil {
		return outcome, err
	}
	if err := ensureDefaultAPIKeys(ctx, tx, accountID, nowText, secret, &outcome); err != nil {
		return outcome, err
	}
	return outcome, nil
}

func ensureDefaultGroups(ctx context.Context, tx *sql.Tx, accountID, nowText string, outcome *AccountOutcome) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT id FROM juhe_business.groups WHERE system_account_id = $1 AND is_default = 1 ORDER BY updated_at DESC, id ASC LIMIT 1`, accountID).Scan(&existing)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	id, err := newID("grp")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO juhe_business.groups (id, system_account_id, name, description, enabled, is_default, created_at, updated_at) VALUES ($1, $2, $3, '', 1, 1, $4, $4)`,
		id, accountID, defaultGroupName, nowText); err != nil {
		return err
	}
	outcome.GroupsEnsured++
	return nil
}

func ensureDefaultRouteStrategies(ctx context.Context, tx *sql.Tx, accountID, nowText string, outcome *AccountOutcome) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM juhe_business.groups WHERE system_account_id = $1 AND is_default = 1 ORDER BY created_at ASC, id ASC`, accountID)
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
		return errors.New(missingDefaultGroupErr)
	}
	for _, group := range groups {
		existing, err := defaultRouteStrategyIDForGroup(ctx, tx, accountID, group.id)
		if err != nil {
			return err
		}
		if existing != "" {
			continue
		}
		name, err := nextDefaultResourceName(ctx, tx, "route_strategies", accountID, defaultRouteStrategyNameForGroup(group.name))
		if err != nil {
			return err
		}
		id, err := newID("route_strategy")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO juhe_business.route_strategies (id, system_account_id, name, description, mode, status, is_default, config_json, created_at, updated_at) VALUES ($1, $2, $3, $4, 'normal', 'active', 1, NULL, $5, $5)`,
			id, accountID, name, fmt.Sprintf(defaultRouteStrategyFor, group.name), nowText); err != nil {
			return err
		}
		bindingID, err := newID("rsg")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO juhe_business.route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at) VALUES ($1, $2, $3, $4, 1, 1, 'active', $5, $5)`,
			bindingID, id, accountID, group.id, nowText); err != nil {
			return err
		}
		outcome.StrategiesEnsured++
		outcome.BindingsEnsured++
	}
	return nil
}

func defaultRouteStrategyIDForGroup(ctx context.Context, tx *sql.Tx, accountID, groupID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT route_strategies.id FROM juhe_business.route_strategies route_strategies INNER JOIN juhe_business.route_strategy_groups route_strategy_groups ON route_strategy_groups.route_strategy_id = route_strategies.id AND route_strategy_groups.system_account_id = route_strategies.system_account_id WHERE route_strategies.system_account_id = $1 AND route_strategies.is_default = 1 AND route_strategy_groups.group_id = $2 ORDER BY route_strategies.updated_at DESC, route_strategies.id ASC LIMIT 1`, accountID, groupID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func ensureDefaultAPIKeys(ctx context.Context, tx *sql.Tx, accountID, nowText, secret string, outcome *AccountOutcome) error {
	rows, err := tx.QueryContext(ctx, `SELECT route_strategies.id, route_strategies.name FROM juhe_business.route_strategies route_strategies INNER JOIN juhe_business.route_strategy_groups route_strategy_groups ON route_strategy_groups.route_strategy_id = route_strategies.id AND route_strategy_groups.system_account_id = route_strategies.system_account_id INNER JOIN juhe_business.groups groups ON groups.id = route_strategy_groups.group_id AND groups.system_account_id = route_strategy_groups.system_account_id WHERE route_strategies.system_account_id = $1 AND route_strategies.is_default = 1 ORDER BY route_strategies.created_at ASC, route_strategies.id ASC`, accountID)
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
		err := tx.QueryRowContext(ctx, `SELECT id FROM juhe_business.api_keys WHERE route_strategy_id = $1 AND is_default = 1 ORDER BY created_at ASC, id ASC LIMIT 1`, route.id).Scan(&existing)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		name, err := nextDefaultResourceName(ctx, tx, "api_keys", accountID, defaultAPIKeyNameForRouteStrategy(route.name))
		if err != nil {
			return err
		}
		if err := insertDefaultAPIKey(ctx, tx, accountID, route.id, name, fmt.Sprintf(defaultAPIKeyFor, route.name), nowText, secret); err != nil {
			return err
		}
		outcome.KeysEnsured++
	}
	return nil
}

// insertDefaultAPIKey 与 gateway authsys insertAPIKey 同源（明文 sk- + 64hex、
// sha256 hex 查找哈希、AES-256-GCM v1 信封），并按调度偏好 P1 契约显式落内
// 置 ID（gateway 侧同函数的补列修复与本命令同批交付）。
func insertDefaultAPIKey(ctx context.Context, tx *sql.Tx, accountID, routeID, name, description, nowText, secret string) error {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("generate api key: %w", err)
	}
	plaintext := "sk-" + hex.EncodeToString(buf)
	sealed, err := sealAPIKeySecret(secret, plaintext)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(plaintext))
	id, err := newID("key")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO juhe_business.api_keys (id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix, key_secret_encrypted, status, is_default, purpose, scheduling_preference_id, expires_at, quota_limits_json, availability_schedule_json, availability_schedule_next_check_at, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'active', 1, 'general', $10, NULL, NULL, NULL, NULL, $11, $11)`,
		id, accountID, routeID, name, description, hex.EncodeToString(sum[:]), plaintext[:8], plaintext[len(plaintext)-8:], sealed, builtinSchedulingPreferenceID, nowText)
	return err
}

// sealAPIKeySecret 镜像 schema.seedEncryptJSONWithOptions 的 v1 AES-256-GCM
// 信封（v1:<iv b64url>:<tag b64url>:<ciphertext b64url>，compact JSON
// {"key":...}，sha256(secret) 作密钥）——与 gateway EncryptJSON/解密读链同形。
func sealAPIKeySecret(secret, plaintext string) (string, error) {
	payload, err := json.Marshal(map[string]string{"key": plaintext})
	if err != nil {
		return "", fmt.Errorf("api key seal payload: %w", err)
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", fmt.Errorf("api key seal cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("api key seal gcm: %w", err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("api key seal iv: %w", err)
	}
	sealed := gcm.Seal(nil, iv, payload, nil)
	tag := sealed[len(sealed)-gcm.Overhead():]
	cipherText := sealed[:len(sealed)-gcm.Overhead()]
	return strings.Join([]string{
		"v1",
		base64.RawURLEncoding.EncodeToString(iv),
		base64.RawURLEncoding.EncodeToString(tag),
		base64.RawURLEncoding.EncodeToString(cipherText),
	}, ":"), nil
}

// nextDefaultResourceName 与 gateway 同名函数同源：基名，否则首个空闲
// "<基名> N"（N∈2..1000），否则 "<基名> <unix-millis>"。
func nextDefaultResourceName(ctx context.Context, tx *sql.Tx, table, accountID, baseName string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM juhe_business.`+table+` WHERE system_account_id = $1 AND (name = $2 OR name LIKE $3 ESCAPE '\')`, accountID, baseName, baseName+" %")
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
		candidate := fmt.Sprintf("%s %d", baseName, index)
		if _, taken := existing[candidate]; !taken {
			return candidate, nil
		}
	}
	return fmt.Sprintf("%s %d", baseName, time.Now().UnixMilli()), nil
}

// defaultRouteStrategyNameForGroup 与 gateway 同名函数同源：尾缀「分组」变
// 「路由」，空名回落「默认路由」。
func defaultRouteStrategyNameForGroup(groupName string) string {
	name := strings.TrimSpace(groupName)
	if name == "" {
		return "默认路由"
	}
	if strings.HasSuffix(name, "分组") {
		return strings.TrimSuffix(name, "分组") + "路由"
	}
	return name
}

// defaultAPIKeyNameForRouteStrategy 与 gateway 同名函数同源：尾缀「路由」变
// 「API Key」。
func defaultAPIKeyNameForRouteStrategy(routeName string) string {
	if strings.HasSuffix(routeName, "路由") {
		return strings.TrimSuffix(routeName, "路由") + "API Key"
	}
	return routeName
}
