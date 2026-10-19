// Package settings owns the Gateway Business settings/provider SQL port.
// It deliberately contains no HTTP, queues, bridges, or schema DDL.
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

// ProviderModel 是内置目录行读投影（models 单表 builtin 行）。Mode 与
// SupportedAPIProtocolsJSON 无物理列：由 supported_endpoint_modes_json 经
// providers.DeriveLegacyModeAndProtocols 逆映射派生（DTO 键保留）。
type ProviderModel struct {
	ProviderCode, Model, Status, Mode                    string
	CatalogOrder                                         sql.NullInt64
	SupportedAPIProtocolsJSON                            string
	ContextWindowTokens, MaxInputTokens, MaxOutputTokens sql.NullInt64
}

// CatalogReplacement is a complete replacement set for one vendor's builtin
// catalog rows. ExpectedProviderUpdatedAt 是 vendors 行 updated_at 的 CAS 门
// （vendors 只读，比较不再回写）。The current vendor revision is mandatory so
// stale configuration writers lose.
type CatalogReplacement struct {
	ProviderCode, ExpectedProviderUpdatedAt string
	Models                                  []CatalogModel
}

// CatalogModel 是一次内置目录重插的行输入：Model 同写 public_model 与
// upstream_model；SupportedAPIProtocolsJSON（旧协议列表）经
// providers.ForwardProtocolsToEndpointModes 正向落 supported_endpoint_modes_json。
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

// ListProviderModels 读 models 单表 builtin 行（scope='builtin'，vendor 窗口
// 等价旧 provider_model_catalog 的 provider_code 窗口）。includeInactive=false
// 附加 active + catalog_visible 过滤（catalog_visible 在 PostgreSQL 为
// boolean、SQLite 为 integer，谓词按方言生成）。
func (s *Store) ListProviderModels(ctx context.Context, providerCode string, includeInactive bool) ([]ProviderModel, error) {
	if strings.TrimSpace(providerCode) == "" {
		return nil, errors.New("provider code is required")
	}
	q := "SELECT COALESCE(vendor_code,'') AS provider_code, public_model AS model, status, catalog_order, supported_endpoint_modes_json, context_window_tokens, max_input_tokens, max_output_tokens FROM " + s.table("models") + " WHERE scope='builtin' AND vendor_code=?"
	args := []any{providerCode}
	if !includeInactive {
		// catalog_visible 在 PostgreSQL 为 boolean、SQLite 为 integer，
		// 比较 must 按方言生成（见 providers.Store.visibleTruePredicate 同因）。
		visiblePredicate := "catalog_visible=1"
		if s.mode == Postgres {
			visiblePredicate = "catalog_visible=TRUE"
		}
		q += " AND status='active' AND " + visiblePredicate
	}
	q += " ORDER BY COALESCE(catalog_order,2147483647), public_model"
	rows, err := s.db.QueryContext(ctx, s.bind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderModel
	for rows.Next() {
		var m ProviderModel
		var endpointModes sql.NullString
		if err := rows.Scan(&m.ProviderCode, &m.Model, &m.Status, &m.CatalogOrder, &endpointModes, &m.ContextWindowTokens, &m.MaxInputTokens, &m.MaxOutputTokens); err != nil {
			return nil, err
		}
		// 旧 mode / supported_api_protocols_json 无物理列：逆映射派生。
		mode, protocols := deriveProviderModelProjection(endpointModes)
		m.Mode = mode
		encoded, encodeErr := json.Marshal(protocols)
		if encodeErr != nil {
			return nil, encodeErr
		}
		m.SupportedAPIProtocolsJSON = string(encoded)
		out = append(out, m)
	}
	return out, rows.Err()
}

// deriveProviderModelProjection 与 forwardProtocolEndpointModes 是 models 单表
// supported_endpoint_modes_json 双向投影的 settings 侧内联副本（同源权威在
// internal/providers/endpoint_modes.go 的 DeriveLegacyModeAndProtocols /
// ForwardProtocolsToEndpointModes 与 chat 面 chain_catalog 同名变量；settings
// 不能 import providers——providers→authsys→settings 依赖环——三处任一改动
// 必须同步，防止目录读/写投影漂移）。
var forwardProtocolEndpointModes = map[string][]string{
	"chat_completions":        {"chat_json", "chat_sse"},
	"responses":               {"responses_json", "responses_sse"},
	"messages":                {"messages_json", "messages_sse"},
	"generate_content":        {"generate_content_json", "generate_content_sse"},
	"stream_generate_content": {"generate_content_sse"},
	"interactions":            {"interactions_json", "interactions_sse"},
	"message_token_counting":  {"message_token_counting"},
	"count_tokens":            {"count_tokens"},
	"embed_content":           {"embed_content"},
	"images":                  {"images_json"},
	"video":                   {"video_create", "video_get", "video_content", "video_cancel"},
	"realtime":                {"realtime_session"},
	"audio_speech":            {"audio_speech"},
	"audio_transcription":     {"audio_transcription_json"},
	"completions":             {},
}

var endpointModeLegacyProjection = map[string][][2]string{
	"chat_json":                {{"chat", "chat_completions"}},
	"chat_sse":                 {{"chat", "chat_completions"}},
	"messages_json":            {{"chat", "messages"}},
	"messages_sse":             {{"chat", "messages"}},
	"responses_json":           {{"responses", "responses"}},
	"responses_sse":            {{"responses", "responses"}},
	"generate_content_json":    {{"chat", "generate_content"}},
	"generate_content_sse":     {{"chat", "generate_content"}, {"chat", "stream_generate_content"}},
	"interactions_json":        {{"chat", "interactions"}},
	"interactions_sse":         {{"chat", "interactions"}},
	"images_json":              {{"image_generation", "images"}},
	"video_create":             {{"video", "video"}},
	"video_get":                {{"video", "video"}},
	"video_content":            {{"video", "video"}},
	"video_cancel":             {{"video", "video"}},
	"audio_speech":             {{"audio", "audio_speech"}},
	"audio_transcription_json": {{"audio", "audio_transcription"}},
	"realtime_session":         {{"audio", "realtime"}},
	"message_token_counting":   {{"embedding", "message_token_counting"}},
	"count_tokens":             {{"embedding", "count_tokens"}},
	"embed_content":            {{"embedding", "embed_content"}},
}

var endpointModeLegacyRank = map[string]int{
	"chat": 0, "responses": 1, "embedding": 2, "image_generation": 3, "audio": 4, "video": 5,
}

// deriveProviderModelProjection 把 supported_endpoint_modes_json 逆映射成旧目
// 录行 (mode, supportedApiProtocols) 投影（空 mode 集渲染空 mode 与空数组）。
func deriveProviderModelProjection(endpointModes sql.NullString) (string, []string) {
	var modes []string
	if endpointModes.Valid && strings.TrimSpace(endpointModes.String) != "" {
		var raw []any
		if err := json.Unmarshal([]byte(endpointModes.String), &raw); err == nil {
			for _, item := range raw {
				if text, ok := item.(string); ok {
					modes = append(modes, text)
				}
			}
		}
	}
	seenProtocols := map[string]bool{}
	protocols := []string{}
	bestMode := ""
	bestRank := -1
	for _, mode := range modes {
		for _, pair := range endpointModeLegacyProjection[mode] {
			if rank, known := endpointModeLegacyRank[pair[0]]; known && rank > bestRank {
				bestRank = rank
				bestMode = pair[0]
			}
			if !seenProtocols[pair[1]] {
				seenProtocols[pair[1]] = true
				protocols = append(protocols, pair[1])
			}
		}
	}
	sort.Strings(protocols)
	return bestMode, protocols
}

// forwardProtocolEndpointModesList 把协议列表正向映射为 endpoint mode 集
// （deny-by-default：未知协议返回 false，与 maintenance 种子同拒）。
func forwardProtocolEndpointModesList(protocols []string) ([]string, bool) {
	seen := map[string]bool{}
	modes := []string{}
	for _, protocol := range protocols {
		declared, known := forwardProtocolEndpointModes[protocol]
		if !known {
			return nil, false
		}
		for _, mode := range declared {
			if !seen[mode] {
				seen[mode] = true
				modes = append(modes, mode)
			}
		}
	}
	sort.Strings(modes)
	return modes, true
}

// ReplaceProviderCatalog performs revision-CAS plus obsolete-catalog cleanup
// in one transaction over the models 单表 builtin 行（供应商解耦设计 §3，
// 与 chain_catalog 读侧闭环）：CAS 门读 vendors.updated_at（vendors 为 9 行
// 只读种子，比较不再回写）；整删重插收窄为 DELETE scope='builtin' 的该
// vendor 行后按身份键 (COALESCE(vendor_code,''), scope,
// COALESCE(system_account_id,''), upstream_model) ON CONFLICT 重插。It is a
// complete replacement, not a best-effort patch.
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
		// 协议列表 deny-by-default 正向校验（未知协议与 maintenance 种子同拒）。
		var protocols []string
		if err := json.Unmarshal([]byte(model.SupportedAPIProtocolsJSON), &protocols); err != nil {
			return errors.New("provider catalog model is invalid")
		}
		if _, ok := forwardProtocolEndpointModesList(protocols); !ok {
			return errors.New("provider catalog model is invalid")
		}
		seen[model.Model] = true
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	casGate := "SELECT updated_at FROM " + s.table("vendors") + " WHERE code=? AND updated_at=?"
	var currentUpdatedAt string
	if err := tx.QueryRowContext(ctx, s.bind(casGate), input.ProviderCode, input.ExpectedProviderUpdatedAt).Scan(&currentUpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCAS
		}
		return err
	}
	if _, err = tx.ExecContext(ctx, s.bind("DELETE FROM "+s.table("models")+" WHERE scope='builtin' AND vendor_code=?"), input.ProviderCode); err != nil {
		return err
	}
	for _, model := range input.Models {
		var protocols []string
		if err := json.Unmarshal([]byte(model.SupportedAPIProtocolsJSON), &protocols); err != nil {
			return err
		}
		endpointModes, ok := forwardProtocolEndpointModesList(protocols)
		if !ok {
			return errors.New("provider catalog model is invalid")
		}
		endpointModesJSON, err := json.Marshal(endpointModes)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, s.bind("INSERT INTO "+s.table("models")+"(id,vendor_code,public_model,upstream_model,scope,status,catalog_order,supported_endpoint_modes_json,context_window_tokens,max_input_tokens,max_output_tokens,catalog_visible,source,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT (id) DO UPDATE SET public_model=excluded.public_model, upstream_model=excluded.upstream_model, status=excluded.status, catalog_order=excluded.catalog_order, supported_endpoint_modes_json=excluded.supported_endpoint_modes_json, context_window_tokens=excluded.context_window_tokens, max_input_tokens=excluded.max_input_tokens, max_output_tokens=excluded.max_output_tokens, catalog_visible=excluded.catalog_visible, source=excluded.source, updated_at=excluded.updated_at"), model.ID, input.ProviderCode, model.Model, model.Model, "builtin", model.Status, model.CatalogOrder, string(endpointModesJSON), model.ContextWindowTokens, model.MaxInputTokens, model.MaxOutputTokens, s.boolBind(model.CatalogVisible), model.Source, now, now)
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

// boolBind 返回 catalog_visible 的方言绑定值：该列在 PG 为 boolean、
// 在 SQLite 为 INTEGER 0/1，pgx 无法把整数编码进 boolean 列。
func (s *Store) boolBind(v bool) any {
	if s.mode == Postgres {
		return v
	}
	if v {
		return 1
	}
	return 0
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
