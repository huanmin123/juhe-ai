// Public target resolution, ported from
// external-public-account-push.target.ts: requirePublicTarget (read-only
// lookup), ensureTargetSystemAccount (auto-create with a random secret),
// resolvePublicOwnedResourceTarget, ensureTargetGroup / findExistingTargetGroup
// and the provider availability prechecks.
package aipublic

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/authsys"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/groups"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

// ownerLookup mirrors the findPublicXxxOwnerById projections.
type ownerLookup struct {
	ID              string
	SystemAccountID string
}

// ResolvedPublicTarget mirrors PublicPushResolvedTarget with the public
// target summary pre-rendered.
type ResolvedPublicTarget struct {
	Public          PublicTarget
	Account         authsys.AccountSummary
	Created         bool
	SystemAccountID string
}

// requirePublicTarget mirrors requirePublicTargetAsync: empty username throws
// 目标用户不能为空; a missing account throws 目标用户不存在：<username>.
func (d *Deps) requirePublicTarget(ctx context.Context, usernameInput string) (*ResolvedPublicTarget, error) {
	username := usernameInput
	if username == "" {
		return nil, errors.New("目标用户不能为空")
	}
	account, err := d.SystemAccounts.FindByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if account.ID == "" {
		return nil, errors.New("目标用户不存在：" + username)
	}
	return &ResolvedPublicTarget{
		Public: PublicTarget{
			Username: account.Username, DisplayName: account.DisplayName,
			SystemAccountID: account.ID, Created: false,
		},
		Account:         account,
		SystemAccountID: account.ID,
	}, nil
}

// ensureTargetSystemAccount mirrors ensureTargetSystemAccountAsync: reuse the
// existing account or create one ("由公开接口自动创建", random password,
// must_change_password). The Go store runs the create in its own transaction
// (Node wraps the whole push in one transaction; tracked as a composition
// difference in the slice report).
func (d *Deps) ensureTargetSystemAccount(ctx context.Context, username string, displayName *string) (*ResolvedPublicTarget, error) {
	if username == "" {
		return nil, errors.New("目标用户不能为空")
	}
	account, err := d.SystemAccounts.FindByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if account.ID != "" {
		return &ResolvedPublicTarget{
			Public: PublicTarget{
				Username: account.Username, DisplayName: account.DisplayName,
				SystemAccountID: account.ID, Created: false,
			},
			Account:         account,
			SystemAccountID: account.ID,
		}, nil
	}
	name := username
	if displayName != nil && *displayName != "" {
		name = *displayName
	}
	password, err := randomSecret()
	if err != nil {
		return nil, err
	}
	created, err := d.SystemAccounts.Create(ctx, authsys.CreateInput{
		Username:           username,
		DisplayName:        name,
		Description:        strPtr("由公开接口自动创建"),
		Role:               "user",
		Status:             "active",
		MustChangePassword: boolPtr(true),
		Password:           password,
	})
	if err != nil {
		return nil, err
	}
	summary, err := d.SystemAccounts.FindByID(ctx, created.ID)
	if err != nil {
		return nil, err
	}
	if summary.ID == "" {
		return nil, errors.New("目标用户创建失败")
	}
	return &ResolvedPublicTarget{
		Public: PublicTarget{
			Username: summary.Username, DisplayName: summary.DisplayName,
			SystemAccountID: summary.ID, Created: true,
		},
		Account:         summary,
		Created:         true,
		SystemAccountID: summary.ID,
	}, nil
}

// resolveOwnedTarget mirrors resolvePublicOwnedResourceTargetAsync: with a
// username the named account must own the resource; without one the owner
// account itself is the target.
func (d *Deps) resolveOwnedTarget(ctx context.Context, username string, hasUsername bool, owner *ownerLookup) (*ResolvedPublicTarget, error) {
	if owner == nil {
		return nil, nil
	}
	var account authsys.AccountSummary
	var err error
	if hasUsername && username != "" {
		account, err = d.SystemAccounts.FindByUsername(ctx, username)
	} else {
		account, err = d.SystemAccounts.FindByID(ctx, owner.SystemAccountID)
	}
	if err != nil {
		return nil, err
	}
	if account.ID == "" || account.ID != owner.SystemAccountID {
		return nil, nil
	}
	return &ResolvedPublicTarget{
		Public: PublicTarget{
			Username: account.Username, DisplayName: account.DisplayName,
			SystemAccountID: account.ID, Created: false,
		},
		Account:         account,
		SystemAccountID: account.ID,
	}, nil
}

// findExistingTargetGroup mirrors findExistingTargetGroupAsync: the option
// list filtered by provider code + case-insensitive name equality.
func (d *Deps) findExistingTargetGroup(ctx context.Context, ownerID, providerCode, groupName string) (*groups.OptionSummary, error) {
	if groupName == "" {
		return nil, errors.New("目标分组不能为空")
	}
	options, err := d.Groups.Options(ctx, groups.AccessScope{ViewerID: ownerID}, groups.OptionsQuery{
		Keyword:      groupName,
		ProviderCode: providerCode,
		Limit:        20,
	})
	if err != nil {
		return nil, err
	}
	for index := range options {
		option := options[index]
		if option.ProviderCode == providerCode && sameText(option.Name, groupName) {
			return &option, nil
		}
	}
	return nil, nil
}

// ensureTargetGroup mirrors ensureTargetGroupAsync: find or create the
// personal group (description 由公开接口自动创建).
func (d *Deps) ensureTargetGroup(ctx context.Context, ownerID, providerCode, groupName string) (*groups.Detail, bool, error) {
	existing, err := d.findExistingTargetGroup(ctx, ownerID, providerCode, groupName)
	if err != nil {
		return nil, false, err
	}
	access := groups.AccessScope{ViewerID: ownerID}
	if existing != nil {
		detail, err := d.Groups.FindDetail(ctx, existing.ID, access)
		if err != nil {
			return nil, false, err
		}
		if detail == nil {
			return nil, false, errors.New("目标分组不存在")
		}
		return detail, false, nil
	}
	name := groupName
	provider := providerCode
	description := "由公开接口自动创建"
	groupType := "personal"
	created, err := d.Groups.Create(ctx, groups.MutationInput{
		Name: &name, ProviderCode: &provider, Description: &description, GroupType: &groupType,
	}, access)
	if err != nil {
		return nil, false, err
	}
	detail, err := d.Groups.FindDetail(ctx, created.ID, access)
	if err != nil || detail == nil {
		return nil, false, err
	}
	return detail, true, nil
}

// ---------------------------------------------------------------------------
// Provider availability (assertProviderCodeEnabled / requireProviderProtocolProfile).
// ---------------------------------------------------------------------------

type providerProfile struct {
	ID           string
	ProviderCode string
	Name         string
	Enabled      bool
	AccountTypes []string
}

// assertProviderCodeEnabled mirrors assertProviderCodeEnabledAsync.
func (d *Deps) assertProviderCodeEnabled(ctx context.Context, providerCode string) error {
	_, err := d.loadProvider(ctx, providerCode)
	return err
}

// loadProvider mirrors the listProviders().find(code) + enabled checks.
func (d *Deps) loadProvider(ctx context.Context, providerCode string) (enabled bool, err error) {
	var providerEnabled int64
	queryErr := d.db().QueryRowContext(ctx, d.bind(`SELECT enabled FROM `+d.table("providers")+`
		WHERE code = ? LIMIT 1`), providerCode).Scan(&providerEnabled)
	if errors.Is(queryErr, sql.ErrNoRows) {
		return false, errors.New("不支持的供应商：" + providerCode)
	}
	if queryErr != nil {
		return false, queryErr
	}
	if providerEnabled != 1 {
		return false, errors.New("供应商已停用：" + providerCode)
	}
	return true, nil
}

// requireProviderProfile mirrors requireProviderProtocolProfile.
func (d *Deps) requireProviderProfile(ctx context.Context, providerCode string, profileID string) (*providerProfile, error) {
	if profileID == "" {
		return nil, errors.New("providerProtocolProfileId 不能为空")
	}
	if _, err := d.loadProvider(ctx, providerCode); err != nil {
		return nil, err
	}
	var row struct {
		id               string
		providerCode     string
		name             string
		enabled          int64
		accountTypesJSON sql.NullString
	}
	err := d.db().QueryRowContext(ctx, d.bind(`SELECT id, provider_code, name, enabled, account_types_json
		FROM `+d.table("provider_protocol_profiles")+` WHERE id = ? LIMIT 1`), profileID).Scan(
		&row.id, &row.providerCode, &row.name, &row.enabled, &row.accountTypesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("供应商未配置协议档案：" + providerCode)
	}
	if err != nil {
		return nil, err
	}
	if row.providerCode != providerCode {
		return nil, errors.New("协议档案 " + row.id + " 不属于供应商 " + providerCode)
	}
	if row.enabled != 1 {
		return nil, errors.New("供应商协议档案已停用：" + row.name)
	}
	types := []string{}
	if row.accountTypesJSON.Valid && row.accountTypesJSON.String != "" {
		var parsed []any
		if jsonUnmarshal([]byte(row.accountTypesJSON.String), &parsed) == nil {
			for _, item := range parsed {
				if text, isString := item.(string); isString {
					types = append(types, text)
				}
			}
		}
	}
	return &providerProfile{ID: row.id, ProviderCode: row.providerCode, Name: row.name, Enabled: true, AccountTypes: types}, nil
}

// assertSupportedPushAccountType mirrors assertSupportedPushAccountType.
func assertSupportedPushAccountType(accountType string, profile *providerProfile) error {
	if accountType == "" {
		return errors.New("账号类型不能为空")
	}
	if accountType != "api_key" {
		return errors.New("账号新增仅支持 API Key 账户")
	}
	if profile != nil && !containsString(profile.AccountTypes, "api_key") {
		return errors.New("当前供应商不支持 API Key 账户")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Owner lookups (findPublicGroupOwnerById / findPublicApiKeyOwnerById /
// findPublicAccountOwnerById / findRouteStrategyMutationVersion).
// ---------------------------------------------------------------------------

func (d *Deps) findGroupOwnerByID(ctx context.Context, groupID string) (*ownerLookup, error) {
	var lookup ownerLookup
	err := d.db().QueryRowContext(ctx, d.bind(`SELECT id, system_account_id FROM `+d.table("groups")+`
		WHERE id = ? LIMIT 1`), groupID).Scan(&lookup.ID, &lookup.SystemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lookup, nil
}

func (d *Deps) findApiKeyOwnerByID(ctx context.Context, apiKeyID string) (*ownerLookup, error) {
	var lookup ownerLookup
	err := d.db().QueryRowContext(ctx, d.bind(`SELECT id, system_account_id FROM `+d.table("api_keys")+`
		WHERE id = ? LIMIT 1`), apiKeyID).Scan(&lookup.ID, &lookup.SystemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lookup, nil
}

func (d *Deps) findAccountOwnerByID(ctx context.Context, accountID string) (*ownerLookup, error) {
	var lookup ownerLookup
	err := d.db().QueryRowContext(ctx, d.bind(`SELECT id, system_account_id FROM `+d.table("accounts")+`
		WHERE id = ? AND deleted_at IS NULL LIMIT 1`), accountID).Scan(&lookup.ID, &lookup.SystemAccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lookup, nil
}

func (d *Deps) findStrategyOwnerByID(ctx context.Context, strategyID string) (*strategyOwnerLookup, error) {
	var lookup strategyOwnerLookup
	err := d.db().QueryRowContext(ctx, d.bind(`SELECT id, system_account_id, updated_at FROM `+d.table("route_strategies")+`
		WHERE id = ? LIMIT 1`), strategyID).Scan(&lookup.ID, &lookup.SystemAccountID, &lookup.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lookup, nil
}

// strategyOwnerLookup mirrors findPublicRouteStrategyOwnerByIdAsync (the
// updatedAt feeds the expected-version of the patch).
type strategyOwnerLookup struct {
	ID              string
	SystemAccountID string
	UpdatedAt       string
}

// ---------------------------------------------------------------------------
// Error rendering + small value helpers.
// ---------------------------------------------------------------------------

// serviceMessage mirrors `error instanceof Error ? error.message : fallback`.
func serviceMessage(err error, fallback string) string {
	if err != nil && err.Error() != "" {
		return err.Error()
	}
	return fallback
}

// writeServiceError mirrors the route error mapping: 不存在 -> 404,
// 已存在/重复 -> 409, otherwise 400.
func (d *Deps) writeServiceError(w http.ResponseWriter, err error, fallback string) {
	message := serviceMessage(err, fallback)
	switch {
	case strings.Contains(message, "不存在"):
		d.writeNotFoundEnvelope(w, message)
	case strings.Contains(message, "已存在"), strings.Contains(message, "重复"):
		writeConflict(w, message)
	default:
		kernelWriteBadRequest(w, message)
	}
}

func writeConflict(w http.ResponseWriter, message string) {
	kernelWriteError(w, http.StatusConflict, message)
}

func kernelWriteError(w http.ResponseWriter, status int, message string) {
	kernelWriteErrorStatus(w, status, message)
}

// randomSecret mirrors autoCreatedTargetPasswordHash's random 18-byte
// base64url password (the hash itself is the store's Node-compatible bcrypt).
func randomSecret() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64URLEncode(buf), nil
}

func boolPtr(value bool) *bool { return &value }

var _ = modelcheckauth.SQLite
