// Code generated from the Node PostgreSQL storage sources. The statements
// in this file are a verbatim extract of one schema group from
// pg_schema.go's postgresSchemaStatements literal (see the header of
// pg_schema.go for the full provenance and execution model). The
// statements are data: do not hand-edit them and keep them byte-identical
// to the Node dump order.

package schema

// postgresSchemaBusinessIndexes holds the juhe_business CREATE INDEX
// statements of the index phase in execution order.
var postgresSchemaBusinessIndexes = []PGStatement{
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_provider_model_catalog_lookup
      ON provider_model_catalog(provider_code, status, catalog_visible, catalog_order, model)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_lock_states_deadline ON account_lock_states(lock_state, deadline_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_jobs_input_outbox_pending
      ON account_health_jobs_input_outbox(status, available_at, created_at, event_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_jobs_input_outbox_account
      ON account_health_jobs_input_outbox(account_id, input_version DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_provider_status ON accounts(provider_code, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_protocol_profile_status ON accounts(provider_protocol_profile_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_provider ON groups(provider_code)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_sessions_expires_at ON system_sessions(expires_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_username_unique_lower ON system_accounts(lower(username))`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_accounts_display_name_unique_lower ON system_accounts(lower(display_name))`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_enabled_priority ON response_inspection_policies(enabled, priority, updated_at DESC, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_external_integration_sources_updated ON external_integration_sources(updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_external_integration_sources_status_updated ON external_integration_sources(status, updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_external_integration_sources_name_lookup ON external_integration_sources(lower(name), id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_external_integration_source_tokens_source ON external_integration_source_tokens(source_ref_id, status, expires_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_accounts_updated_lookup ON system_accounts(updated_at, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_accounts_username_lookup ON system_accounts(lower(username), id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_accounts_display_name_lookup ON system_accounts(lower(display_name), id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_credential_fingerprint ON accounts(credential_fingerprint) WHERE credential_fingerprint IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_owner_name_unique ON accounts(system_account_id, name) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_owner_all_name_lookup
      ON accounts(system_account_id, name, id)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_owner_name_lookup
      ON accounts(system_account_id, name, id)
      WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_name_lookup ON accounts(name, id) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_name_lookup ON accounts(system_account_id, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_term_owner
      ON account_name_search_terms(term, system_account_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_owner_term
      ON account_name_search_terms(system_account_id, term, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_name_search_terms_account
      ON account_name_search_terms(account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_name_search_documents_owner
      ON account_name_search_documents(system_account_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_priority
      ON account_list_availability_projections(
        viewer_system_account_id,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_name
      ON account_list_availability_projections(
        viewer_system_account_id,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_schedulable_priority
      ON account_list_availability_projections(
        viewer_system_account_id,
        schedulable_bucket,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_due
      ON account_list_availability_projections(next_transition_at ASC, viewer_system_account_id, account_id)
      WHERE next_transition_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_tags_lookup
      ON account_list_availability_projection_tags(viewer_system_account_id, tag_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_search_terms_lookup
      ON account_list_availability_projection_search_terms(viewer_system_account_id, term, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_search_terms_name_order
      ON account_list_availability_projection_search_terms(
        viewer_system_account_id,
        term,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_priority
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_name
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_name_search_incomplete
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        name_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )
      WHERE search_index_complete = 0`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_schedulable_priority
      ON account_list_availability_projection_index(
        viewer_system_account_id,
        schedulable_bucket,
        priority_sort_key ASC,
        created_at_sort_key ASC,
        account_id ASC
      )`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_viewer_health_refresh
      ON account_list_availability_projection_viewer_health(is_current, updated_at ASC, viewer_system_account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_dirty_claim
      ON account_list_availability_dirty(available_at_ms ASC, created_at_ms ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_dirty_viewer
      ON account_list_availability_dirty(viewer_system_account_id, available_at_ms ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_viewer_projected
      ON account_list_availability_projections(viewer_system_account_id, projected_at ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_viewer_transition
      ON account_list_availability_projections(viewer_system_account_id, next_transition_at ASC, account_id ASC)
      WHERE next_transition_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_list_availability_runtime_overlay_due
      ON account_list_availability_runtime_overlays(next_reconcile_at ASC, account_id ASC)
      WHERE next_reconcile_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_provider_lookup ON accounts(provider_code, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_protocol_profile_lookup ON accounts(provider_protocol_profile_id, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_provider_lookup ON accounts(system_account_id, provider_code, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_protocol_profile_lookup ON accounts(system_account_id, provider_protocol_profile_id, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_type_lookup ON accounts(type, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_type_lookup ON accounts(system_account_id, type, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account ON accounts(system_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_owner_list_order
      ON accounts(system_account_id, priority ASC, created_at ASC, id ASC)
      WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_proxy_profile ON accounts(proxy_profile_id, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_last_used ON accounts(system_account_id, last_used_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_health_monitor_order
      ON accounts((last_used_at IS NULL) ASC, last_used_at DESC, name ASC, id ASC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_owner_health_monitor_order
      ON accounts(system_account_id, (last_used_at IS NULL) ASC, last_used_at DESC, name ASC, id ASC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_system_account_concurrency ON accounts(system_account_id, concurrency_limit)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_expiry_sweep
      ON accounts(account_expires_at ASC, updated_at ASC, id ASC)
      WHERE account_expires_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_owner_expiry_sweep
      ON accounts(system_account_id, account_expires_at ASC, updated_at ASC, id ASC)
      WHERE account_expires_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_availability_schedule_next_check
      ON accounts(availability_schedule_next_check_at ASC, id ASC)
      WHERE availability_schedule_json IS NOT NULL AND deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_super_priority ON accounts(super_priority_enabled, status, priority)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_dispatch_priority ON accounts(fallback_enabled, super_priority_enabled, status, priority)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_due
      ON accounts(provider_code, type, oauth_refresh_token_present, oauth_access_token_expires_at, status, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_openai_oauth_refresh_pg_due
      ON accounts(provider_protocol_profile_id, type, oauth_refresh_token_present, (oauth_access_token_expires_at IS NOT NULL), oauth_access_token_expires_at ASC, updated_at ASC, id ASC)
      WHERE authorization_instance_authorization_id IS NULL AND deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_health_check_due
      ON accounts(status, next_health_check_at, updated_at, id)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_health_check_candidate_order
      ON accounts(
        (CASE WHEN status = 'pending_test' THEN 0 ELSE 1 END) ASC,
        (CASE WHEN status = 'pending_test' THEN updated_at END) DESC,
        (next_health_check_at IS NOT NULL) ASC,
        next_health_check_at ASC,
        last_health_check_at ASC,
        created_at ASC,
        id ASC
      )
      WHERE deleted_at IS NULL
        AND status IN ('active', 'pending_test')
        AND (status = 'pending_test' OR schedulable = 1)
        AND type IN ('api_key', 'oauth', 'google_oauth')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_due
      ON model_quality_schedules(enabled, next_run_at, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_model_quality_schedules_scope
      ON model_quality_schedules(system_account_id, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_quality_enforcements_recovery
      ON account_quality_enforcements(state, action, recovery_due_at, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_quality_enforcements_scope
      ON account_quality_enforcements(system_account_id, updated_at DESC, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_cooldown_retest_candidate_order
      ON accounts(cooldown_until ASC, priority ASC, created_at ASC, id ASC, health_check_endpoint_mode)
      WHERE deleted_at IS NULL
        AND cooldown_until IS NOT NULL
        AND schedulable = 1
        AND type IN ('api_key', 'oauth', 'google_oauth')
        AND status IN ('temporary_unavailable', 'rate_limited')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_cooldown_retest_legacy_repair_order
      ON accounts(cooldown_until ASC, priority ASC, created_at ASC, id ASC)
      WHERE deleted_at IS NULL
        AND cooldown_until IS NOT NULL
        AND type IN ('api_key', 'oauth', 'google_oauth')
        AND status IN ('temporary_unavailable', 'rate_limited')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_deleted_cleanup
      ON accounts(deleted_at ASC, updated_at ASC, id ASC)
      WHERE deleted_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_projection_receipts_account
      ON account_health_projection_receipts(account_id, applied_at DESC, outcome_id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_projection_cursors_updated
      ON account_health_projection_cursors(updated_at ASC, consumer_key ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_health_jobs_input_versions_reserved
      ON account_health_jobs_input_versions(reserved_at ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_balance_query_due
      ON accounts(balance_query_next_refresh_at ASC, id ASC)
      WHERE balance_query_enabled = 1
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_balance_auto_detect_due
      ON accounts(balance_query_next_refresh_at ASC, id ASC)
      WHERE status = 'active'
        AND schedulable = 1
        AND type = 'api_key'
        AND balance_query_enabled = 0
        AND balance_query_config_json = '{}'
        AND deleted_at IS NULL
        AND authorization_instance_authorization_id IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_account_api_key_runtime_unique
      ON account_api_key_runtime_states(account_id, key_fingerprint)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_status
      ON account_api_key_runtime_states(account_id, status, cooldown_until)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_probe
      ON account_api_key_runtime_states(account_id, status, next_probe_at ASC, updated_at ASC, key_index ASC)
      WHERE next_probe_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_probe_claim
      ON account_api_key_runtime_states(status, next_probe_at ASC, probe_claimed_until ASC)
      WHERE next_probe_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_api_key_runtime_owner
      ON account_api_key_runtime_states(system_account_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_personal_unique
      ON custom_provider_models(provider_code, system_account_id, model)
      WHERE scope = 'personal'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_provider_models_global_unique
      ON custom_provider_models(provider_code, model)
      WHERE scope = 'global'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_custom_provider_models_catalog_lookup
      ON custom_provider_models(provider_code, status, catalog_visible, scope, system_account_id, model)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_provider_default_health_check_models_model
      ON provider_default_health_check_models(provider_code, model, system_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_provider_system_default_health_check_models_model
      ON provider_system_default_health_check_models(model, provider_code)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_supported_models_provider_model ON account_supported_models(provider_code, model, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_model_mappings_source ON account_model_mappings(provider_code, source_model, source_endpoint_family, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_model_mappings_upstream ON account_model_mappings(provider_code, upstream_model, upstream_endpoint_family, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_account_tags_owner_name_unique ON account_tags(system_account_id, name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_tags_owner_name_lookup ON account_tags(system_account_id, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_owner_tag ON account_tag_bindings(system_account_id, tag_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_tag_owner ON account_tag_bindings(tag_id, system_account_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_tag_bindings_tag ON account_tag_bindings(tag_id, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_tasks_request_updated ON account_test_tasks(request_system_account_id, updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_tasks_status_queued ON account_test_tasks(status, queued_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_tasks_finished_cleanup ON account_test_tasks(finished_at ASC, id ASC) WHERE finished_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_sessions_request_updated ON account_test_sessions(request_system_account_id, updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_sessions_status_heartbeat ON account_test_sessions(status, last_heartbeat_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_session_tasks_task ON account_test_session_tasks(task_id, session_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_test_session_tasks_session ON account_test_session_tasks(session_id, task_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_system_account ON groups(system_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_updated ON groups(updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_system_account_updated ON groups(system_account_id, updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_name_unique ON groups(system_account_id, provider_code, name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_name_lookup ON groups(name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_system_account_name_lookup ON groups(system_account_id, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_provider_name_lookup ON groups(provider_code, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_system_account_provider_name_lookup ON groups(system_account_id, provider_code, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_groups_owner_provider_default_unique ON groups(system_account_id, provider_code) WHERE is_default = 1`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_teams_status ON system_teams(status, updated_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_teams_name_unique ON system_teams(name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_teams_name_lookup ON system_teams(name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_team_members_team ON system_team_members(team_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_teams_list_order ON system_teams(status, updated_at DESC, name ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_team_members_team_status_joined ON system_team_members(team_id, status, joined_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_team_members_account ON system_team_members(system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_system_team_members_active_unique ON system_team_members(team_id, system_account_id) WHERE status = 'active'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_resource ON resource_authorizations(resource_type, resource_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_owner ON resource_authorizations(resource_owner_system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_grantee ON resource_authorizations(grantee_system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_expires_at ON resource_authorizations(expires_at, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_resource_authorizations_quota_snapshot
      ON resource_authorizations(status, updated_at DESC, id)
      WHERE limits_json IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorizations_user_unique ON resource_authorizations(resource_type, resource_id, grantee_system_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_authorization ON resource_authorization_sources(authorization_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_sources_team ON resource_authorization_sources(source_team_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_account_authorization ON group_accounts(account_authorization_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_owner_group_enabled ON group_accounts(system_account_id, group_id, enabled, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_group_enabled ON group_accounts(group_id, enabled, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_candidate_window
      ON group_accounts(group_id, system_account_id, enabled, local_fallback_enabled ASC, local_super_priority_enabled DESC, local_priority ASC, created_at ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_account_scope_enabled ON group_accounts(account_id, system_account_id, enabled)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_scope_enabled_updated ON group_accounts(system_account_id, account_id, enabled, updated_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_group_authorization_settings_scope_group
      ON group_authorization_settings(system_account_id, group_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_account_stats_dirty_updated ON group_account_stats_dirty(updated_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_route_strategy ON api_keys(route_strategy_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_default_updated ON api_keys(is_default DESC, updated_at DESC, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_default_updated ON api_keys(system_account_id, is_default DESC, updated_at DESC, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_api_keys_quota_snapshot
      ON api_keys(status, updated_at DESC, id)
      WHERE quota_limits_json IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_api_keys_availability_schedule_next_check
      ON api_keys(availability_schedule_next_check_at ASC, id ASC)
      WHERE availability_schedule_json IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_owner_name_unique ON api_keys(system_account_id, name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_route_default_unique ON api_keys(route_strategy_id) WHERE is_default = 1`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_chat_purpose_unique
      ON api_keys(system_account_id)
      WHERE purpose = 'chat'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_name_lookup ON api_keys(name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_name_lookup ON api_keys(system_account_id, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_request_quota_hourly_scope_bindings_window
      ON request_quota_hourly_window_scope_bindings(window_hours, system_account_id, scope_type, scope_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_request_quota_hourly_scope_bindings_source
      ON request_quota_hourly_window_scope_bindings(source_type, source_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategies_owner_mode ON route_strategies(system_account_id, mode, status, updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategies_owner_name_unique ON route_strategies(system_account_id, name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategies_name_lookup ON route_strategies(name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategies_system_account_name_lookup ON route_strategies(system_account_id, name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_strategy_priority ON route_strategy_groups(route_strategy_id, status, priority ASC, created_at ASC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_route_strategy_groups_unique ON route_strategy_groups(route_strategy_id, group_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_group_strategy ON route_strategy_groups(group_id, route_strategy_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_route_strategy_groups_owner_group ON route_strategy_groups(system_account_id, group_id, route_strategy_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_api_key_schedule_status_events_api_key
      ON api_key_schedule_status_events(api_key_id, executed_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_files_owner_created
      ON openai_compatible_files(system_account_id, api_key_id, created_at DESC, id DESC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_files_purpose_created
      ON openai_compatible_files(system_account_id, api_key_id, purpose, created_at DESC, id DESC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_files_container_created
      ON openai_compatible_files(system_account_id, api_key_id, container_id, created_at DESC, id DESC)
      WHERE deleted_at IS NULL AND container_id IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_vector_stores_owner_created
      ON openai_compatible_vector_stores(system_account_id, api_key_id, created_at DESC, id DESC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_vector_store_files_owner_created
      ON openai_compatible_vector_store_files(system_account_id, api_key_id, vector_store_id, created_at DESC, file_id DESC)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_openai_compatible_vector_store_chunks_search
      ON openai_compatible_vector_store_chunks(system_account_id, api_key_id, vector_store_id, file_id, chunk_index)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_schedule_status_events_account
      ON account_schedule_status_events(account_id, executed_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner ON resource_authorization_grants(resource_owner_system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_resource ON resource_authorization_grants(resource_type, resource_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_user ON resource_authorization_grants(grantee_system_account_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_team ON resource_authorization_grants(grantee_team_id, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_created ON resource_authorization_grants(created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_owner_created ON resource_authorization_grants(resource_owner_system_account_id, status, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_resource_created ON resource_authorization_grants(resource_type, resource_id, status, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_user_created ON resource_authorization_grants(grantee_system_account_id, status, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_grantee_team_created ON resource_authorization_grants(grantee_team_id, status, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_team_quota_snapshot
      ON resource_authorization_grants(resource_type, resource_id, grantee_team_id, status, updated_at DESC, id)
      WHERE grantee_type = 'team' AND limits_json IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_resource_authorization_grants_expiry_sweep
      ON resource_authorization_grants(expires_at ASC, updated_at ASC, id ASC)
      WHERE status IN ('active', 'paused') AND expires_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_active_user_unique ON resource_authorization_grants(resource_type, resource_id, grantee_system_account_id) WHERE status = 'active' AND grantee_type = 'system_account'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_grants_active_team_unique ON resource_authorization_grants(resource_type, resource_id, grantee_team_id) WHERE status = 'active' AND grantee_type = 'team'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_manual_unique ON resource_authorization_sources(authorization_id, source_type) WHERE status = 'active' AND source_type = 'manual'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_resource_authorization_sources_active_team_unique ON resource_authorization_sources(authorization_id, source_type, source_team_id) WHERE status = 'active' AND source_type = 'team'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_proxy_profiles_system_account ON proxy_profiles(system_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_proxy_profiles_updated ON proxy_profiles(updated_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_proxy_profiles_enabled_name_lookup ON proxy_profiles(enabled, name, updated_at DESC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_proxy_profiles_latency_refresh_due
      ON proxy_profiles(enabled, (last_tested_at IS NOT NULL), last_tested_at ASC, updated_at DESC, id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_profiles_name_unique ON proxy_profiles(name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_proxy_profiles_name_lookup ON proxy_profiles(name, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_announcements_public ON announcements(status, published_at DESC, created_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_announcements_admin ON announcements(updated_at DESC, created_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_announcements_admin_page ON announcements(updated_at DESC, created_at DESC, id DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_announcement_reads_account ON announcement_reads(system_account_id, read_at DESC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_enabled_priority ON response_inspection_policies(enabled, priority, updated_at DESC, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_protocol_priority ON response_inspection_policies(protocol_code, priority, updated_at DESC, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_response_inspection_policies_scope_priority ON response_inspection_policies(protocol_code, scope_type, provider_code, priority, updated_at DESC, id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_external_integration_sources_status ON external_integration_sources(status, name)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_external_integration_sources_name_unique_lower ON external_integration_sources(lower(name))`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_grants_user_client_active
      ON oauth_grants(system_account_id, client_id, expires_at, revoked_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_authorization_codes_expiry
      ON oauth_authorization_codes(expires_at, consumed_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_authorization_transactions_expiry
      ON oauth_authorization_transactions(expires_at, completed_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_access_tokens_grant_expiry
      ON oauth_access_tokens(grant_id, expires_at, revoked_at, replaced_at)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_oauth_signing_keys_one_active
      ON oauth_signing_keys(status) WHERE status = 'active'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_device_authorizations_poll
      ON oauth_device_authorizations(device_code_hash, client_id, expires_at, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_oauth_device_authorizations_user_code
      ON oauth_device_authorizations(user_code, expires_at, status)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_authorization ON accounts(authorization_instance_authorization_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_authorization_instance_active_unique
      ON accounts(authorization_instance_authorization_id)
      WHERE authorization_instance_authorization_id IS NOT NULL AND deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_source ON accounts(authorization_instance_source_account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_authorization_instance_source_owner_lookup
      ON accounts(authorization_instance_source_account_id, system_account_id, id)
      WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_accounts_deleted_cleanup
      ON accounts(deleted_at ASC, updated_at ASC, id ASC)
      WHERE deleted_at IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_account ON account_circuit_incidents(account_id, updated_at_ms, circuit_scope_key)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_runtime_state ON account_circuit_incidents(account_runtime_key, state, updated_at_ms, circuit_scope_key)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_projection_gap
      ON account_circuit_incidents(updated_at_ms, circuit_scope_key)
      WHERE projected_ledger_revision < ledger_revision`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_circuit_incidents_closed_cleanup
      ON account_circuit_incidents(retained_until_ms, updated_at_ms, circuit_scope_key)
      WHERE state = 'CLOSED'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_account ON account_circuit_outbox(account_id, dispatch_revision, created_at_ms, event_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_scope ON account_circuit_outbox(circuit_scope_key, ledger_revision, created_at_ms, event_id)
      WHERE circuit_scope_key IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_claim
      ON account_circuit_outbox(status, available_at_ms, claim_until_ms, created_at_ms, event_id)
      WHERE status IN ('pending', 'processing')`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_account_circuit_outbox_ack_cleanup
      ON account_circuit_outbox(acknowledged_at_ms, event_id)
      WHERE status = 'dispatched'`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_priority ON group_accounts(group_id, enabled, local_fallback_enabled, local_super_priority_enabled, local_priority, created_at, account_id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "business",
		SQL: `CREATE INDEX IF NOT EXISTS idx_group_accounts_dispatch_candidate_window
      ON group_accounts(group_id, system_account_id, enabled, local_fallback_enabled ASC, local_super_priority_enabled DESC, local_priority ASC, created_at ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-trigram-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_name_c_trgm_lookup ON accounts USING gin ((name COLLATE "C") juhe_business.gin_trgm_ops) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-trigram-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_provider_code_c_trgm_lookup ON accounts USING gin ((provider_code COLLATE "C") juhe_business.gin_trgm_ops) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-trigram-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_type_c_trgm_lookup ON accounts USING gin ((type COLLATE "C") juhe_business.gin_trgm_ops) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "groups-pg-trigram-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_groups_name_c_trgm_lookup ON groups USING gin ((name COLLATE "C") juhe_business.gin_trgm_ops)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-pg-trigram-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_name_trgm ON account_list_availability_projections USING gin (name_sort_key juhe_business.gin_trgm_ops)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-index-pg-trigram-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_index_name_trgm ON account_list_availability_projection_index USING gin (name_sort_key juhe_business.gin_trgm_ops)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-search-terms-pg-name-order-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_alap_search_term_name_c ON account_list_availability_projection_search_terms(viewer_system_account_id, term, (name_sort_key COLLATE "C") ASC, created_at_sort_key ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-index-pg-name-search-incomplete-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_alap_index_name_incomplete_c ON account_list_availability_projection_index(viewer_system_account_id, (name_sort_key COLLATE "C") ASC, created_at_sort_key ASC, account_id ASC) WHERE search_index_complete = 0`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-list-projection-pg-name-order-index",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_account_list_availability_projection_name_order ON account_list_availability_projections(viewer_system_account_id, ((payload_json::jsonb ->> 'name') COLLATE "C") ASC, created_at_sort_key ASC, account_id ASC)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "account-circuit-key-model-capability-index",
		SQL:        `CREATE UNIQUE INDEX IF NOT EXISTS idx_account_circuit_incidents_key_model_capability ON account_circuit_incidents(scope_kind, capability_hash) WHERE scope_kind = 'key_model' AND capability_hash IS NOT NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "api-keys-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_name_c_lookup ON api_keys((name COLLATE "C"), id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "api-keys-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_api_keys_system_account_name_c_lookup ON api_keys(system_account_id, (name COLLATE "C"), id)`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_name_c_lookup ON accounts((name COLLATE "C"), id) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_owner_name_c_lookup ON accounts(system_account_id, (name COLLATE "C"), id) WHERE deleted_at IS NULL AND authorization_instance_authorization_id IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "accounts-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_accounts_owner_all_name_c_lookup ON accounts(system_account_id, (name COLLATE "C"), id) WHERE deleted_at IS NULL`,
	},
	{
		SchemaName: "juhe_business",
		Source:     "system-teams-pg-prefix-indexes",
		SQL:        `CREATE INDEX IF NOT EXISTS idx_system_teams_name_c_lookup ON system_teams((name COLLATE "C"), id)`,
	},
}
