package contracts

import "testing"

func TestJ3BModelCheckRunsStartedAtIsRequired(t *testing.T) {
	spec, ok := J3BModelCheckColumns["model_check_runs"]["started_at"]
	if !ok {
		t.Fatal("model_check_runs.started_at must be part of the shared contract")
	}
	if spec.Nullable {
		t.Fatal("model_check_runs.started_at must be NOT NULL to match the PostgreSQL bootstrap schema")
	}
}
