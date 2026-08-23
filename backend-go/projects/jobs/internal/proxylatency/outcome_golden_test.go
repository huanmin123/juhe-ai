package proxylatency

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOutcomeMatchesCrossRuntimeGolden(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("testdata", "j3a-outcome-golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want Outcome
	if err := json.Unmarshal(contents, &want); err != nil {
		t.Fatal(err)
	}
	if err := validateOutcome(want); err != nil {
		t.Fatalf("golden outcome is not a valid Go outcome: %v", err)
	}
	var gotJSON, wantJSON map[string]any
	gotBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotBytes, &gotJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, &wantJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("Go outcome JSON drifted from shared golden\n got=%s\nwant=%s", gotBytes, contents)
	}
}
