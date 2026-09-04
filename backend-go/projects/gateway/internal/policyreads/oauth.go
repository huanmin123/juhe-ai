// oauth.go owns the M16c domain: the oauth-management admin reads (and the
// matching management writes) ported from backend/src/modules/oidc-provider
// /oidc-provider.routes.ts oauthManagementRouter plus the OAuth client
// functions of oidc-provider.repository.ts. Node mounts the router behind
// requireAdmin on `${systemApiPrefix}/oauth`; the public protocol surface
// (/oauth/authorize, /oauth/token, ...) is out of scope for this slice.
package policyreads

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/apikeys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
)

const oauthPrefix = "/__aisys__/api/oauth"

// OAuthClient mirrors the OAuthClient projection with the secret hash omitted
// (Node spreads `clientSecretHash: undefined` on every management response).
type OAuthClient struct {
	ID               string   `json:"id"`
	ClientID         string   `json:"clientId"`
	DisplayName      string   `json:"displayName"`
	ClientType       string   `json:"clientType"`
	ClientSecretHash *string  `json:"clientSecretHash,omitempty"`
	RedirectUris     []string `json:"redirectUris"`
	AllowedScopes    []string `json:"allowedScopes"`
	Status           string   `json:"status"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
}

// OAuthClientWithSecret mirrors `{...client, clientSecret}` for create and
// reissue responses; the key is omitted for public clients.
type OAuthClientWithSecret struct {
	OAuthClient
	ClientSecret *string `json:"clientSecret,omitempty"`
}

func (c *OAuthClientWithSecret) scrub() *OAuthClientWithSecret {
	c.ClientSecretHash = nil
	return c
}

// oauthIntegrationPackage mirrors the integration-package response body.
type oauthIntegrationPackage struct {
	Client       OAuthClient `json:"client"`
	ClientSecret *string     `json:"clientSecret,omitempty"`
}

// oauthIntegrationInfo mirrors the integration-info response body.
type oauthIntegrationInfo struct {
	Issuer                      string `json:"issuer"`
	DiscoveryURL                string `json:"discoveryUrl"`
	JwksURL                     string `json:"jwksUrl"`
	AuthorizationEndpoint       string `json:"authorizationEndpoint"`
	TokenEndpoint               string `json:"tokenEndpoint"`
	UserinfoEndpoint            string `json:"userinfoEndpoint"`
	DeviceAuthorizationEndpoint string `json:"deviceAuthorizationEndpoint"`
	RevocationEndpoint          string `json:"revocationEndpoint"`
	TokenRenewalEndpoint        string `json:"tokenRenewalEndpoint"`
	IDTokenSigningAlgorithm     string `json:"idTokenSigningAlgorithm"`
}

// OAuthStore is the dual-mode oauth_clients persistence used by the
// management surface.
type OAuthStore struct {
	baseStore
	// KeyEncryptionSecret mirrors runtimeConfig.oidc.keyEncryptionSecret (the
	// encryptOidcValue/decryptOidcValue AES-GCM key material).
	KeyEncryptionSecret string
}

// NewOAuthStore builds the OAuth management store.
func NewOAuthStore(db *sql.DB, postgres bool, now func() time.Time, newID func(string) string, inval RuntimeInvalidator, keyEncryptionSecret string) (*OAuthStore, error) {
	base, err := newBaseStore(db, postgres, now, newID, inval)
	if err != nil {
		return nil, err
	}
	return &OAuthStore{baseStore: base, KeyEncryptionSecret: keyEncryptionSecret}, nil
}

const oauthClientColumns = `id, client_id, display_name, client_type, client_secret_hash,
	redirect_uris_json, allowed_scopes_json, status, created_at, updated_at`

type oauthClientRow struct {
	id               string
	clientID         string
	displayName      string
	clientType       string
	clientSecretHash sql.NullString
	redirectUrisJSON string
	allowedScopes    string
	status           string
	createdAt        string
	updatedAt        string
}

func scanOAuthClientRow(scan func(...any) error) (oauthClientRow, error) {
	var row oauthClientRow
	err := scan(&row.id, &row.clientID, &row.displayName, &row.clientType, &row.clientSecretHash,
		&row.redirectUrisJSON, &row.allowedScopes, &row.status, &row.createdAt, &row.updatedAt)
	return row, err
}

func oauthClientFromRow(row oauthClientRow) OAuthClient {
	return OAuthClient{
		ID: row.id, ClientID: row.clientID, DisplayName: row.displayName, ClientType: row.clientType,
		ClientSecretHash: nullPtrString(row.clientSecretHash),
		RedirectUris:     decodeStringArray(row.redirectUrisJSON),
		AllowedScopes:    decodeStringArray(row.allowedScopes),
		Status:           row.status, CreatedAt: row.createdAt, UpdatedAt: row.updatedAt,
	}
}

func decodeStringArray(value string) []string {
	parsed := []string{}
	_ = json.Unmarshal([]byte(value), &parsed)
	if parsed == nil {
		return []string{}
	}
	return parsed
}

// ListClients mirrors listOAuthClients (ORDER BY created_at DESC, id DESC).
func (s *OAuthStore) ListClients(ctx context.Context) ([]OAuthClient, error) {
	ctx = ensureCtx(ctx)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+oauthClientColumns+`
		FROM `+s.table("oauth_clients")+`
		ORDER BY created_at DESC, id DESC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := []OAuthClient{}
	for rows.Next() {
		row, scanErr := scanOAuthClientRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		client := oauthClientFromRow(row)
		client.ClientSecretHash = nil
		clients = append(clients, client)
	}
	return clients, rows.Err()
}

// FindClient mirrors findOAuthClient; nil when missing.
func (s *OAuthStore) FindClient(ctx context.Context, clientID string) (*OAuthClient, error) {
	ctx = ensureCtx(ctx)
	row, err := scanOAuthClientRow(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+oauthClientColumns+`
			FROM `+s.table("oauth_clients")+` WHERE client_id = ?`), clientID).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	client := oauthClientFromRow(row)
	client.ClientSecretHash = nil
	return &client, nil
}

// findClientRowForSecret loads the raw row including hash and ciphertext.
func (s *OAuthStore) findClientRowForSecret(ctx context.Context, clientID string) (*oauthClientRow, error) {
	ctx = ensureCtx(ctx)
	row, err := scanOAuthClientRow(func(targets ...any) error {
		return s.db.QueryRowContext(ctx, s.bind(`SELECT `+oauthClientColumns+`
			FROM `+s.table("oauth_clients")+` WHERE client_id = ?`), clientID).Scan(targets...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindClientSecret mirrors findOAuthClientSecret: undefined for public
// clients or missing ciphertext, OidcCiphertextError on decrypt failures.
func (s *OAuthStore) FindClientSecret(ctx context.Context, clientID string) (*string, error) {
	ctx = ensureCtx(ctx)
	var clientType string
	var ciphertext sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT client_type, client_secret_ciphertext
		FROM `+s.table("oauth_clients")+` WHERE client_id = ?`), clientID).Scan(&clientType, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if clientType != "confidential" || !ciphertext.Valid {
		return nil, nil
	}
	var payload struct {
		ClientSecret string `json:"clientSecret"`
	}
	if err := decryptOidcValue(s.KeyEncryptionSecret, ciphertext.String, &payload); err != nil {
		return nil, err
	}
	if payload.ClientSecret == "" {
		return nil, nil
	}
	return &payload.ClientSecret, nil
}

// OAuthClientCreateInput is the zod-validated POST /clients payload.
type OAuthClientCreateInput struct {
	DisplayName   string
	ClientType    string
	RedirectUris  []string
	AllowedScopes []string
}

// CreateClient mirrors createOAuthClient.
func (s *OAuthStore) CreateClient(ctx context.Context, input OAuthClientCreateInput) (*OAuthClientWithSecret, error) {
	ctx = ensureCtx(ctx)
	now := s.nowISO()
	clientID := "juhe_" + randomBase64URLBytes(18)
	var clientSecret string
	var secretHash, secretCiphertext any
	if input.ClientType == "confidential" {
		clientSecret = "jcs_" + randomBase64URLBytes(32)
		secretHash = apikeys.HashSecret(clientSecret)
		ciphertext, err := encryptOidcValue(s.KeyEncryptionSecret, map[string]string{"clientSecret": clientSecret})
		if err != nil {
			return nil, err
		}
		secretCiphertext = ciphertext
	}
	client := OAuthClient{
		ID: newUUIDv4(), ClientID: clientID, DisplayName: input.DisplayName, ClientType: input.ClientType,
		RedirectUris: input.RedirectUris, AllowedScopes: input.AllowedScopes, Status: "active",
		CreatedAt: now, UpdatedAt: now,
	}
	redirectUrisJSON, err := json.Marshal(client.RedirectUris)
	if err != nil {
		return nil, err
	}
	allowedScopesJSON, err := json.Marshal(client.AllowedScopes)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("oauth_clients")+`
		(id, client_id, display_name, client_type, client_secret_hash, client_secret_ciphertext,
		 redirect_uris_json, allowed_scopes_json, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		client.ID, client.ClientID, client.DisplayName, client.ClientType, secretHash, secretCiphertext,
		string(redirectUrisJSON), string(allowedScopesJSON), client.Status, client.CreatedAt, client.UpdatedAt); err != nil {
		return nil, err
	}
	response := &OAuthClientWithSecret{OAuthClient: client}
	response.scrub()
	if clientSecret != "" {
		response.ClientSecret = &clientSecret
	}
	return response, nil
}

// UpdateClientStatus mirrors updateOAuthClientStatus; nil when missing.
func (s *OAuthStore) UpdateClientStatus(ctx context.Context, clientID, status string) (*OAuthClient, error) {
	ctx = ensureCtx(ctx)
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_clients")+`
		SET status = ?, updated_at = ? WHERE client_id = ?`), status, s.nowISO(), clientID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	return s.FindClient(ctx, clientID)
}

// ReissueClientSecret mirrors reissueOAuthClientSecret; nil when missing.
func (s *OAuthStore) ReissueClientSecret(ctx context.Context, clientID string) (*OAuthClientWithSecret, error) {
	ctx = ensureCtx(ctx)
	row, err := s.findClientRowForSecret(ctx, clientID)
	if err != nil {
		return nil, err
	}
	if row == nil || row.clientType != "confidential" {
		return nil, nil
	}
	clientSecret := "jcs_" + randomBase64URLBytes(32)
	ciphertext, err := encryptOidcValue(s.KeyEncryptionSecret, map[string]string{"clientSecret": clientSecret})
	if err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("oauth_clients")+`
		SET client_secret_hash = ?, client_secret_ciphertext = ?, updated_at = ?
		WHERE client_id = ? AND client_type = 'confidential'`),
		apikeys.HashSecret(clientSecret), ciphertext, s.nowISO(), clientID)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	client, err := s.FindClient(ctx, clientID)
	if err != nil || client == nil {
		return nil, err
	}
	response := &OAuthClientWithSecret{OAuthClient: *client, ClientSecret: &clientSecret}
	response.scrub()
	return response, nil
}

// ---------------------------------------------------------------------------
// OIDC value encryption (oidc-provider.crypto.ts encryptOidcValue /
// decryptOidcValue): AES-256-GCM keyed by sha256(oidc.keyEncryptionSecret),
// sealed as "iv.tag.ciphertext" with raw base64url parts and no version tag.
// ---------------------------------------------------------------------------

func encryptOidcValue(secret string, value any) (string, error) {
	plain, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	gcm, err := oidcGCM(secret)
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, iv, plain, nil)
	ciphertext, tag := sealed[:len(sealed)-gcm.Overhead()], sealed[len(sealed)-gcm.Overhead():]
	encode := base64RawURL
	return encode(iv) + "." + encode(tag) + "." + encode(ciphertext), nil
}

func decryptOidcValue(secret, envelope string, target any) error {
	parts := strings.Split(strings.TrimSpace(envelope), ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return &OidcCiphertextError{Message: "OIDC 密文格式无效"}
	}
	decode := func(value string) ([]byte, error) {
		raw, err := base64RawURLDecode(value)
		if err != nil {
			return nil, &OidcCiphertextError{Message: "OIDC 密文格式无效"}
		}
		return raw, nil
	}
	iv, err := decode(parts[0])
	if err != nil {
		return err
	}
	tag, err := decode(parts[1])
	if err != nil {
		return err
	}
	ciphertext, err := decode(parts[2])
	if err != nil {
		return err
	}
	gcm, err := oidcGCM(secret)
	if err != nil {
		return err
	}
	plain, err := gcm.Open(nil, iv, append(ciphertext, tag...), nil)
	if err != nil {
		return &OidcCiphertextError{Message: "OIDC 密文无法读取"}
	}
	return json.Unmarshal(plain, target)
}

func oidcGCM(secret string) (cipher.AEAD, error) {
	if secret == "" {
		return nil, &OidcCiphertextError{Message: "OIDC 事务加密密钥未配置"}
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func base64RawURLDecode(value string) ([]byte, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	index := map[byte]int{}
	for i := 0; i < len(alphabet); i++ {
		index[alphabet[i]] = i
	}
	var out []byte
	var buffer, bits int
	for i := 0; i < len(value); i++ {
		digit, ok := index[value[i]]
		if !ok {
			return nil, errors.New("invalid base64url")
		}
		buffer = buffer<<6 | digit
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(buffer>>bits))
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// M16c route family (mounted behind requireAdmin).
// ---------------------------------------------------------------------------

// OAuthDeps bundles the M16c collaborators plus the OIDC runtime flags Node
// reads from runtimeConfig.oidc.
type OAuthDeps struct {
	Store *OAuthStore
	Auth  *authsys.Deps
	// OIDCEnabled mirrors runtimeConfig.oidc.enabled.
	OIDCEnabled bool
	// OIDCIssuer mirrors runtimeConfig.oidc.issuer.
	OIDCIssuer string
}

// Mount wires the oauth-management route family.
func (d *OAuthDeps) Mount(k *kernel.Kernel) {
	k.Register("GET "+oauthPrefix+"/clients", d.Auth.RequireAdmin(http.HandlerFunc(d.listClients)))
	k.Register("GET "+oauthPrefix+"/clients/{clientId}/integration-package", d.Auth.RequireAdmin(http.HandlerFunc(d.integrationPackage)))
	k.Register("GET "+oauthPrefix+"/integration-info", d.Auth.RequireAdmin(http.HandlerFunc(d.integrationInfo)))
	k.Register("POST "+oauthPrefix+"/clients", d.Auth.RequireAdmin(http.HandlerFunc(d.createClient)))
	k.Register("PATCH "+oauthPrefix+"/clients/{clientId}", d.Auth.RequireAdmin(http.HandlerFunc(d.patchClient)))
	k.Register("POST "+oauthPrefix+"/clients/{clientId}/secret/reissue", d.Auth.RequireAdmin(http.HandlerFunc(d.reissueSecret)))
}

func (d *OAuthDeps) listClients(w http.ResponseWriter, r *http.Request) {
	clients, err := d.Store.ListClients(r.Context())
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	kernel.WriteOK(w, clients, "")
}

func (d *OAuthDeps) integrationPackage(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled || d.OIDCIssuer == "" {
		kernel.WriteError(w, http.StatusConflict, "OIDC Provider 未启用，不能下载对接文档")
		return
	}
	clientID := r.PathValue("clientId")
	client, err := d.Store.FindClient(r.Context(), clientID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if client == nil {
		kernel.WriteError(w, http.StatusNotFound, "Client 不存在")
		return
	}
	var clientSecret *string
	if client.ClientType == "confidential" {
		secret, err := d.Store.FindClientSecret(r.Context(), clientID)
		if err != nil {
			var ciphertext *OidcCiphertextError
			if errors.As(err, &ciphertext) {
				kernel.WriteError(w, http.StatusConflict, "该 Client 的当前 Client Secret 无法读取，请重新签发后再下载对接文档")
				return
			}
			kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
			return
		}
		clientSecret = secret
		if clientSecret == nil {
			kernel.WriteError(w, http.StatusConflict, "该 Client 没有可下载的当前 Client Secret，请先重新签发密钥后再下载对接文档")
			return
		}
	}
	kernel.WriteOK(w, oauthIntegrationPackage{Client: *client, ClientSecret: clientSecret}, "")
}

func (d *OAuthDeps) integrationInfo(w http.ResponseWriter, _ *http.Request) {
	if !d.OIDCEnabled {
		kernel.WriteError(w, http.StatusNotFound, "OIDC Provider 未启用")
		return
	}
	issuer := d.OIDCIssuer
	kernel.WriteOK(w, oauthIntegrationInfo{
		Issuer:                      issuer,
		DiscoveryURL:                issuer + "/.well-known/openid-configuration",
		JwksURL:                     issuer + "/oauth/jwks",
		AuthorizationEndpoint:       issuer + "/oauth/authorize",
		TokenEndpoint:               issuer + "/oauth/token",
		UserinfoEndpoint:            issuer + "/oauth/userinfo",
		DeviceAuthorizationEndpoint: issuer + "/oauth/device_authorization",
		RevocationEndpoint:          issuer + "/oauth/revoke",
		TokenRenewalEndpoint:        issuer + "/oauth/token/renew",
		IDTokenSigningAlgorithm:     "RS256",
	}, "")
}

func (d *OAuthDeps) createClient(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled || d.OIDCIssuer == "" {
		kernel.WriteError(w, http.StatusConflict, "OIDC Provider 未启用，不能创建 Client")
		return
	}
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	input, message := parseOAuthClientCreateBody(body)
	if message == "" {
		message = validateOAuthScopes(input.AllowedScopes)
	}
	if message == "" {
		for _, uri := range input.RedirectUris {
			if !isAllowedRedirectURI(uri, input.ClientType) {
				message = "回调地址必须是精确 HTTPS、反向域名协议或本机回环地址"
				break
			}
		}
	}
	if message != "" {
		kernel.WriteBadRequest(w, message)
		return
	}
	created, err := d.Store.CreateClient(r.Context(), input)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	writeCreatedOK(w, created)
}

func (d *OAuthDeps) patchClient(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !kernel.DecodeJSON(w, r, &body) {
		return
	}
	// Node replies with the fixed message regardless of which zod issue fired.
	status, message := parseOAuthClientStatusBody(body)
	if message != "" {
		kernel.WriteBadRequest(w, "Client 状态参数无效")
		return
	}
	updated, err := d.Store.UpdateClientStatus(r.Context(), r.PathValue("clientId"), status)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if updated == nil {
		kernel.WriteError(w, http.StatusNotFound, "Client 不存在")
		return
	}
	kernel.WriteOK(w, updated, "")
}

func (d *OAuthDeps) reissueSecret(w http.ResponseWriter, r *http.Request) {
	if !d.OIDCEnabled || d.OIDCIssuer == "" {
		kernel.WriteError(w, http.StatusConflict, "OIDC Provider 未启用，不能重新签发 Client Secret")
		return
	}
	clientID := r.PathValue("clientId")
	client, err := d.Store.FindClient(r.Context(), clientID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if client == nil {
		kernel.WriteError(w, http.StatusNotFound, "Client 不存在")
		return
	}
	if client.ClientType != "confidential" {
		kernel.WriteBadRequest(w, "公开 Client 不使用 Client Secret")
		return
	}
	reissued, err := d.Store.ReissueClientSecret(r.Context(), clientID)
	if err != nil {
		kernel.WriteError(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	if reissued == nil {
		kernel.WriteError(w, http.StatusNotFound, "Client 不存在")
		return
	}
	kernel.WriteOK(w, reissued, "")
}

// ---------------------------------------------------------------------------
// M16c validation (clientCreateSchema / clientStatusPatchSchema mirrors).
// ---------------------------------------------------------------------------

var oauthResourceScopes = []string{
	"juhe:profile.read", "juhe:profile.write",
	"juhe:groups.read", "juhe:groups.write",
	"juhe:route_strategies.read", "juhe:route_strategies.write",
	"juhe:api_keys.read", "juhe:api_keys.write",
	"juhe:ai_accounts.read", "juhe:ai_accounts.write",
	"juhe:request_limits.read",
}

var oauthSupportedScopes = append([]string{"openid", "profile"}, oauthResourceScopes...)

var oauthRequiredReadScopeByWriteScope = [][2]string{
	{"juhe:profile.write", "juhe:profile.read"},
	{"juhe:groups.write", "juhe:groups.read"},
	{"juhe:route_strategies.write", "juhe:route_strategies.read"},
	{"juhe:api_keys.write", "juhe:api_keys.read"},
	{"juhe:ai_accounts.write", "juhe:ai_accounts.read"},
}

// validateOAuthScopes mirrors the inline scope checks of POST /clients.
func validateOAuthScopes(scopes []string) string {
	for _, scope := range scopes {
		if !containsString(oauthSupportedScopes, scope) {
			return "Client 参数或 scope 无效"
		}
	}
	if containsString(scopes, "profile") && !containsString(scopes, "openid") {
		return "Client 参数或 scope 无效"
	}
	for _, pair := range oauthRequiredReadScopeByWriteScope {
		if containsString(scopes, pair[0]) && !containsString(scopes, pair[1]) {
			return "Client 参数或 scope 无效"
		}
	}
	return ""
}

var reverseDomainProtocolPattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]*\.[a-z0-9.-]+$`)

// isAllowedRedirectURI mirrors isAllowedRedirectUri.
func isAllowedRedirectURI(uri, clientType string) bool {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	if parsed.Fragment != "" || parsed.User != nil {
		return false
	}
	if parsed.Scheme == "https" {
		return parsed.Host != ""
	}
	if clientType == "public" && parsed.Scheme == "http" && isLoopbackHostname(parsed.Hostname()) {
		return parsed.Host != ""
	}
	return clientType == "public" && reverseDomainProtocolPattern.MatchString(parsed.Scheme)
}

func isLoopbackHostname(hostname string) bool {
	return hostname == "127.0.0.1" || hostname == "::1" || hostname == "[::1]"
}

// parseOAuthClientCreateBody mirrors clientCreateSchema (strict).
func parseOAuthClientCreateBody(body map[string]any) (OAuthClientCreateInput, string) {
	input := OAuthClientCreateInput{}
	raw, present := body["displayName"]
	if !present {
		return input, zodRequired
	}
	displayName, isString := raw.(string)
	if !isString {
		return input, zodInvalidType("string", raw)
	}
	input.DisplayName = strings.TrimSpace(displayName)
	if input.DisplayName == "" {
		return input, zodStringMin(1)
	}
	if runeLen(input.DisplayName) > 120 {
		return input, zodStringMax(120)
	}
	raw, present = body["clientType"]
	if !present {
		return input, zodRequired
	}
	clientType, isString := raw.(string)
	if !isString {
		return input, zodInvalidType("string", raw)
	}
	if clientType != "public" && clientType != "confidential" {
		return input, zodEnumMessage([]string{"public", "confidential"}, clientType)
	}
	input.ClientType = clientType
	raw, present = body["redirectUris"]
	if !present {
		return input, zodRequired
	}
	redirectItems, isList := raw.([]any)
	if !isList {
		return input, zodInvalidType("array", raw)
	}
	if len(redirectItems) < 1 {
		return input, zodArrayMin(1)
	}
	if len(redirectItems) > 20 {
		return input, zodArrayMax(20)
	}
	for _, item := range redirectItems {
		text, isString := item.(string)
		if !isString {
			return input, zodInvalidType("string", item)
		}
		if !isValidURLString(text) {
			return input, "Invalid url"
		}
		input.RedirectUris = append(input.RedirectUris, text)
	}
	raw, present = body["allowedScopes"]
	if !present {
		return input, zodRequired
	}
	scopeItems, isList := raw.([]any)
	if !isList {
		return input, zodInvalidType("array", raw)
	}
	if len(scopeItems) < 1 {
		return input, zodArrayMin(1)
	}
	if len(scopeItems) > 20 {
		return input, zodArrayMax(20)
	}
	for _, item := range scopeItems {
		text, isString := item.(string)
		if !isString {
			return input, zodInvalidType("string", item)
		}
		if text == "" {
			return input, zodStringMin(1)
		}
		input.AllowedScopes = append(input.AllowedScopes, text)
	}
	if message := externalUnknownBodyKey(body, []string{"displayName", "clientType", "redirectUris", "allowedScopes"}); message != "" {
		return input, message
	}
	return input, ""
}

// isValidURLString mirrors z.string().url() (new URL(value) succeeds) for the
// absolute HTTP(S) callbacks this route family accepts.
func isValidURLString(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	return parsed.Scheme != "" && parsed.Host != ""
}

// parseOAuthClientStatusBody mirrors clientStatusPatchSchema (strict).
func parseOAuthClientStatusBody(body map[string]any) (string, string) {
	raw, present := body["status"]
	if !present {
		return "", zodRequired
	}
	status, isString := raw.(string)
	if !isString {
		return "", zodInvalidType("string", raw)
	}
	if status != "active" && status != "disabled" {
		return "", zodEnumMessage([]string{"active", "disabled"}, status)
	}
	if message := externalUnknownBodyKey(body, []string{"status"}); message != "" {
		return "", message
	}
	return status, ""
}
