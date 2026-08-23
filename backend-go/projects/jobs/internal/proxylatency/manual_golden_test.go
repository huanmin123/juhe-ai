package proxylatency

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestManualReportMatchesCrossRuntimeGolden(t *testing.T) {
	goldenPath := filepath.Join("testdata", "j3a-manual-report-golden.json")
	contents, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		SchemaVersion int             `json:"schemaVersion"`
		Job           string          `json:"job"`
		Report        ProxyTestReport `json:"report"`
	}
	if err := json.Unmarshal(contents, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != 1 || envelope.Job != proxyLatencyManualJobName {
		t.Fatalf("golden envelope mismatch: %+v", envelope)
	}
	request := ManualRequest{
		SchemaVersion:  1,
		ProxyID:        "proxy-golden",
		ProxyName:      "Golden proxy",
		ConfigRevision: "2026-08-23T00:00:00.123456Z",
		ProxyType:      "http",
		ProxyHost:      "127.0.0.1",
		ProxyPort:      8080,
		Targets: []ManualTarget{
			{Provider: "openai", ProfileID: "profile-openai", Name: "OpenAI", URL: "https://api.openai.com/v1"},
			{Provider: "gemini", ProfileID: "profile-gemini", Name: "Gemini", URL: "https://generativelanguage.googleapis.com/v1"},
		},
	}
	got := request.Report(Outcome{
		ObservedAt: time.Date(2026, 8, 23, 0, 0, 5, 123456000, time.UTC),
		Items: []ItemResult{
			{Provider: "openai", ProfileID: "profile-openai", Status: ItemPassed, Outcome: OutcomeSuccess, HTTPStatus: 200, LatencyMS: 40},
			{Provider: "gemini", ProfileID: "profile-gemini", Status: ItemUnknown, Outcome: OutcomeProbeTaskFailure, ErrorCode: "deadline"},
		},
	})
	if !reflect.DeepEqual(got, envelope.Report) {
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(envelope.Report)
		t.Fatalf("manual report drifted from cross-runtime golden\n got=%s\nwant=%s", gotJSON, wantJSON)
	}
}
