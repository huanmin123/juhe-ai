package accounts

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsRetryableAccountCreateTxError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"serialization", &pgconn.PgError{Code: "40001"}, true},
		{"wrapped deadlock", fmt.Errorf("accounts slice internal error: %w", &pgconn.PgError{Code: "40P01"}), true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"validation", errors.New("能力域无效"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := isRetryableAccountCreateTxError(tc.err); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
