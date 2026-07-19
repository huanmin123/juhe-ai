package announcements

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

var errAnnouncementStore = errors.New("announcement store failed")

type announcementStoreStub struct {
	port.AnnouncementStore
	publicItems   []port.Announcement
	publicLimit   int
	publicAccount string
	publicErr     error
	readAccount   string
	readIDs       []string
	readAt        time.Time
	readCount     int
	readErr       error
}

func (s *announcementStoreStub) ListPublicAnnouncements(_ context.Context, accountID string, limit int) ([]port.Announcement, error) {
	s.publicAccount, s.publicLimit = accountID, limit
	if s.publicErr != nil {
		return nil, s.publicErr
	}
	return s.publicItems, nil
}

func (s *announcementStoreStub) MarkVisibleAnnouncementsRead(_ context.Context, accountID string, ids []string, readAt time.Time) (int, error) {
	s.readAccount, s.readIDs, s.readAt = accountID, append([]string(nil), ids...), readAt
	if s.readErr != nil {
		return 0, s.readErr
	}
	return s.readCount, nil
}

func TestServiceListPublicNormalizesLimitAndDelegatesWithoutAuth(t *testing.T) {
	store := &announcementStoreStub{publicItems: []port.Announcement{{ID: "a1"}}}
	service := NewServiceWithOptions(ServiceOptions{Store: store})

	items, err := service.ListPublic(context.Background(), PublicListInput{SystemAccountID: "  user-1  ", Limit: 999})
	if err != nil {
		t.Fatalf("ListPublic() error = %v", err)
	}
	if !reflect.DeepEqual(items, []port.Announcement{{ID: "a1"}}) {
		t.Fatalf("ListPublic() = %#v", items)
	}
	if store.publicAccount != "  user-1  " || store.publicLimit != 30 {
		t.Fatalf("store call = account %q, limit %d", store.publicAccount, store.publicLimit)
	}
}

func TestServiceListPublicUsesDefaultLimit(t *testing.T) {
	store := &announcementStoreStub{}
	_, err := NewService(store).ListPublic(context.Background(), PublicListInput{})
	if err != nil {
		t.Fatalf("ListPublic() error = %v", err)
	}
	if store.publicLimit != 30 {
		t.Fatalf("default limit = %d, want 30", store.publicLimit)
	}
}

func TestServiceMarkPublicReadNormalizesIDsAndUsesInjectedClock(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store := &announcementStoreStub{readCount: 2}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }})

	result, err := service.MarkPublicRead(context.Background(), PublicReadInput{
		SystemAccountID: "user-1",
		AnnouncementIDs: []string{" a", "b", "a ", "", " c "},
	})
	if err != nil {
		t.Fatalf("MarkPublicRead() error = %v", err)
	}
	if result.Count != 2 || !result.ReadAt.Equal(now) || !reflect.DeepEqual(store.readIDs, []string{"a", "b", "c"}) || !store.readAt.Equal(now) {
		t.Fatalf("result/store call = %#v, ids %#v, at %s", result, store.readIDs, store.readAt)
	}
}

func TestServiceMarkPublicReadCapsIDsAtThirty(t *testing.T) {
	ids := make([]string, 0, 31)
	for i := 0; i < 31; i++ {
		ids = append(ids, string(rune('a'+i)))
	}
	store := &announcementStoreStub{}
	_, err := NewService(store).MarkPublicRead(context.Background(), PublicReadInput{AnnouncementIDs: ids})
	if err != nil {
		t.Fatalf("MarkPublicRead() error = %v", err)
	}
	if len(store.readIDs) != 30 {
		t.Fatalf("read IDs length = %d, want 30", len(store.readIDs))
	}
}

func TestServiceMarkPublicReadReturnsStoreVisibleCount(t *testing.T) {
	store := &announcementStoreStub{readCount: 1}
	result, err := NewService(store).MarkPublicRead(context.Background(), PublicReadInput{
		AnnouncementIDs: []string{"published", "draft"},
	})
	if err != nil {
		t.Fatalf("MarkPublicRead() error = %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("MarkPublicRead() count = %d, want store-filtered count 1", result.Count)
	}
	if !reflect.DeepEqual(store.readIDs, []string{"published", "draft"}) {
		t.Fatalf("store read IDs = %#v", store.readIDs)
	}
}

func TestServiceMarkPublicReadEmptyInputIsIdempotent(t *testing.T) {
	now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	store := &announcementStoreStub{}
	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }})
	result, err := service.MarkPublicRead(context.Background(), PublicReadInput{AnnouncementIDs: []string{" ", ""}})
	if err != nil || result.Count != 0 || !result.ReadAt.Equal(now) {
		t.Fatalf("MarkPublicRead() = %#v, %v", result, err)
	}
	if store.readIDs != nil {
		t.Fatalf("store should not be called for empty IDs: %#v", store.readIDs)
	}
}

func TestServiceWrapsStoreErrors(t *testing.T) {
	store := &announcementStoreStub{publicErr: errAnnouncementStore}
	_, err := NewService(store).ListPublic(context.Background(), PublicListInput{})
	if !errors.Is(err, errAnnouncementStore) || err.Error() == errAnnouncementStore.Error() {
		t.Fatalf("wrapped ListPublic error = %v", err)
	}

	store = &announcementStoreStub{readErr: errAnnouncementStore}
	_, err = NewService(store).MarkPublicRead(context.Background(), PublicReadInput{AnnouncementIDs: []string{"a"}})
	if !errors.Is(err, errAnnouncementStore) || err.Error() == errAnnouncementStore.Error() {
		t.Fatalf("wrapped MarkPublicRead error = %v", err)
	}
}
