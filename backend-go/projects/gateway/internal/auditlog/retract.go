package auditlog

import (
	"context"
	"errors"
)

// RetractInProgress deletes one lifecycle_status='in_progress' envelope row
// (W13): a capture canceled after the envelope was written leaves a phantom
// shell unless the row is retracted at request end. The delete is guarded by
// the lifecycle status, so unknown ids and rows that a racing finalize already
// converted to 'finalized' are no-ops (the terminal row always wins over the
// retraction). Payload blobs are not touched: in_progress envelopes are
// metadata-only with zero payload references.
func (s *sqlStore) RetractInProgress(ctx context.Context, lease OwnerLease, auditLogID string) (bool, error) {
	if auditLogID == "" {
		return false, nil
	}
	if err := s.EnsureSchema(ctx); err != nil {
		return false, err
	}
	if s.mode == ModeSQLite {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := s.verifyLeaseTx(ctx, tx, lease); err != nil {
		return false, errors.Join(err, tx.Rollback())
	}
	if err := s.lockAuditLogLifecycleTx(ctx, tx, auditLogID); err != nil {
		return false, errors.Join(err, tx.Rollback())
	}
	result, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("audit_logs")+` WHERE id = ? AND lifecycle_status = 'in_progress'`), auditLogID)
	if err != nil {
		return false, errors.Join(err, tx.Rollback())
	}
	matched, err := result.RowsAffected()
	if err != nil {
		return false, errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	committed = true
	return matched == 1, nil
}
