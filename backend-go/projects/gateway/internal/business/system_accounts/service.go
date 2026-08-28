// Package systemaccounts owns the Gateway Business transaction primitives for
// system-account management.  It deliberately contains no HTTP, Node, IPC,
// queue, cache, or schema-creation code.
package systemaccounts

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Mode selects the SQL adapter shape.  The business rules are shared; only
// table qualification, placeholders, and the PostgreSQL row lock differ.
type Mode string

const (
	SQLite   Mode = "sqlite"
	Postgres Mode = "postgres"
)

// OwnerGate is external handoff evidence.  A partial handoff never permits a
// Business mutation, even when the database and all relations are reachable.
type OwnerGate struct {
	Confirmed         bool
	SchemaReady       bool
	NodeWriterStopped bool
}

func (g OwnerGate) Ready() bool {
	return g.Confirmed && g.SchemaReady && g.NodeWriterStopped
}

var (
	ErrOwnerGate      = errors.New("Business SQLite owner handoff gate is not satisfied")
	ErrNotFound       = errors.New("system account not found")
	ErrCAS            = errors.New("system account revision conflict")
	ErrLastSuperAdmin = errors.New("at least one active super_admin must remain")
	ErrInvalidInput   = errors.New("system account input is invalid")
	ErrSecretCipher   = errors.New("system account default API key secret cipher is required")
	ErrContract       = errors.New("system account Business contract is not satisfied")
	ErrInvalidMode    = errors.New("system account database mode is invalid")
	ErrInvalidSchema  = errors.New("system account PostgreSQL schema is invalid")
)

// SecretCipher is an explicit boundary for the configured Node-compatible
// secret encryption. Encrypt receives the generated raw API-key bytes; the
// injected adapter owns the deployment's Node-compatible envelope format.
// The generated API-key secret is never returned or logged; Create fails
// closed if this capability is not supplied.
type SecretCipher interface {
	Encrypt(context.Context, []byte) (string, error)
}

type Store struct {
	db     *sql.DB
	mode   Mode
	schema string
	gate   OwnerGate
	now    func() time.Time
	cipher SecretCipher
}

// NewStore constructs an isolated Business SQL owner.  It never creates or
// alters schema.  A nil cipher is accepted so read-only operations can still
// be exercised, but Create returns ErrSecretCipher rather than storing a
// plaintext default key.
func NewStore(db *sql.DB, mode Mode, schema string, gate OwnerGate, cipher SecretCipher) (*Store, error) {
	if db == nil {
		return nil, errors.New("system accounts database is required")
	}
	if mode != SQLite && mode != Postgres {
		return nil, ErrInvalidMode
	}
	if mode == Postgres {
		if schema == "" {
			schema = "juhe_business"
		}
		if !postgresIdentifier.MatchString(schema) {
			return nil, ErrInvalidSchema
		}
	}
	return &Store{db: db, mode: mode, schema: schema, gate: gate, now: time.Now, cipher: cipher}, nil
}

// New is the service-shaped constructor used by future Gateway assembly.
func New(db *sql.DB, mode Mode, schema string, gate OwnerGate, cipher SecretCipher) (*Service, error) {
	store, err := NewStore(db, mode, schema, gate, cipher)
	if err != nil {
		return nil, err
	}
	return &Service{store: store}, nil
}

// Service keeps handlers from reaching through to raw SQL.  It is intentionally
// a thin forwarding layer; transaction semantics remain in Store.
type Service struct{ store *Store }

func (s *Service) CheckContract(ctx context.Context) error { return s.store.CheckContract(ctx) }
func (s *Service) List(ctx context.Context, options ListOptions) (ListResult, error) {
	return s.store.List(ctx, options)
}
func (s *Service) Options(ctx context.Context, options OptionListOptions) ([]Option, error) {
	return s.store.Options(ctx, options)
}
func (s *Service) Create(ctx context.Context, input CreateInput) (SystemAccount, error) {
	return s.store.Create(ctx, input)
}
func (s *Service) PatchCAS(ctx context.Context, id string, patch Patch) (PatchResult, error) {
	return s.store.PatchCAS(ctx, id, patch)
}

type Port interface {
	CheckContract(context.Context) error
	List(context.Context, ListOptions) (ListResult, error)
	Options(context.Context, OptionListOptions) ([]Option, error)
	Create(context.Context, CreateInput) (SystemAccount, error)
	PatchCAS(context.Context, string, Patch) (PatchResult, error)
}

var _ Port = (*Store)(nil)
var _ Port = (*Service)(nil)

// CoveredManifestOperations records the frozen Node management operations
// covered by this isolated primitive package.  It is descriptive only; the
// migration manifest remains outside this package and is not modified here.
var CoveredManifestOperations = []string{
	"list_system_accounts",
	"list_system_account_options",
	"create_system_account",
	"patch_system_account_cas",
}

// SystemAccount intentionally excludes password_hash and all generated API
// key material.  It is safe to pass across a business/service boundary.
type SystemAccount struct {
	ID                     string  `json:"id"`
	Username               string  `json:"username"`
	DisplayName            string  `json:"displayName"`
	Description            *string `json:"description,omitempty"`
	Role                   string  `json:"role"`
	Status                 string  `json:"status"`
	MustChangePassword     bool    `json:"mustChangePassword"`
	ImageGenerationEnabled bool    `json:"imageGenerationEnabled"`
	AIAccountLimit         *int64  `json:"aiAccountLimit,omitempty"`
	RequestLimitsJSON      *string `json:"requestLimitsJson,omitempty"`
	LastLoginAt            *string `json:"lastLoginAt,omitempty"`
	CreatedAt              string  `json:"createdAt"`
	UpdatedAt              string  `json:"updatedAt"`
}

type ListItem struct {
	ID                     string  `json:"id"`
	Username               string  `json:"username"`
	DisplayName            string  `json:"displayName"`
	Description            *string `json:"description,omitempty"`
	Role                   string  `json:"role"`
	Status                 string  `json:"status"`
	MustChangePassword     bool    `json:"mustChangePassword"`
	ImageGenerationEnabled bool    `json:"imageGenerationEnabled"`
	AIAccountLimit         *int64  `json:"aiAccountLimit,omitempty"`
	RequestLimitsJSON      *string `json:"requestLimitsJson,omitempty"`
	LastLoginAt            *string `json:"lastLoginAt,omitempty"`
	EditVersion            string  `json:"editVersion"`
}

type ListResult struct {
	Items    []ListItem `json:"items"`
	Total    int        `json:"total"`
	HasMore  bool       `json:"hasMore"`
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
}

type Option struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DisabledReason string `json:"disabledReason,omitempty"`
}

type ListOptions struct {
	Page     int
	PageSize int
	Keyword  string
}

type OptionListOptions struct {
	IDs     []string
	Keyword string
	Limit   int
}

// CreateInput accepts a pre-hashed password.  Plaintext passwords are not a
// durable primitive input and therefore do not cross this package boundary.
type CreateInput struct {
	ID                     string
	Username               string
	DisplayName            string
	Description            *string
	PasswordHash           string
	Role                   string
	Status                 string
	MustChangePassword     *bool
	ImageGenerationEnabled *bool
	AIAccountLimit         *int64
	RequestLimitsJSON      *string
}

// Patch uses pointer-to-pointer fields where null must be distinguishable from
// omission.  An actual password change or transition to disabled revokes all
// target sessions in the same transaction.
type Patch struct {
	ExpectedUpdatedAt      string
	DisplayName            *string
	Description            **string
	PasswordHash           *string
	Role                   *string
	Status                 *string
	MustChangePassword     *bool
	ImageGenerationEnabled *bool
	AIAccountLimit         **int64
	RequestLimitsJSON      **string
}

type Change struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

type PatchResult struct {
	Kind                string        `json:"kind"`
	Account             SystemAccount `json:"account"`
	Changes             []Change      `json:"changes"`
	RevokedSessionCount int64         `json:"revokedSessionCount"`
}
