package announcements

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

type announcementWriteStoreStub struct {
	port.AnnouncementStore
	tx      *announcementWriteTxStub
	txCalls int
	txErr   error
}

func (s *announcementWriteStoreStub) AnnouncementInTx(_ context.Context, fn func(context.Context, port.AnnouncementTxStore) error) error {
	s.txCalls++
	if s.txErr != nil {
		return s.txErr
	}
	return fn(context.Background(), s.tx)
}

type announcementWriteTxStub struct {
	current          port.Announcement
	found            bool
	findErr          error
	created          port.Announcement
	createInput      port.AnnouncementCreateInput
	createErr        error
	updated          port.Announcement
	updateInput      port.AnnouncementUpdateInput
	updateFound      bool
	updateErr        error
	published        port.Announcement
	publishFound     bool
	publishID        string
	publishActor     string
	publishAt        time.Time
	publishErr       error
	archived         port.Announcement
	archiveFound     bool
	archiveID        string
	archiveActor     string
	archiveAt        time.Time
	archiveErr       error
	deleted          bool
	deleteID         string
	deleteErr        error
	deletedReadID    string
	deletedReadCount int
	deleteReadsErr   error
}

func (s *announcementWriteTxStub) FindAnnouncementForUpdate(_ context.Context, id string) (port.Announcement, bool, error) {
	if s.findErr != nil {
		return port.Announcement{}, false, s.findErr
	}
	return s.current, s.found, nil
}

func (s *announcementWriteTxStub) CreateAnnouncement(_ context.Context, input port.AnnouncementCreateInput) (port.Announcement, error) {
	s.createInput = input
	if s.createErr != nil {
		return port.Announcement{}, s.createErr
	}
	return s.created, nil
}

func (s *announcementWriteTxStub) UpdateAnnouncement(_ context.Context, input port.AnnouncementUpdateInput) (port.Announcement, bool, error) {
	s.updateInput = input
	if s.updateErr != nil {
		return port.Announcement{}, false, s.updateErr
	}
	return s.updated, s.updateFound, nil
}

func (s *announcementWriteTxStub) PublishAnnouncement(_ context.Context, id string, actorID string, now time.Time) (port.Announcement, bool, error) {
	s.publishID, s.publishActor, s.publishAt = id, actorID, now
	if s.publishErr != nil {
		return port.Announcement{}, false, s.publishErr
	}
	return s.published, s.publishFound, nil
}

func (s *announcementWriteTxStub) ArchiveAnnouncement(_ context.Context, id string, actorID string, now time.Time) (port.Announcement, bool, error) {
	s.archiveID, s.archiveActor, s.archiveAt = id, actorID, now
	if s.archiveErr != nil {
		return port.Announcement{}, false, s.archiveErr
	}
	return s.archived, s.archiveFound, nil
}

func (s *announcementWriteTxStub) DeleteAnnouncement(_ context.Context, id string) (bool, error) {
	s.deleteID = id
	if s.deleteErr != nil {
		return false, s.deleteErr
	}
	return s.deleted, nil
}

func (s *announcementWriteTxStub) DeleteAnnouncementReads(_ context.Context, id string) (int, error) {
	s.deletedReadID = id
	if s.deleteReadsErr != nil {
		return 0, s.deleteReadsErr
	}
	return s.deletedReadCount, nil
}

func fixedAnnouncementNow() time.Time {
	return time.Date(2026, 7, 19, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
}

func newWriteService(store *announcementWriteStoreStub) *Service {
	return NewServiceWithOptions(ServiceOptions{Store: store, Now: fixedAnnouncementNow})
}

func TestServiceCreateNormalizesValuesAndDefaults(t *testing.T) {
	created := port.Announcement{ID: "ann-1", Status: "draft"}
	tx := &announcementWriteTxStub{created: created}
	store := &announcementWriteStoreStub{tx: tx}

	got, err := newWriteService(store).Create(context.Background(), CreateInput{
		ID: " ann-1 ", Title: "  title ", Content: " content ", ActorID: " actor-1 ",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	wantInput := port.AnnouncementCreateInput{
		ID: "ann-1", Title: "title", Content: "content", Level: "info", Status: "draft",
		ActorID: "actor-1", Now: fixedAnnouncementNow().UTC(),
	}
	if !reflect.DeepEqual(tx.createInput, wantInput) {
		t.Fatalf("create input = %#v, want %#v", tx.createInput, wantInput)
	}
	if !reflect.DeepEqual(got, created) || store.txCalls != 1 {
		t.Fatalf("Create() = %#v, tx calls %d", got, store.txCalls)
	}
}

func TestServiceCreatePublishedSetsPublishedAt(t *testing.T) {
	tx := &announcementWriteTxStub{}
	store := &announcementWriteStoreStub{tx: tx}
	_, err := newWriteService(store).Create(context.Background(), CreateInput{
		ID: "ann-1", Title: "title", Content: "content", Status: "published", ActorID: "actor-1",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if tx.createInput.PublishedAt == nil || !tx.createInput.PublishedAt.Equal(fixedAnnouncementNow().UTC()) {
		t.Fatalf("published at = %#v", tx.createInput.PublishedAt)
	}
}

func TestServiceUpdateUsesStrictPartialInputAndClearsReadsOnPublishTransition(t *testing.T) {
	current := port.Announcement{ID: "ann-1", Title: "old", Content: "body", Level: "warning", Status: "draft"}
	tx := &announcementWriteTxStub{
		current: current, found: true, updated: port.Announcement{ID: "ann-1"}, updateFound: true, deletedReadCount: 3,
	}
	store := &announcementWriteStoreStub{tx: tx}
	content := "  new body  "
	status := "published"
	got, err := newWriteService(store).Update(context.Background(), UpdateInput{
		ID: " ann-1 ", Content: &content, Status: &status, ActorID: " actor-1 ",
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if tx.updateInput.Title != current.Title || tx.updateInput.Content != "new body" || tx.updateInput.Level != current.Level || tx.updateInput.Status != "published" {
		t.Fatalf("update input did not preserve fields: %#v", tx.updateInput)
	}
	if tx.updateInput.PublishedAt == nil || !tx.updateInput.PublishedAt.Equal(fixedAnnouncementNow().UTC()) {
		t.Fatalf("published transition time = %#v", tx.updateInput.PublishedAt)
	}
	if tx.deletedReadID != "ann-1" || got.ID != "ann-1" {
		t.Fatalf("result/read cleanup = %#v/%q", got, tx.deletedReadID)
	}
}

func TestServiceUpdateOfPublishedContentDoesNotClearReads(t *testing.T) {
	currentPublishedAt := fixedAnnouncementNow().UTC().Add(-time.Hour)
	current := port.Announcement{ID: "ann-1", Title: "old", Content: "body", Level: "info", Status: "published", PublishedAt: &currentPublishedAt}
	tx := &announcementWriteTxStub{current: current, found: true, updated: port.Announcement{ID: "ann-1"}, updateFound: true}
	store := &announcementWriteStoreStub{tx: tx}
	title := "new title"
	if _, err := newWriteService(store).Update(context.Background(), UpdateInput{ID: "ann-1", Title: &title, ActorID: "actor-1"}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if tx.deletedReadID != "" || tx.updateInput.PublishedAt == nil || !tx.updateInput.PublishedAt.Equal(currentPublishedAt) {
		t.Fatalf("published update changed read state/time: %#v, %q", tx.updateInput, tx.deletedReadID)
	}
}

func TestServiceUnpublishArchivesAndDelegatesWithActorAndNow(t *testing.T) {
	archivedAt := fixedAnnouncementNow().UTC().Add(-time.Hour)
	tx := &announcementWriteTxStub{archived: port.Announcement{ID: "ann-1", Status: "archived", PublishedAt: &archivedAt}, archiveFound: true}
	store := &announcementWriteStoreStub{tx: tx}
	got, err := newWriteService(store).Unpublish(context.Background(), ActionInput{ID: " ann-1 ", ActorID: " actor-1 "})
	if err != nil {
		t.Fatalf("Unpublish() error = %v", err)
	}
	if tx.archiveID != "ann-1" || tx.archiveActor != "actor-1" || !tx.archiveAt.Equal(fixedAnnouncementNow().UTC()) || got.Status != "archived" || got.PublishedAt == nil {
		t.Fatalf("archive call/result = %#v, %#v", tx, got)
	}
}

func TestServicePublishClearsReadsInSameTransaction(t *testing.T) {
	tx := &announcementWriteTxStub{published: port.Announcement{ID: "ann-1", Status: "published"}, publishFound: true, deletedReadCount: 2}
	store := &announcementWriteStoreStub{tx: tx}
	if _, err := newWriteService(store).Publish(context.Background(), ActionInput{ID: "ann-1", ActorID: "actor-1"}); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if tx.publishID != "ann-1" || tx.deletedReadID != "ann-1" {
		t.Fatalf("publish/read cleanup = %#v/%q", tx.publishID, tx.deletedReadID)
	}
}

func TestServiceDeleteReturnsNotFound(t *testing.T) {
	tx := &announcementWriteTxStub{}
	store := &announcementWriteStoreStub{tx: tx}
	if _, err := newWriteService(store).Delete(context.Background(), ActionInput{ID: "ann-missing", ActorID: "actor-1"}); !errors.Is(err, ErrAnnouncementNotFound) {
		t.Fatalf("Delete() error = %v, want not found", err)
	}
}

func TestServiceDeleteReturnsLockedSnapshot(t *testing.T) {
	current := port.Announcement{ID: "ann-1", Title: "published", Status: "published"}
	tx := &announcementWriteTxStub{current: current, found: true, deleted: true}
	store := &announcementWriteStoreStub{tx: tx}
	got, err := newWriteService(store).Delete(context.Background(), ActionInput{ID: "ann-1", ActorID: "actor-1"})
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !reflect.DeepEqual(got, current) || tx.deleteID != "ann-1" {
		t.Fatalf("Delete() = %#v, deleteID=%q", got, tx.deleteID)
	}
}

func TestServiceUpdateAllowsEmptyPatchAndPreservesCurrentValues(t *testing.T) {
	current := port.Announcement{ID: "ann-1", Title: "old", Content: "body", Level: "info", Status: "draft"}
	tx := &announcementWriteTxStub{current: current, found: true, updated: current, updateFound: true}
	store := &announcementWriteStoreStub{tx: tx}
	if _, err := newWriteService(store).Update(context.Background(), UpdateInput{ID: "ann-1", ActorID: "actor-1"}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if tx.updateInput.Title != current.Title || tx.updateInput.Content != current.Content || tx.updateInput.Level != current.Level || tx.updateInput.Status != current.Status {
		t.Fatalf("empty patch input = %#v", tx.updateInput)
	}
}

func TestServiceWriteValidationUsesJavaScriptUTF16Length(t *testing.T) {
	service := newWriteService(&announcementWriteStoreStub{tx: &announcementWriteTxStub{}})
	title := strings.Repeat("😀", 61)
	_, err := service.Create(context.Background(), CreateInput{ID: "id", Title: title, Content: "content", ActorID: "actor"})
	if !errors.Is(err, ErrAnnouncementInputInvalid) {
		t.Fatalf("Create() error = %v, want UTF-16 length rejection", err)
	}
}

func TestServiceWriteValidationRejectsInvalidAndIncompleteInputs(t *testing.T) {
	service := newWriteService(&announcementWriteStoreStub{tx: &announcementWriteTxStub{}})
	tests := []struct {
		name string
		call func() error
	}{
		{"create missing id", func() error {
			_, err := service.Create(context.Background(), CreateInput{Title: "title", Content: "content", ActorID: "actor"})
			return err
		}},
		{"create blank title", func() error {
			_, err := service.Create(context.Background(), CreateInput{ID: "id", Title: " ", Content: "content", ActorID: "actor"})
			return err
		}},
		{"create invalid level", func() error {
			_, err := service.Create(context.Background(), CreateInput{ID: "id", Title: "title", Content: "content", Level: "bad", ActorID: "actor"})
			return err
		}},
		{"update blank content", func() error {
			content := " "
			_, err := service.Update(context.Background(), UpdateInput{ID: "id", Content: &content, ActorID: "actor"})
			return err
		}},
		{"publish missing actor", func() error { _, err := service.Publish(context.Background(), ActionInput{ID: "id"}); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, ErrAnnouncementInputInvalid) {
				t.Fatalf("error = %v, want input invalid", err)
			}
		})
	}
}

func TestServiceWriteWrapsTransactionAndStoreErrors(t *testing.T) {
	transactionErr := errors.New("transaction failed")
	store := &announcementWriteStoreStub{tx: &announcementWriteTxStub{}, txErr: transactionErr}
	_, err := newWriteService(store).Create(context.Background(), CreateInput{ID: "id", Title: "title", Content: "content", ActorID: "actor"})
	if !errors.Is(err, transactionErr) || err.Error() == transactionErr.Error() {
		t.Fatalf("transaction error = %v", err)
	}
	createErr := errors.New("create failed")
	store = &announcementWriteStoreStub{tx: &announcementWriteTxStub{createErr: createErr}}
	_, err = newWriteService(store).Create(context.Background(), CreateInput{ID: "id", Title: "title", Content: "content", ActorID: "actor"})
	if !errors.Is(err, createErr) || err.Error() == createErr.Error() {
		t.Fatalf("create error = %v", err)
	}
}
