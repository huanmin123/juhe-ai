package announcements

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestAnnouncementPostgresLifecycleIntegration(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv("JUHE_AI_TEST_POSTGRES_URL"))
	if rawURL == "" {
		if os.Getenv("JUHE_AI_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("JUHE_AI_TEST_POSTGRES_URL is required when JUHE_AI_REQUIRE_INTEGRATION=1")
		}
		t.Skip("JUHE_AI_TEST_POSTGRES_URL is not configured")
	}

	ctx := t.Context()
	store, err := postgresstore.Open(ctx, rawURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(store.Close)
	pool, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		t.Fatalf("open postgres fixture pool: %v", err)
	}
	t.Cleanup(pool.Close)

	marker := strings.ReplaceAll(uuid.NewString(), "-", "")
	actorID := "sys_ann_actor_" + marker
	readerID := "sys_ann_reader_" + marker
	announcementID := "ann_integration_" + marker
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	for _, fixture := range []struct{ id, username, displayName, role string }{
		{actorID, "ann_actor_" + marker, "公告集成管理员", "admin"},
		{readerID, "ann_reader_" + marker, "公告集成读者", "user"},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO juhe_business.system_accounts
			  (id, username, display_name, role, status, password_hash, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'active', 'integration-only', $5, $5)`,
			fixture.id, fixture.username, fixture.displayName, fixture.role, now); err != nil {
			t.Fatalf("insert system account fixture: %v", err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM juhe_business.announcements WHERE id = $1", announcementID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM juhe_business.system_accounts WHERE id = ANY($1::text[])", []string{actorID, readerID})
	})

	service := NewServiceWithOptions(ServiceOptions{Store: store, Now: func() time.Time { return now }})
	created, err := service.Create(ctx, CreateInput{
		ID: announcementID, Title: " 集成公告 ", Content: " 初始内容 ", ActorID: actorID,
	})
	if err != nil {
		t.Fatalf("create announcement: %v", err)
	}
	if created.Status != "draft" || created.Title != "集成公告" || created.PublishedAt != nil {
		t.Fatalf("created announcement = %+v", created)
	}

	if _, err := service.Update(ctx, UpdateInput{ID: announcementID, ActorID: actorID}); err != nil {
		t.Fatalf("empty update announcement: %v", err)
	}
	published, err := service.Publish(ctx, ActionInput{ID: announcementID, ActorID: actorID})
	if err != nil {
		t.Fatalf("publish announcement: %v", err)
	}
	if published.Status != "published" || published.PublishedAt == nil {
		t.Fatalf("published announcement = %+v", published)
	}

	read, err := service.MarkPublicRead(ctx, PublicReadInput{SystemAccountID: readerID, AnnouncementIDs: []string{announcementID}})
	if err != nil || read.Count != 1 {
		t.Fatalf("mark public read = %+v, err=%v", read, err)
	}
	publicItems, err := service.ListPublic(ctx, PublicListInput{SystemAccountID: readerID, Limit: 30})
	if err != nil || len(publicItems) != 1 || publicItems[0].ReadAt == nil {
		t.Fatalf("public items = %+v, err=%v", publicItems, err)
	}

	content := "发布后更新内容"
	if _, err := service.Update(ctx, UpdateInput{ID: announcementID, Content: &content, ActorID: actorID}); err != nil {
		t.Fatalf("update published announcement: %v", err)
	}
	var readCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM juhe_business.announcement_reads WHERE announcement_id = $1", announcementID).Scan(&readCount); err != nil {
		t.Fatalf("count announcement reads: %v", err)
	}
	if readCount != 1 {
		t.Fatalf("read count after published content update = %d, want 1", readCount)
	}

	archived, err := service.Unpublish(ctx, ActionInput{ID: announcementID, ActorID: actorID})
	if err != nil || archived.Status != "archived" || archived.PublishedAt == nil {
		t.Fatalf("unpublish announcement = %+v, err=%v", archived, err)
	}
	publicItems, err = service.ListPublic(ctx, PublicListInput{SystemAccountID: readerID, Limit: 30})
	if err != nil || len(publicItems) != 0 {
		t.Fatalf("public items after unpublish = %+v, err=%v", publicItems, err)
	}

	deleted, err := service.Delete(ctx, ActionInput{ID: announcementID, ActorID: actorID})
	if err != nil || deleted.Status != "archived" {
		t.Fatalf("delete announcement = %+v, err=%v", deleted, err)
	}
	if _, found, err := service.FindManagement(ctx, announcementID); err != nil || found {
		t.Fatalf("find deleted announcement found=%v err=%v", found, err)
	}
	if _, err := service.Delete(ctx, ActionInput{ID: announcementID, ActorID: actorID}); err == nil || !strings.Contains(err.Error(), ErrAnnouncementNotFound.Error()) {
		t.Fatalf("delete missing announcement error = %v", err)
	}

	management, err := service.ListManagement(ctx, 1, 10)
	if err != nil {
		t.Fatalf("list management announcements: %v", err)
	}
	for _, item := range management.Items {
		if item.ID == announcementID {
			t.Fatalf("deleted announcement remains in management list: %s", fmt.Sprint(item))
		}
	}
}
