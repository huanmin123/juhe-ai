package accounts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type Account struct {
	ID                        string          `json:"id"`
	SystemAccountID           string          `json:"systemAccountId"`
	ProviderCode              string          `json:"providerCode"`
	ProviderProtocolProfileID string          `json:"providerProtocolProfileId"`
	ProtocolCode              string          `json:"protocolCode"`
	ProtocolVersion           string          `json:"protocolVersion"`
	Name                      string          `json:"name"`
	Type                      string          `json:"type"`
	Status                    string          `json:"status"`
	ConfigRevision            int64           `json:"configRevision"`
	DispatchRevision          int64           `json:"dispatchRevision"`
	Schedulable               bool            `json:"schedulable"`
	AvailabilityScheduleJSON  *string         `json:"availabilityScheduleJson,omitempty"`
	CreatedAt                 string          `json:"createdAt"`
	UpdatedAt                 string          `json:"updatedAt"`
	SupportedModels           []string        `json:"supportedModels,omitempty"`
	ModelMappings             []ModelMapping  `json:"modelMappings,omitempty"`
	Tags                      []string        `json:"tags,omitempty"`
	APIKeyBindings            []APIKeyBinding `json:"apiKeyBindings,omitempty"`
}

type ModelMapping struct {
	SourceModel            string `json:"sourceModel"`
	SourceEndpointFamily   string `json:"sourceEndpointFamily"`
	UpstreamModel          string `json:"upstreamModel"`
	UpstreamEndpointFamily string `json:"upstreamEndpointFamily"`
	Enabled                bool   `json:"enabled"`
}

type APIKeyBinding struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
}

type CreateInput struct {
	ID, SystemAccountID, ProviderCode, ProviderProtocolProfileID, ProtocolCode, ProtocolVersion string
	Name, Type, Status, CredentialsEncrypted                                                    string
	Schedulable                                                                                 bool
	AvailabilityScheduleJSON                                                                    *string
	SupportedModels                                                                             []string
	ModelMappings                                                                               []ModelMapping
	Tags                                                                                        []string
	APIKeyBindings                                                                              []APIKeyBinding
}

type Patch struct {
	ExpectedConfigRevision   int64
	Name                     *string
	Status                   *string
	Schedulable              *bool
	CredentialsEncrypted     *string
	AvailabilityScheduleJSON **string
	SupportedModels          *[]string
	ModelMappings            *[]ModelMapping
	Tags                     *[]string
	APIKeyBindings           *[]APIKeyBinding
}

type Port interface {
	CheckContract(context.Context) error
	Create(context.Context, CreateInput) (Account, error)
	Get(context.Context, string, string) (Account, error)
	List(context.Context, string) ([]Account, error)
	Patch(context.Context, string, string, Patch) (Account, error)
	Delete(context.Context, string, string, int64) (Account, error)
}

type Service struct{ store *Store }

func New(db *sql.DB, postgres bool, gate OwnerGate) (*Service, error) {
	s, err := NewStore(db, postgres, gate)
	if err != nil {
		return nil, err
	}
	return &Service{store: s}, nil
}

var _ Port = (*Service)(nil)

func (s *Service) CheckContract(ctx context.Context) error { return s.store.CheckContract(ctx) }
func (s *Service) Create(ctx context.Context, in CreateInput) (Account, error) {
	return s.store.create(ctx, in)
}
func (s *Service) Get(ctx context.Context, systemID, id string) (Account, error) {
	return s.store.get(ctx, systemID, id)
}
func (s *Service) List(ctx context.Context, systemID string) ([]Account, error) {
	return s.store.list(ctx, systemID)
}
func (s *Service) Patch(ctx context.Context, systemID, id string, p Patch) (Account, error) {
	return s.store.patch(ctx, systemID, id, p)
}
func (s *Service) Delete(ctx context.Context, systemID, id string, expected int64) (Account, error) {
	return s.store.delete(ctx, systemID, id, expected)
}

func normalize(in CreateInput) error {
	if strings.TrimSpace(in.ID) == "" || strings.TrimSpace(in.SystemAccountID) == "" || strings.TrimSpace(in.ProviderCode) == "" || strings.TrimSpace(in.ProviderProtocolProfileID) == "" || strings.TrimSpace(in.ProtocolCode) == "" || strings.TrimSpace(in.ProtocolVersion) == "" || strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Type) == "" || strings.TrimSpace(in.CredentialsEncrypted) == "" {
		return errors.New("account identity and credentials are required")
	}
	if in.Status == "" {
		return errors.New("account status is required")
	}
	return nil
}

func (s *Store) create(ctx context.Context, in CreateInput) (Account, error) {
	if err := s.requireOwner(); err != nil {
		return Account{}, err
	}
	if err := normalize(in); err != nil {
		return Account{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	now := s.stamp()
	q := `INSERT INTO ` + s.table("accounts") + ` (id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,protocol_version,name,type,status,credentials_encrypted,config_revision,dispatch_revision,schedulable,availability_schedule_json,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,1,1,?,?,?,?)`
	_, err = tx.ExecContext(ctx, s.bind(q), in.ID, in.SystemAccountID, in.ProviderCode, in.ProviderProtocolProfileID, in.ProtocolCode, in.ProtocolVersion, in.Name, in.Type, in.Status, in.CredentialsEncrypted, boolInt(in.Schedulable), nullableString(in.AvailabilityScheduleJSON), now, now)
	if err != nil {
		// Client retries use the caller-provided account ID as a receipt key.
		// Returning an existing semantically identical aggregate is safe; a
		// different payload remains visible as a conflict.
		_ = tx.Rollback()
		current, readErr := s.get(ctx, in.SystemAccountID, in.ID)
		if readErr == nil && equivalentCreate(current, in) {
			return current, nil
		}
		if readErr == nil {
			return Account{}, fmt.Errorf("%w: account id already has different content", ErrRevisionConflict)
		}
		return Account{}, err
	}
	if err := writeRelations(ctx, tx, s, in.ID, in.SystemAccountID, in.ProviderCode, in.SupportedModels, in.ModelMappings, in.Tags, in.APIKeyBindings, now); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	return s.get(ctx, in.SystemAccountID, in.ID)
}

func (s *Store) get(ctx context.Context, systemID, id string) (Account, error) {
	if err := s.requireOwner(); err != nil {
		return Account{}, err
	}
	var a Account
	var sched int
	var av sql.NullString
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT id,system_account_id,provider_code,provider_protocol_profile_id,protocol_code,protocol_version,name,type,status,config_revision,dispatch_revision,schedulable,availability_schedule_json,created_at,updated_at FROM `+s.table("accounts")+` WHERE id=? AND system_account_id=? AND deleted_at IS NULL`), id, systemID).Scan(&a.ID, &a.SystemAccountID, &a.ProviderCode, &a.ProviderProtocolProfileID, &a.ProtocolCode, &a.ProtocolVersion, &a.Name, &a.Type, &a.Status, &a.ConfigRevision, &a.DispatchRevision, &sched, &av, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	a.Schedulable = sched != 0
	if av.Valid {
		a.AvailabilityScheduleJSON = &av.String
	}
	if err := readRelations(ctx, s.db, s, &a); err != nil {
		return Account{}, err
	}
	return a, nil
}

func (s *Store) list(ctx context.Context, systemID string) ([]Account, error) {
	if err := s.requireOwner(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT id FROM `+s.table("accounts")+` WHERE system_account_id=? AND deleted_at IS NULL ORDER BY id`), systemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		a, err := s.get(ctx, systemID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) patch(ctx context.Context, systemID, id string, p Patch) (Account, error) {
	if err := s.requireOwner(); err != nil {
		return Account{}, err
	}
	if p.ExpectedConfigRevision < 1 {
		return Account{}, errors.New("expected config revision is required")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT config_revision FROM `+s.table("accounts")+` WHERE id=? AND system_account_id=? AND deleted_at IS NULL`), id, systemID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	} else if err != nil {
		return Account{}, err
	}
	if current != p.ExpectedConfigRevision {
		return Account{}, fmt.Errorf("%w: expected %d actual %d", ErrRevisionConflict, p.ExpectedConfigRevision, current)
	}
	sets := []string{}
	args := []any{}
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			return Account{}, errors.New("account name is required")
		}
		sets = append(sets, "name=?")
		args = append(args, *p.Name)
	}
	if p.Status != nil {
		if strings.TrimSpace(*p.Status) == "" {
			return Account{}, errors.New("account status is required")
		}
		sets = append(sets, "status=?")
		args = append(args, *p.Status)
	}
	if p.Schedulable != nil {
		sets = append(sets, "schedulable=?")
		args = append(args, boolInt(*p.Schedulable))
	}
	if p.CredentialsEncrypted != nil {
		if strings.TrimSpace(*p.CredentialsEncrypted) == "" {
			return Account{}, errors.New("encrypted credentials are required")
		}
		sets = append(sets, "credentials_encrypted=?")
		args = append(args, *p.CredentialsEncrypted)
	}
	if p.AvailabilityScheduleJSON != nil {
		sets = append(sets, "availability_schedule_json=?")
		args = append(args, nullableString(*p.AvailabilityScheduleJSON))
	}
	now := s.stamp()
	baseChanged := len(sets) > 0
	if baseChanged {
		sets = append(sets, "config_revision=config_revision+1", "updated_at=?")
		args = append(args, now, id, systemID, current)
		q := `UPDATE ` + s.table("accounts") + ` SET ` + strings.Join(sets, ",") + ` WHERE id=? AND system_account_id=? AND config_revision=? AND deleted_at IS NULL`
		res, err := tx.ExecContext(ctx, s.bind(q), args...)
		if err != nil {
			return Account{}, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return Account{}, ErrRevisionConflict
		}
	}
	if p.SupportedModels != nil || p.ModelMappings != nil || p.Tags != nil || p.APIKeyBindings != nil {
		if err := writeRelationsPatch(ctx, tx, s, id, systemID, p, now); err != nil {
			return Account{}, err
		}
		if !baseChanged {
			result, updateErr := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET config_revision=config_revision+1,updated_at=? WHERE id=? AND system_account_id=? AND config_revision=? AND deleted_at IS NULL`), now, id, systemID, current)
			if updateErr != nil {
				return Account{}, updateErr
			}
			rows, _ := result.RowsAffected()
			if rows != 1 {
				return Account{}, ErrRevisionConflict
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	return s.get(ctx, systemID, id)
}

func (s *Store) delete(ctx context.Context, systemID, id string, expected int64) (Account, error) {
	if err := s.requireOwner(); err != nil {
		return Account{}, err
	}
	if expected < 1 {
		return Account{}, errors.New("expected config revision is required")
	}
	a, err := s.get(ctx, systemID, id)
	if err != nil {
		return Account{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	now := s.stamp()
	res, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("accounts")+` SET status='disabled',schedulable=0,deleted_at=?,deleted_by=?,config_revision=config_revision+1,dispatch_revision=dispatch_revision+1,updated_at=? WHERE id=? AND system_account_id=? AND config_revision=? AND deleted_at IS NULL`), now, systemID, now, id, systemID, expected)
	if err != nil {
		return Account{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return Account{}, ErrRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	a.Status = "disabled"
	a.Schedulable = false
	a.ConfigRevision = expected + 1
	a.DispatchRevision++
	a.UpdatedAt = now
	return a, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func nullableString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func writeRelations(ctx context.Context, tx *sql.Tx, s *Store, id, systemID, provider string, models []string, mappings []ModelMapping, tags []string, keys []APIKeyBinding, now string) error {
	for _, m := range models {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_supported_models")+` (account_id,provider_code,model,created_at) VALUES (?,?,?,?)`), id, provider, m, now); err != nil {
			return err
		}
	}
	for _, m := range mappings {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_model_mappings")+` (account_id,provider_code,source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`), id, provider, m.SourceModel, m.SourceEndpointFamily, m.UpstreamModel, m.UpstreamEndpointFamily, boolInt(m.Enabled), now, now); err != nil {
			return err
		}
	}
	for _, name := range unique(tags) {
		h := sha256.Sum256([]byte(systemID + "\x00" + name))
		tid := "acctag-" + hex.EncodeToString(h[:12])
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_tags")+` (id,system_account_id,name,created_at,updated_at) VALUES (?,?,?,?,?) ON CONFLICT (system_account_id,name) DO UPDATE SET updated_at=excluded.updated_at`), tid, systemID, name, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_tag_bindings")+` (account_id,tag_id,system_account_id,created_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`), id, tid, systemID, now); err != nil {
			return err
		}
	}
	for _, k := range keys {
		if strings.TrimSpace(k.ID) == "" || strings.TrimSpace(k.Fingerprint) == "" {
			return errors.New("api key binding identity is required")
		}
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_api_key_runtime_states")+` (id,system_account_id,account_id,key_fingerprint,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`), k.ID, systemID, id, k.Fingerprint, defaultStatus(k.Status), now, now); err != nil {
			return err
		}
	}
	return nil
}
func writeRelationsPatch(ctx context.Context, tx *sql.Tx, s *Store, id, systemID string, p Patch, now string) error {
	if p.SupportedModels != nil {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_supported_models")+` WHERE account_id=?`), id); err != nil {
			return err
		}
		var provider string
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT provider_code FROM `+s.table("accounts")+` WHERE id=?`), id).Scan(&provider); err != nil {
			return err
		}
		for _, m := range *p.SupportedModels {
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_supported_models")+` (account_id,provider_code,model,created_at) VALUES (?,?,?,?)`), id, provider, m, now); err != nil {
				return err
			}
		}
	}
	if p.ModelMappings != nil {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_model_mappings")+` WHERE account_id=?`), id); err != nil {
			return err
		}
		var provider string
		if err := tx.QueryRowContext(ctx, s.bind(`SELECT provider_code FROM `+s.table("accounts")+` WHERE id=?`), id).Scan(&provider); err != nil {
			return err
		}
		for _, m := range *p.ModelMappings {
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_model_mappings")+` (account_id,provider_code,source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`), id, provider, m.SourceModel, m.SourceEndpointFamily, m.UpstreamModel, m.UpstreamEndpointFamily, boolInt(m.Enabled), now, now); err != nil {
				return err
			}
		}
	}
	if p.Tags != nil {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_tag_bindings")+` WHERE account_id=?`), id); err != nil {
			return err
		}
		if err := writeRelations(ctx, tx, s, id, systemID, "", nil, nil, *p.Tags, nil, now); err != nil {
			return err
		}
	}
	if p.APIKeyBindings != nil {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("account_api_key_runtime_states")+` WHERE account_id=?`), id); err != nil {
			return err
		}
		for _, k := range *p.APIKeyBindings {
			if strings.TrimSpace(k.ID) == "" || strings.TrimSpace(k.Fingerprint) == "" {
				return errors.New("api key binding identity is required")
			}
			if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_api_key_runtime_states")+` (id,system_account_id,account_id,key_fingerprint,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?)`), k.ID, systemID, id, k.Fingerprint, defaultStatus(k.Status), now, now); err != nil {
				return err
			}
		}
	}
	return nil
}
func readRelations(ctx context.Context, db *sql.DB, s *Store, a *Account) error {
	rows, err := db.QueryContext(ctx, s.bind(`SELECT model FROM `+s.table("account_supported_models")+` WHERE account_id=? ORDER BY model`), a.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			rows.Close()
			return err
		}
		a.SupportedModels = append(a.SupportedModels, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, s.bind(`SELECT source_model,source_endpoint_family,upstream_model,upstream_endpoint_family,enabled FROM `+s.table("account_model_mappings")+` WHERE account_id=? ORDER BY source_model,source_endpoint_family`), a.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var m ModelMapping
		var e int
		if err := rows.Scan(&m.SourceModel, &m.SourceEndpointFamily, &m.UpstreamModel, &m.UpstreamEndpointFamily, &e); err != nil {
			rows.Close()
			return err
		}
		m.Enabled = e != 0
		a.ModelMappings = append(a.ModelMappings, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, s.bind(`SELECT t.name FROM `+s.table("account_tag_bindings")+` b JOIN `+s.table("account_tags")+` t ON t.id=b.tag_id WHERE b.account_id=? AND b.system_account_id=? ORDER BY t.name,t.id`), a.ID, a.SystemAccountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			rows.Close()
			return err
		}
		a.Tags = append(a.Tags, tag)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, s.bind(`SELECT id,key_fingerprint,status FROM `+s.table("account_api_key_runtime_states")+` WHERE account_id=? AND system_account_id=? ORDER BY id`), a.ID, a.SystemAccountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key APIKeyBinding
		if err := rows.Scan(&key.ID, &key.Fingerprint, &key.Status); err != nil {
			rows.Close()
			return err
		}
		a.APIKeyBindings = append(a.APIKeyBindings, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	return nil
}

func equivalentCreate(a Account, in CreateInput) bool {
	if a.ID != in.ID || a.SystemAccountID != in.SystemAccountID || a.ProviderCode != in.ProviderCode || a.ProviderProtocolProfileID != in.ProviderProtocolProfileID || a.ProtocolCode != in.ProtocolCode || a.ProtocolVersion != in.ProtocolVersion || a.Name != in.Name || a.Type != in.Type || a.Status != in.Status || a.Schedulable != in.Schedulable || !sameOptionalString(a.AvailabilityScheduleJSON, in.AvailabilityScheduleJSON) {
		return false
	}
	return sameStrings(a.SupportedModels, in.SupportedModels) && sameMappings(a.ModelMappings, in.ModelMappings) && sameStrings(a.Tags, in.Tags) && sameKeys(a.APIKeyBindings, in.APIKeyBindings)
}

func sameOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func sameStrings(a, b []string) bool {
	a, b = sortedStrings(unique(a)), sortedStrings(unique(b))
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func sameMappings(a, b []ModelMapping) bool {
	a, b = sortedMappings(a), sortedMappings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func sameKeys(a, b []APIKeyBinding) bool {
	for index := range a {
		a[index].Status = defaultStatus(a[index].Status)
	}
	for index := range b {
		b[index].Status = defaultStatus(b[index].Status)
	}
	a, b = sortedKeys(a), sortedKeys(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
func sortedMappings(values []ModelMapping) []ModelMapping {
	out := append([]ModelMapping(nil), values...)
	sort.Slice(out, func(i, j int) bool { return mappingKey(out[i]) < mappingKey(out[j]) })
	return out
}
func sortedKeys(values []APIKeyBinding) []APIKeyBinding {
	out := append([]APIKeyBinding(nil), values...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func mappingKey(m ModelMapping) string {
	return m.SourceModel + "\x00" + m.SourceEndpointFamily + "\x00" + m.UpstreamModel + "\x00" + m.UpstreamEndpointFamily + fmt.Sprintf("\x00%t", m.Enabled)
}
func unique(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
func defaultStatus(v string) string {
	if strings.TrimSpace(v) == "" {
		return "active"
	}
	return v
}
