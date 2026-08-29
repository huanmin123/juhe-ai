package keymodelruntime

// ManifestOperations is the explicit request/recovery surface that the Go
// gateway dispatch owner must bind before Node key-model code is archived.
var ManifestOperations = []string{
	"get_key_model_state",
	"record_key_model_failure",
	"admit_key_model_foreground",
	"release_key_model_foreground",
	"renew_key_model_foreground",
	"acquire_key_model_recovery_lease",
	"complete_key_model_recovery",
	"list_due_key_models",
	"record_main_probe_key_model_fence",
	"clear_main_probe_key_model_fence",
	"defer_main_probe_key_model_fence",
	"claim_j1_key_model_confirmation",
}
