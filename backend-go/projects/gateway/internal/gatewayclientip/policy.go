package gatewayclientip

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/ipstats"
)

// Policy types mirror ClientIpPolicyType.
const (
	PolicyTypeBlacklist = "blacklist"
	PolicyTypeAllowlist = "allowlist"
)

// Shared cache names mirror the Node createAppCache / createSharedJsonCache
// names so the Redis key layout stays auditable next to the Node deployment.
const (
	policyByIPCacheName = "gateway:client-ip-policy-by-ip"
	policySnapshotCacheName = "gateway:client-ip-policy-snapshot"
)

// ActiveClientIPPolicy mirrors ActiveClientIpPolicy
// (storage/client-ip-policy.repository.ts).
type ActiveClientIPPolicy struct {
	ID             string  `json:"id"`
	IPHash         string  `json:"ipHash"`
	PolicyType     string  `json:"policyType"`
	AggregateIPKey string  `json:"aggregateIpKey"`
	ClientIP       string  `json:"clientIp"`
	Reason         *string `json:"reason,omitempty"`
	ExpiresAt      *string `json:"expiresAt,omitempty"`
}

// PolicyHitInput mirrors ClientIpPolicyHitInput.
type PolicyHitInput struct {
	IPHash   string `json:"ipHash"`
	PolicyID string `json:"policyId"`
	HitCount int64  `json:"hitCount"`
	HitAt    string `json:"hitAt"`
}

// PolicySource is the direct stats-database seam behind
// listActiveClientIpPoliciesAsync / findActiveClientIpPolicyByHashAsync /
// recordClientIpPolicyHitsAsync. *SQLPolicySource implements it in both
// SQLite and PostgreSQL modes.
type PolicySource interface {
	ListActiveClientIPPolicies(ctx context.Context) ([]ActiveClientIPPolicy, error)
	FindActiveClientIPPolicyByHash(ctx context.Context, ipHash string) (*ActiveClientIPPolicy, error)
	RecordClientIPPolicyHits(ctx context.Context, hits []PolicyHitInput) error
}

// StatsWriterOperation names mirror the BackgroundStatsWriteOperation types
// this family bridges to the stats writer.
const (
	StatsWriterOpListActiveClientIPPolicies  = "list_active_client_ip_policies"
	StatsWriterOpFindActiveClientIPPolicyByHash = "find_active_client_ip_policy_by_hash"
	StatsWriterOpRecordClientIPPolicyHits    = "record_client_ip_policy_hits"
)

// StatsWriterBridge mirrors requestStatsWriter: server/worker processes
// forward the policy reads and hit writes to the stats-writer owner instead
// of touching the database directly (shouldUseStatsWriterBridge).
type StatsWriterBridge interface {
	RequestStatsWriter(ctx context.Context, operation string, payload StatsWriterPayload) (StatsWriterPayload, error)
}

// StatsWriterPayload carries the bridge request/response payloads:
// "policies" for list, "policy" + "ipHash" for find, "hits" for record.
type StatsWriterPayload struct {
	IPHash   string             `json:"ipHash,omitempty"`
	Policies []ActiveClientIPPolicy `json:"policies,omitempty"`
	Policy   *ActiveClientIPPolicy  `json:"policy,omitempty"`
	Hits     []PolicyHitInput       `json:"hits,omitempty"`
}

// requestStatsWriterTimeout mirrors the fixed 1000ms budget the Node family
// passes to requestStatsWriter.
const requestStatsWriterTimeout = 1000 * time.Millisecond

// SQLPolicySource is the dual-mode policy read/hit source
// (storage/client-ip-policy.repository.ts): db is either the SQLite
// database opened from a sqlite path or the PostgreSQL pool handle; postgres
// switches schema qualification and placeholder binding. timezone resolves
// usageStatsTimezone for the hit stat_date; nil falls back to the
// system_settings source from internal/ipstats.
type SQLPolicySource struct {
	db       *sql.DB
	postgres bool
	now      func() time.Time
	timezone ipstats.TimezoneSource
}

// NewSQLPolicySource builds the dual-mode source. The caller opens db from a
// SQLite path or from the pg pool.
func NewSQLPolicySource(db *sql.DB, postgres bool, now func() time.Time, timezone ipstats.TimezoneSource) (*SQLPolicySource, error) {
	if db == nil {
		return nil, errors.New("gatewayclientip SQLPolicySource requires a stats database")
	}
	if now == nil {
		now = time.Now
	}
	if timezone == nil {
		timezone = ipstats.NewSystemSettingsTimezoneSource(db, postgres)
	}
	return &SQLPolicySource{db: db, postgres: postgres, now: now, timezone: timezone}, nil
}

func (s *SQLPolicySource) table(name string) string {
	if s.postgres {
		return "juhe_stats." + name
	}
	return name
}

// bind mirrors the pg placeholder rewrite of internal/ipstats.
func (s *SQLPolicySource) bind(query string) string {
	if !s.postgres {
		return query
	}
	var out strings.Builder
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString("$" + fmt.Sprintf("%d", index))
			index++
		} else {
			out.WriteByte(query[i])
		}
	}
	return out.String()
}

const activePolicySelectColumns = `policies.id, policies.ip_hash, policies.policy_type, policies.reason, policies.expires_at,
		registry.aggregate_ip_key, registry.client_ip`

func (s *SQLPolicySource) scanPolicies(rows *sql.Rows) ([]ActiveClientIPPolicy, error) {
	defer rows.Close()
	policies := []ActiveClientIPPolicy{}
	for rows.Next() {
		var (
			policy   ActiveClientIPPolicy
			reason   sql.NullString
			expiresAt sql.NullString
		)
		if err := rows.Scan(&policy.ID, &policy.IPHash, &policy.PolicyType, &reason, &expiresAt,
			&policy.AggregateIPKey, &policy.ClientIP); err != nil {
			return nil, err
		}
		if reason.Valid {
			policy.Reason = &reason.String
		}
		if expiresAt.Valid {
			policy.ExpiresAt = &expiresAt.String
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}

// ListActiveClientIPPolicies mirrors listActiveClientIpPolicies: the active
// rows joined with the registry, filtered to unexpired instants.
func (s *SQLPolicySource) ListActiveClientIPPolicies(ctx context.Context) ([]ActiveClientIPPolicy, error) {
	rows, err := s.db.QueryContext(ctx, s.bind(`
		SELECT `+activePolicySelectColumns+`
		FROM `+s.table("client_ip_policies")+` policies
		INNER JOIN `+s.table("client_ip_registry")+` registry ON registry.ip_hash = policies.ip_hash
		WHERE policies.status = 'active'`))
	if err != nil {
		return nil, err
	}
	policies, err := s.scanPolicies(rows)
	if err != nil {
		return nil, err
	}
	nowMs := s.now().UnixMilli()
	filtered := make([]ActiveClientIPPolicy, 0, len(policies))
	for _, policy := range policies {
		active, err := isActiveClientIPPolicyAt(policy, nowMs)
		if err != nil {
			return nil, err
		}
		if active {
			filtered = append(filtered, policy)
		}
	}
	return filtered, nil
}

// FindActiveClientIPPolicyByHash mirrors findActiveClientIpPolicyByHash.
// Malformed hashes read as missing, matching the Node normalizeIpHash guard.
func (s *SQLPolicySource) FindActiveClientIPPolicyByHash(ctx context.Context, inputIPHash string) (*ActiveClientIPPolicy, error) {
	ipHash := NormalizeIPHashForRuntime(inputIPHash)
	if ipHash == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`
		SELECT `+activePolicySelectColumns+`
		FROM `+s.table("client_ip_policies")+` policies
		INNER JOIN `+s.table("client_ip_registry")+` registry ON registry.ip_hash = policies.ip_hash
		WHERE policies.ip_hash = ?
			AND policies.status = 'active'
		LIMIT 1`), ipHash)
	if err != nil {
		return nil, err
	}
	policies, err := s.scanPolicies(rows)
	if err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return nil, nil
	}
	active, err := isActiveClientIPPolicyAt(policies[0], s.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	return &policies[0], nil
}

// policyHitEntry carries the normalized hit plus the timezone-resolved
// stat_date the SQL upsert needs.
type policyHitEntry struct {
	ipHash    string
	statDate  string
	policyID  string
	hitCount  int64
	hitAt     string
}

// RecordClientIPPolicyHits mirrors recordClientIpPolicyHits(Async): one
// upsert per hit keyed (ip_hash, stat_date, policy_id). SQLite runs the
// batch in one transaction like beginDatabaseTransaction; PostgreSQL chunks
// 500-row multi-row inserts like the Node async branch.
func (s *SQLPolicySource) RecordClientIPPolicyHits(ctx context.Context, hits []PolicyHitInput) error {
	if len(hits) == 0 {
		return nil
	}
	timezone, err := s.timezone(ctx)
	if err != nil {
		return err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return fmt.Errorf("系统设置 usageStatsTimezone 无效：%s", timezone)
	}
	updatedAt := s.now().UTC().Format("2006-01-02T15:04:05.000Z")
	entries := make([]policyHitEntry, 0, len(hits))
	for _, hit := range hits {
		ipHash := NormalizeIPHashForRuntime(hit.IPHash)
		policyID := strings.TrimSpace(hit.PolicyID)
		if ipHash == "" || policyID == "" {
			continue
		}
		// optionalRfc3339Instant(hit.hitAt, 'Client-IP 策略 hitAt') ??
		// updatedAt: empty reads as the batch timestamp, malformed throws.
		hitAt := updatedAt
		if hit.HitAt != "" {
			if _, ok := rfc3339Millis(hit.HitAt); !ok {
				return errors.New("Client-IP 策略 hitAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
			}
			hitAt = hit.HitAt
		}
		hitAtTime, parseErr := time.Parse(time.RFC3339Nano, hitAt)
		if parseErr != nil {
			return errors.New("Client-IP 策略 hitAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
		}
		hitCount := hit.HitCount
		if hitCount < 1 {
			hitCount = 1
		}
		entries = append(entries, policyHitEntry{
			ipHash:   ipHash,
			statDate: hitAtTime.In(location).Format("2006-01-02"),
			policyID: policyID,
			hitCount: hitCount,
			hitAt:    hitAt,
		})
	}
	if len(entries) == 0 {
		return nil
	}
	if s.postgres {
		return s.recordHitsPostgres(ctx, entries, updatedAt)
	}
	return s.recordHitsSQLite(ctx, entries, updatedAt)
}

func (s *SQLPolicySource) recordHitsSQLite(ctx context.Context, entries []policyHitEntry, updatedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, s.bind(`
		INSERT INTO client_ip_policy_hits (
			ip_hash, stat_date, policy_id, hit_count, last_hit_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(ip_hash, stat_date, policy_id) DO UPDATE SET
			hit_count = hit_count + excluded.hit_count,
			last_hit_at = CASE
				WHEN client_ip_policy_hits.last_hit_at IS NULL OR excluded.last_hit_at > client_ip_policy_hits.last_hit_at THEN excluded.last_hit_at
				ELSE client_ip_policy_hits.last_hit_at
			END,
			updated_at = excluded.updated_at`))
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, entry := range entries {
		if _, err := statement.ExecContext(ctx, entry.ipHash, entry.statDate, entry.policyID, entry.hitCount, entry.hitAt, updatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const policyHitsChunkSize = 500

func (s *SQLPolicySource) recordHitsPostgres(ctx context.Context, entries []policyHitEntry, updatedAt string) error {
	for start := 0; start < len(entries); start += policyHitsChunkSize {
		end := minInt(start+policyHitsChunkSize, len(entries))
		chunk := entries[start:end]
		var values strings.Builder
		args := make([]any, 0, len(chunk)*6)
		for i := range chunk {
			if i > 0 {
				values.WriteString(", ")
			}
			base := i * 6
			values.WriteString(fmt.Sprintf("($%d, $%d, $%d, $%d, $%d, $%d)",
				base+1, base+2, base+3, base+4, base+5, base+6))
			args = append(args, chunk[i].ipHash, chunk[i].statDate, chunk[i].policyID, chunk[i].hitCount, chunk[i].hitAt, updatedAt)
		}
		query := `
			INSERT INTO juhe_stats.client_ip_policy_hits (
				ip_hash, stat_date, policy_id, hit_count, last_hit_at, updated_at
			) VALUES ` + values.String() + `
			ON CONFLICT(ip_hash, stat_date, policy_id) DO UPDATE SET
				hit_count = juhe_stats.client_ip_policy_hits.hit_count + EXCLUDED.hit_count,
				last_hit_at = CASE
					WHEN juhe_stats.client_ip_policy_hits.last_hit_at IS NULL OR EXCLUDED.last_hit_at > juhe_stats.client_ip_policy_hits.last_hit_at THEN EXCLUDED.last_hit_at
					ELSE juhe_stats.client_ip_policy_hits.last_hit_at
				END,
				updated_at = EXCLUDED.updated_at`
		if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return nil
}

// isActiveClientIPPolicyAt mirrors isActiveClientIpPolicyAt: expiresAt
// malformed throws like the Node repository.
func isActiveClientIPPolicyAt(policy ActiveClientIPPolicy, nowMs int64) (bool, error) {
	if policy.ExpiresAt == nil {
		return true, nil
	}
	expiresAtMs, ok := rfc3339Millis(*policy.ExpiresAt)
	if !ok {
		return false, errors.New("Client-IP 策略 expiresAt 必须是带 Z 或数值 offset 的 RFC3339 时间")
	}
	return expiresAtMs > nowMs, nil
}
