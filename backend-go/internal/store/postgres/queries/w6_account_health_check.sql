-- First-pass scheduler candidate scan. The caller supplies a hard bounded limit
-- and advances by account id to avoid OFFSET drift during background writes.
SELECT
  a.id,
  a.config_revision,
  a.status,
  a.schedulable,
  COALESCE(binding.group_id, ''),
  a.account_expires_at,
  a.next_health_check_at
FROM juhe_business.accounts AS a
LEFT JOIN LATERAL (
  SELECT ga.group_id
  FROM juhe_business.group_accounts AS ga
  WHERE ga.account_id = a.id
    AND ga.system_account_id = a.system_account_id
    AND ga.enabled = true
  ORDER BY ga.updated_at DESC, ga.group_id
  LIMIT 1
) AS binding ON true
WHERE a.deleted_at IS NULL
  AND a.authorization_instance_source_account_id IS NULL
  AND a.authorization_instance_authorization_id IS NULL
  AND a.authorization_instance_owner_system_account_id IS NULL
  AND a.status IN ('active', 'pending_test')
  AND (a.status = 'pending_test' OR a.schedulable = true)
  AND (a.account_expires_at IS NULL OR a.account_expires_at > $3)
  AND (a.next_health_check_at IS NULL OR a.next_health_check_at <= $3)
  AND binding.group_id IS NOT NULL
  AND ($1 = '' OR a.id > $1)
ORDER BY a.id
LIMIT $2;
