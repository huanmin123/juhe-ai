// Package accountruntime owns the account-runtime transaction group.
//
// This package is deliberately storage-local.  It performs the fenced SQL
// transitions used by gateway account/key dispatch; it does not call Node,
// HTTP, IPC, queues, or create schema.  Credential decryption, usage/stats
// ownership, and schedule evaluation are explicit fail-closed ports.
package accountruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrOwnerGate                     = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrInvalidMode                   = errors.New("account runtime database mode is invalid")
	ErrInvalidSchema                 = errors.New("account runtime PostgreSQL schema is invalid")
	ErrStaleCAS                      = errors.New("account runtime CAS fence is stale")
	ErrLeaseLost                     = errors.New("account API key probe lease is lost")
	ErrOutstandingStatsOwner         = errors.New("account runtime quota requires the unmigrated stats owner")
	ErrOutstandingCredentialResolver = errors.New("account runtime probe requires the unmigrated credential resolver")
	ErrOutstandingScheduleEvaluator  = errors.New("account runtime schedule sync requires an explicit schedule evaluator")
	ErrInvalidInput                  = errors.New("account runtime input is invalid")
)

type Mode string

const (
	SQLite   Mode = "sqlite"
	Postgres Mode = "postgres"
)

type OwnerGate struct{ Confirmed, SchemaReady, NodeWriterStopped bool }

func (g OwnerGate) Ready() bool { return g.Confirmed && g.SchemaReady && g.NodeWriterStopped }

type QuotaCosts struct {
	Hourly  float64 `json:"hourly"`
	Daily   float64 `json:"daily"`
	Weekly  float64 `json:"weekly"`
	Monthly float64 `json:"monthly"`
	Total   float64 `json:"total"`
}

// RequestQuotaCosts is retained as an alias matching the Node contract.
type RequestQuotaCosts = QuotaCosts
type QuotaDecision struct {
	Allowed bool   `json:"allowed"`
	Message string `json:"message,omitempty"`
}

type APIKeyEntry struct {
	Key, Fingerprint string
	Index            int
}
type Account struct {
	ID, SystemAccountID                                  string
	ConfigRevision, DispatchRevision                     int64
	Status                                               string
	Schedulable                                          bool
	Type, ProviderCode, ProtocolCode, ProtocolVersion    string
	Name, CredentialsEncrypted                           string
	SelectedKeyFingerprint                               string
	SelectedKeyIndex                                     int
	APIKeys                                              []APIKeyEntry
	LastErrorCode, LastErrorMessage, LastErrorTraceID    string
	AccountExpiresAt, CooldownRetestObservationStartedAt string
}

// AccountSecret is the storage-safe input shape used by the Node operation.
// It may carry credentials only in memory; this package never persists or logs
// plaintext key material.
type AccountSecret = Account

type GatewayAPIKey struct {
	ID, SystemAccountID, RouteStrategyID, RouteStrategyMode                      string
	RouteStrategyConfigJSON, SelectedGroupID, Status, ExpiresAt, QuotaLimitsJSON string
	AvailabilityScheduleJSON, AvailabilityScheduleNextCheckAt                    string
	SystemAccountImageGenerationEnabled                                          bool
	SystemAccountRequestLimitsJSON                                               string
	GroupBindings                                                                []GatewayAPIKeyGroupBinding
}
type GatewayApiKeyRow = GatewayAPIKey
type GatewayAPIKeyGroupBinding struct {
	ID, APIKeyID, SystemAccountID, GroupID string
	Priority, Weight                       int
	Status, ProviderCode                   string
	GroupEnabled                           bool
}

type APIKeyRuntimeStatus string

const (
	RuntimeUnverified           APIKeyRuntimeStatus = "unverified"
	RuntimeTemporaryUnavailable APIKeyRuntimeStatus = "temporary_unavailable"
	RuntimeRateLimited          APIKeyRuntimeStatus = "rate_limited"
	RuntimeActive               APIKeyRuntimeStatus = "active"
	RuntimeDisabled             APIKeyRuntimeStatus = "disabled"
)

type CursorPurpose string

const (
	HealthCheck    CursorPurpose = "health_check"
	CooldownRetest CursorPurpose = "cooldown_retest"
)

type ProbeCursor struct {
	AccountID                                      string
	Purpose                                        CursorPurpose
	LastCompletedKeyFingerprint, KeySetFingerprint string
	ConfigRevision                                 int64
	DispatchRevision                               *int64
	CooldownGeneration                             string
	SourceConfigRevision                           *int64
	UpdatedAt                                      string
}
type AccountAPIKeyPoolProbeCursorInput = ProbeCursor
type AccountAPIKeyPoolProbeCursorResult struct {
	Cursor  *ProbeCursor
	Changed bool
}
type AccountApiKeyPoolProbeCursor = ProbeCursor

type MutationResult struct {
	Changed       bool   `json:"changed"`
	SkippedReason string `json:"skippedReason,omitempty"`
	Count         int    `json:"count,omitempty"`
	Triggered     bool   `json:"triggered,omitempty"`
	Action        string `json:"action,omitempty"`
}
type AccountApiKeyRuntimeWriteResult = MutationResult

type FailureInput struct {
	Status                                                      APIKeyRuntimeStatus
	StatusCode                                                  int
	ErrorCode, ErrorMessage, TraceID, CooldownUntil, ObservedAt string
	QuotaRecoveryMode                                           string
	BreakQuotaRecoveryWindow                                    bool
	ExpectedStatus                                              APIKeyRuntimeStatus
	ExpectedNextProbeAt, ExpectedStateUpdatedAt                 string
	ExpectedAccountConfigRevision                               int64
	ExpectedProbeClaimToken                                     string
}
type AccountApiKeyRuntimeFailureInput = FailureInput
type SuccessInput struct {
	ObservedAt                                  string
	ExpectedStatus                              APIKeyRuntimeStatus
	ExpectedNextProbeAt, ExpectedStateUpdatedAt string
	ExpectedAccountConfigRevision               int64
	ExpectedProbeClaimToken                     string
}
type AccountApiKeyRuntimeSuccessInput = SuccessInput
type ProbeDeferInput struct {
	ExpectedStatus                              APIKeyRuntimeStatus
	ExpectedNextProbeAt, ExpectedStateUpdatedAt string
	ExpectedAccountConfigRevision               int64
	ExpectedProbeClaimToken                     string
	DelaySeconds                                int
	ObservedAt                                  string
	BreakQuotaRecoveryWindow                    bool
}
type AccountApiKeyRuntimeProbeDeferInput = ProbeDeferInput

type ProbeCandidate struct {
	AccountID, AccountName, KeyFingerprint, APIKey                                                  string
	KeyIndex                                                                                        int
	Status                                                                                          APIKeyRuntimeStatus
	NextProbeAt, StateUpdatedAt                                                                     string
	AccountConfigRevision                                                                           int64
	ProbeClaimToken, ProbeClaimedUntil, RecoveryStartedAt, LastErrorCode                            string
	systemAccountID, accountType, providerCode, protocolCode, protocolVersion, credentialsEncrypted string
}
type AccountAPIKeyRuntimeProbeCandidate = ProbeCandidate

type ErrorPolicyDecision struct {
	Action, RuleName, RuleID, RuleSource, CooldownUntil, ErrorCode, QuotaRecoveryMode string
	KeyScoped                                                                         bool
}
type AccountErrorPolicyDecision = ErrorPolicyDecision
type ErrorHandlingInput struct {
	Success                                                 bool
	StatusCode                                              int
	ErrorMessage, UpstreamErrorSummary, TraceID, ObservedAt string
	DispatchRevision                                        int64
	PolicyDecision                                          *ErrorPolicyDecision
}
type AccountErrorHandlingInput = ErrorHandlingInput

type StreamFailureInput struct {
	AccountID                              string
	ThresholdCount, ThresholdWindowMinutes int
	Action, Reason, TraceID                string
}
type PrecheckTemporaryUnavailableInput struct {
	Account                   Account
	Reason, PrecheckStartedAt string
	ExpectedDispatchRevision  int64
	ExpectedStatus            string
}
type TemporaryUnavailableInput struct {
	Account         Account
	Reason, TraceID string
}
type ClearFailureInput struct {
	AccountID                                  string
	AllowPendingTestRestore, AllowErrorRestore bool
	ExpectedLastErrorCodes                     []string
	ExpectedConfigRevision                     int64
	ExpectedCooldownRetestObservationStartedAt string
}
type ExceptionInput struct {
	AccountID, ErrorCode, Reason, TraceID string
	PreserveDisabled                      bool
	ExpectedConfigRevision                int64
	ExpectedStatus                        string
}
type AccountErrorHandlingOptions struct {
	PreserveDisabled       bool
	ExpectedConfigRevision int64
	ExpectedStatus         string
}

type QuotaUsagePort interface {
	ReadAPIKeyQuotaCosts(context.Context, string, string, time.Time, *int) (QuotaCosts, error)
}
type QuotaUsageFunc func(context.Context, string, string, time.Time, *int) (QuotaCosts, error)

func (f QuotaUsageFunc) ReadAPIKeyQuotaCosts(ctx context.Context, sa, id string, now time.Time, h *int) (QuotaCosts, error) {
	return f(ctx, sa, id, now, h)
}

type CredentialResolver interface {
	ResolveAccountAPIKeys(context.Context, Account) ([]APIKeyEntry, error)
}
type CredentialResolverFunc func(context.Context, Account) ([]APIKeyEntry, error)

func (f CredentialResolverFunc) ResolveAccountAPIKeys(ctx context.Context, a Account) ([]APIKeyEntry, error) {
	return f(ctx, a)
}

type ScheduleDecision struct {
	Status      string
	EventKey    string
	NextCheckAt string
}
type ScheduleEvaluator interface {
	EvaluateSchedule(context.Context, string, time.Time) (ScheduleDecision, error)
}
type ScheduleEvaluatorFunc func(context.Context, string, time.Time) (ScheduleDecision, error)

func (f ScheduleEvaluatorFunc) EvaluateSchedule(ctx context.Context, j string, t time.Time) (ScheduleDecision, error) {
	return f(ctx, j, t)
}

type Dependencies struct {
	QuotaUsage  QuotaUsagePort
	Credentials CredentialResolver
	Schedule    ScheduleEvaluator
}

type Store struct {
	db     *sql.DB
	mode   Mode
	schema string
	gate   OwnerGate
	deps   Dependencies
	now    func() time.Time
}
type Port interface {
	CheckContract(context.Context) error
	ValidateGatewayAPIKey(context.Context, string) (GatewayAPIKey, error)
	CheckAPIKeyQuota(context.Context, GatewayAPIKey) (QuotaDecision, error)
	ReadAPIKeyQuotaCosts(context.Context, GatewayAPIKey) (QuotaCosts, error)
}

var _ Port = (*Store)(nil)

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func New(db *sql.DB, mode Mode, schema string, gate OwnerGate, deps ...Dependencies) (*Store, error) {
	if db == nil {
		return nil, errors.New("account runtime database is required")
	}
	if mode != SQLite && mode != Postgres {
		return nil, ErrInvalidMode
	}
	schema = strings.TrimSpace(schema)
	if mode == Postgres {
		if schema == "" {
			schema = "juhe_business"
		}
		if !identifier.MatchString(schema) {
			return nil, ErrInvalidSchema
		}
	}
	d := Dependencies{}
	if len(deps) > 0 {
		d = deps[0]
	}
	return &Store{db: db, mode: mode, schema: schema, gate: gate, deps: d, now: time.Now}, nil
}
func NewStore(db *sql.DB, mode Mode, schema string, gate OwnerGate, deps ...Dependencies) (*Store, error) {
	return New(db, mode, schema, gate, deps...)
}
func (s *Store) table(n string) string {
	if s.mode == Postgres {
		return s.schema + "." + n
	}
	return n
}
func (s *Store) bind(q string) string {
	if s.mode != Postgres {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteString("$")
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func (s *Store) requireWrite() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}
func (s *Store) CheckContract(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrOwnerGate
	}
	for _, t := range []string{"accounts", "account_api_key_runtime_states", "account_api_key_pool_probe_cursors", "api_keys", "route_strategies", "route_strategy_groups", "groups", "system_accounts", "resource_authorizations", "group_authorization_settings"} {
		if _, err := s.db.ExecContext(ctx, "SELECT 1 FROM "+s.table(t)+" LIMIT 0"); err != nil {
			return fmt.Errorf("verify account runtime relation %s: %w", t, err)
		}
	}
	return nil
}
func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}
func nowString(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// authorizationExpiryAfterNow keeps the Business TEXT ISO-8601 contract
// comparable on both SQLite and PostgreSQL.  PostgreSQL must cast both the
// stored text and bound value; SQLite needs datetime() so offsets and the
// RFC3339 `T` separator are interpreted chronologically rather than lexically.
func (s *Store) authorizationExpiryAfterNow(column string) string {
	if s.mode == Postgres {
		return "(" + column + " IS NULL OR " + column + "::timestamptz > ?::timestamptz)"
	}
	return "(" + column + " IS NULL OR datetime(" + column + ") > datetime(?))"
}
func parseTime(v string) (time.Time, error) {
	if strings.TrimSpace(v) == "" {
		return time.Time{}, ErrInvalidInput
	}
	return time.Parse(time.RFC3339Nano, v)
}
func nullable(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
func randomToken() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return ""
	}
	return hex.EncodeToString(b)
}
func hashKey(key string) string { h := sha256.Sum256([]byte(key)); return hex.EncodeToString(h[:]) }
func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
func validStatus(v APIKeyRuntimeStatus) bool {
	return v == RuntimeUnverified || v == RuntimeTemporaryUnavailable || v == RuntimeRateLimited
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func sanitize(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 1000 {
		return v[:1000]
	}
	return v
}

// ValidateGatewayAPIKey follows the Node route ownership join and never
// returns the encrypted or plaintext key secret.
func (s *Store) ValidateGatewayAPIKey(ctx context.Context, key string) (GatewayAPIKey, error) {
	if !strings.HasPrefix(key, "sk-") {
		return GatewayAPIKey{}, sql.ErrNoRows
	}
	h := hashKey(key)
	var a GatewayAPIKey
	var imageGeneration int
	q := `SELECT k.id,k.system_account_id,k.route_strategy_id,rs.mode,COALESCE(rs.config_json,''),k.status,COALESCE(k.expires_at,''),COALESCE(k.quota_limits_json,''),COALESCE(k.availability_schedule_json,''),COALESCE(k.availability_schedule_next_check_at,''),COALESCE(sa.image_generation_enabled,0),COALESCE(sa.request_limits_json,'') FROM ` + s.table("api_keys") + ` k JOIN ` + s.table("system_accounts") + ` sa ON sa.id=k.system_account_id JOIN ` + s.table("route_strategies") + ` rs ON rs.id=k.route_strategy_id AND rs.system_account_id=k.system_account_id WHERE k.key_hash=? AND k.status='active' AND sa.status='active' AND rs.status='active'`
	if err := s.db.QueryRowContext(ctx, s.bind(q), h).Scan(&a.ID, &a.SystemAccountID, &a.RouteStrategyID, &a.RouteStrategyMode, &a.RouteStrategyConfigJSON, &a.Status, &a.ExpiresAt, &a.QuotaLimitsJSON, &a.AvailabilityScheduleJSON, &a.AvailabilityScheduleNextCheckAt, &imageGeneration, &a.SystemAccountRequestLimitsJSON); err != nil {
		return GatewayAPIKey{}, err
	}
	a.SystemAccountImageGenerationEnabled = imageGeneration != 0
	if a.ExpiresAt != "" {
		t, e := time.Parse(time.RFC3339Nano, a.ExpiresAt)
		if e != nil {
			return GatewayAPIKey{}, fmt.Errorf("invalid gateway API key expiry: %w", e)
		}
		if !t.After(s.clock()) {
			return GatewayAPIKey{}, sql.ErrNoRows
		}
	}
	q = `SELECT rsg.id,rsg.group_id,rsg.priority,rsg.weight,rsg.status,g.provider_code,g.enabled FROM ` + s.table("route_strategies") + ` rs JOIN ` + s.table("route_strategy_groups") + ` rsg ON rsg.route_strategy_id=rs.id AND rsg.system_account_id=rs.system_account_id JOIN ` + s.table("groups") + ` g ON g.id=rsg.group_id LEFT JOIN ` + s.table("resource_authorizations") + ` ga ON ga.resource_type='group' AND ga.resource_id=g.id AND ga.grantee_system_account_id=rsg.system_account_id AND ga.scope='use' AND ga.status='active' AND ` + s.authorizationExpiryAfterNow("ga.expires_at") + ` LEFT JOIN ` + s.table("group_authorization_settings") + ` gas ON gas.authorization_id=ga.id AND gas.system_account_id=rsg.system_account_id AND gas.group_id=g.id WHERE rs.id=? AND rs.system_account_id=? AND rs.status='active' AND rsg.status='active' AND g.enabled=1 AND (g.system_account_id=rsg.system_account_id OR (ga.id IS NOT NULL AND COALESCE(gas.enabled,1)=1)) ORDER BY rsg.priority ASC,rsg.created_at ASC,rsg.id ASC`
	rows, err := s.db.QueryContext(ctx, s.bind(q), nowString(s.clock()), a.RouteStrategyID, a.SystemAccountID)
	if err != nil {
		return GatewayAPIKey{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var b GatewayAPIKeyGroupBinding
		var enabled int
		if err = rows.Scan(&b.ID, &b.GroupID, &b.Priority, &b.Weight, &b.Status, &b.ProviderCode, &enabled); err != nil {
			return GatewayAPIKey{}, err
		}
		b.APIKeyID = a.ID
		b.SystemAccountID = a.SystemAccountID
		b.GroupEnabled = enabled != 0
		a.GroupBindings = append(a.GroupBindings, b)
	}
	if err = rows.Err(); err != nil {
		return GatewayAPIKey{}, err
	}
	if len(a.GroupBindings) == 0 {
		return GatewayAPIKey{}, sql.ErrNoRows
	}
	a.SelectedGroupID = a.GroupBindings[0].GroupID
	return a, nil
}

type quotaLimit struct {
	Enabled           bool    `json:"enabled"`
	Limit             float64 `json:"limit"`
	HourlyWindowHours int     `json:"windowHours"`
}
type quotaLimits struct {
	Hourly  quotaLimit `json:"hourly"`
	Daily   quotaLimit `json:"daily"`
	Weekly  quotaLimit `json:"weekly"`
	Monthly quotaLimit `json:"monthly"`
	Total   quotaLimit `json:"total"`
}

func decodeLimits(raw string) (quotaLimits, bool, error) {
	var x quotaLimits
	if strings.TrimSpace(raw) == "" || raw == "null" || raw == "{}" {
		return x, false, nil
	}
	if e := json.Unmarshal([]byte(raw), &x); e != nil {
		return x, false, e
	}
	return x, x.Hourly.Enabled || x.Daily.Enabled || x.Weekly.Enabled || x.Monthly.Enabled || x.Total.Enabled, nil
}
func exceeds(x quotaLimits, c QuotaCosts) bool {
	return (x.Hourly.Enabled && c.Hourly >= x.Hourly.Limit) || (x.Daily.Enabled && c.Daily >= x.Daily.Limit) || (x.Weekly.Enabled && c.Weekly >= x.Weekly.Limit) || (x.Monthly.Enabled && c.Monthly >= x.Monthly.Limit) || (x.Total.Enabled && c.Total >= x.Total.Limit)
}
func (s *Store) ReadAPIKeyQuotaCosts(ctx context.Context, key GatewayAPIKey) (QuotaCosts, error) {
	limits, enabled, e := decodeLimits(key.QuotaLimitsJSON)
	if e != nil {
		return QuotaCosts{}, e
	}
	if !enabled {
		return QuotaCosts{}, nil
	}
	if s.deps.QuotaUsage == nil {
		return QuotaCosts{}, ErrOutstandingStatsOwner
	}
	var h *int
	if limits.Hourly.Enabled {
		n := limits.Hourly.HourlyWindowHours
		if n <= 0 {
			n = 1
		}
		h = &n
	}
	return s.deps.QuotaUsage.ReadAPIKeyQuotaCosts(ctx, key.SystemAccountID, key.ID, s.clock(), h)
}
func (s *Store) CheckAPIKeyQuota(ctx context.Context, key GatewayAPIKey) (QuotaDecision, error) {
	c, e := s.ReadAPIKeyQuotaCosts(ctx, key)
	if e != nil {
		return QuotaDecision{}, e
	}
	l, on, e := decodeLimits(key.QuotaLimitsJSON)
	if e != nil {
		return QuotaDecision{}, e
	}
	if !on || !exceeds(l, c) {
		return QuotaDecision{Allowed: true}, nil
	}
	return QuotaDecision{Allowed: false, Message: "额度已用完，请联系管理员提升额度"}, nil
}

func cursorArgs(c ProbeCursor) ([]any, error) {
	if strings.TrimSpace(c.AccountID) == "" || strings.TrimSpace(string(c.Purpose)) == "" || strings.TrimSpace(c.KeySetFingerprint) == "" || c.ConfigRevision < 1 {
		return nil, ErrInvalidInput
	}
	return []any{c.AccountID, c.Purpose, nullable(c.LastCompletedKeyFingerprint), c.KeySetFingerprint, c.ConfigRevision, c.DispatchRevision, c.CooldownGeneration, c.SourceConfigRevision, nowString(time.Now())}, nil
}
func (s *Store) AccountAPIKeyPoolProbeCursor(ctx context.Context, c ProbeCursor, action string) (AccountAPIKeyPoolProbeCursorResult, error) {
	if strings.TrimSpace(c.AccountID) == "" {
		return AccountAPIKeyPoolProbeCursorResult{}, ErrInvalidInput
	}
	if action == "read" {
		var x ProbeCursor
		var last sql.NullString
		var dispatch, source sql.NullInt64
		var cooldown sql.NullString
		var rev int64
		var updated string
		e := s.db.QueryRowContext(ctx, s.bind("SELECT account_id,purpose,last_completed_key_fingerprint,key_set_fingerprint,config_revision,dispatch_revision,cooldown_generation,source_config_revision,updated_at FROM "+s.table("account_api_key_pool_probe_cursors")+" WHERE account_id=? AND purpose=?"), c.AccountID, c.Purpose).Scan(&x.AccountID, &x.Purpose, &last, &x.KeySetFingerprint, &rev, &dispatch, &cooldown, &source, &updated)
		if e != nil {
			return AccountAPIKeyPoolProbeCursorResult{}, e
		}
		x.LastCompletedKeyFingerprint = last.String
		x.ConfigRevision = rev
		x.UpdatedAt = updated
		x.DispatchRevision = parseNullableInt64(dispatch)
		x.CooldownGeneration = cooldown.String
		x.SourceConfigRevision = parseNullableInt64(source)
		return AccountAPIKeyPoolProbeCursorResult{Cursor: &x}, nil
	}
	if e := s.requireWrite(); e != nil {
		return AccountAPIKeyPoolProbeCursorResult{}, e
	}
	if action == "delete" {
		r, e := s.db.ExecContext(ctx, s.bind("DELETE FROM "+s.table("account_api_key_pool_probe_cursors")+" WHERE account_id=? AND purpose=?"), c.AccountID, c.Purpose)
		if e != nil {
			return AccountAPIKeyPoolProbeCursorResult{}, e
		}
		n, _ := r.RowsAffected()
		return AccountAPIKeyPoolProbeCursorResult{Changed: n == 1}, nil
	}
	args, e := cursorArgs(c)
	if e != nil {
		return AccountAPIKeyPoolProbeCursorResult{}, e
	}
	args[8] = nowString(s.clock())
	q := "INSERT INTO " + s.table("account_api_key_pool_probe_cursors") + " (account_id,purpose,last_completed_key_fingerprint,key_set_fingerprint,config_revision,dispatch_revision,cooldown_generation,source_config_revision,updated_at) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(account_id,purpose) DO UPDATE SET last_completed_key_fingerprint=excluded.last_completed_key_fingerprint,key_set_fingerprint=excluded.key_set_fingerprint,config_revision=excluded.config_revision,dispatch_revision=excluded.dispatch_revision,cooldown_generation=excluded.cooldown_generation,source_config_revision=excluded.source_config_revision,updated_at=excluded.updated_at"
	if _, e = s.db.ExecContext(ctx, s.bind(q), args...); e != nil {
		return AccountAPIKeyPoolProbeCursorResult{}, e
	}
	return AccountAPIKeyPoolProbeCursorResult{Changed: true}, nil
}
func parseNullableInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

func (s *Store) keyTarget(a Account) bool {
	if a.ID == "" || a.SystemAccountID == "" || a.SelectedKeyFingerprint == "" || len(a.APIKeys) < 2 {
		return false
	}
	if a.Type != "" && a.Type != "api_key" {
		return false
	}
	for _, key := range a.APIKeys {
		fingerprint := key.Fingerprint
		if fingerprint == "" && key.Key != "" {
			fingerprint = hashKey(key.Key)
		}
		if fingerprint == a.SelectedKeyFingerprint {
			return true
		}
	}
	return false
}
func (s *Store) ensureTarget(a Account) error {
	if !s.keyTarget(a) {
		return errors.New("account is not an isolated API-key pool")
	}
	return nil
}
func (s *Store) expectedWhere(q *strings.Builder, args *[]any, in FailureInput, includeClaim bool) {
	if in.ExpectedStatus != "" {
		q.WriteString(" AND status=?")
		*args = append(*args, in.ExpectedStatus)
	}
	if in.ExpectedNextProbeAt != "" {
		q.WriteString(" AND COALESCE(next_probe_at,'')=?")
		*args = append(*args, in.ExpectedNextProbeAt)
	}
	if in.ExpectedStateUpdatedAt != "" {
		q.WriteString(" AND updated_at=?")
		*args = append(*args, in.ExpectedStateUpdatedAt)
	}
	if in.ExpectedAccountConfigRevision > 0 {
		q.WriteString(" AND EXISTS (SELECT 1 FROM " + s.table("accounts") + " ac WHERE ac.id=" + s.table("account_api_key_runtime_states") + ".account_id AND ac.config_revision=?)")
		*args = append(*args, in.ExpectedAccountConfigRevision)
	}
	if includeClaim && in.ExpectedProbeClaimToken != "" {
		q.WriteString(" AND probe_claim_token=?")
		*args = append(*args, in.ExpectedProbeClaimToken)
	}
}
func (s *Store) runtimeUpdate(ctx context.Context, a Account, in FailureInput, success bool, deferProbe bool) (MutationResult, error) {
	if e := s.requireWrite(); e != nil {
		return MutationResult{}, e
	}
	if e := s.ensureTarget(a); e != nil {
		return MutationResult{}, e
	}
	now := s.clock()
	observed := in.ObservedAt
	if observed == "" {
		observed = nowString(now)
	}
	if _, e := parseTime(observed); e != nil {
		return MutationResult{}, e
	}
	status := in.Status
	if success {
		status = RuntimeActive
	}
	if deferProbe {
		status = in.ExpectedStatus
		if !validStatus(status) {
			return MutationResult{}, ErrInvalidInput
		}
	}
	if status == "" {
		status = RuntimeTemporaryUnavailable
	}
	if !success && status == RuntimeActive {
		return MutationResult{}, ErrInvalidInput
	}
	if status != RuntimeActive && !validStatus(status) {
		return MutationResult{}, ErrInvalidInput
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return MutationResult{}, e
	}
	defer tx.Rollback()
	var existingStatus string
	var oldBackoff int
	var lastAttempt sql.NullString
	existingErr := tx.QueryRowContext(ctx, s.bind("SELECT status,COALESCE(probe_backoff_seconds,0),last_attempt_at FROM "+s.table("account_api_key_runtime_states")+" WHERE account_id=? AND key_fingerprint=?"), a.ID, a.SelectedKeyFingerprint).Scan(&existingStatus, &oldBackoff, &lastAttempt)
	if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
		return MutationResult{}, existingErr
	}
	exists := existingErr == nil
	if exists && existingStatus == string(RuntimeDisabled) {
		return MutationResult{SkippedReason: "key_disabled"}, nil
	}
	expectedProvided := in.ExpectedStatus != "" || in.ExpectedNextProbeAt != "" || in.ExpectedStateUpdatedAt != "" || in.ExpectedAccountConfigRevision > 0 || in.ExpectedProbeClaimToken != ""
	if !exists && (deferProbe || success && expectedProvided || !success && expectedProvided) {
		return MutationResult{SkippedReason: "stale_probe_state"}, nil
	}
	if deferProbe {
		next := now.Add(time.Duration(clamp(in.StatusCode, 3, 3600)) * time.Second).Format(time.RFC3339Nano)
		q := "UPDATE " + s.table("account_api_key_runtime_states") + " SET next_probe_at=?,last_attempt_at=?,probe_claim_token=NULL,probe_claimed_until=NULL,recovery_started_at=CASE WHEN ?=1 THEN NULL ELSE recovery_started_at END,updated_at=? WHERE account_id=? AND key_fingerprint=? AND (last_attempt_at IS NULL OR last_attempt_at<=? )"
		args := []any{next, observed, boolInt(in.BreakQuotaRecoveryWindow), observed, a.ID, a.SelectedKeyFingerprint, observed}
		qBuilder := strings.Builder{}
		qBuilder.WriteString(q)
		s.expectedWhere(&qBuilder, &args, in, true)
		r, e := tx.ExecContext(ctx, s.bind(qBuilder.String()), args...)
		if e != nil {
			return MutationResult{}, e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return MutationResult{SkippedReason: "stale_probe_state"}, nil
		}
		if e = tx.Commit(); e != nil {
			return MutationResult{}, e
		}
		return MutationResult{Changed: true}, nil
	}
	if success {
		if in.ExpectedStatus == RuntimeRateLimited || in.ExpectedStatus == RuntimeTemporaryUnavailable || in.ExpectedStatus == RuntimeUnverified || in.ExpectedStatus == RuntimeDisabled {
		}
		if exists {
			q := "UPDATE " + s.table("account_api_key_runtime_states") + " SET status='active',failure_count=0,consecutive_failures=0,success_count=success_count+1,cooldown_until=NULL,next_probe_at=NULL,probe_backoff_seconds=0,recovery_started_at=NULL,last_attempt_at=?,last_success_at=?,last_error_code=NULL,last_error_message=NULL,last_trace_id=NULL,probe_claim_token=NULL,probe_claimed_until=NULL,updated_at=? WHERE account_id=? AND key_fingerprint=? AND status NOT IN ('disabled','error') AND (last_attempt_at IS NULL OR last_attempt_at<=?)"
			args := []any{observed, observed, observed, a.ID, a.SelectedKeyFingerprint, observed}
			qb := strings.Builder{}
			qb.WriteString(q)
			s.expectedWhere(&qb, &args, in, true)
			r, e := tx.ExecContext(ctx, s.bind(qb.String()), args...)
			if e != nil {
				return MutationResult{}, e
			}
			n, _ := r.RowsAffected()
			if n != 1 {
				return MutationResult{SkippedReason: "stale_probe_state"}, nil
			}
		} else {
			q := "INSERT INTO " + s.table("account_api_key_runtime_states") + " (id,system_account_id,account_id,key_fingerprint,key_index,status,failure_count,consecutive_failures,success_count,last_attempt_at,last_success_at,created_at,updated_at) VALUES (?,?,?,?,?,'active',0,0,1,?,?,?,?)"
			if _, e := tx.ExecContext(ctx, s.bind(q), "account-runtime-"+randomToken(), a.SystemAccountID, a.ID, a.SelectedKeyFingerprint, a.SelectedKeyIndex, observed, observed, observed, observed); e != nil {
				return MutationResult{}, e
			}
		}
		if e = tx.Commit(); e != nil {
			return MutationResult{}, e
		}
		return MutationResult{Changed: true}, nil
	}
	backoff := 3
	if oldBackoff > 0 {
		backoff = clamp(oldBackoff*2, 3, 3600)
	}
	next := now.Add(time.Duration(backoff) * time.Second)
	if in.CooldownUntil != "" {
		t, x := time.Parse(time.RFC3339Nano, in.CooldownUntil)
		if x != nil {
			return MutationResult{}, x
		}
		if t.After(next) {
			next = t
		}
	}
	code := sanitize(in.ErrorCode)
	if code == "" {
		if in.QuotaRecoveryMode == "explicit_reset" {
			code = "api_key_quota_explicit_reset"
		} else if in.QuotaRecoveryMode == "generic" {
			code = "api_key_quota_generic"
		} else if in.StatusCode > 0 {
			code = "http_" + strconv.Itoa(in.StatusCode)
		} else {
			code = "account_api_key_failure"
		}
	}
	message := sanitize(in.ErrorMessage)
	if message == "" {
		if in.StatusCode > 0 {
			message = "上游返回 HTTP " + strconv.Itoa(in.StatusCode)
		} else {
			message = "上游请求失败"
		}
	}
	if exists {
		q := "UPDATE " + s.table("account_api_key_runtime_states") + " SET system_account_id=?,key_index=?,status=?,failure_count=failure_count+1,consecutive_failures=consecutive_failures+1,cooldown_until=?,next_probe_at=?,probe_backoff_seconds=?,recovery_started_at=CASE WHEN ?=1 THEN NULL WHEN ?<>'' THEN ? ELSE COALESCE(recovery_started_at,?) END,last_attempt_at=?,last_failure_at=?,last_error_code=?,last_error_message=?,last_trace_id=?,probe_claim_token=NULL,probe_claimed_until=NULL,updated_at=? WHERE account_id=? AND key_fingerprint=? AND status<>'disabled' AND (last_attempt_at IS NULL OR last_attempt_at<?) AND NOT (?='generic' AND last_error_code IN ('api_key_quota_explicit_reset','api_key_quota_generic') AND cooldown_until IS NOT NULL AND julianday(cooldown_until)>julianday(?))"
		recovery := nullable(observed)
		args := []any{a.SystemAccountID, a.SelectedKeyIndex, status, next.Format(time.RFC3339Nano), next.Format(time.RFC3339Nano), backoff, boolInt(in.BreakQuotaRecoveryWindow), in.QuotaRecoveryMode, recovery, observed, observed, observed, code, message, sanitize(in.TraceID), observed, a.ID, a.SelectedKeyFingerprint, observed, in.QuotaRecoveryMode, observed}
		qb := strings.Builder{}
		qb.WriteString(q)
		s.expectedWhere(&qb, &args, in, true)
		r, e := tx.ExecContext(ctx, s.bind(qb.String()), args...)
		if e != nil {
			return MutationResult{}, e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return MutationResult{SkippedReason: "stale_probe_state"}, nil
		}
	} else {
		q := "INSERT INTO " + s.table("account_api_key_runtime_states") + " (id,system_account_id,account_id,key_fingerprint,key_index,status,failure_count,consecutive_failures,success_count,cooldown_until,next_probe_at,probe_backoff_seconds,recovery_started_at,last_attempt_at,last_failure_at,last_error_code,last_error_message,last_trace_id,created_at,updated_at) VALUES (?,?,?,?,?,?,1,1,0,?,?,?,?,?,?,?,?,?,?,?)"
		recovery := nullable(observed)
		if in.BreakQuotaRecoveryWindow {
			recovery = nil
		}
		if _, e := tx.ExecContext(ctx, s.bind(q), "account-runtime-"+randomToken(), a.SystemAccountID, a.ID, a.SelectedKeyFingerprint, a.SelectedKeyIndex, status, next.Format(time.RFC3339Nano), next.Format(time.RFC3339Nano), backoff, recovery, observed, observed, code, message, sanitize(in.TraceID), observed, observed); e != nil {
			return MutationResult{}, e
		}
	}
	if e = tx.Commit(); e != nil {
		return MutationResult{}, e
	}
	return MutationResult{Changed: true}, nil
}
func (s *Store) RecordAccountAPIKeyFailure(ctx context.Context, a Account, in FailureInput) (MutationResult, error) {
	return s.runtimeUpdate(ctx, a, in, false, false)
}
func (s *Store) RecordAccountAPIKeySuccess(ctx context.Context, a Account, in SuccessInput) (MutationResult, error) {
	if in.ExpectedStatus == RuntimeDisabled || in.ExpectedStatus == "error" {
		return MutationResult{SkippedReason: "manual_restore_required"}, nil
	}
	return s.runtimeUpdate(ctx, a, FailureInput{ObservedAt: in.ObservedAt, ExpectedStatus: in.ExpectedStatus, ExpectedNextProbeAt: in.ExpectedNextProbeAt, ExpectedStateUpdatedAt: in.ExpectedStateUpdatedAt, ExpectedAccountConfigRevision: in.ExpectedAccountConfigRevision, ExpectedProbeClaimToken: in.ExpectedProbeClaimToken}, true, false)
}
func (s *Store) DeferAccountAPIKeyProbe(ctx context.Context, a Account, in ProbeDeferInput) (MutationResult, error) {
	if strings.TrimSpace(in.ExpectedNextProbeAt) == "" {
		return MutationResult{SkippedReason: "missing_expected_probe_at"}, nil
	}
	if _, err := parseTime(in.ExpectedNextProbeAt); err != nil {
		return MutationResult{SkippedReason: "invalid_expected_probe_at"}, nil
	}
	if !validStatus(in.ExpectedStatus) {
		return MutationResult{SkippedReason: "invalid_expected_status"}, nil
	}
	return s.runtimeUpdate(ctx, a, FailureInput{Status: in.ExpectedStatus, StatusCode: in.DelaySeconds, ObservedAt: in.ObservedAt, ExpectedStatus: in.ExpectedStatus, ExpectedNextProbeAt: in.ExpectedNextProbeAt, ExpectedStateUpdatedAt: in.ExpectedStateUpdatedAt, ExpectedAccountConfigRevision: in.ExpectedAccountConfigRevision, ExpectedProbeClaimToken: in.ExpectedProbeClaimToken}, false, true)
}

func (s *Store) ListAccountAPIKeyRuntimeStatesDueForProbe(ctx context.Context, limit int) ([]ProbeCandidate, error) {
	if e := s.requireWrite(); e != nil {
		return nil, e
	}
	if s.deps.Credentials == nil {
		return nil, ErrOutstandingCredentialResolver
	}
	limit = clamp(limit, 1, 100)
	now := nowString(s.clock())
	q := "SELECT r.account_id,COALESCE(a.name,''),r.key_fingerprint,r.key_index,r.status,r.next_probe_at,r.updated_at,a.config_revision,a.system_account_id,a.type,a.provider_code,a.protocol_code,a.protocol_version,COALESCE(a.credentials_encrypted,''),COALESCE(r.recovery_started_at,''),COALESCE(r.last_error_code,'') FROM " + s.table("account_api_key_runtime_states") + " r JOIN " + s.table("accounts") + " a ON a.id=r.account_id WHERE r.status IN ('unverified','temporary_unavailable','rate_limited') AND r.next_probe_at IS NOT NULL AND r.next_probe_at<=? AND (r.probe_claimed_until IS NULL OR r.probe_claimed_until<=?) AND a.deleted_at IS NULL AND a.status IN ('active','rate_limited','temporary_unavailable') AND a.schedulable=1 AND (a.account_expires_at IS NULL OR a.account_expires_at>?) ORDER BY r.next_probe_at ASC,r.updated_at ASC,r.account_id ASC,r.key_index ASC LIMIT ?"
	rows, e := s.db.QueryContext(ctx, s.bind(q), now, now, now, limit)
	if e != nil {
		return nil, e
	}
	var candidates []ProbeCandidate
	for rows.Next() {
		var c ProbeCandidate
		if e = rows.Scan(&c.AccountID, &c.AccountName, &c.KeyFingerprint, &c.KeyIndex, &c.Status, &c.NextProbeAt, &c.StateUpdatedAt, &c.AccountConfigRevision, &c.systemAccountID, &c.accountType, &c.providerCode, &c.protocolCode, &c.protocolVersion, &c.credentialsEncrypted, &c.RecoveryStartedAt, &c.LastErrorCode); e != nil {
			return nil, e
		}
		candidates = append(candidates, c)
	}
	if e = rows.Err(); e != nil {
		rows.Close()
		return nil, e
	}
	if e = rows.Close(); e != nil {
		return nil, e
	}
	var out []ProbeCandidate
	for _, c := range candidates {
		var a Account
		a.ID = c.AccountID
		a.Name = c.AccountName
		a.ConfigRevision = c.AccountConfigRevision
		a.SystemAccountID = c.systemAccountID
		a.Type, a.ProviderCode, a.ProtocolCode, a.ProtocolVersion, a.CredentialsEncrypted = c.accountType, c.providerCode, c.protocolCode, c.protocolVersion, c.credentialsEncrypted
		keys, e := s.deps.Credentials.ResolveAccountAPIKeys(ctx, a)
		if e != nil {
			return nil, fmt.Errorf("resolve account %s: %w", c.AccountID, e)
		}
		for _, k := range keys {
			if k.Fingerprint == c.KeyFingerprint {
				c.APIKey = k.Key
				break
			}
		}
		if c.APIKey == "" {
			return nil, fmt.Errorf("account %s probe key fingerprint not found", c.AccountID)
		}
		c.ProbeClaimToken = randomToken()
		if c.ProbeClaimToken == "" {
			return nil, errors.New("generate probe claim token")
		}
		lease := s.clock().Add(10 * time.Minute).Format(time.RFC3339Nano)
		u, er := s.db.ExecContext(ctx, s.bind("UPDATE "+s.table("account_api_key_runtime_states")+" SET probe_claim_token=?,probe_claimed_until=?,last_probe_at=?,updated_at=? WHERE account_id=? AND key_fingerprint=? AND status=? AND COALESCE(next_probe_at,'')=? AND updated_at=? AND (probe_claimed_until IS NULL OR probe_claimed_until<=?)"), c.ProbeClaimToken, lease, now, now, c.AccountID, c.KeyFingerprint, c.Status, c.NextProbeAt, c.StateUpdatedAt, now)
		if er != nil {
			return nil, er
		}
		n, _ := u.RowsAffected()
		if n == 1 {
			c.ProbeClaimedUntil = lease
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) updateAccount(ctx context.Context, id string, set string, args []any, where string, wargs []any) (MutationResult, error) {
	if e := s.requireWrite(); e != nil {
		return MutationResult{}, e
	}
	now := nowString(s.clock())
	q := "UPDATE " + s.table("accounts") + " SET " + set + ",updated_at=? WHERE id=? AND deleted_at IS NULL"
	args = append(args, now, id)
	q += where
	args = append(args, wargs...)
	r, e := s.db.ExecContext(ctx, s.bind(q), args...)
	if e != nil {
		return MutationResult{}, e
	}
	n, _ := r.RowsAffected()
	return MutationResult{Changed: n == 1}, nil
}

func (s *Store) updateAccountScoped(ctx context.Context, account Account, set string, setArgs []any, where string, whereArgs []any) (MutationResult, error) {
	if account.ID == "" || account.SystemAccountID == "" {
		return MutationResult{}, ErrInvalidInput
	}
	if account.ConfigRevision < 1 {
		return MutationResult{}, ErrInvalidInput
	}
	where = " AND system_account_id=? AND config_revision=?" + where
	whereArgs = append([]any{account.SystemAccountID, account.ConfigRevision}, whereArgs...)
	return s.updateAccount(ctx, account.ID, set, setArgs, where, whereArgs)
}
func (s *Store) MarkAccountException(ctx context.Context, in ExceptionInput) (MutationResult, error) {
	if in.AccountID == "" || in.ErrorCode == "" {
		return MutationResult{}, ErrInvalidInput
	}
	set := "status='error',schedulable=0,cooldown_until=NULL,last_error_code=?,last_error_message=?,last_error_trace_id=?,cooldown_retest_observation_started_at=NULL,stream_failure_count=0,stream_failure_window_started_at=NULL"
	where := " AND status NOT IN ('error','disabled')"
	setArgs := []any{sanitize(in.ErrorCode), sanitize(in.Reason), sanitize(in.TraceID)}
	whereArgs := []any{}
	if in.ExpectedConfigRevision > 0 {
		where += " AND config_revision=?"
		whereArgs = append(whereArgs, in.ExpectedConfigRevision)
	}
	if in.ExpectedStatus != "" {
		where += " AND status=?"
		whereArgs = append(whereArgs, in.ExpectedStatus)
	}
	if in.PreserveDisabled {
		set = "status=CASE WHEN status='disabled' THEN status ELSE 'error' END,schedulable=0,last_error_code=?,last_error_message=?,last_error_trace_id=?"
		where = ""
	}
	return s.updateAccount(ctx, in.AccountID, set, setArgs, where, whereArgs)
}
func (s *Store) MarkAccountExceptionWithOptions(ctx context.Context, accountID, errorCode, reason, traceID string, options AccountErrorHandlingOptions) (MutationResult, error) {
	return s.MarkAccountException(ctx, ExceptionInput{AccountID: accountID, ErrorCode: errorCode, Reason: reason, TraceID: traceID, PreserveDisabled: options.PreserveDisabled, ExpectedConfigRevision: options.ExpectedConfigRevision, ExpectedStatus: options.ExpectedStatus})
}
func (s *Store) MarkAccountTemporaryUnavailable(ctx context.Context, in TemporaryUnavailableInput) (MutationResult, error) {
	if in.Account.ID == "" {
		return MutationResult{}, ErrInvalidInput
	}
	now := s.clock()
	cooldownUntil := now.Add(time.Minute).Format(time.RFC3339Nano)
	set := "status='temporary_unavailable',schedulable=1,cooldown_until=?,last_error_code='temporary_unavailable',last_error_message=?,last_error_trace_id=?,cooldown_retest_failure_count=0,cooldown_retest_observation_started_at=?,cooldown_retest_generation=?,cooldown_retest_last_at=NULL,cooldown_retest_last_status_code=NULL,stream_failure_count=0,stream_failure_window_started_at=NULL"
	return s.updateAccount(ctx, in.Account.ID, set, []any{cooldownUntil, sanitize(in.Reason), sanitize(in.TraceID), nowString(now), "account-runtime-" + randomToken()}, " AND status NOT IN ('disabled','error')", nil)
}
func (s *Store) MarkAccountPrecheckTemporaryUnavailable(ctx context.Context, in PrecheckTemporaryUnavailableInput) (MutationResult, error) {
	if _, e := parseTime(in.PrecheckStartedAt); e != nil {
		return MutationResult{}, e
	}
	if in.Account.ID == "" || in.ExpectedDispatchRevision < 1 {
		return MutationResult{}, ErrInvalidInput
	}
	where := " AND dispatch_revision=? AND status NOT IN ('disabled','error')"
	whereArgs := []any{in.ExpectedDispatchRevision}
	if in.ExpectedStatus != "" {
		where += " AND status=?"
		whereArgs = append(whereArgs, in.ExpectedStatus)
	}
	now := s.clock()
	set := "status='temporary_unavailable',schedulable=1,last_error_code='precheck_temporary_unavailable',last_error_message=?,cooldown_until=?,cooldown_retest_observation_started_at=?,cooldown_retest_generation=?"
	return s.updateAccount(ctx, in.Account.ID, set, []any{sanitize(in.Reason), now.Add(time.Minute).Format(time.RFC3339Nano), nowString(now), "account-runtime-" + randomToken()}, where, whereArgs)
}
func (s *Store) ClearAccountFailureState(ctx context.Context, in ClearFailureInput) (MutationResult, error) {
	if in.AccountID == "" {
		return MutationResult{}, ErrInvalidInput
	}
	if e := s.requireWrite(); e != nil {
		return MutationResult{}, e
	}
	var status, expiresAt, errorCode string
	if e := s.db.QueryRowContext(ctx, s.bind("SELECT status,COALESCE(account_expires_at,''),COALESCE(last_error_code,'') FROM "+s.table("accounts")+" WHERE id=? AND deleted_at IS NULL"), in.AccountID).Scan(&status, &expiresAt, &errorCode); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return MutationResult{}, nil
		}
		return MutationResult{}, e
	}
	if len(in.ExpectedLastErrorCodes) > 0 {
		matched := false
		for _, code := range in.ExpectedLastErrorCodes {
			if strings.TrimSpace(code) == errorCode {
				matched = true
				break
			}
		}
		if !matched {
			return MutationResult{SkippedReason: "stale_failure_state"}, nil
		}
	}
	if errorCode == "account_error_policy_cooldown" {
		return MutationResult{SkippedReason: "explicit_policy_restore_required"}, nil
	}
	if status == "pending_test" && !in.AllowPendingTestRestore {
		return MutationResult{}, nil
	}
	if status == "error" && !in.AllowErrorRestore {
		return MutationResult{}, nil
	}
	if expiresAt != "" {
		t, e := time.Parse(time.RFC3339Nano, expiresAt)
		if e != nil {
			return MutationResult{}, fmt.Errorf("invalid account expiry: %w", e)
		}
		if !t.After(s.clock()) {
			set := "status='disabled',schedulable=0,cooldown_until=NULL,last_error_code='account_expired',last_error_message=?,last_error_trace_id=NULL,cooldown_retest_failure_count=0,cooldown_retest_observation_started_at=NULL,cooldown_retest_last_at=NULL,cooldown_retest_last_status_code=NULL,stream_failure_count=0,stream_failure_window_started_at=NULL"
			return s.updateAccount(ctx, in.AccountID, set, []any{"账户套餐已过期，已自动停用"}, "", nil)
		}
	}
	where := " AND status NOT IN ('disabled')"
	whereArgs := []any{}
	if in.ExpectedConfigRevision > 0 {
		where += " AND config_revision=?"
		whereArgs = append(whereArgs, in.ExpectedConfigRevision)
	}
	if status == "pending_test" || status == "error" {
		set := "status='pending_test',schedulable=0,config_revision=config_revision+1,cooldown_until=NULL,last_error_code=NULL,last_error_message='账户已重置，等待后台健康检查',last_error_trace_id=NULL,cooldown_retest_failure_count=0,cooldown_retest_observation_started_at=NULL,cooldown_retest_last_at=NULL,cooldown_retest_last_status_code=NULL,last_health_check_at=NULL,next_health_check_at=NULL,last_health_success_at=NULL,health_check_failure_count=0,health_check_failure_started_at=NULL,last_health_check_status_code=NULL,last_health_check_error_code=NULL,last_health_check_error_message=NULL,last_health_check_trace_id=NULL,stream_failure_count=0,stream_failure_window_started_at=NULL"
		where += " AND status=?"
		whereArgs = append(whereArgs, status)
		return s.updateAccount(ctx, in.AccountID, set, nil, where, whereArgs)
	}
	set := "status='active',schedulable=1,cooldown_until=NULL,last_error_code=NULL,last_error_message=NULL,last_error_trace_id=NULL,cooldown_retest_failure_count=0,cooldown_retest_observation_started_at=NULL,cooldown_retest_last_at=NULL,cooldown_retest_last_status_code=NULL,stream_failure_count=0,stream_failure_window_started_at=NULL"
	where += " AND status IN ('temporary_unavailable','rate_limited')"
	if in.ExpectedCooldownRetestObservationStartedAt != "" {
		where += " AND COALESCE(cooldown_retest_observation_started_at,'')=?"
		whereArgs = append(whereArgs, in.ExpectedCooldownRetestObservationStartedAt)
	}
	return s.updateAccount(ctx, in.AccountID, set, nil, where, whereArgs)
}
func (s *Store) ClearAccountStreamFailureState(ctx context.Context, id string) (MutationResult, error) {
	if id == "" {
		return MutationResult{}, ErrInvalidInput
	}
	set := "stream_failure_count=0,stream_failure_window_started_at=NULL,last_error_code=CASE WHEN status='active' THEN NULL ELSE last_error_code END,last_error_message=CASE WHEN status='active' THEN NULL ELSE last_error_message END,last_error_trace_id=CASE WHEN status='active' THEN NULL ELSE last_error_trace_id END"
	return s.updateAccount(ctx, id, set, nil, " AND status NOT IN ('disabled','error') AND (stream_failure_count<>0 OR stream_failure_window_started_at IS NOT NULL OR (status='active' AND last_error_code IS NOT NULL) OR (status='active' AND last_error_message IS NOT NULL) OR (status='active' AND last_error_trace_id IS NOT NULL))", nil)
}
func (s *Store) RecordAccountStreamFailure(ctx context.Context, in StreamFailureInput) (MutationResult, error) {
	if in.AccountID == "" || in.ThresholdCount < 1 || in.ThresholdWindowMinutes < 1 {
		return MutationResult{}, ErrInvalidInput
	}
	now := s.clock()
	var count int
	var started sql.NullString
	var status string
	if e := s.db.QueryRowContext(ctx, s.bind("SELECT status,stream_failure_count,stream_failure_window_started_at FROM "+s.table("accounts")+" WHERE id=?"), in.AccountID).Scan(&status, &count, &started); e != nil {
		return MutationResult{}, e
	}
	if status == "disabled" || status == "error" {
		return MutationResult{Count: count}, nil
	}
	if !started.Valid {
		count = 0
	} else if t, e := time.Parse(time.RFC3339Nano, started.String); e != nil || now.Sub(t) > time.Duration(in.ThresholdWindowMinutes)*time.Minute {
		count = 0
	}
	count++
	set := "stream_failure_count=?,stream_failure_window_started_at=COALESCE(stream_failure_window_started_at,?),last_error_code='stream_failure',last_error_message=?,last_error_trace_id=?"
	r, e := s.updateAccount(ctx, in.AccountID, set, []any{count, nowString(now), sanitize(in.Reason), sanitize(in.TraceID)}, "", nil)
	if e != nil {
		return MutationResult{}, e
	}
	triggered := count >= in.ThresholdCount
	if triggered && in.Action == "disable" {
		_, e = s.MarkAccountException(ctx, ExceptionInput{AccountID: in.AccountID, ErrorCode: "stream_failure_threshold", Reason: in.Reason, TraceID: in.TraceID})
		if e != nil {
			return MutationResult{}, e
		}
	}
	if triggered && in.Action == "cooldown" {
		_, e = s.MarkAccountTemporaryUnavailable(ctx, TemporaryUnavailableInput{Account: Account{ID: in.AccountID}, Reason: in.Reason, TraceID: in.TraceID})
		if e != nil {
			return MutationResult{}, e
		}
	}
	r.Count = count
	r.Triggered = triggered
	return r, nil
}

func (s *Store) ApplyAccountErrorHandling(ctx context.Context, a Account, in ErrorHandlingInput) (MutationResult, error) {
	if a.ID == "" {
		return MutationResult{}, ErrInvalidInput
	}
	if in.Success {
		set := "last_health_success_at=?,last_health_check_at=?"
		obs := in.ObservedAt
		if obs == "" {
			obs = nowString(s.clock())
		}
		return s.updateAccount(ctx, a.ID, set, []any{obs, obs}, " AND status NOT IN ('disabled','error')", nil)
	}
	if in.PolicyDecision == nil {
		return MutationResult{SkippedReason: "no_explicit_policy"}, nil
	}
	p := in.PolicyDecision
	if p.KeyScoped {
		return MutationResult{SkippedReason: "api_key_key_scoped_quota_recovery"}, nil
	}
	switch p.Action {
	case "retry_next", "retry_next_account", "none":
		return MutationResult{Action: p.Action, SkippedReason: "policy_no_account_mutation"}, nil
	case "disable", "error":
		return s.MarkAccountException(ctx, ExceptionInput{AccountID: a.ID, ErrorCode: p.ErrorCode, Reason: in.ErrorMessage, TraceID: in.TraceID})
	case "cooldown", "temporary_unavailable", "rate_limited":
		status := a.Status
		_ = status
		set := "status='temporary_unavailable',schedulable=1,last_error_code=?,last_error_message=?,last_error_trace_id=?,cooldown_until=?"
		until := p.CooldownUntil
		return s.updateAccount(ctx, a.ID, set, []any{firstNonEmpty(p.ErrorCode, "account_error_policy"), sanitize(in.ErrorMessage), sanitize(in.TraceID), nullable(until)}, " AND status NOT IN ('disabled','error')", nil)
	default:
		return MutationResult{SkippedReason: "unknown_policy_action"}, nil
	}
}

func (s *Store) MarkAccountCooldown(ctx context.Context, account Account, until, reason string, status APIKeyRuntimeStatus, traceID string) (MutationResult, error) {
	if account.ID == "" || status != RuntimeTemporaryUnavailable && status != RuntimeRateLimited {
		return MutationResult{}, ErrInvalidInput
	}
	if status == RuntimeRateLimited && strings.TrimSpace(until) == "" {
		return MutationResult{}, ErrInvalidInput
	}
	if until != "" {
		if _, err := parseTime(until); err != nil {
			return MutationResult{}, err
		}
	}
	if status == RuntimeTemporaryUnavailable {
		return s.MarkAccountTemporaryUnavailable(ctx, TemporaryUnavailableInput{Account: account, Reason: reason, TraceID: traceID})
	}
	set := "status='temporary_unavailable',schedulable=1,cooldown_until=?,last_error_code='rate_limited',last_error_message=?,last_error_trace_id=?,cooldown_retest_observation_started_at=?,cooldown_retest_generation=?,stream_failure_count=0,stream_failure_window_started_at=NULL"
	now := s.clock()
	return s.updateAccount(ctx, account.ID, set, []any{until, sanitize(reason), sanitize(traceID), nowString(now), "account-runtime-" + randomToken()}, " AND status NOT IN ('disabled','error')", nil)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
func (s *Store) SyncAPIKeyAvailabilityScheduleStatuses(ctx context.Context) (MutationResult, error) {
	if s.deps.Schedule == nil {
		return MutationResult{}, ErrOutstandingScheduleEvaluator
	}
	if e := s.requireWrite(); e != nil {
		return MutationResult{}, e
	}
	now := s.clock()
	rows, e := s.db.QueryContext(ctx, s.bind("SELECT id,COALESCE(availability_schedule_json,''),status FROM "+s.table("api_keys")+" WHERE availability_schedule_json IS NOT NULL AND availability_schedule_json<>'' AND (availability_schedule_next_check_at IS NULL OR availability_schedule_next_check_at<=?)"), nowString(now))
	if e != nil {
		return MutationResult{}, e
	}
	defer rows.Close()
	type scheduleRow struct{ id, json, status string }
	var scheduled []scheduleRow
	for rows.Next() {
		var id, j, status string
		if e = rows.Scan(&id, &j, &status); e != nil {
			return MutationResult{}, e
		}
		scheduled = append(scheduled, scheduleRow{id: id, json: j, status: status})
	}
	if e = rows.Err(); e != nil {
		return MutationResult{}, e
	}
	if e = rows.Close(); e != nil {
		return MutationResult{}, e
	}
	n := 0
	for _, row := range scheduled {
		id, j := row.id, row.json
		d, e := s.deps.Schedule.EvaluateSchedule(ctx, j, now)
		if e != nil {
			return MutationResult{}, e
		}
		next := d.NextCheckAt
		if next == "" {
			next = now.Add(time.Minute).Format(time.RFC3339Nano)
		}
		if d.EventKey != "" && d.Status != "" {
			r, e := s.db.ExecContext(ctx, s.bind("INSERT INTO "+s.table("api_key_schedule_status_events")+" (event_key,api_key_id,status,executed_at) VALUES (?,?,?,?) ON CONFLICT(event_key) DO NOTHING"), id+":"+d.EventKey, id, d.Status, nowString(now))
			if e != nil {
				return MutationResult{}, e
			}
			x, _ := r.RowsAffected()
			if x == 0 {
				continue
			}
		}
		if d.Status != "" {
			r, e := s.db.ExecContext(ctx, s.bind("UPDATE "+s.table("api_keys")+" SET status=?,availability_schedule_next_check_at=?,updated_at=? WHERE id=? AND availability_schedule_json IS NOT NULL AND status<>?"), d.Status, next, nowString(now), id, d.Status)
			if e != nil {
				return MutationResult{}, e
			}
			x, _ := r.RowsAffected()
			n += int(x)
		} else {
			r, e := s.db.ExecContext(ctx, s.bind("UPDATE "+s.table("api_keys")+" SET availability_schedule_next_check_at=? WHERE id=? AND availability_schedule_json IS NOT NULL AND COALESCE(availability_schedule_next_check_at,'')<>COALESCE(?, '')"), next, id, next)
			if e != nil {
				return MutationResult{}, e
			}
			x, _ := r.RowsAffected()
			n += int(x)
		}
	}
	return MutationResult{Changed: n > 0, Count: n}, nil
}
