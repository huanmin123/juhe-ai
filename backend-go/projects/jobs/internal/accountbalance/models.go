package accountbalance

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Trigger identifies the three J2 execution paths.  The value is persisted
// with an outcome so a manual result can never be mistaken for a scheduled
// failure sequence.
type Trigger string

const (
	TriggerPeriodic   Trigger = "periodic"
	TriggerFirstProbe Trigger = "first_probe"
	TriggerManual     Trigger = "manual"
)

// CredentialEnvelope is an opaque, jobs-owned encrypted value.  Plaintext
// credentials must not cross the input boundary or be stored in a jobs row.
type CredentialEnvelope struct {
	Kind       string `json:"kind"`
	Ciphertext string `json:"ciphertext"`
}

// Candidate is the immutable account fact set supplied to a runner.  A
// candidate is intentionally not a database row: callers may populate it from
// a read-only business adapter or a test fixture without giving this package
// access to Node-owned storage.
type Candidate struct {
	AccountID       string              `json:"account_id"`
	SystemAccountID string              `json:"system_account_id"`
	InputVersion    int64               `json:"input_version"`
	ConfigRevision  int64               `json:"config_revision"`
	Provider        string              `json:"provider"`
	Type            string              `json:"type"`
	Status          string              `json:"status"`
	Schedulable     bool                `json:"schedulable"`
	Deleted         bool                `json:"deleted"`
	Authorized      bool                `json:"authorized"`
	BaseURL         string              `json:"base_url"`
	Config          QueryConfig         `json:"config"`
	BalanceEnabled  bool                `json:"balance_query_enabled"`
	FirstProbe      bool                `json:"first_probe"`
	Recovery        bool                `json:"-"`
	APIKeyCount     int                 `json:"api_key_count"`
	APIKey          CredentialEnvelope  `json:"api_key"`
	Credential      CredentialEnvelope  `json:"credential,omitempty"`
	Proxy           *CredentialEnvelope `json:"proxy,omitempty"`
	IssuedAt        time.Time           `json:"issued_at"`
	ExpiresAt       time.Time           `json:"expires_at"`
	NextRefreshAt   *time.Time          `json:"next_refresh_at,omitempty"`
}

// Input is a frozen, single-account execution contract.  The runner never
// rereads mutable business facts while using this value; a newer input or
// config revision can therefore fence the result at the jobs Store boundary.
type Input struct {
	AccountID       string              `json:"account_id"`
	SystemAccountID string              `json:"system_account_id"`
	InputVersion    int64               `json:"input_version"`
	ConfigRevision  int64               `json:"config_revision"`
	Provider        string              `json:"provider"`
	Type            string              `json:"type"`
	Status          string              `json:"status"`
	Schedulable     bool                `json:"schedulable"`
	BaseURL         string              `json:"base_url"`
	Config          QueryConfig         `json:"config"`
	APIKey          CredentialEnvelope  `json:"api_key"`
	Credential      CredentialEnvelope  `json:"credential,omitempty"`
	Proxy           *CredentialEnvelope `json:"proxy,omitempty"`
	Trigger         Trigger             `json:"trigger"`
	IssuedAt        time.Time           `json:"issued_at"`
	ExpiresAt       time.Time           `json:"expires_at"`
	NextRefreshAt   *time.Time          `json:"next_refresh_at,omitempty"`
	Recovery        bool                `json:"-"`
}

// ToInput converts a candidate into a bounded input.  It deliberately does
// not choose a key from a pool: callers must provide an already-normalized
// single-key envelope and an explicit APIKeyCount.
func (c Candidate) ToInput(trigger Trigger, now time.Time, ttl time.Duration) (Input, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if ttl <= 0 || ttl > 15*time.Minute {
		return Input{}, errors.New("account-balance input TTL 必须在 1ns..15m")
	}
	if trigger != TriggerPeriodic && trigger != TriggerFirstProbe && trigger != TriggerManual {
		return Input{}, errors.New("account-balance input trigger 无效")
	}
	if strings.TrimSpace(c.AccountID) == "" || strings.TrimSpace(c.SystemAccountID) == "" || c.InputVersion < 1 || c.ConfigRevision < 1 {
		return Input{}, errors.New("account-balance candidate 缺少账户或 revision")
	}
	if c.Deleted || c.Authorized {
		return Input{}, errors.New("account-balance candidate 不可执行")
	}
	if c.Provider == "" || c.Type != "api_key" {
		return Input{}, errors.New("account-balance 仅支持 API Key 账户")
	}
	keyCount := c.APIKeyCount
	if keyCount == 0 && (strings.TrimSpace(c.APIKey.Ciphertext) != "" || strings.TrimSpace(c.Credential.Ciphertext) != "") {
		keyCount = 1
	}
	if keyCount != 1 {
		return Input{}, errors.New("account-balance candidate 必须恰好包含一个 API Key")
	}
	credential := c.APIKey
	if strings.TrimSpace(credential.Ciphertext) == "" {
		credential = c.Credential
	}
	if strings.TrimSpace(credential.Kind) == "" || strings.TrimSpace(credential.Ciphertext) == "" {
		return Input{}, errors.New("account-balance candidate 缺少加密 API Key")
	}
	if c.Config.Adapter == "" {
		if trigger != TriggerFirstProbe {
			return Input{}, errors.New("account-balance candidate 缺少查询配置")
		}
		c.Config = QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5}
	}
	configMap := queryConfigMap(c.Config)
	if c.Config.IntervalMinutes == 0 {
		configMap["intervalMinutes"] = 5
	}
	normalizedConfig, err := NormalizeConfig(configMap)
	if err != nil {
		return Input{}, fmt.Errorf("account-balance candidate 查询配置无效: %w", err)
	}
	c.Config = normalizedConfig
	if strings.TrimSpace(c.BaseURL) == "" {
		return Input{}, errors.New("account-balance candidate 缺少 Base URL")
	}
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return Input{}, errors.New("account-balance candidate Base URL 必须是无用户信息的 HTTP(S) 地址")
	}
	issued := c.IssuedAt.UTC()
	if issued.IsZero() {
		issued = now.UTC()
	}
	expires := c.ExpiresAt.UTC()
	if expires.IsZero() {
		expires = now.UTC().Add(ttl)
	}
	if !expires.After(now.UTC()) {
		return Input{}, errors.New("account-balance candidate 已过期")
	}
	return Input{
		AccountID: c.AccountID, SystemAccountID: c.SystemAccountID, InputVersion: c.InputVersion, ConfigRevision: c.ConfigRevision,
		Provider: c.Provider, Type: c.Type, Status: c.Status, Schedulable: c.Schedulable,
		BaseURL: strings.TrimRight(c.BaseURL, "/"), Config: c.Config, APIKey: credential, Credential: credential,
		Proxy: cloneCredential(c.Proxy), Trigger: trigger, IssuedAt: issued, ExpiresAt: expires, NextRefreshAt: cloneTime(c.NextRefreshAt), Recovery: c.Recovery,
	}, nil
}

func (i Input) Validate(now time.Time) error {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if strings.TrimSpace(i.AccountID) == "" || strings.TrimSpace(i.SystemAccountID) == "" || i.InputVersion < 1 || i.ConfigRevision < 1 {
		return errors.New("account-balance input 的账户或 revision 无效")
	}
	if i.Type != "api_key" || strings.TrimSpace(i.Provider) == "" {
		return errors.New("account-balance input 的 provider/type 不受支持")
	}
	if i.IssuedAt.IsZero() || i.ExpiresAt.IsZero() || !i.ExpiresAt.After(now.UTC()) {
		return errors.New("account-balance input 已过期或缺少时间 fence")
	}
	if i.IssuedAt.After(now.UTC()) || i.ExpiresAt.Sub(i.IssuedAt) > 15*time.Minute {
		return errors.New("account-balance input issued/expires 时间窗无效")
	}
	if i.Trigger != TriggerPeriodic && i.Trigger != TriggerFirstProbe && i.Trigger != TriggerManual {
		return errors.New("account-balance input trigger 无效")
	}
	if (i.Trigger == TriggerPeriodic || i.Trigger == TriggerFirstProbe) && i.NextRefreshAt == nil && !i.Recovery {
		return errors.New("account-balance scheduled input 缺少 next_refresh_at fence")
	}
	credential := i.APIKey
	if strings.TrimSpace(credential.Ciphertext) == "" {
		credential = i.Credential
	}
	if strings.TrimSpace(credential.Kind) == "" || strings.TrimSpace(credential.Ciphertext) == "" {
		return errors.New("account-balance input 缺少加密 API Key")
	}
	configMap := queryConfigMap(i.Config)
	if i.Config.IntervalMinutes == 0 {
		configMap["intervalMinutes"] = 5
	}
	if _, err := NormalizeConfig(configMap); err != nil {
		return fmt.Errorf("account-balance input 查询配置无效: %w", err)
	}
	parsed, err := url.Parse(i.BaseURL)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("account-balance input Base URL 无效")
	}
	return nil
}

func queryConfigMap(config QueryConfig) map[string]any {
	result := map[string]any{"adapter": string(config.Adapter), "intervalMinutes": config.IntervalMinutes}
	if config.PreferredBuiltinAdapter != "" {
		result["preferredBuiltinAdapter"] = string(config.PreferredBuiltinAdapter)
	}
	if config.Custom != nil {
		custom := map[string]any{"path": config.Custom.Path}
		if config.Custom.RemainingPointer != "" {
			custom["remainingPointer"] = config.Custom.RemainingPointer
		}
		if config.Custom.TotalPointer != "" {
			custom["totalPointer"] = config.Custom.TotalPointer
		}
		if config.Custom.UsedPointer != "" {
			custom["usedPointer"] = config.Custom.UsedPointer
		}
		if config.Custom.Divisor != "" {
			custom["divisor"] = config.Custom.Divisor
		}
		result["custom"] = custom
	}
	return result
}

func cloneCredential(value *CredentialEnvelope) *CredentialEnvelope {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

// Outcome is the jobs-owned terminal record.  Snapshot is copied into the
// same transaction by Store.AppendOutcome, making request IDs idempotent.
type Outcome struct {
	OutcomeID             string     `json:"outcome_id"`
	RequestID             string     `json:"request_id"`
	AccountID             string     `json:"account_id"`
	SystemAccountID       string     `json:"system_account_id"`
	InputVersion          int64      `json:"input_version"`
	ConfigRevision        int64      `json:"config_revision"`
	Trigger               Trigger    `json:"trigger"`
	ObservedAt            time.Time  `json:"observed_at"`
	Snapshot              Snapshot   `json:"snapshot"`
	Adapter               Adapter    `json:"adapter,omitempty"`
	NextRefreshAt         *time.Time `json:"next_refresh_at,omitempty"`
	ExpectedInput         int64      `json:"expected_input,omitempty"`
	ExpectedConfig        int64      `json:"expected_config,omitempty"`
	ExpectedNextRefreshAt *time.Time `json:"-"`
	// ExpectedNextRefreshSet distinguishes a frozen null due value (periodic
	// recovery) from no business due fence at all (manual refresh).
	ExpectedNextRefreshSet bool `json:"-"`
	// ExpectedSnapshot* is an internal jobs-store CAS fence. It must never be
	// serialized into the outcome: Node uses expected_* as frozen business
	// input facts, while these values describe only an earlier jobs snapshot.
	ExpectedSnapshotInput         int64      `json:"-"`
	ExpectedSnapshotConfig        int64      `json:"-"`
	ExpectedSnapshotNextRefreshAt *time.Time `json:"-"`
	ErrorCode                     string     `json:"error_code,omitempty"`
	ErrorMessage                  string     `json:"error_message,omitempty"`
}

type outcomeJSON Outcome

func (o Outcome) MarshalJSON() ([]byte, error) {
	encoded, err := json.Marshal(outcomeJSON(o))
	if err != nil || !o.ExpectedNextRefreshSet {
		return encoded, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	if o.ExpectedNextRefreshAt == nil {
		fields["expected_next_refresh_at"] = json.RawMessage("null")
	} else {
		value, err := json.Marshal(o.ExpectedNextRefreshAt.UTC())
		if err != nil {
			return nil, err
		}
		fields["expected_next_refresh_at"] = value
	}
	return json.Marshal(fields)
}

func (o *Outcome) UnmarshalJSON(data []byte) error {
	var decoded outcomeJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*o = Outcome(decoded)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	value, ok := fields["expected_next_refresh_at"]
	if !ok {
		return nil
	}
	o.ExpectedNextRefreshSet = true
	if string(value) == "null" {
		return nil
	}
	var expected time.Time
	if err := json.Unmarshal(value, &expected); err != nil {
		return err
	}
	o.ExpectedNextRefreshAt = &expected
	return nil
}

type SnapshotRecord struct {
	AccountID      string
	InputVersion   int64
	ConfigRevision int64
	Trigger        Trigger
	Snapshot       Snapshot
	NextRefreshAt  *time.Time
	UpdatedAt      time.Time
}

type SnapshotMutation struct {
	Input          Input
	Snapshot       Snapshot
	NextRefreshAt  *time.Time
	ExpectedInput  int64
	ExpectedConfig int64
}

type RunReport struct {
	Trigger  Trigger
	Seen     int
	Executed int
	Skipped  int
	Stale    int
	Errors   map[string]error
}

func (r *RunReport) addError(accountID string, err error) {
	if r.Errors == nil {
		r.Errors = make(map[string]error)
	}
	r.Errors[accountID] = err
}
