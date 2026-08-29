package circuitruntime

// ManifestOperations is the runtime operation surface that must be wired by
// the Gateway dispatch owner before Node circuit code can be archived.
var ManifestOperations = []string{
	"get_account_circuit",
	"suspect_account_circuit",
	"acquire_account_circuit_confirmation_lease",
	"complete_account_circuit_confirmation",
	"acquire_account_circuit_canary_lease",
	"complete_account_circuit_canary",
	"record_account_circuit_protocol_model_open_evidence",
	"clear_account_circuit_escalation_evidence",
	"replace_account_circuit_dispatch_revision",
	"replace_account_circuit_account_dispatch_revision",
	"restore_account_circuit",
	"list_due_account_circuits",
	"account_circuit_runtime_index_backfill",
	"account_circuit_incident_restore",
	"account_circuit_revision_projection",
	"get_key_model_state",
	"record_key_model_failure",
	"admit_key_model_foreground",
	"release_key_model_foreground",
	"renew_key_model_foreground",
	"acquire_key_model_recovery_lease",
	"complete_key_model_recovery",
	"list_due_key_models",
	"main_probe_key_model_fence",
	"j1_key_model_confirmation_fence",
}
