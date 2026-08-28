// Package groups contains Gateway-owned Business domain primitives for groups,
// route strategies and API keys.  It is deliberately an in-process SQL
// boundary: callers provide an authenticated actor and every mutation is
// fenced by the Business owner handoff gate.
package groups

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/modelcheckauth"
)

var (
	ErrOwnerGate        = errors.New("Business owner handoff gate is not satisfied")
	ErrForbidden        = errors.New("actor is not allowed to mutate this Business owner")
	ErrRevisionConflict = errors.New("resource revision conflict")
)

// OwnerGate is captured during the cutover. A partial handoff never permits a
// write, even when the database itself is reachable.
type OwnerGate struct {
	Confirmed         bool
	SchemaReady       bool
	NodeWriterStopped bool
}

func (g OwnerGate) Ready() bool { return g.Confirmed && g.SchemaReady && g.NodeWriterStopped }

type Actor struct {
	SystemAccountID string
	Role            string
}

func (a Actor) Admin() bool { return a.Role == "admin" || a.Role == "super_admin" }

type Mode = modelcheckauth.Mode

type Service struct {
	db           *sql.DB
	mode         Mode
	gate         OwnerGate
	now          func() time.Time
	cipher       SecretCipher
	lastRevision atomic.Int64
}

// SecretCipher must produce the same protected storage representation used by
// the configured Business deployment. The domain layer never persists an API
// key secret as plaintext or invents a compatibility cipher.
type SecretCipher interface {
	Encrypt(context.Context, []byte) (string, error)
}

func New(db *sql.DB, mode Mode, gate OwnerGate, now func() time.Time, cipher SecretCipher) (*Service, error) {
	if db == nil || cipher == nil || (mode != modelcheckauth.SQLite && mode != modelcheckauth.Postgres) {
		return nil, errors.New("invalid groups service database")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{db: db, mode: mode, gate: gate, now: now, cipher: cipher}, nil
}

func (s *Service) requireWrite(actor Actor) error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	if strings.TrimSpace(actor.SystemAccountID) == "" {
		return ErrForbidden
	}
	return nil
}

func (s *Service) owner(actor Actor, requested string) (string, error) {
	id := strings.TrimSpace(requested)
	if id == "" {
		id = strings.TrimSpace(actor.SystemAccountID)
	}
	if !actor.Admin() && id != strings.TrimSpace(actor.SystemAccountID) {
		return "", ErrForbidden
	}
	if id == "" {
		return "", ErrForbidden
	}
	return id, nil
}

type Group struct {
	ID              string  `json:"id"`
	SystemAccountID string  `json:"systemAccountId"`
	Name            string  `json:"name"`
	ProviderCode    string  `json:"providerCode"`
	Description     *string `json:"description,omitempty"`
	Enabled         bool    `json:"enabled"`
	IsDefault       bool    `json:"isDefault"`
	Revision        string  `json:"revision"`
}

type GroupInput struct {
	SystemAccountID string
	Name            string
	ProviderCode    string
	Description     *string
	Enabled         *bool
}

type RouteBinding struct {
	GroupID  string `json:"groupId"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Status   string `json:"status"`
}

type RouteStrategy struct {
	ID              string         `json:"id"`
	SystemAccountID string         `json:"systemAccountId"`
	Name            string         `json:"name"`
	Description     *string        `json:"description,omitempty"`
	Mode            string         `json:"mode"`
	Status          string         `json:"status"`
	IsDefault       bool           `json:"isDefault"`
	Revision        string         `json:"revision"`
	Bindings        []RouteBinding `json:"groupBindings"`
}

type RouteStrategyInput struct {
	SystemAccountID string
	Name            string
	Description     *string
	Mode            string
	Status          string
	Bindings        []RouteBinding
}

type APIKey struct {
	ID              string  `json:"id"`
	SystemAccountID string  `json:"systemAccountId"`
	RouteStrategyID string  `json:"routeStrategyId"`
	Name            string  `json:"name"`
	Description     *string `json:"description,omitempty"`
	KeyPrefix       string  `json:"keyPrefix"`
	KeySuffix       string  `json:"keySuffix"`
	Status          string  `json:"status"`
	IsDefault       bool    `json:"isDefault"`
	Purpose         string  `json:"purpose"`
	Revision        string  `json:"revision"`
}

type APIKeyInput struct {
	SystemAccountID string
	RouteStrategyID string
	Name            string
	Description     *string
	Secret          string
	Status          string
	Purpose         string
}

func (s *Service) CreateGroup(ctx context.Context, actor Actor, in GroupInput) (Group, error) {
	if err := s.requireWrite(actor); err != nil {
		return Group{}, err
	}
	owner, err := s.owner(actor, in.SystemAccountID)
	if err != nil {
		return Group{}, err
	}
	name, provider := strings.TrimSpace(in.Name), strings.TrimSpace(in.ProviderCode)
	if name == "" || provider == "" {
		return Group{}, errors.New("group name and provider are required")
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	now := s.timestamp()
	id := newID("grp")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.bind(`INSERT INTO groups (id,system_account_id,name,provider_code,description,enabled,is_default,group_type,created_at,updated_at) VALUES (?,?,?,?,?, ?,0,'personal',?,?)`), id, owner, name, provider, nullable(in.Description), boolInt(enabled), now, now); err != nil {
		return Group{}, fmt.Errorf("create group: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return Group{}, err
	}
	return Group{ID: id, SystemAccountID: owner, Name: name, ProviderCode: provider, Description: in.Description, Enabled: enabled, Revision: now}, nil
}

// FindGroup is scoped to a concrete Business owner. An administrator must
// explicitly select that owner instead of receiving an accidental cross-owner
// aggregate.
func (s *Service) FindGroup(ctx context.Context, actor Actor, ownerID, id string) (Group, error) {
	if err := s.requireWrite(actor); err != nil {
		return Group{}, err
	}
	owner, err := s.owner(actor, ownerID)
	if err != nil {
		return Group{}, err
	}
	var out Group
	var description sql.NullString
	var enabled, isDefault int
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT id,system_account_id,name,provider_code,description,enabled,is_default,updated_at FROM groups WHERE id=? AND system_account_id=?`), id, owner).
		Scan(&out.ID, &out.SystemAccountID, &out.Name, &out.ProviderCode, &description, &enabled, &isDefault, &out.Revision)
	if err != nil {
		return Group{}, err
	}
	if description.Valid {
		out.Description = &description.String
	}
	out.Enabled, out.IsDefault = enabled != 0, isDefault != 0
	return out, nil
}

func (s *Service) ListGroups(ctx context.Context, actor Actor, ownerID string, limit int) ([]Group, error) {
	if err := s.requireWrite(actor); err != nil {
		return nil, err
	}
	owner, err := s.owner(actor, ownerID)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("group list limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id,system_account_id,name,provider_code,description,enabled,is_default,updated_at FROM groups WHERE system_account_id=? ORDER BY updated_at DESC,id DESC LIMIT ?`), owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Group, 0)
	for rows.Next() {
		var item Group
		var description sql.NullString
		var enabled, isDefault int
		if err := rows.Scan(&item.ID, &item.SystemAccountID, &item.Name, &item.ProviderCode, &description, &enabled, &isDefault, &item.Revision); err != nil {
			return nil, err
		}
		if description.Valid {
			item.Description = &description.String
		}
		item.Enabled, item.IsDefault = enabled != 0, isDefault != 0
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Service) UpdateGroup(ctx context.Context, actor Actor, id, expectedRevision string, in GroupInput) (Group, error) {
	if err := s.requireWrite(actor); err != nil {
		return Group{}, err
	}
	owner, err := s.owner(actor, in.SystemAccountID)
	if err != nil {
		return Group{}, err
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(expectedRevision) == "" {
		return Group{}, ErrRevisionConflict
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.ProviderCode) == "" {
		return Group{}, errors.New("group name and provider are required")
	}
	now := s.timestamp()
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, s.bind(`UPDATE groups SET name=?,provider_code=?,description=?,enabled=?,updated_at=? WHERE id=? AND system_account_id=? AND updated_at=? AND is_default=0`), strings.TrimSpace(in.Name), strings.TrimSpace(in.ProviderCode), nullable(in.Description), boolInt(enabled), now, id, owner, expectedRevision)
	if err != nil {
		return Group{}, fmt.Errorf("update group: %w", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return Group{}, ErrRevisionConflict
	}
	if err = tx.Commit(); err != nil {
		return Group{}, err
	}
	return Group{ID: id, SystemAccountID: owner, Name: strings.TrimSpace(in.Name), ProviderCode: strings.TrimSpace(in.ProviderCode), Description: in.Description, Enabled: enabled, Revision: now}, nil
}

func (s *Service) DeleteGroup(ctx context.Context, actor Actor, id, expectedRevision string) error {
	if err := s.requireWrite(actor); err != nil {
		return err
	}
	owner, err := s.owner(actor, "")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var isDefault int
	var rev string
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT is_default,updated_at FROM groups WHERE id=? AND system_account_id=?`), id, owner).Scan(&isDefault, &rev); err != nil {
		return err
	}
	if isDefault != 0 {
		return errors.New("default group cannot be deleted")
	}
	if rev != expectedRevision {
		return ErrRevisionConflict
	}
	var refs int
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(1) FROM route_strategy_groups WHERE group_id=? AND system_account_id=?`), id, owner).Scan(&refs); err != nil {
		return err
	}
	if refs > 0 {
		return errors.New("group is referenced by route strategy")
	}
	res, err := tx.ExecContext(ctx, s.bind(`DELETE FROM groups WHERE id=? AND system_account_id=? AND updated_at=? AND is_default=0`), id, owner, expectedRevision)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrRevisionConflict
	}
	return tx.Commit()
}

func (s *Service) CreateRouteStrategy(ctx context.Context, actor Actor, in RouteStrategyInput) (RouteStrategy, error) {
	if err := s.requireWrite(actor); err != nil {
		return RouteStrategy{}, err
	}
	owner, err := s.owner(actor, in.SystemAccountID)
	if err != nil {
		return RouteStrategy{}, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return RouteStrategy{}, errors.New("route strategy name is required")
	}
	mode := strings.TrimSpace(in.Mode)
	if mode == "" {
		mode = "normal"
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "disabled" {
		return RouteStrategy{}, errors.New("invalid route strategy status")
	}
	now := s.timestamp()
	id := newID("route_strategy")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RouteStrategy{}, err
	}
	defer tx.Rollback()
	if err = s.validateBindings(ctx, tx, owner, in.Bindings); err != nil {
		return RouteStrategy{}, err
	}
	if err = validateModeBindings(mode, in.Bindings); err != nil {
		return RouteStrategy{}, err
	}
	if _, err = tx.ExecContext(ctx, s.bind(`INSERT INTO route_strategies (id,system_account_id,name,description,mode,status,is_default,created_at,updated_at) VALUES (?,?,?,?,?,?,0,?,?)`), id, owner, strings.TrimSpace(in.Name), nullable(in.Description), mode, status, now, now); err != nil {
		return RouteStrategy{}, fmt.Errorf("create route strategy: %w", err)
	}
	if err = s.replaceBindings(ctx, tx, owner, id, in.Bindings, now); err != nil {
		return RouteStrategy{}, err
	}
	if err = tx.Commit(); err != nil {
		return RouteStrategy{}, err
	}
	return RouteStrategy{ID: id, SystemAccountID: owner, Name: strings.TrimSpace(in.Name), Description: in.Description, Mode: mode, Status: status, Revision: now, Bindings: normalizeBindings(in.Bindings)}, nil
}

func (s *Service) FindRouteStrategy(ctx context.Context, actor Actor, ownerID, id string) (RouteStrategy, error) {
	if err := s.requireWrite(actor); err != nil {
		return RouteStrategy{}, err
	}
	owner, err := s.owner(actor, ownerID)
	if err != nil {
		return RouteStrategy{}, err
	}
	var out RouteStrategy
	var description sql.NullString
	var isDefault int
	if err = s.db.QueryRowContext(ctx, s.bind(`SELECT id,system_account_id,name,description,mode,status,is_default,updated_at FROM route_strategies WHERE id=? AND system_account_id=?`), id, owner).Scan(&out.ID, &out.SystemAccountID, &out.Name, &description, &out.Mode, &out.Status, &isDefault, &out.Revision); err != nil {
		return RouteStrategy{}, err
	}
	if description.Valid {
		out.Description = &description.String
	}
	out.IsDefault = isDefault != 0
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT group_id,priority,weight,status FROM route_strategy_groups WHERE route_strategy_id=? AND system_account_id=? ORDER BY priority,group_id`), id, owner)
	if err != nil {
		return RouteStrategy{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var b RouteBinding
		if err := rows.Scan(&b.GroupID, &b.Priority, &b.Weight, &b.Status); err != nil {
			return RouteStrategy{}, err
		}
		out.Bindings = append(out.Bindings, b)
	}
	return out, rows.Err()
}

func (s *Service) ListRouteStrategies(ctx context.Context, actor Actor, ownerID string, limit int) ([]RouteStrategy, error) {
	if err := s.requireWrite(actor); err != nil {
		return nil, err
	}
	owner, err := s.owner(actor, ownerID)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("route strategy list limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM route_strategies WHERE system_account_id=? ORDER BY updated_at DESC,id DESC LIMIT ?`), owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RouteStrategy, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		item, err := s.FindRouteStrategy(ctx, actor, owner, id)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Service) UpdateRouteStrategy(ctx context.Context, actor Actor, id, expectedRevision string, in RouteStrategyInput) (RouteStrategy, error) {
	if err := s.requireWrite(actor); err != nil {
		return RouteStrategy{}, err
	}
	owner, err := s.owner(actor, in.SystemAccountID)
	if err != nil {
		return RouteStrategy{}, err
	}
	if expectedRevision == "" {
		return RouteStrategy{}, ErrRevisionConflict
	}
	now := s.timestamp()
	mode := strings.TrimSpace(in.Mode)
	if mode == "" {
		mode = "normal"
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "disabled" {
		return RouteStrategy{}, errors.New("invalid route strategy status")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RouteStrategy{}, err
	}
	defer tx.Rollback()
	if err = s.validateBindings(ctx, tx, owner, in.Bindings); err != nil {
		return RouteStrategy{}, err
	}
	if err = validateModeBindings(mode, in.Bindings); err != nil {
		return RouteStrategy{}, err
	}
	res, err := tx.ExecContext(ctx, s.bind(`UPDATE route_strategies SET name=?,description=?,mode=?,status=?,updated_at=? WHERE id=? AND system_account_id=? AND updated_at=? AND is_default=0`), strings.TrimSpace(in.Name), nullable(in.Description), mode, status, now, id, owner, expectedRevision)
	if err != nil {
		return RouteStrategy{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return RouteStrategy{}, ErrRevisionConflict
	}
	if err = s.replaceBindings(ctx, tx, owner, id, in.Bindings, now); err != nil {
		return RouteStrategy{}, err
	}
	if err = tx.Commit(); err != nil {
		return RouteStrategy{}, err
	}
	return RouteStrategy{ID: id, SystemAccountID: owner, Name: strings.TrimSpace(in.Name), Description: in.Description, Mode: mode, Status: status, Revision: now, Bindings: normalizeBindings(in.Bindings)}, nil
}

func (s *Service) DeleteRouteStrategy(ctx context.Context, actor Actor, id, expectedRevision string) error {
	if err := s.requireWrite(actor); err != nil {
		return err
	}
	owner, err := s.owner(actor, "")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var def int
	var rev string
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT is_default,updated_at FROM route_strategies WHERE id=? AND system_account_id=?`), id, owner).Scan(&def, &rev); err != nil {
		return err
	}
	if def != 0 {
		return errors.New("default route strategy cannot be deleted")
	}
	if rev != expectedRevision {
		return ErrRevisionConflict
	}
	var refs int
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT COUNT(1) FROM api_keys WHERE route_strategy_id=? AND system_account_id=?`), id, owner).Scan(&refs); err != nil {
		return err
	}
	if refs > 0 {
		return errors.New("route strategy is referenced by API key")
	}
	res, err := tx.ExecContext(ctx, s.bind(`DELETE FROM route_strategies WHERE id=? AND system_account_id=? AND updated_at=? AND is_default=0`), id, owner, expectedRevision)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrRevisionConflict
	}
	return tx.Commit()
}

func (s *Service) CreateAPIKey(ctx context.Context, actor Actor, in APIKeyInput) (APIKey, string, error) {
	if err := s.requireWrite(actor); err != nil {
		return APIKey{}, "", err
	}
	owner, err := s.owner(actor, in.SystemAccountID)
	if err != nil {
		return APIKey{}, "", err
	}
	if strings.TrimSpace(in.RouteStrategyID) == "" || strings.TrimSpace(in.Name) == "" || in.Secret == "" {
		return APIKey{}, "", errors.New("api key route strategy, name and secret are required")
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "active"
	}
	purpose := strings.TrimSpace(in.Purpose)
	if purpose == "" {
		purpose = "general"
	}
	encrypted, err := s.cipher.Encrypt(ctx, []byte(in.Secret))
	if err != nil {
		return APIKey{}, "", fmt.Errorf("encrypt api key secret: %w", err)
	}
	if strings.TrimSpace(encrypted) == "" {
		return APIKey{}, "", errors.New("api key cipher returned an empty ciphertext")
	}
	now := s.timestamp()
	id := newID("key")
	sum := sha256.Sum256([]byte(in.Secret))
	hash := hex.EncodeToString(sum[:])
	prefix := in.Secret
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	suffix := in.Secret
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, "", err
	}
	defer tx.Rollback()
	var route string
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM route_strategies WHERE id=? AND system_account_id=? AND status='active'`), in.RouteStrategyID, owner).Scan(&route); err != nil {
		return APIKey{}, "", errors.New("route strategy does not exist or is not active")
	}
	if _, err = tx.ExecContext(ctx, s.bind(`INSERT INTO api_keys (id,system_account_id,route_strategy_id,name,description,key_hash,key_prefix,key_suffix,key_secret_encrypted,status,is_default,purpose,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,0,?,?,?)`), id, owner, in.RouteStrategyID, strings.TrimSpace(in.Name), nullable(in.Description), hash, prefix, suffix, encrypted, status, purpose, now, now); err != nil {
		return APIKey{}, "", fmt.Errorf("create api key: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return APIKey{}, "", err
	}
	return APIKey{ID: id, SystemAccountID: owner, RouteStrategyID: in.RouteStrategyID, Name: strings.TrimSpace(in.Name), Description: in.Description, KeyPrefix: prefix, KeySuffix: suffix, Status: status, Purpose: purpose, Revision: now}, in.Secret, nil
}

func (s *Service) FindAPIKey(ctx context.Context, actor Actor, ownerID, id string) (APIKey, error) {
	if err := s.requireWrite(actor); err != nil {
		return APIKey{}, err
	}
	owner, err := s.owner(actor, ownerID)
	if err != nil {
		return APIKey{}, err
	}
	var out APIKey
	var description sql.NullString
	var def int
	err = s.db.QueryRowContext(ctx, s.bind(`SELECT id,system_account_id,route_strategy_id,name,description,key_prefix,key_suffix,status,is_default,purpose,updated_at FROM api_keys WHERE id=? AND system_account_id=?`), id, owner).Scan(&out.ID, &out.SystemAccountID, &out.RouteStrategyID, &out.Name, &description, &out.KeyPrefix, &out.KeySuffix, &out.Status, &def, &out.Purpose, &out.Revision)
	if err != nil {
		return APIKey{}, err
	}
	if description.Valid {
		out.Description = &description.String
	}
	out.IsDefault = def != 0
	return out, nil
}

func (s *Service) ListAPIKeys(ctx context.Context, actor Actor, ownerID string, limit int) ([]APIKey, error) {
	if err := s.requireWrite(actor); err != nil {
		return nil, err
	}
	owner, err := s.owner(actor, ownerID)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 1000 {
		return nil, errors.New("api key list limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM api_keys WHERE system_account_id=? ORDER BY updated_at DESC,id DESC LIMIT ?`), owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]APIKey, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		item, err := s.FindAPIKey(ctx, actor, owner, id)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Service) UpdateAPIKey(ctx context.Context, actor Actor, id, expectedRevision string, in APIKeyInput) (APIKey, error) {
	if err := s.requireWrite(actor); err != nil {
		return APIKey{}, err
	}
	owner, err := s.owner(actor, in.SystemAccountID)
	if err != nil {
		return APIKey{}, err
	}
	if expectedRevision == "" {
		return APIKey{}, ErrRevisionConflict
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.RouteStrategyID) == "" {
		return APIKey{}, errors.New("api key route strategy and name are required")
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "disabled" {
		return APIKey{}, errors.New("invalid api key status")
	}
	purpose := strings.TrimSpace(in.Purpose)
	if purpose == "" {
		purpose = "general"
	}
	now := s.timestamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, err
	}
	defer tx.Rollback()
	var route string
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT id FROM route_strategies WHERE id=? AND system_account_id=? AND status='active'`), in.RouteStrategyID, owner).Scan(&route); err != nil {
		return APIKey{}, errors.New("route strategy does not exist or is not active")
	}
	var prefix, suffix string
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT key_prefix,key_suffix FROM api_keys WHERE id=? AND system_account_id=?`), id, owner).Scan(&prefix, &suffix); err != nil {
		return APIKey{}, err
	}
	res, err := tx.ExecContext(ctx, s.bind(`UPDATE api_keys SET route_strategy_id=?,name=?,description=?,status=?,purpose=?,updated_at=? WHERE id=? AND system_account_id=? AND updated_at=? AND is_default=0`), in.RouteStrategyID, strings.TrimSpace(in.Name), nullable(in.Description), status, purpose, now, id, owner, expectedRevision)
	if err != nil {
		return APIKey{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return APIKey{}, ErrRevisionConflict
	}
	if err = tx.Commit(); err != nil {
		return APIKey{}, err
	}
	return APIKey{ID: id, SystemAccountID: owner, RouteStrategyID: in.RouteStrategyID, Name: strings.TrimSpace(in.Name), Description: in.Description, KeyPrefix: prefix, KeySuffix: suffix, Status: status, Purpose: purpose, Revision: now}, nil
}

// RotateAPIKeySecret creates a new protected secret for a non-default API key
// using the same revision fence as other management mutations. The raw secret
// is returned once to the immediate caller and is never written to logs or a
// result record.
func (s *Service) RotateAPIKeySecret(ctx context.Context, actor Actor, id, expectedRevision, secret string) (APIKey, string, error) {
	if err := s.requireWrite(actor); err != nil {
		return APIKey{}, "", err
	}
	owner, err := s.owner(actor, "")
	if err != nil {
		return APIKey{}, "", err
	}
	if expectedRevision == "" || strings.TrimSpace(secret) == "" {
		return APIKey{}, "", ErrRevisionConflict
	}
	encrypted, err := s.cipher.Encrypt(ctx, []byte(secret))
	if err != nil {
		return APIKey{}, "", fmt.Errorf("encrypt api key secret: %w", err)
	}
	if strings.TrimSpace(encrypted) == "" {
		return APIKey{}, "", errors.New("api key cipher returned an empty ciphertext")
	}
	sum := sha256.Sum256([]byte(secret))
	hash := hex.EncodeToString(sum[:])
	prefix, suffix := secret, secret
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	now := s.timestamp()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, "", err
	}
	defer tx.Rollback()
	var out APIKey
	var description sql.NullString
	var def int
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT id,system_account_id,route_strategy_id,name,description,status,is_default,purpose FROM api_keys WHERE id=? AND system_account_id=? AND updated_at=?`), id, owner, expectedRevision).Scan(&out.ID, &out.SystemAccountID, &out.RouteStrategyID, &out.Name, &description, &out.Status, &def, &out.Purpose); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIKey{}, "", ErrRevisionConflict
		}
		return APIKey{}, "", err
	}
	if def != 0 {
		return APIKey{}, "", errors.New("default api key cannot be rotated")
	}
	res, err := tx.ExecContext(ctx, s.bind(`UPDATE api_keys SET key_hash=?,key_prefix=?,key_suffix=?,key_secret_encrypted=?,updated_at=? WHERE id=? AND system_account_id=? AND updated_at=? AND is_default=0`), hash, prefix, suffix, encrypted, now, id, owner, expectedRevision)
	if err != nil {
		return APIKey{}, "", err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return APIKey{}, "", ErrRevisionConflict
	}
	if err = tx.Commit(); err != nil {
		return APIKey{}, "", err
	}
	if description.Valid {
		out.Description = &description.String
	}
	out.IsDefault = false
	out.KeyPrefix = prefix
	out.KeySuffix = suffix
	out.Revision = now
	return out, secret, nil
}

func (s *Service) DeleteAPIKey(ctx context.Context, actor Actor, id, expectedRevision string) error {
	if err := s.requireWrite(actor); err != nil {
		return err
	}
	owner, err := s.owner(actor, "")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var def int
	var rev string
	if err = tx.QueryRowContext(ctx, s.bind(`SELECT is_default,updated_at FROM api_keys WHERE id=? AND system_account_id=?`), id, owner).Scan(&def, &rev); err != nil {
		return err
	}
	if def != 0 {
		return errors.New("default api key cannot be deleted")
	}
	if rev != expectedRevision {
		return ErrRevisionConflict
	}
	res, err := tx.ExecContext(ctx, s.bind(`DELETE FROM api_keys WHERE id=? AND system_account_id=? AND updated_at=?`), id, owner, expectedRevision)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrRevisionConflict
	}
	return tx.Commit()
}

func (s *Service) validateBindings(ctx context.Context, tx *sql.Tx, owner string, bindings []RouteBinding) error {
	if len(bindings) == 0 {
		return errors.New("route strategy requires at least one group binding")
	}
	seen := map[string]struct{}{}
	for _, b := range bindings {
		id := strings.TrimSpace(b.GroupID)
		if id == "" {
			return errors.New("group binding id is required")
		}
		if _, ok := seen[id]; ok {
			return errors.New("duplicate group binding")
		}
		seen[id] = struct{}{}
		var one int
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT 1 FROM groups WHERE id=? AND system_account_id=? AND enabled=1`), id, owner).Scan(&one); err != nil {
			return errors.New("group binding does not belong to owner or is disabled")
		}
	}
	return nil
}

func validateModeBindings(mode string, bindings []RouteBinding) error {
	active := 0
	priorities := map[int]struct{}{}
	for i, b := range bindings {
		status := b.Status
		if status == "" {
			status = "active"
		}
		if status != "active" && status != "disabled" {
			return errors.New("invalid route strategy binding status")
		}
		if status == "active" {
			active++
			priority := b.Priority
			if priority < 1 {
				priority = i + 1
			}
			if _, exists := priorities[priority]; exists {
				return errors.New("active route strategy binding priorities must be unique")
			}
			priorities[priority] = struct{}{}
		}
	}
	switch mode {
	case "normal":
		if len(bindings) != 1 || active != 1 {
			return errors.New("normal route strategy requires exactly one active group")
		}
	case "failover":
		if len(bindings) < 2 || active < 2 {
			return errors.New("failover route strategy requires a primary and an active fallback group")
		}
	default:
		if active == 0 {
			return errors.New("route strategy requires an active group")
		}
	}
	return nil
}
func (s *Service) replaceBindings(ctx context.Context, tx *sql.Tx, owner, id string, bindings []RouteBinding, now string) error {
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM route_strategy_groups WHERE route_strategy_id=? AND system_account_id=?`), id, owner); err != nil {
		return err
	}
	for i, b := range bindings {
		priority := b.Priority
		if priority < 1 {
			priority = i + 1
		}
		weight := b.Weight
		if weight < 1 {
			weight = 1
		}
		status := b.Status
		if status == "" {
			status = "active"
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO route_strategy_groups (id,route_strategy_id,system_account_id,group_id,priority,weight,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?, ?,?)`), newID("rsg"), id, owner, b.GroupID, priority, weight, status, now, now); err != nil {
			return err
		}
	}
	return nil
}
func normalizeBindings(in []RouteBinding) []RouteBinding {
	out := make([]RouteBinding, len(in))
	copy(out, in)
	for i := range out {
		if out[i].Priority < 1 {
			out[i].Priority = i + 1
		}
		if out[i].Weight < 1 {
			out[i].Weight = 1
		}
		if out[i].Status == "" {
			out[i].Status = "active"
		}
	}
	return out
}
func (s *Service) table(name string) string {
	if s.mode == modelcheckauth.Postgres {
		return "juhe_business." + name
	}
	return name
}
func (s *Service) bind(q string) string {
	q = strings.NewReplacer("route_strategy_groups", "{route_strategy_groups}", "route_strategies", "{route_strategies}", "api_keys", "{api_keys}", "groups", "{groups}").Replace(q)
	q = strings.NewReplacer("{route_strategy_groups}", s.table("route_strategy_groups"), "{route_strategies}", s.table("route_strategies"), "{api_keys}", s.table("api_keys"), "{groups}", s.table("groups")).Replace(q)
	if s.mode != modelcheckauth.Postgres {
		return q
	}
	for i := 1; strings.Contains(q, "?"); i++ {
		q = strings.Replace(q, "?", fmt.Sprintf("$%d", i), 1)
	}
	return q
}
func (s *Service) timestamp() string {
	for {
		next := s.now().UTC().UnixNano()
		previous := s.lastRevision.Load()
		if next <= previous {
			next = previous + 1
		}
		if s.lastRevision.CompareAndSwap(previous, next) {
			return time.Unix(0, next).UTC().Format(time.RFC3339Nano)
		}
	}
}
func nullable(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

var idSequence atomic.Uint64

// ID allocation remains local to the Gateway process. The monotonically
// increasing suffix prevents same-tick collisions under concurrent mutations;
// a production integration can replace this package boundary with the shared
// platform ID service without changing transaction semantics.
func newID(prefix string) string {
	return fmt.Sprintf("%s_%d_%d", prefix, time.Now().UnixNano(), idSequence.Add(1))
}
