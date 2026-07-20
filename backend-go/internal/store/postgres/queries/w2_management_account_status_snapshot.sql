-- name: ListManagementAccountStatusProjections :many
SELECT a.id, a.system_account_id, a.name, a.status, a.schedulable,
       COALESCE(a.account_expires_at::text, ''), COALESCE(a.cooldown_until::text, ''),
       COALESCE(a.last_error_code, ''), COALESCE(a.last_error_message, ''), COALESCE(a.last_error_trace_id, ''),
       COALESCE(a.last_health_check_at::text, ''), COALESCE(a.next_health_check_at::text, ''),
       COALESCE(a.last_health_check_status_code, 0), COALESCE(a.last_health_check_error_code, ''),
       COALESCE(a.last_health_check_error_message, ''), COALESCE(a.last_health_check_trace_id, ''), COALESCE(a.last_used_at::text, ''),
       COALESCE(ra.id, ''), COALESCE(ra.status, ''), COALESCE(ra.expires_at::text, ''),
       COALESCE(a.authorization_instance_source_account_id, ''), COALESCE(source.schedulable, false), COALESCE(source.account_expires_at::text, ''), COALESCE(source.status, ''),
       COALESCE(source.cooldown_until::text, ''), COALESCE(source.last_error_code, ''), COALESCE(source.last_error_message, ''), COALESCE(source.last_error_trace_id, ''),
       COALESCE(gb.group_id, ''), COALESCE(g.name, ''),
       CASE
         WHEN gb.group_id IS NULL THEN 'unbound'
         WHEN a.authorization_instance_authorization_id IS NOT NULL AND COALESCE(gb.account_authorization_id, '') <> COALESCE(ra.id, '') THEN 'authorization_unavailable'
         ELSE 'bound'
       END,
       COALESCE((SELECT jsonb_build_object('requestCount', usd.request_count, 'successCount', usd.success_count, 'errorCount', usd.error_count, 'inputTokens', usd.input_tokens, 'outputTokens', usd.output_tokens, 'totalCost', usd.total_cost_usd)::text FROM juhe_stats.usage_stats_daily usd WHERE usd.scope_type = 'account' AND usd.scope_id = a.id ORDER BY usd.stat_date DESC LIMIT 1), '{}')
FROM juhe_business.accounts a
LEFT JOIN juhe_business.resource_authorizations ra ON ra.id = a.authorization_instance_authorization_id
LEFT JOIN juhe_business.accounts source ON source.id = a.authorization_instance_source_account_id AND source.deleted_at IS NULL
LEFT JOIN LATERAL (SELECT ga.group_id, ga.account_authorization_id FROM juhe_business.group_accounts ga WHERE ga.account_id = a.id AND ga.system_account_id = a.system_account_id AND ga.enabled = true ORDER BY ga.updated_at DESC, ga.group_id LIMIT 1) gb ON true
LEFT JOIN juhe_business.groups g ON g.id = gb.group_id AND g.system_account_id = a.system_account_id
WHERE a.deleted_at IS NULL AND a.id = ANY(sqlc.arg(account_ids)::text[]) AND (sqlc.arg(system_account_id)::text = '' OR a.system_account_id = sqlc.arg(system_account_id)::text)
ORDER BY array_position(sqlc.arg(account_ids)::text[], a.id);
