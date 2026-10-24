package legacybusinessimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// Options configures one rebuild run.
type Options struct {
	// Secret decrypts legacy account credentials so the endpoint-mode set and
	// the stored base_url can be lifted into the new normalised tables.
	Secret string
	// DryRun performs every read and translation but rolls back instead of
	// committing, so the report is produced without changing the target.
	DryRun bool
	// Now is recorded in the report; zero means time.Now.
	Now func() string
}

// AccountCredit is the credential JSON payload the legacy schema stored inline:
// the API key or OAuth material plus the endpoint modes the account declared.
type accountCredit struct {
	BaseURL                string   `json:"base_url"`
	SupportedEndpointModes []string `json:"supported_endpoint_modes"`
}

// Import rebuilds juhe_business from the legacy rows. The caller must have run
// --ensure-schema --seed against the target first: this function never creates
// structure, it only fills tables the fresh schema (and its seed) already made.
//
// The source and target may be the same database only when the legacy tables
// still exist (a dry run); the production cutover restores the legacy copy into
// a separate database.
func Import(ctx context.Context, source, target *sql.DB, opts Options) (Report, error) {
	report := Report{Mode: "apply", Tables: map[string]int{}, LegacyProfiles: map[string]string{}}
	if opts.DryRun {
		report.Mode = "dry-run"
	}
	report.DroppedDomains = []string{
		"juhe_usage.*", "juhe_dataset.*", "juhe_stats.*", "juhe_j3b.*", "juhe_jobs.*",
		"(historical usage, audit, statistics, model-check runs and probe outcomes are intentionally discarded)",
	}
	if strings.TrimSpace(opts.Secret) == "" {
		return report, errors.New("回迁需要生产 JUHE_AI_SECRET 解密旧账户凭据")
	}
	if err := source.QueryRowContext(ctx, "SELECT current_database()").Scan(&report.SourceIdentity); err != nil {
		return report, fmt.Errorf("读取源库标识: %w", err)
	}
	if err := target.QueryRowContext(ctx, "SELECT current_database()").Scan(&report.TargetIdentity); err != nil {
		return report, fmt.Errorf("读取目标库标识: %w", err)
	}

	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("开启回迁事务: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := preflightTarget(ctx, tx); err != nil {
		return report, err
	}

	seeded, err := seedVendors(ctx, tx)
	if err != nil {
		return report, err
	}
	report.SeededVendors = seeded

	owners, err := importSystemAccounts(ctx, source, tx)
	if err != nil {
		return report, err
	}
	if err := importProxyProfiles(ctx, source, tx, &report); err != nil {
		return report, err
	}
	if err := importSchedulingPreferences(ctx, source, tx, &report); err != nil {
		return report, err
	}
	if err := importTeams(ctx, source, tx, &report); err != nil {
		return report, err
	}
	groupIDs, err := importGroups(ctx, source, tx, owners, &report)
	if err != nil {
		return report, err
	}
	strategyIDs, err := importRouteStrategies(ctx, source, tx, groupIDs, &report)
	if err != nil {
		return report, err
	}
	accountIDs, err := importAccounts(ctx, source, tx, opts.Secret, &report)
	if err != nil {
		return report, err
	}
	if err := importGroupAccounts(ctx, source, tx, groupIDs, accountIDs, owners, &report); err != nil {
		return report, err
	}
	if err := importAuthorizations(ctx, source, tx, groupIDs, accountIDs, owners, &report); err != nil {
		return report, err
	}
	if err := importGroupAuthorizationSettings(ctx, source, tx, groupIDs, &report); err != nil {
		return report, err
	}
	if err := importTags(ctx, source, tx, accountIDs, owners, &report); err != nil {
		return report, err
	}
	if err := importAPIKeys(ctx, source, tx, strategyIDs, owners, &report); err != nil {
		return report, err
	}
	if err := importSettings(ctx, source, tx, owners, &report); err != nil {
		return report, err
	}
	if err := importAnnouncementsAndPolicies(ctx, source, tx, &report); err != nil {
		return report, err
	}
	if err := importExternalSources(ctx, source, tx, &report); err != nil {
		return report, err
	}
	if err := importQuotaWindows(ctx, source, tx, &report); err != nil {
		return report, err
	}

	if opts.DryRun {
		if err := tx.Rollback(); err != nil {
			return report, fmt.Errorf("dry-run 回滚失败: %w", err)
		}
		report.Mode = "dry-run"
		return report, nil
	}
	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("提交回迁事务: %w", err)
	}
	committed = true
	return report, nil
}

// preflightTarget refuses to fill a target that already holds business rows:
// the cutover contract is "fresh ensured schema + one backfill", not a merge.
func preflightTarget(ctx context.Context, tx *sql.Tx) error {
	var accounts int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.accounts`).Scan(&accounts); err != nil {
		return fmt.Errorf("读取目标账户数: %w", err)
	}
	if accounts != 0 {
		return fmt.Errorf("目标库已有 %d 个账户行；回迁只接受 ensure-schema 后的空业务库", accounts)
	}
	var vendors int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.vendors`).Scan(&vendors); err != nil {
		return fmt.Errorf("读取目标 venders 数: %w", err)
	}
	if vendors == 0 {
		return errors.New("目标库缺少 vendors 种子；先运行 --ensure-schema --seed")
	}
	return nil
}

// seedVendors upserts the nine real vendors so account FK references resolve
// even if the seed step was skipped. Names mirror the connector registry.
func seedVendors(ctx context.Context, tx *sql.Tx) (int, error) {
	rows := []struct{ code, name string }{
		{"gpt", "OpenAI"}, {"xai", "xAI"}, {"deepseek", "DeepSeek"},
		{"anthropic", "Anthropic"}, {"gemini", "Google Gemini"}, {"glm", "智谱 GLM"},
		{"minimax", "MiniMax"}, {"volcengine", "火山引擎"}, {"qwen", "通义千问"},
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO juhe_business.vendors (id, code, name, enabled, default_supported_models_json, created_at, updated_at)
			VALUES ($1,$1,$2,1,'[]',now()::text,now()::text)
			ON CONFLICT (code) DO NOTHING`, row.code, row.name); err != nil {
			return 0, fmt.Errorf("写入 vendor %s: %w", row.code, err)
		}
	}
	return len(rows), nil
}

// decodeCredit decrypts one legacy credential envelope and returns the parts the
// rebuild needs. A credential that cannot be decrypted is a hard error: the
// account would otherwise migrate without any usable upstream address.
func decodeCredit(secret, envelope string) (accountCredit, error) {
	var credit accountCredit
	if strings.TrimSpace(envelope) == "" {
		return credit, errors.New("凭据密文为空")
	}
	if err := accountcrypto.DecryptJSON(secret, envelope, &credit); err != nil {
		return credit, err
	}
	return credit, nil
}

// normalizeModes keeps only modes the preset vocabulary actually supports, in
// vocabulary order, and de-duplicates. Modes outside the vocabulary cannot be
// executed by the target driver, so keeping them would create dead entries.
func normalizeModes(preset string, declared []string) []string {
	surface := presetSurfaces[preset]
	vocabulary := presetVocabulary[surface]
	allowed := make(map[string]struct{}, len(vocabulary))
	for _, mode := range vocabulary {
		allowed[mode] = struct{}{}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(declared))
	for _, mode := range declared {
		mode = strings.TrimSpace(mode)
		if _, ok := allowed[mode]; !ok {
			continue
		}
		if _, dup := seen[mode]; dup {
			continue
		}
		seen[mode] = struct{}{}
		out = append(out, mode)
	}
	return out
}

// effectiveModes returns the declared set when any of it survives normalisation,
// otherwise the whole preset vocabulary (the account keeps working with the
// driver's full surface instead of migrating with zero executable modes).
func effectiveModes(preset string, declared []string) []string {
	if normalized := normalizeModes(preset, declared); len(normalized) > 0 {
		return normalized
	}
	vocabulary := presetVocabulary[presetSurfaces[preset]]
	return append([]string(nil), vocabulary...)
}


