package contracts

var J3BModelCheckTables = []string{
	"model_check_input_versions",
	"model_check_inputs",
	"model_check_execution_claims",
	"model_check_outcomes",
}

var J3BModelCheckIndexes = map[string]string{
	"idx_model_check_outcomes_cursor": "on juhe_jobs.model_check_outcomes using btree (stored_at, outcome_id)",
	"idx_model_check_inputs_target":   "on juhe_jobs.model_check_inputs using btree (target_id, issued_at)",
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
}

var J3BModelCheckConstraints = map[string][]string{
	"model_check_input_versions":   {"primary key (identity_key)"},
	"model_check_inputs":           {"primary key (input_id)", "unique (identity_key, input_version)", "unique (identity_key, input_digest)"},
	"model_check_execution_claims": {"primary key (input_id)"},
	"model_check_outcomes":         {"primary key (outcome_id)", "unique (input_id)"},
}
