package proxylatency

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
)

const (
	proxyLatencyInputPolicyVersion = "j3a-proxy-latency-v1"
	proxyLatencyStatementTimeout   = "5s"
	proxyLatencyLockTimeout        = "1s"
	maxProxyLatencyInputLimit      = maxProxyLatencyWorkItems
	maxProxyLatencyPageSize        = maxProxyLatencyWorkItems
)

// PostgresDirectInputReader reads only frozen proxy and provider facts. It
// never opens Node SQLite and never writes juhe_business or the jobs Store.
type PostgresDirectInputReader struct {
	db  *sql.DB
	ttl time.Duration
	now func() time.Time
}

func NewPostgresDirectInputReader(db *sql.DB, ttl time.Duration, now func() time.Time) (*PostgresDirectInputReader, error) {
	if db == nil || ttl < time.Minute || ttl > 15*time.Minute {
		return nil, errors.New("J3a PG direct input 配置无效")
	}
	if now == nil {
		now = time.Now
	}
	return &PostgresDirectInputReader{db: db, ttl: ttl, now: now}, nil
}

// CheckContract performs only zero-row reads so missing business grants or
// schema are observable without trying to repair Node-owned state.
func (r *PostgresDirectInputReader) CheckContract(ctx context.Context) error {
	tx, err := r.beginReadOnly(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, relation := range proxyLatencyRequiredRelations {
		if _, err := tx.ExecContext(ctx, "SELECT 1 FROM "+relation+" LIMIT 0"); err != nil {
			return fmt.Errorf("J3a PG direct input 缺少业务只读契约 %s: %w", relation, err)
		}
	}
	rows, err := tx.QueryContext(ctx, proxyLatencyCandidatesSQL, 1, 0)
	if err != nil {
		return fmt.Errorf("验证 J3a PG direct input 候选查询失败: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("关闭 J3a PG direct input 契约游标失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交 J3a PG direct input 契约预检事务失败: %w", err)
	}
	return nil
}

var proxyLatencyRequiredRelations = []string{
	"juhe_business.proxy_profiles",
	// 供应商解耦后探针 target 事实源（§9.11：providers/provider_protocol_
	// profiles 表退役，vendor 目录与 registry 派生 target 相交）。
	"juhe_business.vendors",
}

func (r *PostgresDirectInputReader) LoadDue(ctx context.Context, limit int) ([]InputDraft, error) {
	if limit < 1 || limit > maxProxyLatencyInputLimit {
		return nil, errors.New("J3a PG direct input limit 必须在 1..5096")
	}
	tx, err := r.beginReadOnly(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := r.now().UTC()
	result := make([]InputDraft, 0, limit)
	pageSize := limit * 4
	if pageSize < 40 {
		pageSize = 40
	}
	if pageSize > maxProxyLatencyPageSize {
		pageSize = maxProxyLatencyPageSize
	}
	for offset := 0; len(result) < limit; offset += pageSize {
		rows, err := tx.QueryContext(ctx, proxyLatencyCandidatesSQL, pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("读取 J3a PG direct input 候选失败: %w", err)
		}
		page, count, err := r.collectPage(rows, now, limit-len(result))
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, fmt.Errorf("关闭 J3a PG direct input 候选游标失败: %w", closeErr)
		}
		result = append(result, page...)
		if count < pageSize {
			break
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交 J3a PG direct input 只读事务失败: %w", err)
	}
	return result, nil
}

func (r *PostgresDirectInputReader) beginReadOnly(ctx context.Context) (*sql.Tx, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, fmt.Errorf("开始 J3a PG direct input 只读事务失败: %w", err)
	}
	for _, statement := range []string{
		"SET LOCAL TRANSACTION READ ONLY",
		"SET LOCAL statement_timeout = '" + proxyLatencyStatementTimeout + "'",
		"SET LOCAL lock_timeout = '" + proxyLatencyLockTimeout + "'",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("配置 J3a PG direct input 只读事务失败: %w", err)
		}
	}
	return tx, nil
}

type proxyLatencyCandidateRow struct {
	proxyID, proxyType, proxyHost, proxyUsername, passwordEncrypted string
	proxyPort                                                       int64
	proxyEnabled                                                    bool
	configRevision, lastTestedAt                                    string
	lastTestedAtValid                                               bool
	provider, profileID, targetURL                                  string
}

type proxyLatencyCandidateAssembly struct {
	row     proxyLatencyCandidateRow
	targets []Target
}

func (r *PostgresDirectInputReader) collectPage(rows *sql.Rows, now time.Time, remaining int) ([]InputDraft, int, error) {
	result := make([]InputDraft, 0, remaining)
	var current *proxyLatencyCandidateAssembly
	proxyCount := 0
	flush := func() {
		if current == nil || len(result) >= remaining {
			return
		}
		candidate, err := makeProxyLatencyInputDraft(*current, now, r.ttl)
		if err != nil {
			// Do not include encrypted envelopes in the diagnostic. A malformed
			// proxy is isolated while later ordered proxies remain probeable.
			slog.Default().Warn("J3a 跳过无效 PG 代理候选", "proxy_id", current.row.proxyID, "error", err)
			return
		}
		result = append(result, candidate)
	}
	for rows.Next() {
		row, err := scanProxyLatencyCandidate(rows)
		if err != nil {
			return nil, proxyCount, err
		}
		if current == nil || current.row.proxyID != row.proxyID {
			flush()
			current = &proxyLatencyCandidateAssembly{row: row}
			proxyCount++
		}
		current.targets = append(current.targets, Target{Provider: row.provider, ProfileID: row.profileID, URL: row.targetURL})
	}
	flush()
	if err := rows.Err(); err != nil {
		return nil, proxyCount, fmt.Errorf("遍历 J3a PG direct input 候选失败: %w", err)
	}
	return result, proxyCount, nil
}

// scanProxyLatencyCandidate decodes raw PostgreSQL scalar values without
// converting invalid booleans/timestamps to permissive defaults.
func scanProxyLatencyCandidate(rows *sql.Rows) (proxyLatencyCandidateRow, error) {
	var result proxyLatencyCandidateRow
	var id, typ, host, username, password, revision, provider, profileID, target sql.NullString
	var port sql.NullInt64
	var enabled sql.NullBool
	var lastTested sql.NullString
	if err := rows.Scan(&id, &typ, &host, &port, &username, &password, &enabled, &revision, &lastTested, &provider, &profileID, &target); err != nil {
		return proxyLatencyCandidateRow{}, fmt.Errorf("解码 J3a PG direct input 候选失败: %w", err)
	}
	if !id.Valid || !typ.Valid || !host.Valid || !port.Valid || !enabled.Valid || !revision.Valid || !provider.Valid || !profileID.Valid || !target.Valid {
		return proxyLatencyCandidateRow{}, errors.New("J3a PG direct input 候选列类型无效")
	}
	result = proxyLatencyCandidateRow{proxyID: id.String, proxyType: typ.String, proxyHost: host.String, proxyPort: port.Int64, proxyEnabled: enabled.Bool, configRevision: revision.String, provider: provider.String, profileID: profileID.String, targetURL: target.String}
	if username.Valid {
		result.proxyUsername = username.String
	}
	if password.Valid {
		result.passwordEncrypted = password.String
	}
	if lastTested.Valid {
		result.lastTestedAt, result.lastTestedAtValid = lastTested.String, true
	}
	return result, nil
}

func makeProxyLatencyInputDraft(assembly proxyLatencyCandidateAssembly, now time.Time, ttl time.Duration) (InputDraft, error) {
	row := assembly.row
	if strings.TrimSpace(row.proxyID) == "" || !row.proxyEnabled || !validProxyLatencyType(row.proxyType) || strings.TrimSpace(row.proxyHost) == "" || row.proxyPort < 1 || row.proxyPort > 65535 {
		return InputDraft{}, errors.New("代理配置无效")
	}
	canonicalRevision, err := canonicalConfigRevision(row.configRevision)
	if err != nil {
		return InputDraft{}, err
	}
	if row.lastTestedAtValid {
		if _, err := parseProxyLatencyUTC(row.lastTestedAt, "last_tested_at"); err != nil {
			return InputDraft{}, err
		}
	}
	var password *CredentialEnvelope
	if strings.TrimSpace(row.passwordEncrypted) != "" {
		if !validProxyLatencyEnvelope(row.passwordEncrypted) {
			return InputDraft{}, errors.New("代理 password envelope 无效")
		}
		password = &CredentialEnvelope{Kind: "proxy_password", Ciphertext: row.passwordEncrypted}
	}
	if ttl < time.Minute || ttl > 15*time.Minute || now.IsZero() {
		return InputDraft{}, errors.New("输入 TTL 或签发时间无效")
	}
	if len(assembly.targets) == 0 {
		return InputDraft{}, errors.New("代理缺少启用 provider target")
	}
	targets := make([]Target, 0, len(assembly.targets))
	seenProviders := make(map[string]struct{}, len(assembly.targets))
	for _, target := range assembly.targets {
		canonicalTarget, err := canonicalizeProbeTarget(target)
		if err != nil {
			return InputDraft{}, err
		}
		if _, exists := seenProviders[canonicalTarget.Provider]; exists {
			return InputDraft{}, errors.New("provider target 重复")
		}
		seenProviders[canonicalTarget.Provider] = struct{}{}
		targets = append(targets, canonicalTarget)
	}
	issuedAt := now.UTC()
	return InputDraft{ProxyID: row.proxyID, ConfigRevision: canonicalRevision, Trigger: TriggerPeriodic, IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(ttl), PolicyVersion: proxyLatencyInputPolicyVersion, ProxyType: row.proxyType, ProxyHost: row.proxyHost, ProxyPort: int(row.proxyPort), ProxyUsername: row.proxyUsername, ProxyPassword: password, Targets: targets}, nil
}

// canonicalizeProbeTarget keeps the legacy Node probe contract at the catalog
// boundary. A blank or unsupported provider base URL was
// never an executable target in Node; it produced one explicit unknown item
// while the other providers for the same proxy continued. Persist only a
// stable failure code, never the malformed source URL, so no outbound request
// or sensitive configuration echo can occur later in the pipeline.
func canonicalizeProbeTarget(target Target) (Target, error) {
	provider := strings.ToLower(strings.TrimSpace(target.Provider))
	profileID := strings.TrimSpace(target.ProfileID)
	if provider == "" || profileID == "" {
		return Target{}, errors.New("J3a input draft target 标识无效")
	}
	canonical, err := canonicalizeTarget(Target{Provider: provider, ProfileID: profileID, URL: target.URL, ProbeError: target.ProbeError})
	if err == nil {
		return canonical, nil
	}
	if target.ProbeError != "" {
		return Target{}, err
	}
	return Target{Provider: provider, ProfileID: profileID, ProbeError: targetProbeErrorInvalidURL}, nil
}

func validProxyLatencyType(value string) bool {
	return value == "http" || value == "https" || value == "socks5" || value == "socks5h"
}

func validProxyLatencyEnvelope(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 4 || parts[0] != "v1" {
		return false
	}
	nonce, nonceErr := base64.RawURLEncoding.DecodeString(parts[1])
	tag, tagErr := base64.RawURLEncoding.DecodeString(parts[2])
	ciphertext, ciphertextErr := base64.RawURLEncoding.DecodeString(parts[3])
	return nonceErr == nil && tagErr == nil && ciphertextErr == nil && len(nonce) == 12 && len(tag) == 16 && len(ciphertext) > 0
}

func parseProxyLatencyUTC(value, field string) (time.Time, error) {
	text := strings.TrimSpace(value)
	if !strings.HasSuffix(text, "Z") {
		return time.Time{}, fmt.Errorf("%s 必须为 RFC3339 UTC", field)
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || parsed.IsZero() {
		return time.Time{}, fmt.Errorf("%s 必须为 RFC3339 UTC", field)
	}
	return parsed.UTC(), nil
}

// 供应商解耦后（设计 §9.1/§9.3/§9.11）providers/provider_protocol_profiles
// 表退役：每 vendor 的探针 target 由编译期 registry 确定性派生——preset 取
// vendor 语义默认档（与 gateway vendorDefaultProtocolContext 同口径：
// gemini→native、glm→coding openai、其余唯一 chat/媒体预设），URL 取
// registry default_base_url（与 seed/UI 初始值同一口径），profile ID 走
// exactkeyprobe.ConnectionPresetProfileID 的 preset→profile_* 固定映射
// （下游取值形状不变）。无已注册探针协议目录的 vendor（纯媒体/新增）不在
// target 集内，fail-closed。运行时仍按 vendors.enabled 过滤。
//
// Candidates are paged by proxy before targets are expanded. This retains the
// Node ordering and lets a malformed proxy be skipped without starving later
// rows. Enabled vendors win via vendors.enabled; target ordering stays
// provider_name ASC, provider ASC.
type j3aVendorProbeTarget struct {
	provider, profileID, targetURL string
}

// j3aVendorProbePresets 是 vendor→探针预设的固定表（§1.1 registry 词表）。
var j3aVendorProbePresets = []struct{ vendor, preset string }{
	{"gpt", contracts.PresetGPTOpenAIV1},
	{"xai", contracts.PresetXAIOpenAIV1},
	{"deepseek", contracts.PresetDeepSeekOpenAIV1},
	{"anthropic", contracts.PresetAnthropicV1},
	{"gemini", contracts.PresetGeminiNativeV1Beta},
	{"glm", contracts.PresetGLMCodingOpenAIV1},
	{"minimax", contracts.PresetMiniMaxMedia},
	{"volcengine", contracts.PresetVolcengineArk},
	{"qwen", contracts.PresetQwenDashScope},
}

// j3aVendorProbeTargets 是 registry 派生的探针 target 全集（init 期固定）。
var j3aVendorProbeTargets = buildJ3aVendorProbeTargets()

func buildJ3aVendorProbeTargets() []j3aVendorProbeTarget {
	targets := make([]j3aVendorProbeTarget, 0, len(j3aVendorProbePresets))
	for _, entry := range j3aVendorProbePresets {
		profileID, ok := exactkeyprobe.ConnectionPresetProfileID(entry.preset)
		if !ok {
			// 无已注册 J1 探针协议目录的 preset 不构造 target（fail-closed）。
			continue
		}
		targetURL := contracts.PresetDefaultBaseURL(entry.preset)
		if strings.TrimSpace(targetURL) == "" {
			continue
		}
		targets = append(targets, j3aVendorProbeTarget{provider: entry.vendor, profileID: profileID, targetURL: targetURL})
	}
	return targets
}

// j3aVendorTargetsSQL 把 registry target 全集内联为 VALUES 行（provider,
// profile_id, target_url），运行时与 vendors.enabled 相交。
func j3aVendorTargetsSQL() string {
	rows := make([]string, 0, len(j3aVendorProbeTargets))
	for _, target := range j3aVendorProbeTargets {
		rows = append(rows, "('"+target.provider+"','"+target.profileID+"','"+target.targetURL+"')")
	}
	return strings.Join(rows, ",")
}

var proxyLatencyCandidatesSQL = `
WITH selected_proxies AS (
  SELECT p.id,p.type,p.host,p.port,p.username,p.password_encrypted,p.enabled,
    to_char(p.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') AS config_revision,
    CASE WHEN p.last_tested_at IS NULL THEN NULL ELSE to_char(p.last_tested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') END AS last_tested_at
  FROM juhe_business.proxy_profiles p
  WHERE p.enabled = TRUE
  ORDER BY (p.last_tested_at IS NOT NULL) ASC,p.last_tested_at ASC,p.updated_at DESC,p.id ASC
  LIMIT $1 OFFSET $2
), selected_targets AS (
  SELECT v.code AS provider,v.name AS provider_name,t.profile_id,t.target_url
  FROM juhe_business.vendors v
  JOIN (VALUES ` + j3aVendorTargetsSQL() + `) AS t(provider,profile_id,target_url)
    ON t.provider = v.code
  WHERE v.enabled = 1
)
SELECT proxy.id,proxy.type,proxy.host,proxy.port,proxy.username,proxy.password_encrypted,proxy.enabled,
  proxy.config_revision,proxy.last_tested_at,target.provider,target.profile_id,target.target_url
FROM selected_proxies proxy
CROSS JOIN selected_targets target
ORDER BY (proxy.last_tested_at IS NOT NULL) ASC,proxy.last_tested_at ASC,proxy.config_revision DESC,proxy.id ASC,target.provider_name ASC,target.provider ASC`
