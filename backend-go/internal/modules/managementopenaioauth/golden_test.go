package managementopenaioauth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGoContractReadsReviewedNodeAuthorityGolden(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	fixturePath := filepath.Join(filepath.Dir(filename), "..", "..", "..", "..", "testdata", "openai-oauth-contract", "v1", "node-authority.json")
	body, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Version   int    `json:"version"`
		Authority string `json:"authority"`
		Transport struct {
			RouterEndpoints []struct {
				Path                      string `json:"path"`
				SuccessStatus             int    `json:"successStatus"`
				MutationGuardOperationKey string `json:"mutationGuardOperationKey"`
			} `json:"routerEndpoints"`
		} `json:"transport"`
		Session struct {
			TTLSeconds int    `json:"ttlSeconds"`
			Consume    string `json:"consume"`
		} `json:"session"`
		TokenExchange struct {
			ResponseMaxBytes int `json:"responseMaxBytes"`
		} `json:"tokenExchange"`
		KnownNodeDefects []struct {
			ID          string `json:"id"`
			Disposition string `json:"disposition"`
		} `json:"knownNodeDefects"`
	}
	if err := json.Unmarshal(body, &golden); err != nil {
		t.Fatal(err)
	}
	if golden.Version != ContractVersion || golden.Authority != "node" {
		t.Fatalf("golden version/authority = %d/%q", golden.Version, golden.Authority)
	}
	contracts := OperationContracts()
	if len(golden.Transport.RouterEndpoints) != len(contracts) {
		t.Fatalf("golden routes = %d, Go contracts = %d", len(golden.Transport.RouterEndpoints), len(contracts))
	}
	for i, endpoint := range golden.Transport.RouterEndpoints {
		goPath := strings.ReplaceAll(endpoint.Path, ":id", "{id}")
		if contracts[i].Path != goPath || contracts[i].SuccessStatus != endpoint.SuccessStatus || contracts[i].MutationGuardOperationKey != endpoint.MutationGuardOperationKey {
			t.Fatalf("route %d drift: golden=%#v Go=%#v", i, endpoint, contracts[i])
		}
	}
	if golden.Session.TTLSeconds != int((30*time.Minute)/time.Second) {
		t.Fatalf("golden session TTL = %d", golden.Session.TTLSeconds)
	}
	if golden.Session.Consume != "after_token_success_atomic_compare_delete_once" {
		t.Fatalf("golden session consume = %q", golden.Session.Consume)
	}
	if golden.TokenExchange.ResponseMaxBytes != TokenResponseMaxBytes {
		t.Fatalf("golden token response max bytes = %d, Go = %d", golden.TokenExchange.ResponseMaxBytes, TokenResponseMaxBytes)
	}
	wantDefects := map[string]bool{
		"no-stable-machine-error-code": false,
	}
	for _, defect := range golden.KnownNodeDefects {
		if _, tracked := wantDefects[defect.ID]; tracked {
			if defect.Disposition != "fix_in_go_do_not_copy" {
				t.Fatalf("defect %q disposition = %q", defect.ID, defect.Disposition)
			}
			wantDefects[defect.ID] = true
		}
	}
	for id, found := range wantDefects {
		if !found {
			t.Fatalf("reviewed golden no longer records required Go fix %q", id)
		}
	}
}
