-- name: ListPublicAnnouncements :many
SELECT
  a.id, a.title, a.level, a.published_at, ar.read_at
FROM juhe_business.announcements a
LEFT JOIN juhe_business.announcement_reads ar
  ON ar.announcement_id = a.id
 AND ar.system_account_id = sqlc.arg(system_account_id)::text
WHERE a.status = 'published'
  AND a.published_at IS NOT NULL
ORDER BY a.published_at DESC, a.created_at DESC, a.id DESC
LIMIT sqlc.arg(row_limit)::integer;

-- name: FindPublicAnnouncement :one
SELECT a.id, a.title, a.content, a.level, a.published_at
FROM juhe_business.announcements a
WHERE a.id = sqlc.arg(id)::text
  AND a.status = 'published'
  AND a.published_at IS NOT NULL;

-- name: MarkVisibleAnnouncementsRead :many
WITH visible AS (
  SELECT a.id
  FROM juhe_business.announcements a
  WHERE a.id = ANY(sqlc.arg(announcement_ids)::text[])
    AND a.status = 'published'
    AND a.published_at IS NOT NULL
), written AS (
  INSERT INTO juhe_business.announcement_reads (announcement_id, system_account_id, read_at)
  SELECT id, sqlc.arg(system_account_id)::text, sqlc.arg(read_at)::timestamptz
  FROM visible
  ON CONFLICT (announcement_id, system_account_id)
  DO UPDATE SET read_at = EXCLUDED.read_at
  RETURNING announcement_id
)
SELECT announcement_id FROM written;

-- name: ListManagementAnnouncements :many
SELECT
  a.id, a.title,
  CAST(CASE WHEN char_length(a.content) > 240 THEN substr(a.content, 1, 240) || '...' ELSE a.content END AS text) AS content_preview,
  a.level, a.status,
  a.created_by, creator.display_name AS created_by_name,
  a.updated_by, updater.display_name AS updated_by_name,
  a.published_at, a.created_at, a.updated_at
FROM juhe_business.announcements a
LEFT JOIN juhe_business.system_accounts creator ON creator.id = a.created_by
LEFT JOIN juhe_business.system_accounts updater ON updater.id = a.updated_by
ORDER BY a.updated_at DESC, a.created_at DESC, a.id DESC
LIMIT sqlc.arg(row_limit)::integer OFFSET sqlc.arg(row_offset)::integer;

-- name: FindManagementAnnouncement :one
SELECT
  a.id, a.title, a.content, a.level, a.status,
  a.created_by, creator.display_name AS created_by_name,
  a.updated_by, updater.display_name AS updated_by_name,
  a.published_at, a.created_at, a.updated_at
FROM juhe_business.announcements a
LEFT JOIN juhe_business.system_accounts creator ON creator.id = a.created_by
LEFT JOIN juhe_business.system_accounts updater ON updater.id = a.updated_by
WHERE a.id = sqlc.arg(id)::text;

-- name: FindAnnouncementForUpdate :one
SELECT id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at
FROM juhe_business.announcements
WHERE id = sqlc.arg(id)::text
FOR UPDATE;

-- name: CreateAnnouncement :one
INSERT INTO juhe_business.announcements (
  id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at
) VALUES (
  sqlc.arg(id)::text, sqlc.arg(title)::text, sqlc.arg(content)::text,
  sqlc.arg(level)::text, sqlc.arg(status)::text, sqlc.arg(actor_id)::text,
  sqlc.arg(actor_id)::text, sqlc.narg(published_at)::timestamptz,
  sqlc.arg(now_at)::timestamptz, sqlc.arg(now_at)::timestamptz
)
RETURNING id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at;

-- name: UpdateAnnouncement :one
UPDATE juhe_business.announcements
SET title = sqlc.arg(title)::text,
    content = sqlc.arg(content)::text,
    level = sqlc.arg(level)::text,
    status = sqlc.arg(status)::text,
    updated_by = sqlc.arg(actor_id)::text,
    published_at = sqlc.narg(published_at)::timestamptz,
    updated_at = sqlc.arg(now_at)::timestamptz
WHERE id = sqlc.arg(id)::text
RETURNING id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at;

-- name: PublishAnnouncement :one
UPDATE juhe_business.announcements
SET status = 'published', updated_by = sqlc.arg(actor_id)::text,
    published_at = sqlc.arg(now_at)::timestamptz, updated_at = sqlc.arg(now_at)::timestamptz
WHERE id = sqlc.arg(id)::text
RETURNING id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at;

-- name: ArchiveAnnouncement :one
UPDATE juhe_business.announcements
SET status = 'archived', updated_by = sqlc.arg(actor_id)::text,
    updated_at = sqlc.arg(now_at)::timestamptz
WHERE id = sqlc.arg(id)::text
RETURNING id, title, content, level, status, created_by, updated_by, published_at, created_at, updated_at;

-- name: DeleteAnnouncement :execrows
DELETE FROM juhe_business.announcements WHERE id = sqlc.arg(id)::text;

-- name: DeleteAnnouncementReads :execrows
DELETE FROM juhe_business.announcement_reads WHERE announcement_id = sqlc.arg(id)::text;
