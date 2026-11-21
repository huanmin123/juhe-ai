// Package authzrevision computes the material-change fingerprint of an AI
// account used by the authorization governance model.
//
// The fingerprint is deliberately computed from a database snapshot, not from
// the runtime selection row: every enabled connection, endpoint mode, proxy
// pool member and model mapping participates. Gateway and maintenance share
// this package so the migration backfill and the runtime accept/reconfirm CAS
// compare the same value.
package authzrevision

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accountcrypto"
)

// Version is the fingerprint algorithm version. Increment it when the
// canonical input changes; maintenance must then mark all non-terminal
// sources needs_review.
const Version = 1

// Queryer is the database subset required by Compute.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Snapshot is the canonical material-change input. Exporting it keeps the
// migration/verification tooling able to report field-level differences.
type Snapshot struct {
	AccountID             string         `json:"accountId"`
	VendorCode            string         `json:"vendorCode"`
	ConnectionDriverCode  string         `json:"connectionDriverCode"`
	ProtocolCode          string         `json:"protocolCode"`
	ProtocolVersion       string         `json:"protocolVersion"`
	AccountType           string         `json:"accountType"`
	CapabilityDomain      string         `json:"capabilityDomain"`
	OAuthType string `json:"oauthType"`
	// APIKeyPoolFingerprint and OAuthSubjectFingerprint are derived inside
	// Load from the decrypted credential payload. They are HMACs (never the
	// material itself). The accounts.credential_fingerprint column is
	// deliberately not part of the canonical input: for OAuth accounts it
	// hashes the rotating refresh token, and design §4.5 says token rotation
	// with an unchanged subject is not a material change.
	APIKeyPoolFingerprint   string         `json:"apiKeyPoolFingerprint,omitempty"`
	OAuthSubjectFingerprint string         `json:"oauthSubjectFingerprint,omitempty"`
	ProxyProfile            *ProxyProfile  `json:"proxyProfile,omitempty"`
	ProxyPool               *ProxyPool     `json:"proxyPool,omitempty"`
	Connections             []Connection   `json:"connections"`
	SupportedModels         []string       `json:"supportedModels"`
	EntryMappings           []EntryMapping `json:"entryMappings"`
	SchemaVersion           int            `json:"schemaVersion"`
}

// Connection is one enabled account_connections row and its endpoint modes.
type Connection struct {
	ConnectionPresetCode string   `json:"connectionPresetCode"`
	ProtocolSurfaceCode  string   `json:"protocolSurfaceCode"`
	BaseURL              string   `json:"baseUrl"`
	EndpointModes        []string `json:"endpointModes"`
}

// ProxyProfile is the direct proxy binding summary (no credentials).
type ProxyProfile struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Enabled  bool   `json:"enabled"`
}

// ProxyPool is the effective pool shape referenced by the account.
type ProxyPool struct {
	ID       string        `json:"id"`
	Strategy string        `json:"strategy"`
	Enabled  bool          `json:"enabled"`
	Members  []ProxyMember `json:"members"`
}

// ProxyMember is one pool member with its routing-relevant fields.
type ProxyMember struct {
	Profile  ProxyProfile `json:"profile"`
	Priority int          `json:"priority"`
	Weight   int          `json:"weight"`
	Enabled  bool         `json:"enabled"`
}

// EntryMapping is one account_entry_mappings row (including disabled rows:
// enabling one is a material change too).
type EntryMapping struct {
	SourceModel          string `json:"sourceModel"`
	SourceEndpointFamily string `json:"sourceEndpointFamily"`
	TargetConnectionID   string `json:"targetConnectionId"`
	TargetModelID        string `json:"targetModelId"`
	TargetEndpointFamily string `json:"targetEndpointFamily"`
	Enabled              bool   `json:"enabled"`
}

// Load reads the canonical snapshot for accountID. A missing account returns
// sql.ErrNoRows (callers decide whether that is a terminal resource deletion).
// secret is the runtime credential/revision key: the OAuth subject and the
// full API-key pool are only available after opening the AES envelope, and an
// unreadable envelope fails closed instead of silently skipping material.
func Load(ctx context.Context, db Queryer, accountID, secret string) (Snapshot, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return Snapshot{}, errors.New("authz revision secret 不能为空")
	}
	var snap Snapshot
	snap.AccountID = accountID
	snap.SchemaVersion = Version
	var proxyProfileID, proxyPoolID sql.NullString
	var credentialsEncrypted string
	err := db.QueryRowContext(ctx, `SELECT
		COALESCE(vendor_code, ''), COALESCE(connection_driver_code, ''),
		COALESCE(protocol_code, ''), COALESCE(protocol_version, ''),
		COALESCE(type, ''), COALESCE(capability_domain, 'chat'),
		COALESCE(oauth_type, ''),
		credentials_encrypted, proxy_profile_id, proxy_pool_id
		FROM juhe_business.accounts WHERE id = $1`, accountID).Scan(
		&snap.VendorCode, &snap.ConnectionDriverCode,
		&snap.ProtocolCode, &snap.ProtocolVersion,
		&snap.AccountType, &snap.CapabilityDomain,
		&snap.OAuthType,
		&credentialsEncrypted, &proxyProfileID, &proxyPoolID)
	if err != nil {
		return Snapshot{}, err
	}
	credentials := map[string]any{}
	if err := accountcrypto.DecryptJSON(secret, credentialsEncrypted, &credentials); err != nil {
		return Snapshot{}, fmt.Errorf("解密账户 %s 凭据失败: %w", accountID, err)
	}
	snap.APIKeyPoolFingerprint, snap.OAuthSubjectFingerprint = credentialMaterialFingerprints(secret, credentials)

	if proxyProfileID.Valid && strings.TrimSpace(proxyProfileID.String) != "" {
		profile, err := loadProxyProfile(ctx, db, proxyProfileID.String)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Snapshot{}, err
		}
		if err == nil {
			snap.ProxyProfile = &profile
		}
	}
	if proxyPoolID.Valid && strings.TrimSpace(proxyPoolID.String) != "" {
		pool, err := loadProxyPool(ctx, db, proxyPoolID.String)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Snapshot{}, err
		}
		if err == nil {
			snap.ProxyPool = &pool
		}
	}

	connections, err := loadConnections(ctx, db, accountID)
	if err != nil {
		return Snapshot{}, err
	}
	snap.Connections = connections

	models, err := loadStringColumn(ctx, db, `SELECT model_id FROM juhe_business.account_supported_models WHERE account_id = $1 ORDER BY model_id`, accountID)
	if err != nil {
		return Snapshot{}, err
	}
	snap.SupportedModels = models

	mappings, err := loadMappings(ctx, db, accountID)
	if err != nil {
		return Snapshot{}, err
	}
	snap.EntryMappings = mappings
	return snap, nil
}

func loadProxyProfile(ctx context.Context, db Queryer, id string) (ProxyProfile, error) {
	var profile ProxyProfile
	err := db.QueryRowContext(ctx, `SELECT id, type, host, port, COALESCE(username, ''), enabled
		FROM juhe_business.proxy_profiles WHERE id = $1`, id).Scan(
		&profile.ID, &profile.Type, &profile.Host, &profile.Port, &profile.Username, &profile.Enabled)
	return profile, err
}

func loadProxyPool(ctx context.Context, db Queryer, id string) (ProxyPool, error) {
	var pool ProxyPool
	err := db.QueryRowContext(ctx, `SELECT id, strategy, enabled FROM juhe_business.proxy_pools WHERE id = $1`, id).
		Scan(&pool.ID, &pool.Strategy, &pool.Enabled)
	if err != nil {
		return ProxyPool{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT p.id, p.type, p.host, p.port, COALESCE(p.username, ''), p.enabled,
			m.priority, m.weight, m.enabled
		FROM juhe_business.proxy_pool_members m
		JOIN juhe_business.proxy_profiles p ON p.id = m.proxy_profile_id
		WHERE m.pool_id = $1
		ORDER BY p.id`, id)
	if err != nil {
		return ProxyPool{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var member ProxyMember
		if err := rows.Scan(&member.Profile.ID, &member.Profile.Type, &member.Profile.Host, &member.Profile.Port,
			&member.Profile.Username, &member.Profile.Enabled, &member.Priority, &member.Weight, &member.Enabled); err != nil {
			return ProxyPool{}, err
		}
		pool.Members = append(pool.Members, member)
	}
	if err := rows.Err(); err != nil {
		return ProxyPool{}, err
	}
	return pool, nil
}

func loadConnections(ctx context.Context, db Queryer, accountID string) ([]Connection, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.id, c.connection_preset_code,
			COALESCE(c.protocol_surface_code, ''), c.base_url,
			COALESCE(array_agg(m.endpoint_mode ORDER BY m.endpoint_mode) FILTER (WHERE m.endpoint_mode IS NOT NULL), '{}')
		FROM juhe_business.account_connections c
		LEFT JOIN juhe_business.account_endpoint_modes m ON m.connection_id = c.id
		WHERE c.account_id = $1 AND c.enabled = 1
		GROUP BY c.id, c.connection_preset_code, c.protocol_surface_code, c.base_url
		ORDER BY c.connection_preset_code, c.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		var connectionID string
		var item Connection
		var modes []string
		if err := rows.Scan(&connectionID, &item.ConnectionPresetCode, &item.ProtocolSurfaceCode, &item.BaseURL, &modes); err != nil {
			return nil, err
		}
		item.BaseURL = NormalizeBaseURL(item.BaseURL)
		item.EndpointModes = append([]string(nil), modes...)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func loadMappings(ctx context.Context, db Queryer, accountID string) ([]EntryMapping, error) {
	rows, err := db.QueryContext(ctx, `SELECT source_model, source_endpoint_family,
			target_connection_id, target_model_id, target_endpoint_family, enabled
		FROM juhe_business.account_entry_mappings
		WHERE account_id = $1
		ORDER BY source_endpoint_family, source_model, target_connection_id, target_model_id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EntryMapping{}
	for rows.Next() {
		var mapping EntryMapping
		if err := rows.Scan(&mapping.SourceModel, &mapping.SourceEndpointFamily, &mapping.TargetConnectionID,
			&mapping.TargetModelID, &mapping.TargetEndpointFamily, &mapping.Enabled); err != nil {
			return nil, err
		}
		out = append(out, mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func loadStringColumn(ctx context.Context, db Queryer, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// credentialMaterialFingerprints derives the two credential-identity HMACs
// from the decrypted payload:
//
//   - API-key pool: the full effective key list in stored order, not only the
//     first key that accounts.credential_fingerprint covers. Replacing,
//     adding, removing or reordering a pool member is a material change.
//   - OAuth subject: sub / account_id / chatgpt_account_id / organization_id /
//     email. Token rotation with an unchanged subject leaves it stable, per
//     design §4.5.
//
// Both values are HMAC-SHA256 with the runtime secret; raw material never
// reaches the snapshot, stored revisions or logs.
func credentialMaterialFingerprints(secret string, credentials map[string]any) (pool, subject string) {
	trimmed := func(value any) string {
		text, ok := value.(string)
		if !ok {
			return ""
		}
		return strings.TrimSpace(text)
	}
	keys := []string{}
	if list, ok := credentials["api_keys"].([]any); ok {
		for _, item := range list {
			if value := trimmed(item); value != "" {
				keys = append(keys, value)
			}
		}
	} else if list, ok := credentials["api_keys"].([]string); ok {
		for _, item := range list {
			if value := strings.TrimSpace(item); value != "" {
				keys = append(keys, value)
			}
		}
	}
	if len(keys) == 0 {
		if value := trimmed(credentials["api_key"]); value != "" {
			keys = append(keys, value)
		}
	}
	if len(keys) > 0 {
		pool = credentialIdentityHMAC(secret, "api_key_pool\x00"+strings.Join(keys, "\x1f"))
	}
	subjectParts := []string{}
	for _, field := range []string{"sub", "account_id", "chatgpt_account_id", "organization_id", "email"} {
		if value := trimmed(credentials[field]); value != "" {
			subjectParts = append(subjectParts, field+"="+value)
		}
	}
	if len(subjectParts) > 0 {
		subject = credentialIdentityHMAC(secret, "oauth_subject\x00"+strings.Join(subjectParts, "\x1f"))
	}
	return pool, subject
}

func credentialIdentityHMAC(secret, canonical string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

// CanonicalJSON returns the deterministic JSON input to the HMAC.
func (s Snapshot) CanonicalJSON() ([]byte, error) {
	// Defensive normalization: callers may construct a Snapshot directly
	// (tests, migration diffs). Keep every slice non-nil so an empty list is
	// encoded as [] rather than null.
	if s.Connections == nil {
		s.Connections = []Connection{}
	}
	if s.SupportedModels == nil {
		s.SupportedModels = []string{}
	}
	if s.EntryMappings == nil {
		s.EntryMappings = []EntryMapping{}
	}
	for i := range s.Connections {
		s.Connections[i].BaseURL = NormalizeBaseURL(s.Connections[i].BaseURL)
		sort.Strings(s.Connections[i].EndpointModes)
	}
	sort.Slice(s.Connections, func(i, j int) bool {
		left, right := s.Connections[i], s.Connections[j]
		if left.ConnectionPresetCode != right.ConnectionPresetCode {
			return left.ConnectionPresetCode < right.ConnectionPresetCode
		}
		if left.ProtocolSurfaceCode != right.ProtocolSurfaceCode {
			return left.ProtocolSurfaceCode < right.ProtocolSurfaceCode
		}
		return left.BaseURL < right.BaseURL
	})
	sort.Strings(s.SupportedModels)
	if s.ProxyPool != nil {
		sort.Slice(s.ProxyPool.Members, func(i, j int) bool {
			return s.ProxyPool.Members[i].Profile.ID < s.ProxyPool.Members[j].Profile.ID
		})
	}
	return json.Marshal(s)
}

// Hash returns the hex HMAC-SHA256 of the canonical snapshot. An empty secret
// is rejected: production wiring must pass the persistent runtime secret.
func (s Snapshot) Hash(secret string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("authz revision secret 不能为空")
	}
	canonical, err := s.CanonicalJSON()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// Compute loads and hashes in one call.
// Group computes the material fingerprint of a group resource. Group
// membership/material is reviewed account-by-account through the snapshot, so
// the group-level fingerprint only binds the group identity and owner.
func Group(secret, groupID, ownerID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("group\x00" + groupID + "\x00" + ownerID))
	return hex.EncodeToString(mac.Sum(nil))
}

func Compute(ctx context.Context, db Queryer, accountID, secret string) (string, int, Snapshot, error) {
	snapshot, err := Load(ctx, db, accountID, secret)
	if err != nil {
		return "", Version, Snapshot{}, err
	}
	revision, err := snapshot.Hash(secret)
	if err != nil {
		return "", Version, Snapshot{}, err
	}
	return revision, Version, snapshot, nil
}

// NormalizeBaseURL produces the stable base_url form used by the fingerprint:
// scheme and host lowercased, default ports removed, query parameters sorted,
// fragment and userinfo dropped. Invalid URLs are trimmed and otherwise kept
// verbatim so a malformed stored URL still participates in change detection.
func NormalizeBaseURL(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	parsed, err := url.Parse(text)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return text
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	hostPort := host
	if port != "" {
		hostPort = fmt.Sprintf("%s:%s", host, port)
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	query := parsed.Query().Encode()
	if query != "" {
		query = "?" + query
	}
	return scheme + "://" + hostPort + path + query
}
