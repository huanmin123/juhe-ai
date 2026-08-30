package contracts

var J3BModelCheckTables = []string{
	"model_check_input_versions",
	"model_check_inputs",
	"model_check_execution_claims",
	"model_check_outcomes",
	"model_check_runs",
	"model_check_items",
	"model_check_observations",
	"account_quality_health_hourly",
	"model_check_scheduler_tasks",
	"model_token_intercept_baseline_versions",
}

var J3BModelCheckIndexes = map[string]string{
	"idx_model_check_outcomes_cursor":                  "on juhe_j3b.model_check_outcomes using btree (stored_at, outcome_id)",
	"idx_model_check_inputs_target":                    "on juhe_j3b.model_check_inputs using btree (target_id, issued_at)",
	"idx_model_check_runs_created":                     "on juhe_j3b.model_check_runs using btree (created_at, id)",
	"idx_model_check_runs_quality_health_sync_retry":   "on juhe_j3b.model_check_runs using btree (quality_health_sync_status, updated_at, id)",
	"idx_model_check_items_run_order":                  "on juhe_j3b.model_check_items using btree (run_id, created_at, id)",
	"idx_model_check_items_run_key":                    "on juhe_j3b.model_check_items using btree (run_id, item_key, id)",
	"idx_model_check_observations_cursor":              "on juhe_j3b.model_check_observations using btree (created_at, id)",
	"idx_model_check_observations_pending_aggregation": "on juhe_j3b.model_check_observations using btree (created_at, id)",
	"idx_account_quality_health_hourly_scope":          "on juhe_j3b.account_quality_health_hourly using btree (system_account_id, stat_hour, account_id)",
	"idx_model_check_scheduler_tasks_due":              "on juhe_j3b.model_check_scheduler_tasks using btree (kind, due_at, claim_until, id)",
	"idx_model_token_intercept_baseline_active":        "on juhe_j3b.model_token_intercept_baseline_versions using btree (cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, version_status, baseline_version)",
}

var J3BModelCheckColumns = map[string]map[string]PostgresColumnSpec{
	"model_check_input_versions": {
		"identity_key": {DataType: "text", UdtName: "text"}, "next_version": {DataType: "bigint", UdtName: "int8"}, "updated_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"},
	},
	"model_check_inputs": {
		"input_id": {DataType: "text", UdtName: "text"}, "identity_key": {DataType: "text", UdtName: "text"}, "input_version": {DataType: "bigint", UdtName: "int8"}, "input_digest": {DataType: "text", UdtName: "text"}, "target_id": {DataType: "text", UdtName: "text"}, "config_revision": {DataType: "text", UdtName: "text"}, "policy_revision": {DataType: "text", UdtName: "text"}, "trigger": {DataType: "text", UdtName: "text"}, "issued_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "expires_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "payload": {DataType: "jsonb", UdtName: "jsonb"},
	},
	"model_check_execution_claims": {
		"input_id": {DataType: "text", UdtName: "text"}, "claim_token": {DataType: "text", UdtName: "text"}, "outcome_id": {DataType: "text", UdtName: "text"}, "owner_id": {DataType: "text", UdtName: "text"}, "fence_token": {DataType: "bigint", UdtName: "int8"}, "claim_until": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "updated_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"},
	},
	"model_check_outcomes": {
		"outcome_id": {DataType: "text", UdtName: "text"}, "input_id": {DataType: "text", UdtName: "text"}, "input_digest": {DataType: "text", UdtName: "text"}, "fence_token": {DataType: "bigint", UdtName: "int8"}, "observed_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "stored_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "payload": {DataType: "jsonb", UdtName: "jsonb"}, "payload_digest": {DataType: "text", UdtName: "text"}, "committed": {DataType: "boolean", UdtName: "bool"},
	},
	"model_check_runs": {
		"id": {DataType: "text", UdtName: "text"}, "system_account_id": {DataType: "text", UdtName: "text"}, "actor_system_account_id": {DataType: "text", UdtName: "text"}, "provider_code": {DataType: "text", UdtName: "text"}, "target_type": {DataType: "text", UdtName: "text"}, "target_id": {DataType: "text", UdtName: "text"}, "account_id": {DataType: "text", UdtName: "text", Nullable: true}, "model": {DataType: "text", UdtName: "text"}, "profile": {DataType: "text", UdtName: "text"}, "trigger_kind": {DataType: "text", UdtName: "text"}, "schedule_id": {DataType: "text", UdtName: "text", Nullable: true}, "status": {DataType: "text", UdtName: "text"}, "level": {DataType: "text", UdtName: "text"}, "score": {DataType: "integer", UdtName: "int4"}, "max_score": {DataType: "integer", UdtName: "int4"}, "message": {DataType: "text", UdtName: "text"}, "request_summary_json": {DataType: "text", UdtName: "text"}, "result_summary_json": {DataType: "text", UdtName: "text"}, "policy_snapshot_json": {DataType: "text", UdtName: "text"}, "quality_decision_json": {DataType: "text", UdtName: "text"}, "probe_set_version": {DataType: "text", UdtName: "text"}, "started_at": {DataType: "text", UdtName: "text"}, "trace_id": {DataType: "text", UdtName: "text", Nullable: true}, "quality_health_sync_status": {DataType: "text", UdtName: "text", Nullable: true}, "created_at": {DataType: "text", UdtName: "text"}, "updated_at": {DataType: "text", UdtName: "text"}, "finished_at": {DataType: "text", UdtName: "text", Nullable: true},
	},
	"model_check_items": {
		"id": {DataType: "text", UdtName: "text"}, "run_id": {DataType: "text", UdtName: "text"}, "item_key": {DataType: "text", UdtName: "text"}, "item_type": {DataType: "text", UdtName: "text"}, "status": {DataType: "text", UdtName: "text"}, "score": {DataType: "integer", UdtName: "int4"}, "max_score": {DataType: "integer", UdtName: "int4"}, "duration_ms": {DataType: "integer", UdtName: "int4", Nullable: true}, "trace_id": {DataType: "text", UdtName: "text", Nullable: true}, "evidence_summary_json": {DataType: "text", UdtName: "text"}, "error_code": {DataType: "text", UdtName: "text", Nullable: true}, "error_message": {DataType: "text", UdtName: "text", Nullable: true}, "created_at": {DataType: "text", UdtName: "text"}, "updated_at": {DataType: "text", UdtName: "text"},
	},
	"model_check_observations": {
		"id": {DataType: "text", UdtName: "text"}, "run_id": {DataType: "text", UdtName: "text"}, "system_account_id": {DataType: "text", UdtName: "text"}, "account_id": {DataType: "text", UdtName: "text"}, "provider_code": {DataType: "text", UdtName: "text"}, "requested_model": {DataType: "text", UdtName: "text"}, "mapped_upstream_model": {DataType: "text", UdtName: "text"}, "probe_family": {DataType: "text", UdtName: "text"}, "observation_status": {DataType: "text", UdtName: "text"}, "identity_status": {DataType: "text", UdtName: "text"}, "mapping_status": {DataType: "text", UdtName: "text"}, "protocol_status": {DataType: "text", UdtName: "text"}, "evidence_coverage": {DataType: "integer", UdtName: "int4"}, "created_at": {DataType: "text", UdtName: "text"},
	},
	"account_quality_health_hourly": {
		"account_id": {DataType: "text", UdtName: "text"}, "system_account_id": {DataType: "text", UdtName: "text"}, "provider_code": {DataType: "text", UdtName: "text"}, "stat_hour": {DataType: "text", UdtName: "text"}, "observed_at": {DataType: "text", UdtName: "text"}, "model_check_run_id": {DataType: "text", UdtName: "text"}, "model": {DataType: "text", UdtName: "text"}, "profile": {DataType: "text", UdtName: "text"}, "score": {DataType: "integer", UdtName: "int4"}, "threshold": {DataType: "integer", UdtName: "int4"}, "level": {DataType: "text", UdtName: "text"}, "error_code": {DataType: "text", UdtName: "text", Nullable: true}, "error_message": {DataType: "text", UdtName: "text", Nullable: true}, "updated_at": {DataType: "text", UdtName: "text"},
	},
	"model_check_scheduler_tasks": {
		"id": {DataType: "text", UdtName: "text"}, "kind": {DataType: "text", UdtName: "text"}, "due_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "claim_owner": {DataType: "text", UdtName: "text"}, "claim_until": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "fence_token": {DataType: "bigint", UdtName: "int8"}, "state": {DataType: "text", UdtName: "text"}, "last_error": {DataType: "text", UdtName: "text"}, "completed_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"}, "payload": {DataType: "jsonb", UdtName: "jsonb"}, "updated_at": {DataType: "timestamp with time zone", UdtName: "timestamptz"},
	},
	"model_token_intercept_baseline_versions": {
		"cohort_key_hmac": {DataType: "text", UdtName: "text"}, "requested_model": {DataType: "text", UdtName: "text"}, "tokenizer_version": {DataType: "text", UdtName: "text"}, "probe_set_version": {DataType: "text", UdtName: "text"}, "baseline_version": {DataType: "integer", UdtName: "int4"}, "version_status": {DataType: "text", UdtName: "text"}, "evidence_status": {DataType: "text", UdtName: "text"}, "independent_source_count": {DataType: "integer", UdtName: "int4"}, "retained_source_count": {DataType: "integer", UdtName: "int4"}, "excluded_source_count": {DataType: "integer", UdtName: "int4"}, "median_intercept": {DataType: "double precision", UdtName: "float8", Nullable: true}, "mad_intercept": {DataType: "double precision", UdtName: "float8", Nullable: true}, "q10_intercept": {DataType: "double precision", UdtName: "float8", Nullable: true}, "q90_intercept": {DataType: "double precision", UdtName: "float8", Nullable: true}, "strong_threshold_intercept": {DataType: "double precision", UdtName: "float8", Nullable: true}, "strong_gate_enabled": {DataType: "integer", UdtName: "int4"}, "calibration_note": {DataType: "text", UdtName: "text", Nullable: true}, "first_observed_at": {DataType: "text", UdtName: "text"}, "last_observed_at": {DataType: "text", UdtName: "text"}, "updated_at": {DataType: "text", UdtName: "text"},
	},
}

var J3BModelCheckConstraints = map[string][]string{
	"model_check_input_versions":              {"primary key (identity_key)"},
	"model_check_inputs":                      {"primary key (input_id)", "unique (identity_key, input_version)", "unique (identity_key, input_digest)"},
	"model_check_execution_claims":            {"primary key (input_id)"},
	"model_check_outcomes":                    {"primary key (outcome_id)", "unique (input_id)"},
	"model_check_runs":                        {"primary key (id)"},
	"model_check_items":                       {"primary key (id)"},
	"model_check_observations":                {"primary key (id)"},
	"account_quality_health_hourly":           {"primary key (account_id, stat_hour)"},
	"model_check_scheduler_tasks":             {"primary key (id)"},
	"model_token_intercept_baseline_versions": {"primary key (cohort_key_hmac, requested_model, tokenizer_version, probe_set_version, baseline_version)"},
}
