package managementopenaioauth

import (
	"net/http"
	"testing"
	"time"
)

func TestOperationContractsFreezeSixNodeVisibleActions(t *testing.T) {
	want := []OperationContract{
		{Operation: OperationAuthURL, Path: "/auth-url", SuccessStatus: http.StatusOK},
		{Operation: OperationCreateFromCode, Path: "/create-from-code", SuccessStatus: http.StatusCreated, MutationGuardOperationKey: "openai_oauth.create_from_code", ProcessingTTL: 180 * time.Second},
		{Operation: OperationCreateFromRefreshToken, Path: "/create-from-refresh-token", SuccessStatus: http.StatusCreated, MutationGuardOperationKey: "openai_oauth.create_from_refresh_token", ProcessingTTL: 180 * time.Second},
		{Operation: OperationRefreshToken, Path: "/accounts/{id}/refresh-token", SuccessStatus: http.StatusOK},
		{Operation: OperationReauthorizeFromCode, Path: "/accounts/{id}/reauthorize-from-code", SuccessStatus: http.StatusOK},
		{Operation: OperationReauthorizeFromRefreshToken, Path: "/accounts/{id}/reauthorize-from-refresh-token", SuccessStatus: http.StatusOK},
	}

	got := OperationContracts()
	if len(got) != len(want) {
		t.Fatalf("contract count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("contract[%d] = %#v, want %#v", i, got[i], want[i])
		}
		if !got[i].Operation.Valid() {
			t.Fatalf("operation %q must be valid", got[i].Operation)
		}
	}
	if Operation("unknown").Valid() {
		t.Fatal("unknown operation must be invalid")
	}

	got[0].Path = "/mutated"
	if OperationContracts()[0].Path != "/auth-url" {
		t.Fatal("OperationContracts must return a defensive copy")
	}
}
