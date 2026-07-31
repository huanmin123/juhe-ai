-- +goose Up
-- Restore only the canonical xAI default marker when that owner has no xAI default yet.
UPDATE juhe_business.groups AS candidate
SET is_default = true
WHERE candidate.provider_code = 'xai'
  AND candidate.is_default = false
  AND candidate.system_account_id = 'sys_admin'
  AND candidate.id = 'grp_default_xai_sys_admin'
  AND NOT EXISTS (
    SELECT 1
    FROM juhe_business.groups AS existing_default
    WHERE existing_default.system_account_id = candidate.system_account_id
      AND existing_default.provider_code = candidate.provider_code
      AND existing_default.is_default = true
  );

INSERT INTO juhe_business.groups (
  id, system_account_id, name, provider_code, description,
  enabled, is_default, created_at, updated_at
)
SELECT
  CASE
    WHEN system_accounts.id = 'sys_admin' THEN 'grp_default_xai_sys_admin'
    ELSE 'grp_default_xai_' || system_accounts.id
  END,
  system_accounts.id,
  CASE
    WHEN EXISTS (
      SELECT 1
      FROM juhe_business.groups AS same_name
      WHERE same_name.system_account_id = system_accounts.id
        AND same_name.provider_code = 'xai'
        AND lower(same_name.name) = lower('默认 xAI 分组')
    ) THEN '默认 xAI 分组（系统默认：' || system_accounts.id || CASE
      WHEN candidate_suffix.suffix = 0 THEN ''
      ELSE ' #' || candidate_suffix.suffix
    END || '）'
    ELSE '默认 xAI 分组'
  END,
  'xai', '',
  true, true, now(), now()
FROM juhe_business.system_accounts AS system_accounts
LEFT JOIN LATERAL (
  SELECT candidate_suffix.suffix
  FROM generate_series(
    0,
    (
      SELECT COUNT(*)
      FROM juhe_business.groups AS fallback_name
      WHERE fallback_name.system_account_id = system_accounts.id
        AND fallback_name.provider_code = 'xai'
        AND lower(fallback_name.name) LIKE lower('默认 xAI 分组') || '（系统默认：%）'
    )
  ) AS candidate_suffix(suffix)
  WHERE NOT EXISTS (
    SELECT 1
    FROM juhe_business.groups AS existing_fallback_name
    WHERE existing_fallback_name.system_account_id = system_accounts.id
      AND existing_fallback_name.provider_code = 'xai'
      AND lower(existing_fallback_name.name) = lower(
        '默认 xAI 分组（系统默认：' || system_accounts.id || CASE
          WHEN candidate_suffix.suffix = 0 THEN ''
          ELSE ' #' || candidate_suffix.suffix
        END || '）'
      )
  )
  ORDER BY candidate_suffix.suffix
  LIMIT 1
) AS candidate_suffix ON true
WHERE NOT EXISTS (
  SELECT 1
  FROM juhe_business.groups AS existing_default
  WHERE existing_default.system_account_id = system_accounts.id
    AND existing_default.provider_code = 'xai'
    AND existing_default.is_default = true
)
ON CONFLICT DO NOTHING;

-- +goose Down
-- no-op: the xAI default group is current business state and may contain user bindings.
