//go:build integration

package integration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestW8AnnouncementSchemaCoexistenceSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if connString := strings.TrimSpace(os.Getenv("JUHE_AI_W8_ANNOUNCEMENT_SCHEMA_POSTGRES_URL")); connString != "" {
		runW8AnnouncementSchemaCoexistenceSmoke(t, ctx, connString)
		return
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("juhe_ai"),
		tcpostgres.WithUsername("juhe_ai"),
		tcpostgres.WithPassword("juhe_ai_password"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer terminateContainer(t, ctx, container)

	connString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	runW8AnnouncementSchemaCoexistenceSmoke(t, ctx, connString)
}

func runW8AnnouncementSchemaCoexistenceSmoke(t *testing.T, ctx context.Context, connString string) {
	t.Helper()
	db := openSQLDB(t, connString)
	defer closeSQLDB(t, db)

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	migrationDir := filepath.Join(repoRoot(t), "db", "migrations")
	if err := goose.UpTo(db, migrationDir, 57); err != nil {
		t.Fatalf("goose up to 57: %v", err)
	}
	createNodeAnnouncementTables(t, db)
	seedNodeAnnouncementData(t, db)

	if err := goose.UpTo(db, migrationDir, 58); err != nil {
		t.Fatalf("apply announcement migration over Node tables: %v", err)
	}
	assertAnnouncementSchemaConstraints(t, db)

	if err := goose.DownTo(db, migrationDir, 57); err != nil {
		t.Fatalf("goose down announcement migration: %v", err)
	}
	if err := goose.UpTo(db, migrationDir, 58); err != nil {
		t.Fatalf("reapply announcement migration after no-op down: %v", err)
	}
	assertAnnouncementSchemaConstraints(t, db)
}

func seedNodeAnnouncementData(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
INSERT INTO juhe_business.announcements (
  id, title, content, level, status, created_by, published_at, created_at, updated_at
) VALUES (
  'node-announcement', 'Node announcement', 'content', 'info', 'published',
  'w8-announcement-admin', '2026-07-19T08:00:00.000Z',
  '2026-07-19T07:00:00.000Z', '2026-07-19T08:00:00.000Z'
);
INSERT INTO juhe_business.announcement_reads (announcement_id, system_account_id, read_at)
VALUES ('node-announcement', 'w8-announcement-admin', '2026-07-19T09:00:00.000Z');`)
	if err != nil {
		t.Fatalf("seed Node announcement data: %v", err)
	}
}

func createNodeAnnouncementTables(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
INSERT INTO juhe_business.system_accounts (
  id, username, display_name, role, status, password_hash,
  must_change_password, image_generation_enabled, created_at, updated_at
) VALUES (
  'w8-announcement-admin', 'w8-announcement-admin', 'W8 Announcement Admin',
  'admin', 'active', 'hash', false, false, now(), now()
);
CREATE TABLE juhe_business.announcements (
  id text PRIMARY KEY,
  title text NOT NULL,
  content text NOT NULL,
  level text NOT NULL DEFAULT 'info',
  status text NOT NULL DEFAULT 'draft',
  created_by text NOT NULL REFERENCES juhe_business.system_accounts(id),
  updated_by text REFERENCES juhe_business.system_accounts(id),
  published_at text,
  created_at text NOT NULL,
  updated_at text NOT NULL
);
CREATE TABLE juhe_business.announcement_reads (
  announcement_id text NOT NULL REFERENCES juhe_business.announcements(id) ON DELETE CASCADE,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  read_at text NOT NULL,
  PRIMARY KEY (announcement_id, system_account_id)
);
CREATE INDEX idx_announcements_public ON juhe_business.announcements(status, published_at DESC, created_at DESC);
CREATE INDEX idx_announcements_admin ON juhe_business.announcements(updated_at DESC, created_at DESC);
CREATE INDEX idx_announcements_admin_page ON juhe_business.announcements(updated_at DESC, created_at DESC, id DESC);
CREATE INDEX idx_announcement_reads_account ON juhe_business.announcement_reads(system_account_id, read_at DESC);`)
	if err != nil {
		t.Fatalf("create Node announcement tables: %v", err)
	}
}

func assertAnnouncementSchemaConstraints(t *testing.T, db *sql.DB) {
	t.Helper()
	var publishedAt time.Time
	var readAt time.Time
	if err := db.QueryRow(`SELECT a.published_at, ar.read_at
FROM juhe_business.announcements a
JOIN juhe_business.announcement_reads ar ON ar.announcement_id = a.id
WHERE a.id = 'node-announcement'`).Scan(&publishedAt, &readAt); err != nil {
		t.Fatalf("read converted Node announcement data: %v", err)
	}
	if publishedAt.UTC().Format(time.RFC3339) != "2026-07-19T08:00:00Z" || readAt.UTC().Format(time.RFC3339) != "2026-07-19T09:00:00Z" {
		t.Fatalf("converted Node times = (%s, %s), want preserved UTC values", publishedAt, readAt)
	}

	for _, column := range []struct {
		table string
		name  string
	}{
		{table: "announcements", name: "published_at"},
		{table: "announcements", name: "created_at"},
		{table: "announcements", name: "updated_at"},
		{table: "announcement_reads", name: "read_at"},
	} {
		var dataType string
		if err := db.QueryRow(`SELECT data_type FROM information_schema.columns
WHERE table_schema = 'juhe_business' AND table_name = $1 AND column_name = $2`, column.table, column.name).Scan(&dataType); err != nil {
			t.Fatalf("read %s.%s data type: %v", column.table, column.name, err)
		}
		if dataType != "timestamp with time zone" {
			t.Fatalf("%s.%s data type = %q, want timestamp with time zone", column.table, column.name, dataType)
		}
	}

	var readIndexDefinition string
	if err := db.QueryRow(`SELECT indexdef FROM pg_indexes
WHERE schemaname = 'juhe_business' AND indexname = 'idx_announcement_reads_account'`).Scan(&readIndexDefinition); err != nil {
		t.Fatalf("read announcement account index: %v", err)
	}
	if !strings.Contains(readIndexDefinition, "(system_account_id, announcement_id)") || strings.Contains(readIndexDefinition, "read_at") {
		t.Fatalf("announcement account index = %q, want current account/id definition", readIndexDefinition)
	}
	var legacyIndexCount int
	if err := db.QueryRow(`SELECT count(*) FROM pg_indexes
WHERE schemaname = 'juhe_business' AND indexname IN (
  'idx_announcements_public', 'idx_announcements_admin', 'idx_announcements_admin_page'
)`).Scan(&legacyIndexCount); err != nil {
		t.Fatalf("count legacy announcement indexes: %v", err)
	}
	if legacyIndexCount != 0 {
		t.Fatalf("legacy announcement indexes = %d, want 0", legacyIndexCount)
	}

	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "blank title", query: `INSERT INTO juhe_business.announcements
  (id, title, content, level, status, created_by, created_at, updated_at)
VALUES ('invalid-title', ' ', 'content', 'info', 'draft', 'w8-announcement-admin', now(), now())`},
		{name: "published without timestamp", query: `INSERT INTO juhe_business.announcements
  (id, title, content, level, status, created_by, created_at, updated_at)
VALUES ('invalid-published', 'title', 'content', 'info', 'published', 'w8-announcement-admin', now(), now())`},
	} {
		if _, err := db.Exec(test.query); err == nil || !strings.Contains(err.Error(), "violates check constraint") {
			t.Fatalf("%s error = %v, want check constraint violation", test.name, err)
		}
	}
}
