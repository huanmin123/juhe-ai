-- +goose Up
CREATE TABLE IF NOT EXISTS juhe_business.announcements (
  id text PRIMARY KEY,
  title text NOT NULL,
  content text NOT NULL,
  level text NOT NULL DEFAULT 'info',
  status text NOT NULL DEFAULT 'draft',
  created_by text NOT NULL REFERENCES juhe_business.system_accounts(id),
  updated_by text REFERENCES juhe_business.system_accounts(id),
  published_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CONSTRAINT announcements_title_length_check
    CHECK (char_length(btrim(title)) BETWEEN 1 AND 120),
  CONSTRAINT announcements_content_length_check
    CHECK (char_length(btrim(content)) BETWEEN 1 AND 5000),
  CONSTRAINT announcements_level_check
    CHECK (level IN ('critical', 'warning', 'info', 'normal')),
  CONSTRAINT announcements_status_check
    CHECK (status IN ('draft', 'published', 'archived')),
  CONSTRAINT announcements_published_at_check
    CHECK (status <> 'published' OR published_at IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS juhe_business.announcement_reads (
  announcement_id text NOT NULL REFERENCES juhe_business.announcements(id) ON DELETE CASCADE,
  system_account_id text NOT NULL REFERENCES juhe_business.system_accounts(id) ON DELETE CASCADE,
  read_at timestamptz NOT NULL,
  PRIMARY KEY (announcement_id, system_account_id)
);

ALTER TABLE juhe_business.announcements
  DROP CONSTRAINT IF EXISTS announcements_title_length_check,
  DROP CONSTRAINT IF EXISTS announcements_content_length_check,
  DROP CONSTRAINT IF EXISTS announcements_level_check,
  DROP CONSTRAINT IF EXISTS announcements_status_check,
  DROP CONSTRAINT IF EXISTS announcements_published_at_check;

ALTER TABLE juhe_business.announcements
  ADD CONSTRAINT announcements_title_length_check
    CHECK (char_length(btrim(title)) BETWEEN 1 AND 120),
  ADD CONSTRAINT announcements_content_length_check
    CHECK (char_length(btrim(content)) BETWEEN 1 AND 5000),
  ADD CONSTRAINT announcements_level_check
    CHECK (level IN ('critical', 'warning', 'info', 'normal')),
  ADD CONSTRAINT announcements_status_check
    CHECK (status IN ('draft', 'published', 'archived')),
  ADD CONSTRAINT announcements_published_at_check
    CHECK (status <> 'published' OR published_at IS NOT NULL);

CREATE INDEX IF NOT EXISTS idx_announcements_public_order
  ON juhe_business.announcements (published_at DESC, created_at DESC, id DESC)
  WHERE status = 'published' AND published_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_announcements_management_order
  ON juhe_business.announcements (updated_at DESC, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_announcement_reads_account
  ON juhe_business.announcement_reads (system_account_id, announcement_id);

-- +goose Down
-- no-op: announcement tables contain business data.
