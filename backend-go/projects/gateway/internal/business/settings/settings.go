// Package settings owns the Gateway Business settings/provider SQL port.
// It deliberately contains no HTTP, queues, bridges, or schema DDL.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrOwnerGate = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrCAS       = errors.New("Business setting revision is stale")
)

type Mode string

const (
	SQLite   Mode = "sqlite"
	Postgres Mode = "postgres"
)

type OwnerGate struct{ Confirmed, SchemaReady, NodeWriterStopped bool }

func (g OwnerGate) Ready() bool { return g.Confirmed && g.SchemaReady && g.NodeWriterStopped }

type Store struct {
	db     *sql.DB
	mode   Mode
	schema string
	gate   OwnerGate
	now    func() time.Time
}
type Setting struct{ Key, ValueJSON, UpdatedAt string }
type ProviderModel struct {
	ProviderCode, Model, Status, Mode                    string
	CatalogOrder                                         sql.NullInt64
	SupportedAPIProtocolsJSON                            string
	ContextWindowTokens, MaxInputTokens, MaxOutputTokens sql.NullInt64
}

// CatalogReplacement is a complete replacement set for one provider. The
// current provider revision is mandatory so stale configuration writers lose.
type CatalogReplacement struct {
	ProviderCode, ExpectedProviderUpdatedAt string
	Models                                  []CatalogModel
}

type CatalogModel struct {
	ID, Model, Status, Mode, SupportedAPIProtocolsJSON, Source string
	CatalogOrder                                               *int64
	CatalogVisible                                             bool
	ContextWindowTokens, MaxInputTokens, MaxOutputTokens       *int64
}

func New(db *sql.DB, mode Mode, schema string, gate OwnerGate) (*Store, error) {
	if db == nil {
		return nil, errors.New("settings database is required")
	}
	if mode != SQLite && mode != Postgres {
		return nil, errors.New("settings database mode is invalid")
	}
	return &Store{db: db, mode: mode, schema: strings.TrimSpace(schema), gate: gate, now: time.Now}, nil
}
func (s *Store) table(name string) string {
	if s.mode == Postgres {
		if s.schema == "" {
			return name
		}
		return s.schema + "." + name
	}
	return name
}
func (s *Store) bind(q string) string {
	if s.mode != Postgres {
		return q
	}
	n := 1
	var b strings.Builder
	for _, c := range q {
		if c == '?' {
			fmt.Fprintf(&b, "$%d", n)
			n++
		} else {
			b.WriteRune(c)
		}
	}
	return b.String()
}
func (s *Store) requireOwner() error {
	if s == nil || s.db == nil || !s.gate.Ready() {
		return ErrOwnerGate
	}
	return nil
}

func (s *Store) GetGlobal(ctx context.Context, key string) (Setting, bool, error) {
	return s.get(ctx, s.table("global_settings"), "", key)
}
func (s *Store) GetSystem(ctx context.Context, accountID, key string) (Setting, bool, error) {
	if strings.TrimSpace(accountID) == "" {
		return Setting{}, false, errors.New("system account id is required")
	}
	return s.get(ctx, s.table("system_settings"), accountID, key)
}
func (s *Store) get(ctx context.Context, table, accountID, key string) (Setting, bool, error) {
	if strings.TrimSpace(key) == "" {
		return Setting{}, false, errors.New("setting key is required")
	}
	var row Setting
	var err error
	if accountID == "" {
		err = s.db.QueryRowContext(ctx, s.bind("SELECT key,value_json,updated_at FROM "+table+" WHERE key=?"), key).Scan(&row.Key, &row.ValueJSON, &row.UpdatedAt)
	} else {
		err = s.db.QueryRowContext(ctx, s.bind("SELECT key,value_json,updated_at FROM "+table+" WHERE system_account_id=? AND key=?"), accountID, key).Scan(&row.Key, &row.ValueJSON, &row.UpdatedAt)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Setting{}, false, nil
	}
	if err != nil {
		return Setting{}, false, err
	}
	return row, true, nil
}

func (s *Store) PutGlobal(ctx context.Context, key, valueJSON, expectedUpdatedAt string) (Setting, error) {
	return s.put(ctx, s.table("global_settings"), "", key, valueJSON, expectedUpdatedAt)
}
func (s *Store) PutSystem(ctx context.Context, accountID, key, valueJSON, expectedUpdatedAt string) (Setting, error) {
	if strings.TrimSpace(accountID) == "" {
		return Setting{}, errors.New("system account id is required")
	}
	return s.put(ctx, s.table("system_settings"), accountID, key, valueJSON, expectedUpdatedAt)
}
func (s *Store) put(ctx context.Context, table, accountID, key, valueJSON, expected string) (Setting, error) {
	if err := s.requireOwner(); err != nil {
		return Setting{}, err
	}
	if strings.TrimSpace(key) == "" {
		return Setting{}, errors.New("setting key is required")
	}
	if !json.Valid([]byte(valueJSON)) {
		return Setting{}, errors.New("setting value_json must be valid JSON")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Setting{}, err
	}
	defer tx.Rollback()
	var changed int64
	if accountID == "" {
		if expected == "" {
			r, e := tx.ExecContext(ctx, s.bind("INSERT INTO "+table+"(key,value_json,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO NOTHING"), key, valueJSON, now)
			if e != nil {
				return Setting{}, e
			}
			changed, _ = r.RowsAffected()
			if changed == 0 {
				return Setting{}, ErrCAS
			}
		} else {
			r, e := tx.ExecContext(ctx, s.bind("UPDATE "+table+" SET value_json=?,updated_at=? WHERE key=? AND updated_at=?"), valueJSON, now, key, expected)
			if e != nil {
				return Setting{}, e
			}
			changed, _ = r.RowsAffected()
			if changed == 0 {
				return Setting{}, ErrCAS
			}
		}
	} else {
		if expected == "" {
			r, e := tx.ExecContext(ctx, s.bind("INSERT INTO "+table+"(system_account_id,key,value_json,updated_at) VALUES(?,?,?,?) ON CONFLICT(system_account_id,key) DO NOTHING"), accountID, key, valueJSON, now)
			if e != nil {
				return Setting{}, e
			}
			changed, _ = r.RowsAffected()
			if changed == 0 {
				return Setting{}, ErrCAS
			}
		} else {
			r, e := tx.ExecContext(ctx, s.bind("UPDATE "+table+" SET value_json=?,updated_at=? WHERE system_account_id=? AND key=? AND updated_at=?"), valueJSON, now, accountID, key, expected)
			if e != nil {
				return Setting{}, e
			}
			changed, _ = r.RowsAffected()
			if changed == 0 {
				return Setting{}, ErrCAS
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return Setting{}, err
	}
	return Setting{Key: key, ValueJSON: valueJSON, UpdatedAt: now}, nil
}

func (s *Store) ListProviderModels(ctx context.Context, providerCode string, includeInactive bool) ([]ProviderModel, error) {
	if strings.TrimSpace(providerCode) == "" {
		return nil, errors.New("provider code is required")
	}
	q := "SELECT provider_code,model,status,COALESCE(mode,''),catalog_order,supported_api_protocols_json,context_window_tokens,max_input_tokens,max_output_tokens FROM " + s.table("provider_model_catalog") + " WHERE provider_code=?"
	args := []any{providerCode}
	if !includeInactive {
		q += " AND status='active' AND catalog_visible=1"
	}
	q += " ORDER BY COALESCE(catalog_order,2147483647),model"
	rows, err := s.db.QueryContext(ctx, s.bind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderModel
	for rows.Next() {
		var m ProviderModel
		if err := rows.Scan(&m.ProviderCode, &m.Model, &m.Status, &m.Mode, &m.CatalogOrder, &m.SupportedAPIProtocolsJSON, &m.ContextWindowTokens, &m.MaxInputTokens, &m.MaxOutputTokens); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReplaceProviderCatalog performs revision-CAS plus obsolete-catalog cleanup
// in one transaction. It is a complete replacement, not a best-effort patch.
func (s *Store) ReplaceProviderCatalog(ctx context.Context, input CatalogReplacement) error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	input.ProviderCode = strings.TrimSpace(input.ProviderCode)
	input.ExpectedProviderUpdatedAt = strings.TrimSpace(input.ExpectedProviderUpdatedAt)
	if input.ProviderCode == "" || input.ExpectedProviderUpdatedAt == "" {
		return errors.New("provider code and expected revision are required")
	}
	seen := map[string]bool{}
	for _, model := range input.Models {
		if strings.TrimSpace(model.ID) == "" || strings.TrimSpace(model.Model) == "" || strings.TrimSpace(model.Source) == "" || (model.Status != "active" && model.Status != "disabled") || !json.Valid([]byte(model.SupportedAPIProtocolsJSON)) {
			return errors.New("provider catalog model is invalid")
		}
		if seen[model.Model] {
			return errors.New("provider catalog contains duplicate model")
		}
		seen[model.Model] = true
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated, err := tx.ExecContext(ctx, s.bind("UPDATE "+s.table("providers")+" SET updated_at=? WHERE code=? AND updated_at=?"), now, input.ProviderCode, input.ExpectedProviderUpdatedAt)
	if err != nil {
		return err
	}
	count, _ := updated.RowsAffected()
	if count != 1 {
		return ErrCAS
	}
	if _, err = tx.ExecContext(ctx, s.bind("DELETE FROM "+s.table("provider_model_catalog")+" WHERE provider_code=?"), input.ProviderCode); err != nil {
		return err
	}
	for _, model := range input.Models {
		visible := 0
		if model.CatalogVisible {
			visible = 1
		}
		_, err = tx.ExecContext(ctx, s.bind("INSERT INTO "+s.table("provider_model_catalog")+"(id,provider_code,model,status,mode,catalog_order,supported_api_protocols_json,context_window_tokens,max_input_tokens,max_output_tokens,catalog_visible,source,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)"), model.ID, input.ProviderCode, model.Model, model.Status, nullableString(model.Mode), model.CatalogOrder, model.SupportedAPIProtocolsJSON, model.ContextWindowTokens, model.MaxInputTokens, model.MaxOutputTokens, visible, model.Source, now, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// Port keeps future Gateway management handlers from acquiring raw SQL.
type Port interface {
	GetGlobal(context.Context, string) (Setting, bool, error)
	GetSystem(context.Context, string, string) (Setting, bool, error)
	PutGlobal(context.Context, string, string, string) (Setting, error)
	PutSystem(context.Context, string, string, string, string) (Setting, error)
	ListProviderModels(context.Context, string, bool) ([]ProviderModel, error)
	ReplaceProviderCatalog(context.Context, CatalogReplacement) error
}

var _ Port = (*Store)(nil)
