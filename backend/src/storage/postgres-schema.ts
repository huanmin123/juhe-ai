import type { DatabaseSync } from 'node:sqlite'

import { applyBusinessSchema, applyChatSchema, applyCodexContextStateSchema, applyDatasetSchema, applyStatsSchema, applyUsageCatalogSchema } from './schema.js'
import type { DatabaseClient } from './database-client.js'
import { applyUsageRecordShardBaseSchema } from './usage-record-shards.js'

export type PostgresSchemaName = 'juhe_business' | 'juhe_chat' | 'juhe_dataset' | 'juhe_usage' | 'juhe_stats' | 'juhe_codex_context'

export interface PostgresSchemaStatement {
  schemaName: PostgresSchemaName
  source: string
  sql: string
}

interface SchemaSourceDefinition {
  source: string
  schemaName: PostgresSchemaName
  apply: (database: DatabaseSync) => void
}

const schemaSourceDefinitions: SchemaSourceDefinition[] = [
  { source: 'business', schemaName: 'juhe_business', apply: applyBusinessSchema },
  { source: 'chat', schemaName: 'juhe_chat', apply: applyChatSchema },
  { source: 'dataset', schemaName: 'juhe_dataset', apply: applyDatasetSchema },
  { source: 'usage-catalog', schemaName: 'juhe_usage', apply: applyUsageCatalogSchema },
  { source: 'usage-records', schemaName: 'juhe_usage', apply: applyUsageRecordShardBaseSchema },
  { source: 'stats', schemaName: 'juhe_stats', apply: applyStatsSchema },
  { source: 'codex-context', schemaName: 'juhe_codex_context', apply: applyCodexContextStateSchema }
]

const supplementalSchemaStatements: PostgresSchemaStatement[] = [
  {
    schemaName: 'juhe_business',
    source: 'account-lock-retry-timestamp-pg-column',
    sql: `DO $$
BEGIN
  IF to_regclass('juhe_business.account_lock_states') IS NOT NULL THEN
    ALTER TABLE account_lock_states ALTER COLUMN next_retry_at_ms TYPE bigint;
  END IF;
END
$$`
  },
  {
    schemaName: 'juhe_business',
    source: 'account-list-projection-pg-trigram-extension',
    sql: 'CREATE EXTENSION IF NOT EXISTS pg_trgm'
  },
  {
    schemaName: 'juhe_business',
    source: 'accounts-pg-trigram-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_accounts_name_c_trgm_lookup ON accounts USING gin ((name COLLATE "C") juhe_business.gin_trgm_ops) WHERE deleted_at IS NULL'
  },
  {
    schemaName: 'juhe_business',
    source: 'accounts-pg-trigram-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_accounts_provider_code_c_trgm_lookup ON accounts USING gin ((provider_code COLLATE "C") juhe_business.gin_trgm_ops) WHERE deleted_at IS NULL'
  },
  {
    schemaName: 'juhe_business',
    source: 'accounts-pg-trigram-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_accounts_type_c_trgm_lookup ON accounts USING gin ((type COLLATE "C") juhe_business.gin_trgm_ops) WHERE deleted_at IS NULL'
  },
  {
    schemaName: 'juhe_business',
    source: 'groups-pg-trigram-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_groups_name_c_trgm_lookup ON groups USING gin ((name COLLATE "C") juhe_business.gin_trgm_ops)'
  },
  {
    schemaName: 'juhe_business',
    source: 'account-list-projection-pg-trigram-index',
    sql: 'CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_name_trgm ON account_list_availability_projections USING gin (name_sort_key juhe_business.gin_trgm_ops)'
  },
  {
    schemaName: 'juhe_business',
    source: 'account-list-projection-index-pg-trigram-index',
    sql: 'CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_name_trgm ON account_list_availability_projection_index USING gin (name_sort_key juhe_business.gin_trgm_ops)'
  },
  {
    schemaName: 'juhe_business',
    source: 'account-list-projection-search-terms-pg-name-order-index',
    sql: 'CREATE INDEX IF NOT EXISTS idx_alap_search_term_name_c ON account_list_availability_projection_search_terms(viewer_system_account_id, term, (name_sort_key COLLATE "C") ASC, created_at_sort_key ASC, account_id ASC)'
  },
  {
    schemaName: 'juhe_business',
    source: 'account-list-projection-index-pg-name-search-incomplete-index',
    sql: 'CREATE INDEX IF NOT EXISTS idx_alap_index_name_incomplete_c ON account_list_availability_projection_index(viewer_system_account_id, (name_sort_key COLLATE "C") ASC, created_at_sort_key ASC, account_id ASC) WHERE search_index_complete = 0'
  },
  {
    schemaName: 'juhe_business',
    source: 'account-list-projection-pg-name-order-index',
    sql: 'CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_name_order ON account_list_availability_projections(viewer_system_account_id, ((payload_json::jsonb ->> \'name\') COLLATE "C") ASC, created_at_sort_key ASC, account_id ASC)'
  },
  {
    schemaName: 'juhe_usage',
    source: 'upstream-response-model-pg-columns',
    sql: 'ALTER TABLE usage_records ADD COLUMN IF NOT EXISTS upstream_response_model text'
  },
  {
    schemaName: 'juhe_business',
    source: 'account-test-task-queued-deadline-pg-column',
    sql: 'ALTER TABLE account_test_tasks ADD COLUMN IF NOT EXISTS queued_deadline_at timestamptz'
  },
  {
    schemaName: 'juhe_business',
    source: 'system-account-ai-account-limit-pg-column',
    sql: 'ALTER TABLE system_accounts ADD COLUMN IF NOT EXISTS ai_account_limit integer CHECK (ai_account_limit BETWEEN 0 AND 1000000)'
  },
  {
    schemaName: 'juhe_business',
    source: 'account-circuit-key-model-capability-index',
    sql: "CREATE UNIQUE INDEX IF NOT EXISTS idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL"
  },
  {
    schemaName: 'juhe_business',
    source: 'api-keys-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_api_keys_name_c_lookup ON api_keys((name COLLATE "C"), id)'
  },
  {
    schemaName: 'juhe_business',
    source: 'api-keys-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_name_c_lookup ON api_keys(system_account_id, (name COLLATE "C"), id)'
  },
  {
    schemaName: 'juhe_business',
    source: 'accounts-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_accounts_name_c_lookup ON accounts((name COLLATE "C"), id) WHERE deleted_at IS NULL'
  },
  {
    schemaName: 'juhe_business',
    source: 'accounts-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_accounts_owner_name_c_lookup ON accounts(system_account_id, (name COLLATE "C"), id) WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL'
  },
  {
    schemaName: 'juhe_business',
    source: 'accounts-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_accounts_owner_all_name_c_lookup ON accounts(system_account_id, (name COLLATE "C"), id) WHERE deleted_at IS NULL'
  },
  {
    schemaName: 'juhe_business',
    source: 'system-teams-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_system_teams_name_c_lookup ON system_teams((name COLLATE "C"), id)'
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-pg-indexes',
    sql: "CREATE INDEX IF NOT EXISTS idx_usage_records_recent_openai_account_shape ON usage_records(account_id, created_at DESC, id DESC, provider_code) WHERE api_key_id IS NOT NULL AND traffic_source = 'gateway' AND endpoint IS NOT NULL AND btrim(endpoint) <> ''"
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-pg-indexes',
    sql: "CREATE INDEX IF NOT EXISTS idx_usage_records_recent_openai_group_shape ON usage_records(group_id, created_at DESC, id DESC, provider_code) WHERE api_key_id IS NOT NULL AND traffic_source = 'gateway' AND endpoint IS NOT NULL AND btrim(endpoint) <> ''"
  },
  {
    schemaName: 'juhe_usage',
    source: 'usage-records-pg-prefix-indexes',
    sql: 'CREATE INDEX IF NOT EXISTS idx_usage_records_system_trace_c_created_sort ON usage_records(system_account_id, (trace_id COLLATE "C"), created_at DESC, id DESC)'
  },
]

const accountListAvailabilityProjectionTriggerStatements: PostgresSchemaStatement[] = [
  {
    schemaName: 'juhe_business',
    source: 'account-list-projection-pg-dirty-triggers',
    sql: `
CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_accounts(
  p_account_ids text[],
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
DECLARE
  v_now_ms bigint;
BEGIN
  IF COALESCE(array_length(p_account_ids, 1), 0) = 0 THEN
    RETURN;
  END IF;
  v_now_ms := FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint;
  WITH requested_accounts AS (
    SELECT DISTINCT requested.account_id
    FROM unnest(p_account_ids) AS requested(account_id)
    WHERE account_id IS NOT NULL AND btrim(account_id) <> ''
  ), affected_accounts AS (
    SELECT DISTINCT accounts.id
    FROM accounts
    INNER JOIN requested_accounts
      ON accounts.id = requested_accounts.account_id
        OR accounts.authorization_instance_source_account_id = requested_accounts.account_id
  )
  INSERT INTO account_list_availability_dirty (
    account_id, viewer_system_account_id, generation, applied_generation, reason,
    available_at_ms, claim_token, claimed_by, claim_until_ms, attempt_count,
    created_at_ms, updated_at_ms
  )
  SELECT accounts.id, accounts.system_account_id,
    COALESCE((
      SELECT MAX(projections.source_generation)
      FROM account_list_availability_projections projections
      WHERE projections.account_id = accounts.id
    ), 0) + 1,
    0, left(p_reason, 128), v_now_ms, NULL, NULL, NULL, 0, v_now_ms, v_now_ms
  FROM affected_accounts
  INNER JOIN accounts ON accounts.id = affected_accounts.id
  ON CONFLICT (account_id) DO UPDATE SET
    viewer_system_account_id = excluded.viewer_system_account_id,
    generation = account_list_availability_dirty.generation + 1,
    reason = excluded.reason,
    available_at_ms = LEAST(account_list_availability_dirty.available_at_ms, excluded.available_at_ms),
    claim_token = NULL,
    claimed_by = NULL,
    claim_until_ms = NULL,
    updated_at_ms = excluded.updated_at_ms;
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_account_family(
  p_account_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[p_account_id], p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_authorization_family(
  p_authorization_id text,
  p_resource_type text,
  p_resource_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT accounts.id
    FROM accounts
    WHERE accounts.authorization_instance_authorization_id = p_authorization_id
       OR (
         p_resource_type = 'account'
         AND (accounts.id = p_resource_id OR accounts.authorization_instance_source_account_id = p_resource_id)
       )
  ), p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_group(
  p_group_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT group_accounts.account_id
    FROM group_accounts
    WHERE group_accounts.group_id = p_group_id
  ), p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_tag(
  p_tag_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT account_tag_bindings.account_id
    FROM account_tag_bindings
    WHERE account_tag_bindings.tag_id = p_tag_id
  ), p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_proxy(
  p_proxy_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT accounts.id FROM accounts WHERE accounts.proxy_profile_id = p_proxy_id
  ), p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_profile(
  p_profile_id text,
  p_reason text
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT accounts.id FROM accounts WHERE accounts.provider_protocol_profile_id = p_profile_id
  ), p_reason);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_mark_dirty_quota_crossing(
  p_scope_type text,
  p_scope_id text,
  p_period text,
  p_old_cost double precision,
  p_new_cost double precision
) RETURNS void
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF p_scope_type NOT IN ('account_authorization', 'account_authorization_team') THEN
    RETURN;
  END IF;
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
    SELECT accounts.id
    FROM accounts
    INNER JOIN resource_authorizations authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    LEFT JOIN resource_authorization_grants team_grants
      ON p_scope_type = 'account_authorization_team'
      AND team_grants.resource_type = authorizations.resource_type
      AND team_grants.resource_id = authorizations.resource_id
      AND team_grants.grantee_type = 'team'
      AND team_grants.grantee_team_id = authorizations.effective_source_team_id
      AND team_grants.status = 'active'
      AND (team_grants.expires_at IS NULL OR team_grants.expires_at > to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'))
    WHERE ((
          p_scope_type = 'account_authorization'
          AND authorizations.id = p_scope_id
        ) OR (
          p_scope_type = 'account_authorization_team'
          AND accounts.id || ':' || authorizations.effective_source_team_id = p_scope_id
        ))
      AND COALESCE(((CASE
        WHEN p_scope_type = 'account_authorization_team' THEN team_grants.limits_json
        ELSE authorizations.limits_json
      END)::jsonb -> p_period ->> 'enabled')::boolean, false)
      AND (
        COALESCE(p_old_cost, 0) >= COALESCE(((CASE
          WHEN p_scope_type = 'account_authorization_team' THEN team_grants.limits_json
          ELSE authorizations.limits_json
        END)::jsonb -> p_period ->> 'limit')::double precision, 0)
      ) IS DISTINCT FROM (
        COALESCE(p_new_cost, 0) >= COALESCE(((CASE
          WHEN p_scope_type = 'account_authorization_team' THEN team_grants.limits_json
          ELSE authorizations.limits_json
        END)::jsonb -> p_period ->> 'limit')::double precision, 0)
      )
  ), 'authorization_quota_' || p_period || '_crossed');
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_accounts_insert_dirty_statement_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
DECLARE
  v_account_ids text[];
BEGIN
  SELECT array_agg(id) INTO v_account_ids FROM new_accounts;
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(v_account_ids, 'account_fact_changed');
  RETURN NULL;
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_accounts_update_dirty_statement_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
DECLARE
  v_account_ids text[];
BEGIN
  SELECT array_agg(new_accounts.id) INTO v_account_ids
  FROM new_accounts
  INNER JOIN old_accounts USING (id)
  WHERE ROW(
    new_accounts.config_revision,
    new_accounts.system_account_id,
    new_accounts.provider_code,
    new_accounts.provider_protocol_profile_id,
    new_accounts.protocol_code,
    new_accounts.protocol_version,
    new_accounts.name,
    new_accounts.type,
    new_accounts.status,
    new_accounts.proxy_profile_id,
    new_accounts.concurrency_limit,
    new_accounts.priority,
    new_accounts.super_priority_enabled,
    new_accounts.fallback_enabled,
    new_accounts.client_compatibility,
    new_accounts.schedulable,
    new_accounts.availability_schedule_json,
    new_accounts.availability_schedule_next_check_at,
    new_accounts.notes,
    new_accounts.account_expires_at,
    new_accounts.cooldown_until,
    new_accounts.last_error_code,
    new_accounts.last_error_message,
    new_accounts.last_error_trace_id,
    new_accounts.cooldown_retest_failure_count,
    new_accounts.cooldown_retest_observation_started_at,
    new_accounts.cooldown_retest_last_at,
    new_accounts.cooldown_retest_last_status_code,
    new_accounts.temporary_unavailable_continuous_probe_enabled,
    new_accounts.health_check_model,
    new_accounts.health_check_endpoint_mode,
    new_accounts.last_health_check_at,
    new_accounts.next_health_check_at,
    new_accounts.last_health_success_at,
    new_accounts.health_check_failure_count,
    new_accounts.health_check_failure_started_at,
    new_accounts.last_health_check_status_code,
    new_accounts.last_health_check_error_code,
    new_accounts.last_health_check_error_message,
    new_accounts.last_health_check_trace_id,
    new_accounts.stream_failure_count,
    new_accounts.stream_failure_window_started_at,
    new_accounts.balance_query_enabled,
    new_accounts.balance_query_config_json,
    new_accounts.balance_query_next_refresh_at,
    new_accounts.authorization_instance_source_account_id,
    new_accounts.authorization_instance_authorization_id,
    new_accounts.authorization_instance_owner_system_account_id,
    new_accounts.deleted_at
  ) IS DISTINCT FROM ROW(
    old_accounts.config_revision,
    old_accounts.system_account_id,
    old_accounts.provider_code,
    old_accounts.provider_protocol_profile_id,
    old_accounts.protocol_code,
    old_accounts.protocol_version,
    old_accounts.name,
    old_accounts.type,
    old_accounts.status,
    old_accounts.proxy_profile_id,
    old_accounts.concurrency_limit,
    old_accounts.priority,
    old_accounts.super_priority_enabled,
    old_accounts.fallback_enabled,
    old_accounts.client_compatibility,
    old_accounts.schedulable,
    old_accounts.availability_schedule_json,
    old_accounts.availability_schedule_next_check_at,
    old_accounts.notes,
    old_accounts.account_expires_at,
    old_accounts.cooldown_until,
    old_accounts.last_error_code,
    old_accounts.last_error_message,
    old_accounts.last_error_trace_id,
    old_accounts.cooldown_retest_failure_count,
    old_accounts.cooldown_retest_observation_started_at,
    old_accounts.cooldown_retest_last_at,
    old_accounts.cooldown_retest_last_status_code,
    old_accounts.temporary_unavailable_continuous_probe_enabled,
    old_accounts.health_check_model,
    old_accounts.health_check_endpoint_mode,
    old_accounts.last_health_check_at,
    old_accounts.next_health_check_at,
    old_accounts.last_health_success_at,
    old_accounts.health_check_failure_count,
    old_accounts.health_check_failure_started_at,
    old_accounts.last_health_check_status_code,
    old_accounts.last_health_check_error_code,
    old_accounts.last_health_check_error_message,
    old_accounts.last_health_check_trace_id,
    old_accounts.stream_failure_count,
    old_accounts.stream_failure_window_started_at,
    old_accounts.balance_query_enabled,
    old_accounts.balance_query_config_json,
    old_accounts.balance_query_next_refresh_at,
    old_accounts.authorization_instance_source_account_id,
    old_accounts.authorization_instance_authorization_id,
    old_accounts.authorization_instance_owner_system_account_id,
    old_accounts.deleted_at
  );
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(v_account_ids, 'account_fact_changed');
  RETURN NULL;
END;
$function$;

/**
 * Usage traffic updates last_used_at continuously. It is a displayed/sorted
 * telemetry value, not an availability decision, so update that projection
 * column in place instead of making the whole viewer unavailable for every
 * gateway request.
 */
CREATE OR REPLACE FUNCTION account_list_availability_accounts_last_used_projection_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  UPDATE account_list_availability_projections projections
  SET last_used_at_sort_key = NEW.last_used_at,
      payload_json = CASE
        WHEN NEW.last_used_at IS NULL THEN (projections.payload_json::jsonb - 'lastUsedAt')::text
        ELSE jsonb_set(
          projections.payload_json::jsonb,
          '{lastUsedAt}',
          to_jsonb(NEW.last_used_at),
          true
        )::text
      END
  WHERE projections.account_id = NEW.id;
  RETURN NEW;
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_authorizations_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_authorization_family(
      NEW.id, NEW.resource_type, NEW.resource_id, 'authorization_fact_changed'
    );
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_authorization_family(
      OLD.id, OLD.resource_type, OLD.resource_id, 'authorization_fact_changed'
    );
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_authorization_sources_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_authorization_family(NEW.authorization_id, '', '', 'authorization_source_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_authorization_family(OLD.authorization_id, '', '', 'authorization_source_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_authorization_grants_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' AND NEW.grantee_type = 'team' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
      SELECT accounts.id
      FROM accounts
      INNER JOIN resource_authorizations authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      WHERE authorizations.resource_type = NEW.resource_type
        AND authorizations.resource_id = NEW.resource_id
        AND authorizations.effective_source_team_id = NEW.grantee_team_id
    ), 'authorization_team_grant_changed');
  END IF;
  IF TG_OP <> 'INSERT' AND OLD.grantee_type = 'team' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY(
      SELECT accounts.id
      FROM accounts
      INNER JOIN resource_authorizations authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      WHERE authorizations.resource_type = OLD.resource_type
        AND authorizations.resource_id = OLD.resource_id
        AND authorizations.effective_source_team_id = OLD.grantee_team_id
    ), 'authorization_team_grant_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_group_accounts_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[NEW.account_id], 'group_binding_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[OLD.account_id], 'group_binding_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_groups_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_group(COALESCE(NEW.id, OLD.id), 'group_fact_changed');
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_tag_bindings_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[NEW.account_id], 'tag_binding_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[OLD.account_id], 'tag_binding_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_tags_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_tag(NEW.id, 'tag_fact_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_tag(OLD.id, 'tag_fact_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_name_search_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
DECLARE
  v_account_id text;
BEGIN
  v_account_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.account_id ELSE NEW.account_id END;
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(
    ARRAY[v_account_id], 'account_name_search_changed'
  );
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_runtime_state_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_accounts(ARRAY[COALESCE(NEW.account_id, OLD.account_id)], 'api_key_runtime_changed');
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_circuit_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  PERFORM juhe_business.account_list_availability_mark_dirty_account_family(COALESCE(NEW.account_id, OLD.account_id), 'circuit_changed');
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_proxy_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_proxy(NEW.id, 'proxy_fact_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_proxy(OLD.id, 'proxy_fact_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_profile_dirty_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  IF TG_OP <> 'DELETE' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_profile(NEW.id, 'profile_fact_changed');
  END IF;
  IF TG_OP <> 'INSERT' THEN
    PERFORM juhe_business.account_list_availability_mark_dirty_profile(OLD.id, 'profile_fact_changed');
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_system_account_health_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  INSERT INTO account_list_availability_projection_viewer_health (
    viewer_system_account_id, projection_count, oldest_projected_at,
    next_transition_at, is_current, updated_at
  ) VALUES (NEW.id, 0, NULL, NULL, 1, to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'))
  ON CONFLICT(viewer_system_account_id) DO NOTHING;
  RETURN NEW;
END;
$function$;

CREATE OR REPLACE FUNCTION account_list_availability_projection_delete_health_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
BEGIN
  INSERT INTO account_list_availability_projection_viewer_health (
    viewer_system_account_id, projection_count, oldest_projected_at,
    next_transition_at, is_current, updated_at
  ) VALUES (OLD.viewer_system_account_id, 0, NULL, NULL, 0, to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'))
  ON CONFLICT(viewer_system_account_id) DO UPDATE SET
    is_current = 0,
    updated_at = excluded.updated_at;
  RETURN OLD;
END;
$function$;

DROP TRIGGER IF EXISTS account_list_availability_accounts_insert ON accounts;
CREATE TRIGGER account_list_availability_accounts_insert
AFTER INSERT ON accounts
REFERENCING NEW TABLE AS new_accounts
FOR EACH STATEMENT EXECUTE FUNCTION account_list_availability_accounts_insert_dirty_statement_trigger();
DROP TRIGGER IF EXISTS account_list_availability_accounts_update ON accounts;
CREATE TRIGGER account_list_availability_accounts_update
AFTER UPDATE ON accounts
REFERENCING OLD TABLE AS old_accounts NEW TABLE AS new_accounts
FOR EACH STATEMENT EXECUTE FUNCTION account_list_availability_accounts_update_dirty_statement_trigger();
DROP TRIGGER IF EXISTS account_list_availability_accounts_last_used ON accounts;
CREATE TRIGGER account_list_availability_accounts_last_used
AFTER UPDATE OF last_used_at ON accounts
FOR EACH ROW EXECUTE FUNCTION account_list_availability_accounts_last_used_projection_trigger();
DROP TRIGGER IF EXISTS account_list_availability_authorizations ON resource_authorizations;
CREATE TRIGGER account_list_availability_authorizations
AFTER INSERT OR UPDATE OR DELETE ON resource_authorizations
FOR EACH ROW EXECUTE FUNCTION account_list_availability_authorizations_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_authorization_sources ON resource_authorization_sources;
CREATE TRIGGER account_list_availability_authorization_sources
AFTER INSERT OR UPDATE OR DELETE ON resource_authorization_sources
FOR EACH ROW EXECUTE FUNCTION account_list_availability_authorization_sources_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_authorization_grants ON resource_authorization_grants;
CREATE TRIGGER account_list_availability_authorization_grants
AFTER INSERT OR UPDATE OR DELETE ON resource_authorization_grants
FOR EACH ROW EXECUTE FUNCTION account_list_availability_authorization_grants_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_group_accounts ON group_accounts;
CREATE TRIGGER account_list_availability_group_accounts
AFTER INSERT OR UPDATE OR DELETE ON group_accounts
FOR EACH ROW EXECUTE FUNCTION account_list_availability_group_accounts_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_groups ON groups;
CREATE TRIGGER account_list_availability_groups
AFTER UPDATE OR DELETE ON groups
FOR EACH ROW EXECUTE FUNCTION account_list_availability_groups_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_tag_bindings ON account_tag_bindings;
CREATE TRIGGER account_list_availability_tag_bindings
AFTER INSERT OR UPDATE OR DELETE ON account_tag_bindings
FOR EACH ROW EXECUTE FUNCTION account_list_availability_tag_bindings_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_tags ON account_tags;
CREATE TRIGGER account_list_availability_tags
AFTER UPDATE OR DELETE ON account_tags
FOR EACH ROW EXECUTE FUNCTION account_list_availability_tags_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_name_search_documents ON account_name_search_documents;
CREATE TRIGGER account_list_availability_name_search_documents
AFTER INSERT OR UPDATE OR DELETE ON account_name_search_documents
FOR EACH ROW EXECUTE FUNCTION account_list_availability_name_search_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_name_search_terms ON account_name_search_terms;
CREATE TRIGGER account_list_availability_name_search_terms
AFTER INSERT OR UPDATE OR DELETE ON account_name_search_terms
FOR EACH ROW EXECUTE FUNCTION account_list_availability_name_search_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_api_key_runtime ON account_api_key_runtime_states;
CREATE TRIGGER account_list_availability_api_key_runtime
AFTER INSERT OR UPDATE OR DELETE ON account_api_key_runtime_states
FOR EACH ROW EXECUTE FUNCTION account_list_availability_runtime_state_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_circuits ON account_circuit_incidents;
CREATE TRIGGER account_list_availability_circuits
AFTER INSERT OR UPDATE OR DELETE ON account_circuit_incidents
FOR EACH ROW EXECUTE FUNCTION account_list_availability_circuit_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_proxies ON proxy_profiles;
CREATE TRIGGER account_list_availability_proxies
AFTER UPDATE OR DELETE ON proxy_profiles
FOR EACH ROW EXECUTE FUNCTION account_list_availability_proxy_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_profiles ON provider_protocol_profiles;
CREATE TRIGGER account_list_availability_profiles
AFTER UPDATE OR DELETE ON provider_protocol_profiles
FOR EACH ROW EXECUTE FUNCTION account_list_availability_profile_dirty_trigger();
DROP TRIGGER IF EXISTS account_list_availability_system_account_health ON system_accounts;
CREATE TRIGGER account_list_availability_system_account_health
AFTER INSERT ON system_accounts
FOR EACH ROW EXECUTE FUNCTION account_list_availability_system_account_health_trigger();
DROP TRIGGER IF EXISTS account_list_availability_projection_delete_health ON account_list_availability_projections;
CREATE TRIGGER account_list_availability_projection_delete_health
AFTER DELETE ON account_list_availability_projections
FOR EACH ROW EXECUTE FUNCTION account_list_availability_projection_delete_health_trigger();
`
  },
  {
    schemaName: 'juhe_stats',
    source: 'account-list-projection-pg-quota-crossing-triggers',
    sql: `
CREATE OR REPLACE FUNCTION account_list_availability_quota_crossing_trigger()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = juhe_business, public
AS $function$
DECLARE
  v_old_cost double precision;
  v_new_cost double precision;
BEGIN
  v_old_cost := CASE WHEN TG_OP = 'INSERT' THEN 0 ELSE COALESCE(OLD.total_cost_usd, 0) END;
  v_new_cost := CASE WHEN TG_OP = 'DELETE' THEN 0 ELSE COALESCE(NEW.total_cost_usd, 0) END;
  PERFORM juhe_business.account_list_availability_mark_dirty_quota_crossing(
    COALESCE(NEW.scope_type, OLD.scope_type),
    COALESCE(NEW.scope_id, OLD.scope_id),
    TG_ARGV[0], v_old_cost, v_new_cost
  );
  RETURN COALESCE(NEW, OLD);
END;
$function$;

DROP TRIGGER IF EXISTS account_list_availability_quota_total ON usage_stats_totals;
CREATE TRIGGER account_list_availability_quota_total
AFTER INSERT OR UPDATE OR DELETE ON usage_stats_totals
FOR EACH ROW EXECUTE FUNCTION account_list_availability_quota_crossing_trigger('total');
DROP TRIGGER IF EXISTS account_list_availability_quota_daily ON usage_stats_daily;
CREATE TRIGGER account_list_availability_quota_daily
AFTER INSERT OR UPDATE OR DELETE ON usage_stats_daily
FOR EACH ROW EXECUTE FUNCTION account_list_availability_quota_crossing_trigger('daily');
DROP TRIGGER IF EXISTS account_list_availability_quota_weekly ON usage_stats_weekly;
CREATE TRIGGER account_list_availability_quota_weekly
AFTER INSERT OR UPDATE OR DELETE ON usage_stats_weekly
FOR EACH ROW EXECUTE FUNCTION account_list_availability_quota_crossing_trigger('weekly');
DROP TRIGGER IF EXISTS account_list_availability_quota_monthly ON usage_stats_monthly;
CREATE TRIGGER account_list_availability_quota_monthly
AFTER INSERT OR UPDATE OR DELETE ON usage_stats_monthly
FOR EACH ROW EXECUTE FUNCTION account_list_availability_quota_crossing_trigger('monthly');
DROP TRIGGER IF EXISTS account_list_availability_quota_hourly ON usage_quota_hourly_windows;
CREATE TRIGGER account_list_availability_quota_hourly
AFTER INSERT OR UPDATE OR DELETE ON usage_quota_hourly_windows
FOR EACH ROW EXECUTE FUNCTION account_list_availability_quota_crossing_trigger('hourly');
`
  }
]

const postgresBigintColumnNames = new Set([
  'bytes',
  'asset_bytes',
  'content_bytes',
  'storage_reserved_bytes',
  'reserved_bytes',
  'original_bytes',
  'processed_bytes',
  'request_body_bytes',
  'usage_bytes',
  'storage_offset_bytes',
  'raw_size_bytes',
  'compressed_size_bytes',
  'raw_payload_bytes',
  'compressed_payload_bytes',
  'compression_saved_bytes',
  'request_size_bytes',
  'response_size_bytes',
  'request_count',
  'success_count',
  'error_count',
  'input_tokens',
  'output_tokens',
  'cache_read_tokens',
  'cache_write_tokens',
  'cache_write_1h_tokens',
  'thinking_tokens',
  'input_image_tokens',
  'output_image_tokens',
  'duration_ms_sum',
  'duration_ms_count',
  'duration_ms_max',
  'first_token_ms_sum',
  'first_token_ms_count',
  'first_token_ms_max',
  'sample_count',
  'event_loop_lag_ms_count',
  'network_rx_bytes_per_sec_count',
  'network_tx_bytes_per_sec_count',
  'hit_count',
  'row_count',
  'page_count',
  'freelist_count',
  'table_count',
  'index_count',
  'growth_bytes_1h',
  'growth_rows_1h',
  'growth_bytes_24h',
  'growth_rows_24h',
  'next_sequence_no',
  'user_turn_count',
  'message_revision',
  'sequence_no',
  'context_revision',
  'compacted_through_sequence',
  'context_claim_revision',
  'context_claim_through_sequence',
  'context_progress_sequence',
  'dispatch_revision',
  'circuit_projection_revision',
  'ledger_revision',
  'projected_ledger_revision',
  'open_until_ms',
  'next_transition_at_ms',
  'next_retry_at_ms',
  'lease_until_ms',
  'generation',
  'fencing_token',
  'attempt_started_at_ms',
  'attempt_hard_deadline_ms',
  'retained_until_ms',
  'available_at_ms',
  'claim_until_ms',
  'acknowledged_at_ms',
  'created_at_ms',
  'updated_at_ms',
  'source_revision',
  'source_from_sequence',
  'source_through_sequence',
  'recent_tail_from_sequence',
  'entry_from_sequence',
  'entry_through_sequence',
  'sequence',
  'active_context_tokens',
  'effective_context_limit_tokens',
  'estimated_input_tokens',
  'upstream_input_tokens',
  'token_count'
])

export function collectPostgresSchemaStatements(): PostgresSchemaStatement[] {
  const statements: PostgresSchemaStatement[] = []
  for (const definition of schemaSourceDefinitions) {
    const rawStatements = collectSqlStatements(definition.apply)
    for (const rawStatement of rawStatements) {
      const normalized = transformSqliteStatementToPostgres(rawStatement, definition.schemaName)
      if (!normalized) continue
      statements.push({
        schemaName: definition.schemaName,
        source: definition.source,
        sql: normalized
      })
    }
  }
  statements.push(...supplementalSchemaStatements, ...accountListAvailabilityProjectionTriggerStatements)
  return orderSchemaStatements(statements)
}

export function buildPostgresSchemaSql(): string {
  const statements = collectPostgresSchemaStatements()
  const chunks: string[] = [
    '-- Generated from the SQLite schema definitions in backend/src/storage/schema.',
    '-- Each schema is initialized with a dedicated search_path for PostgreSQL.'
  ]
  const seenSchemas = new Set<PostgresSchemaName>()
  for (const statement of statements) {
    if (!seenSchemas.has(statement.schemaName)) {
      seenSchemas.add(statement.schemaName)
      chunks.push('')
      chunks.push(`-- schema: ${statement.schemaName}`)
      chunks.push(`CREATE SCHEMA IF NOT EXISTS ${quoteIdentifier(statement.schemaName)};`)
      chunks.push(`SET search_path TO ${quoteIdentifier(statement.schemaName)}, public;`)
    }
    chunks.push(`${statement.sql};`)
  }
  return chunks.join('\n')
}

export async function applyPostgresSchema(client: Pick<DatabaseClient, 'execute'>): Promise<{ schemaCount: number; statementCount: number }> {
  const statements = collectPostgresSchemaStatements()
  const createdSchemas = new Set<PostgresSchemaName>()
  for (const statement of statements) {
    if (!createdSchemas.has(statement.schemaName)) {
      createdSchemas.add(statement.schemaName)
      await client.execute(`CREATE SCHEMA IF NOT EXISTS ${quoteIdentifier(statement.schemaName)}`)
    }
    await client.execute(`SET search_path TO ${quoteIdentifier(statement.schemaName)}, public;\n${statement.sql}`)
  }
  return {
    schemaCount: createdSchemas.size,
    statementCount: statements.length
  }
}

function collectSqlStatements(applySchema: (database: DatabaseSync) => void): string[] {
  const statements: string[] = []
  const recorder = {
    exec(sql: string): void {
      statements.push(sql)
    },
    prepare() {
      return {
        all: () => [],
        get: () => undefined,
        run: () => ({ changes: 0, lastInsertRowid: 0 })
      }
    }
  } as unknown as DatabaseSync
  applySchema(recorder)
  return statements.flatMap(splitSqlStatements)
}

function splitSqlStatements(sql: string): string[] {
  const statements: string[] = []
  let buffer = ''
  let inSingleQuote = false
  let inDoubleQuote = false
  let inLineComment = false
  let inBlockComment = false

  for (let index = 0; index < sql.length; index += 1) {
    const current = sql[index]
    const next = sql[index + 1]

    if (inLineComment) {
      if (current === '\n') {
        inLineComment = false
        buffer += current
      }
      continue
    }

    if (inBlockComment) {
      if (current === '*' && next === '/') {
        inBlockComment = false
        index += 1
      }
      continue
    }

    if (!inSingleQuote && !inDoubleQuote && current === '-' && next === '-') {
      inLineComment = true
      index += 1
      continue
    }

    if (!inSingleQuote && !inDoubleQuote && current === '/' && next === '*') {
      inBlockComment = true
      index += 1
      continue
    }

    if (current === "'" && !inDoubleQuote) {
      buffer += current
      if (inSingleQuote && next === "'") {
        buffer += next
        index += 1
        continue
      }
      inSingleQuote = !inSingleQuote
      continue
    }

    if (current === '"' && !inSingleQuote) {
      buffer += current
      if (inDoubleQuote && next === '"') {
        buffer += next
        index += 1
        continue
      }
      inDoubleQuote = !inDoubleQuote
      continue
    }

    if (current === ';' && !inSingleQuote && !inDoubleQuote) {
      const statement = buffer.trim()
      if (statement.length > 0) {
        statements.push(statement)
      }
      buffer = ''
      continue
    }

    buffer += current
  }

  const finalStatement = buffer.trim()
  if (finalStatement.length > 0) {
    statements.push(finalStatement)
  }

  return statements
}

function transformSqliteStatementToPostgres(sql: string, schemaName: PostgresSchemaName): string | undefined {
  const trimmed = sql.trim()
  if (trimmed.length === 0) {
    return undefined
  }
  if (/^PRAGMA\b/i.test(trimmed)) {
    return undefined
  }
  if (/^ALTER\s+TABLE\b/i.test(trimmed)) {
    return undefined
  }

  let transformed = trimmed
  transformed = transformed.replace(/CHECK\s*\(\s*json_valid\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)\s+AND\s+json_type\(\s*\1\s*\)\s*=\s*'(object|array)'\s*\)/gi, (_match, columnName: string, jsonType: string) => {
    return `CHECK (jsonb_typeof(${columnName}::jsonb) = '${jsonType.toLowerCase()}')`
  })
  transformed = transformed.replace(/\bjson_array_length\(\s*([A-Za-z_][A-Za-z0-9_]*)\s*\)/gi, (_match, columnName: string) => {
    return `jsonb_array_length(${columnName}::jsonb)`
  })
  transformed = transformed.replace(/\b([A-Za-z_][A-Za-z0-9_]*)\s+COLLATE\s+NOCASE\b/gi, (_match, columnName: string) => {
    return `lower(${columnName})`
  })
  transformed = transformed.replace(/\bAUTOINCREMENT\b/gi, '')
  transformed = transformed.replace(/\bBLOB\b/gi, 'bytea')
  transformed = transformed.replace(/\bREAL\b/gi, 'double precision')
  transformed = transformed.replace(/\bINTEGER\b/gi, 'integer')
  transformed = transformed.replace(/^(\s*)([A-Za-z_][A-Za-z0-9_]*)(\s+)integer\b/gim, (match, indent: string, columnName: string, spacing: string) => {
    return postgresBigintColumnNames.has(columnName.toLowerCase())
      ? `${indent}${columnName}${spacing}bigint`
      : match
  })
  transformed = transformed.replace(/\bTEXT\b/gi, 'text')
  transformed = transformProviderModelCatalogTableForPostgres(transformed, schemaName)
  transformed = transformCustomProviderModelsTableForPostgres(transformed, schemaName)
  transformed = transformUsageRecordsTableForPostgres(transformed, schemaName)
  transformed = transformChatMessagesTableForPostgres(transformed, schemaName)
  transformed = transformAccountRuntimeTablesForPostgres(transformed, schemaName)
  transformed = transformProxyProfilesTableForPostgres(transformed, schemaName)
  transformed = transformed.replace(/[ \t]+\n/g, '\n')
  transformed = transformed.replace(/\n{3,}/g, '\n\n')
  return transformed
}

function transformAccountRuntimeTablesForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  const normalized = sql.trim()
  if (schemaName === 'juhe_business' && /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+account_test_tasks\s*\(/i.test(normalized)) {
    return postgresTimestampColumns(
      sql.replace(/\bcancel_requested\s+integer\s+NOT\s+NULL\s+DEFAULT\s+0\b/i, 'cancel_requested boolean NOT NULL DEFAULT false'),
      ['queued_at', 'queued_deadline_at', 'started_at', 'finished_at', 'created_at', 'updated_at']
    )
  }
  if (schemaName === 'juhe_business' && /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+account_test_sessions\s*\(/i.test(normalized)) {
    return postgresTimestampColumns(sql, ['last_heartbeat_at', 'cancel_requested_at', 'finished_at', 'created_at', 'updated_at'])
  }
  if (schemaName === 'juhe_business' && /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+account_test_session_tasks\s*\(/i.test(normalized)) {
    return postgresTimestampColumns(sql, ['created_at'])
  }
  if (schemaName === 'juhe_stats' && /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+account_usage_snapshots\s*\(/i.test(normalized)) {
    return postgresTimestampColumns(sql, ['last_attempt_at', 'last_success_at', 'next_refresh_after', 'updated_at', 'created_at'])
  }
  return sql
}

function postgresTimestampColumns(sql: string, columnNames: string[]): string {
  let transformed = sql
  for (const columnName of columnNames) {
    transformed = transformed.replace(
      new RegExp(`\\b${columnName}\\s+text\\b`, 'i'),
      `${columnName} timestamptz`
    )
  }
  return transformed
}

function transformProxyProfilesTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_business') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+proxy_profiles\s*\(/i.test(sql.trim())) return sql
  return postgresTimestampColumns(
    sql.replace(/\benabled\s+integer\s+NOT\s+NULL\s+DEFAULT\s+1\b/i, 'enabled boolean NOT NULL DEFAULT true'),
    ['last_tested_at', 'created_at', 'updated_at']
  )
}

function transformProviderModelCatalogTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_business') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+provider_model_catalog\s*\(/i.test(sql.trim())) return sql
  return sql
    .replace(/\blong_context_input_token_threshold_inclusive\s+integer\s+NOT\s+NULL\s+DEFAULT\s+0\b/i, 'long_context_input_token_threshold_inclusive boolean NOT NULL DEFAULT false')
    .replace(/\bsupports_prompt_caching\s+integer\s+NOT\s+NULL\s+DEFAULT\s+0\b/i, 'supports_prompt_caching boolean NOT NULL DEFAULT false')
    .replace(/\bcatalog_visible\s+integer\s+NOT\s+NULL\s+DEFAULT\s+1\b/i, 'catalog_visible boolean NOT NULL DEFAULT true')
}

function transformCustomProviderModelsTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_business') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+custom_provider_models\s*\(/i.test(sql.trim())) return sql
  return sql.replace(/\bcatalog_visible\s+integer\s+NOT\s+NULL\s+DEFAULT\s+1\b/i, 'catalog_visible boolean NOT NULL DEFAULT true')
}

function transformChatMessagesTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_chat') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+chat_messages\s*\(/i.test(sql.trim())) return sql
  return sql
    .replace(/\bid\s+text\s+PRIMARY\s+KEY\b/i, 'id text NOT NULL')
    .replace(/\n\s*\)\s*$/i, ',\n      PRIMARY KEY (created_at, id)\n    ) PARTITION BY RANGE (created_at)')
}

function transformUsageRecordsTableForPostgres(sql: string, schemaName: PostgresSchemaName): string {
  if (schemaName !== 'juhe_usage') return sql
  if (!/^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+usage_records\s*\(/i.test(sql.trim())) return sql
  return sql
    .replace(/\bid\s+text\s+PRIMARY\s+KEY\b/i, 'id text NOT NULL')
    .replace(/\n\s*\)\s*$/i, ',\n      PRIMARY KEY (created_at, id)\n    ) PARTITION BY RANGE (created_at)')
}

function orderSchemaStatements(statements: PostgresSchemaStatement[]): PostgresSchemaStatement[] {
  const ordered: PostgresSchemaStatement[] = []
  const groups = new Map<PostgresSchemaName, PostgresSchemaStatement[]>()
  for (const statement of statements) {
    const group = groups.get(statement.schemaName) ?? []
    group.push(statement)
    groups.set(statement.schemaName, group)
  }

  for (const group of groups.values()) {
    const tableStatements = group
      .map((statement) => ({ statement, tableName: extractCreatedTableName(statement.sql) }))
      .filter((entry): entry is { statement: PostgresSchemaStatement; tableName: string } => Boolean(entry.tableName))
    const nonTableStatements = group
      .filter((statement) => !extractCreatedTableName(statement.sql))
      .sort((left, right) => schemaStatementPhase(left.sql) - schemaStatementPhase(right.sql))
    const tableNames = new Set(tableStatements.map((entry) => entry.tableName))
    const resolvedTables = new Set<string>()
    const remaining = [...tableStatements]

    while (remaining.length > 0) {
      const nextIndex = remaining.findIndex((entry) => {
        const dependencies = extractReferencedTableNames(entry.statement.sql)
          .filter((tableName) => tableName !== entry.tableName && tableNames.has(tableName))
        return dependencies.every((tableName) => resolvedTables.has(tableName))
      })
      const selectedIndex = nextIndex >= 0 ? nextIndex : 0
      const [selected] = remaining.splice(selectedIndex, 1)
      resolvedTables.add(selected.tableName)
      ordered.push(selected.statement)
    }

    ordered.push(...nonTableStatements)
  }

  return ordered
}

function schemaStatementPhase(sql: string): number {
  const normalized = sql.trim().toUpperCase()
  if (normalized.startsWith('ALTER TABLE') || normalized.startsWith('DO $$')) return 0
  if (normalized.startsWith('CREATE INDEX') || normalized.startsWith('CREATE UNIQUE INDEX')) return 2
  return 1
}

function extractCreatedTableName(sql: string): string | undefined {
  const match = /^CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+"?([A-Za-z_][A-Za-z0-9_]*)"?\s*\(/i.exec(sql.trim())
  return match?.[1]?.toLowerCase()
}

function extractReferencedTableNames(sql: string): string[] {
  const tableNames: string[] = []
  for (const match of sql.matchAll(/\bREFERENCES\s+"?([A-Za-z_][A-Za-z0-9_]*)"?\s*\(/gi)) {
    tableNames.push(match[1].toLowerCase())
  }
  return tableNames
}

function quoteIdentifier(identifier: string): string {
  return `"${identifier.replace(/"/g, '""')}"`
}
