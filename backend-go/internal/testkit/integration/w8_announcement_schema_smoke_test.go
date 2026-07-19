//go:build integration

package integration

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestW8AnnouncementSchemaCoexistenceSmoke(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

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
  published_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE TABLE juhe_business.announcement_reads (
  announcement_id text NOT NULL REFERENCES juhe_business.announcements(id) ON DELETE CASCADE,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  read_at timestamptz NOT NULL,
  PRIMARY KEY (announcement_id, system_account_id)
);`)
	if err != nil {
		t.Fatalf("create Node announcement tables: %v", err)
	}
}

func assertAnnouncementSchemaConstraints(t *testing.T, db *sql.DB) {
	t.Helper()
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
