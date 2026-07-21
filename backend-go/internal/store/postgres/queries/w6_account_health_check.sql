-- First-pass scheduler candidate scan. The caller supplies a hard bounded limit
-- and advances by status priority plus account id to avoid OFFSET drift.
SELECT
  a.id,
  a.config_revision,
  a.status,
  a.schedulable,
  COALESCE(binding.group_id, ''),
  a.account_expires_at,
  a.next_health_check_at,
  CASE WHEN a.status = 'pending_test' THEN 0 ELSE 1 END AS status_priority
FROM juhe_business.accounts AS a
LEFT JOIN juhe_business.resource_authorizations AS ra
  ON ra.id = a.authorization_instance_authorization_id
LEFT JOIN juhe_business.accounts AS source
  ON source.id = a.authorization_instance_source_account_id
  AND source.deleted_at IS NULL
LEFT JOIN LATERAL (
  SELECT ga.group_id
  FROM juhe_business.group_accounts AS ga
  WHERE ga.account_id = a.id
    AND ga.system_account_id = a.system_account_id
    AND ga.enabled = true
    AND (
      a.authorization_instance_authorization_id IS NULL
      OR ga.account_authorization_id = a.authorization_instance_authorization_id
    )
  ORDER BY ga.updated_at DESC, ga.group_id
  LIMIT 1
) AS binding ON true
WHERE a.deleted_at IS NULL
  AND a.status IN ('active', 'pending_test')
  AND (a.status = 'pending_test' OR a.schedulable = true)
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $4)
  AND (
    a.authorization_instance_authorization_id IS NULL
    OR (
      ra.id IS NOT NULL
      AND ra.status = 'active'
      AND (ra.expires_at IS NULL OR ra.expires_at > $4)
      AND source.id IS NOT NULL
      AND source.status = 'active'
      AND source.schedulable = true
      AND (source.last_error_code IS NULL OR source.last_error_code <> 'account_expired')
      AND (source.account_expires_at IS NULL OR source.account_expires_at > $4)
      AND (source.cooldown_until IS NULL OR source.cooldown_until <= $4)
    )
  )
  AND (a.next_health_check_at IS NULL OR a.next_health_check_at <= $4)
  AND binding.group_id IS NOT NULL
  AND (
    $1 < 0
    OR (CASE WHEN a.status = 'pending_test' THEN 0 ELSE 1 END, a.id) > ($1, $2)
  )
ORDER BY CASE WHEN a.status = 'pending_test' THEN 0 ELSE 1 END, a.id
LIMIT $3;
