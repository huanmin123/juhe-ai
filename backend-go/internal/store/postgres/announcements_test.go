package postgres

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/store/postgres/postgresqueries"
)

func TestAnnouncementQueriesPreserveVisibilityPaginationAndTransactions(t *testing.T) {
	raw, err := os.ReadFile("queries/w8_announcements.sql")
	if err != nil {
		t.Fatalf("read announcement queries: %v", err)
	}
	sql := string(raw)

	for _, required := range []string{
		"-- name: ListPublicAnnouncements :many",
		"a.status = 'published'",
		"a.published_at IS NOT NULL",
		"ORDER BY a.published_at DESC, a.created_at DESC, a.id DESC",
		"LEFT JOIN juhe_business.announcement_reads ar",
		"-- name: MarkVisibleAnnouncementsRead :many",
		"ON CONFLICT (announcement_id, system_account_id)",
		"DO UPDATE SET read_at = EXCLUDED.read_at",
		"-- name: ListManagementAnnouncements :many",
		"CAST(CASE WHEN char_length(a.content) > 240 THEN substr(a.content, 1, 240) || '...' ELSE a.content END AS text) AS content",
		"ORDER BY a.updated_at DESC, a.created_at DESC, a.id DESC",
		"LEFT JOIN juhe_business.system_accounts creator",
		"LEFT JOIN juhe_business.system_accounts updater",
		"-- name: FindManagementAnnouncement :one",
		"-- name: CreateAnnouncement :one",
		"-- name: UpdateAnnouncement :one",
		"-- name: PublishAnnouncement :one",
		"-- name: ArchiveAnnouncement :one",
		"-- name: DeleteAnnouncement :execrows",
		"-- name: DeleteAnnouncementReads :execrows",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("announcement queries missing %q", required)
		}
	}
	if got := strings.Count(sql, "CAST(CASE WHEN char_length(a.content) > 240 THEN substr(a.content, 1, 240) || '...' ELSE a.content END AS text) AS content"); got != 1 {
		t.Fatalf("announcement management content summary expressions = %d, want 1", got)
	}
}

func TestNormalizeAnnouncementBounds(t *testing.T) {
	for _, test := range []struct {
		limit int
		want  int
	}{
		{limit: -1, want: 30},
		{limit: 0, want: 30},
		{limit: 1, want: 1},
		{limit: 30, want: 30},
		{limit: 31, want: 30},
	} {
		if got := normalizeAnnouncementLimit(test.limit); got != test.want {
			t.Fatalf("normalizeAnnouncementLimit(%d) = %d, want %d", test.limit, got, test.want)
		}
	}

	for _, test := range []struct {
		page, pageSize int
		wantPage       int
		wantPageSize   int
	}{
		{page: 0, pageSize: 0, wantPage: 1, wantPageSize: 50},
		{page: 2, pageSize: 1, wantPage: 2, wantPageSize: 1},
		{page: 3, pageSize: 101, wantPage: 3, wantPageSize: 100},
		{page: int(^uint(0) >> 1), pageSize: 100, wantPage: 10, wantPageSize: 100},
		{page: int(^uint(0) >> 1), pageSize: 1, wantPage: 1000, wantPageSize: 1},
		{page: 11, pageSize: 100, wantPage: 10, wantPageSize: 100},
		{page: 1001, pageSize: 1, wantPage: 1000, wantPageSize: 1},
		{page: 1000, pageSize: 50, wantPage: 20, wantPageSize: 50},
	} {
		page, pageSize := normalizeAnnouncementPage(test.page, test.pageSize)
		if page != test.wantPage || pageSize != test.wantPageSize {
			t.Fatalf("normalizeAnnouncementPage(%d, %d) = (%d, %d), want (%d, %d)", test.page, test.pageSize, page, pageSize, test.wantPage, test.wantPageSize)
		}
	}
}

func TestAnnouncementInTxBindsTransactionalQueriesAndPropagatesCallbackError(t *testing.T) {
	wantErr := errors.New("stop announcement transaction")
	queries := postgresqueries.New(nil)
	inTxCalled := false
	callbackCalled := false

	err := announcementInTx(t.Context(), func(ctx context.Context, fn TxFunc) error {
		inTxCalled = true
		return fn(ctx, queries)
	}, func(_ context.Context, store port.AnnouncementTxStore) error {
		callbackCalled = true
		txStore, ok := store.(announcementTxStore)
		if !ok || txStore.queries != queries {
			t.Fatalf("announcement tx store = %#v, want transactional queries %p", store, queries)
		}
		return wantErr
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("announcementInTx error = %v, want %v", err, wantErr)
	}
	if !inTxCalled || !callbackCalled {
		t.Fatalf("transaction calls = inTx:%v callback:%v, want both true", inTxCalled, callbackCalled)
	}
}

func TestAnnouncementInTxRejectsUnexpectedReader(t *testing.T) {
	err := announcementInTx(t.Context(), func(ctx context.Context, fn TxFunc) error {
		return fn(ctx, announcementTestReader{})
	}, func(context.Context, port.AnnouncementTxStore) error {
		t.Fatal("announcement callback must not run for an unexpected reader")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "query type is invalid") {
		t.Fatalf("announcementInTx error = %v, want invalid query type", err)
	}
}

type announcementTestReader struct{}

func (announcementTestReader) ListBaselineSchemas(context.Context, []string) ([]string, error) {
	return nil, nil
}

func TestUniqueAnnouncementIDsTrimsDeduplicatesAndCaps(t *testing.T) {
	input := []string{" first ", "", "first", " second "}
	for index := 3; index <= 40; index++ {
		input = append(input, strings.Repeat("x", index))
	}
	got := uniqueAnnouncementIDs(input)
	if len(got) != 30 {
		t.Fatalf("unique announcement IDs length = %d, want 30", len(got))
	}
	if want := []string{"first", "second", "xxx"}; !reflect.DeepEqual(got[:3], want) {
		t.Fatalf("unique announcement IDs prefix = %v, want %v", got[:3], want)
	}
}

func TestAnnouncementNullableValuesPreserveNullAndUTC(t *testing.T) {
	if got := pgTextPtrValue(pgtype.Text{}); got != nil {
		t.Fatalf("invalid pg text = %v, want nil", got)
	}
	if got := timestamptzPtr(pgtype.Timestamptz{}); got != nil {
		t.Fatalf("invalid timestamptz = %v, want nil", got)
	}

	local := time.Date(2026, 7, 19, 16, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	value := announcementPgTimestamptzPtr(&local)
	if !value.Valid || !value.Time.Equal(local) || value.Time.Location() != time.UTC {
		t.Fatalf("announcement timestamptz = %+v, want valid UTC value", value)
	}
}
