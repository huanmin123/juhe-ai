package announcements

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"juhe-ai/backend-go/internal/store/port"
)

type managementAnnouncementStoreStub struct {
	port.AnnouncementStore
	listPage     int
	listPageSize int
	listResult   port.AnnouncementPage
	listErr      error
	findID       string
	findResult   port.Announcement
	findFound    bool
	findErr      error
}

func (s *managementAnnouncementStoreStub) ListManagementAnnouncements(_ context.Context, page int, pageSize int) (port.AnnouncementPage, error) {
	s.listPage = page
	s.listPageSize = pageSize
	if s.listErr != nil {
		return port.AnnouncementPage{}, s.listErr
	}
	return s.listResult, nil
}

func (s *managementAnnouncementStoreStub) FindManagementAnnouncement(_ context.Context, id string) (port.Announcement, bool, error) {
	s.findID = id
	if s.findErr != nil {
		return port.Announcement{}, false, s.findErr
	}
	return s.findResult, s.findFound, nil
}

func TestServiceListManagementNormalizesDefaultsAndPreservesProgressivePage(t *testing.T) {
	want := port.AnnouncementPage{
		Items:          []port.Announcement{{ID: "announcement-1"}},
		Page:           1,
		PageSize:       50,
		PageUpperBound: 2,
		HasMore:        true,
	}
	store := &managementAnnouncementStoreStub{listResult: want}

	got, err := NewService(store).ListManagement(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("ListManagement() error = %v", err)
	}
	if store.listPage != 1 || store.listPageSize != 50 {
		t.Fatalf("store pagination = page %d, pageSize %d; want 1, 50", store.listPage, store.listPageSize)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListManagement() = %#v, want %#v", got, want)
	}
}

func TestServiceListManagementCapsPageSizeAndPageToCurrentWindow(t *testing.T) {
	store := &managementAnnouncementStoreStub{listResult: port.AnnouncementPage{
		Page:           10,
		PageSize:       100,
		PageUpperBound: 1000,
	}}

	_, err := NewService(store).ListManagement(context.Background(), int(^uint(0)>>1), 999)
	if err != nil {
		t.Fatalf("ListManagement() error = %v", err)
	}
	if store.listPage != 10 || store.listPageSize != 100 {
		t.Fatalf("store pagination = page %d, pageSize %d; want 10, 100", store.listPage, store.listPageSize)
	}
}

func TestServiceListManagementWrapsStoreError(t *testing.T) {
	store := &managementAnnouncementStoreStub{listErr: errAnnouncementStore}

	_, err := NewService(store).ListManagement(context.Background(), 1, 50)
	if !errors.Is(err, errAnnouncementStore) || err.Error() == errAnnouncementStore.Error() {
		t.Fatalf("ListManagement() error = %v; want wrapped store error", err)
	}
}

func TestServiceFindManagementTrimsIDAndPreservesFoundResult(t *testing.T) {
	want := port.Announcement{ID: "announcement-1", Title: "维护通知"}
	store := &managementAnnouncementStoreStub{findResult: want, findFound: true}

	got, found, err := NewService(store).FindManagement(context.Background(), "  announcement-1  ")
	if err != nil {
		t.Fatalf("FindManagement() error = %v", err)
	}
	if store.findID != "announcement-1" {
		t.Fatalf("store id = %q, want trimmed id", store.findID)
	}
	if !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("FindManagement() = %#v, %v; want %#v, true", got, found, want)
	}
}

func TestServiceFindManagementRejectsBlankID(t *testing.T) {
	store := &managementAnnouncementStoreStub{}

	_, found, err := NewService(store).FindManagement(context.Background(), " \t ")
	if found || !errors.Is(err, ErrAnnouncementInputInvalid) {
		t.Fatalf("FindManagement() = found %v, error %v; want input invalid", found, err)
	}
	if store.findID != "" {
		t.Fatalf("store should not be called, id = %q", store.findID)
	}
}

func TestServiceFindManagementPreservesNotFound(t *testing.T) {
	store := &managementAnnouncementStoreStub{}

	got, found, err := NewService(store).FindManagement(context.Background(), "missing")
	if err != nil || found || got != (port.Announcement{}) {
		t.Fatalf("FindManagement() = %#v, %v, %v; want zero, false, nil", got, found, err)
	}
}

func TestServiceFindManagementWrapsStoreError(t *testing.T) {
	store := &managementAnnouncementStoreStub{findErr: errAnnouncementStore}

	_, _, err := NewService(store).FindManagement(context.Background(), "announcement-1")
	if !errors.Is(err, errAnnouncementStore) || err.Error() == errAnnouncementStore.Error() {
		t.Fatalf("FindManagement() error = %v; want wrapped store error", err)
	}
}
