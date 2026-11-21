// Source-revision material-change hook (authorization governance §5.9).
package accounts

import (
	"context"
	"database/sql"
)

// SourceRevisionMarker is implemented by the authz store. The interface keeps
// the accounts package independent of the authz package (composition wires it).
type SourceRevisionMarker interface {
	MarkAccountSourcesNeedsReview(ctx context.Context, tx *sql.Tx, accountID, now string) error
}

// SetSourceRevisionMarker wires the material-change marker.
func (s *Store) SetSourceRevisionMarker(marker SourceRevisionMarker) {
	s.sourceRevisionMarker = marker
}

func (s *Store) markSourceRevision(ctx context.Context, tx *sql.Tx, accountID, now string) error {
	if s.sourceRevisionMarker == nil || tx == nil {
		return nil
	}
	return s.sourceRevisionMarker.MarkAccountSourcesNeedsReview(ctx, tx, accountID, now)
}
