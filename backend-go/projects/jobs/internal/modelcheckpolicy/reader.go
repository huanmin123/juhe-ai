// Package modelcheckpolicy reads the J3b effective quality policy directly
// from the business database. It is read-only and has no Node/IPC fallback.
package modelcheckpolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
)

type Reader struct {
	db          *sql.DB
	tablePrefix string
	postgres    bool
}

func NewPostgresReader(db *sql.DB) (*Reader, error) {
	return newReader(db, "juhe_business.", true)
}

func NewSQLiteReader(db *sql.DB) (*Reader, error) {
	return newReader(db, "", false)
}

func newReader(db *sql.DB, tablePrefix string, postgres bool) (*Reader, error) {
	if db == nil {
		return nil, errors.New("model check policy database is required")
	}
	return &Reader{db: db, tablePrefix: tablePrefix, postgres: postgres}, nil
}

func (r *Reader) CheckContract(ctx context.Context) error {
	if r == nil || r.db == nil {
		return errors.New("model check policy reader is not initialized")
	}
	tx, err := r.beginReadOnly(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `SELECT system_account_id,revision,profile,manual_enforcement_enabled,penalty_threshold,penalty_action,recovery_interval_minutes FROM ` + r.tablePrefix + `model_quality_policies WHERE ` + r.falsePredicate()
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("verify model check policy reader contract: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close model check policy reader contract: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit model check policy reader contract: %w", err)
	}
	return nil
}

// Load returns the Node-equivalent default when no explicit policy exists.
// Callers persist the returned value in the immutable J3b input before probe
// execution, so later policy edits cannot change an already-issued outcome.
func (r *Reader) Load(ctx context.Context, systemAccountID string) (modelcheckinput.PolicySnapshot, error) {
	if r == nil || r.db == nil {
		return modelcheckinput.PolicySnapshot{}, errors.New("model check policy reader is not initialized")
	}
	systemAccountID = strings.TrimSpace(systemAccountID)
	if systemAccountID == "" {
		return modelcheckinput.PolicySnapshot{}, errors.New("model check policy system account is required")
	}
	tx, err := r.beginReadOnly(ctx)
	if err != nil {
		return modelcheckinput.PolicySnapshot{}, err
	}
	defer tx.Rollback()
	query := `SELECT revision,profile,manual_enforcement_enabled,penalty_threshold,penalty_action,recovery_interval_minutes FROM ` + r.tablePrefix + `model_quality_policies WHERE system_account_id=` + r.placeholder(1) + ` LIMIT 1`
	var revision, profile, action string
	var manual bool
	var threshold, recovery int
	err = tx.QueryRowContext(ctx, query, systemAccountID).Scan(&revision, &profile, &manual, &threshold, &action, &recovery)
	if errors.Is(err, sql.ErrNoRows) {
		policy, policyErr := modelcheckinput.NewPolicySnapshot("0", "quick", true, 70, "fallback", 10)
		if policyErr != nil {
			return modelcheckinput.PolicySnapshot{}, policyErr
		}
		if err := tx.Commit(); err != nil {
			return modelcheckinput.PolicySnapshot{}, fmt.Errorf("commit default model check policy read: %w", err)
		}
		return policy, nil
	}
	if err != nil {
		return modelcheckinput.PolicySnapshot{}, fmt.Errorf("read model check policy: %w", err)
	}
	policy, err := modelcheckinput.NewPolicySnapshot(revision, profile, manual, threshold, action, recovery)
	if err != nil {
		return modelcheckinput.PolicySnapshot{}, fmt.Errorf("validate model check policy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return modelcheckinput.PolicySnapshot{}, fmt.Errorf("commit model check policy read: %w", err)
	}
	return policy, nil
}

func (r *Reader) beginReadOnly(ctx context.Context) (*sql.Tx, error) {
	options := &sql.TxOptions{ReadOnly: true}
	if r != nil && r.postgres {
		options.Isolation = sql.LevelRepeatableRead
	}
	tx, err := r.db.BeginTx(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("open model check policy read transaction: %w", err)
	}
	if r.postgres {
		if _, err := tx.ExecContext(ctx, "SET LOCAL TRANSACTION READ ONLY"); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("set model check policy transaction read-only: %w", err)
		}
	}
	return tx, nil
}

func (r *Reader) falsePredicate() string {
	if r != nil && r.postgres {
		return "FALSE"
	}
	return "0"
}

func (r *Reader) placeholder(position int) string {
	if r != nil && r.postgres {
		return "$" + strconv.Itoa(position)
	}
	return "?"
}
