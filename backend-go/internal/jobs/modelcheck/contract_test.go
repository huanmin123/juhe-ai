package modelcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type goldenContract struct {
	TaskType                  string            `json:"taskType"`
	PayloadVersion            int               `json:"payloadVersion"`
	Queue                     string            `json:"queue"`
	MaxRetry                  int               `json:"maxRetry"`
	DeadlinePolicy            string            `json:"deadlinePolicy"`
	LeaseFencingPolicy        string            `json:"leaseFencingPolicy"`
	EnqueueHandoffPolicy      string            `json:"enqueueHandoffPolicy"`
	FinishCASPolicy           string            `json:"finishCasPolicy"`
	PublicStatuses            []RunStatus       `json:"publicStatuses"`
	TerminalStatuses          []RunStatus       `json:"terminalStatuses"`
	ApplyTransitions          [][2]RunStatus    `json:"applyTransitions"`
	NoopTransitions           [][2]RunStatus    `json:"noopTransitions"`
	RejectedTransitions       [][2]RunStatus    `json:"rejectedTransitions"`
	WriteStages               []string          `json:"writeStages"`
	ProhibitedPayloadKeys     []string          `json:"prohibitedPayloadKeys"`
	ExamplePayload            RunTaskPayload    `json:"examplePayload"`
	ExampleRequestFingerprint string            `json:"exampleRequestFingerprint"`
	Idempotency               map[string]string `json:"idempotency"`
	NodeDivergenceReasons     []string          `json:"nodeDivergenceReasons"`
}

func TestWriterLifecycleGoldenContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "v1", "writer-lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden goldenContract
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&golden); err != nil {
		t.Fatal(err)
	}

	if golden.TaskType != TaskTypeRun || golden.PayloadVersion != PayloadVersionV1 || golden.Queue != QueueName || golden.MaxRetry != DefaultMaxRetry || golden.DeadlinePolicy != DeadlinePolicyV1 {
		t.Fatalf("task contract drifted: type=%q version=%d queue=%q retry=%d deadline=%q", golden.TaskType, golden.PayloadVersion, golden.Queue, golden.MaxRetry, golden.DeadlinePolicy)
	}
	if golden.LeaseFencingPolicy != LeaseFencingV1 || golden.EnqueueHandoffPolicy != EnqueueHandoffV1 || golden.FinishCASPolicy != FinishCASV1 {
		t.Fatalf("coordination contract drifted: fencing=%q handoff=%q finish=%q", golden.LeaseFencingPolicy, golden.EnqueueHandoffPolicy, golden.FinishCASPolicy)
	}
	wantStatuses := []RunStatus{RunStatusRunning, RunStatusCompleted, RunStatusFailed, RunStatusCanceled}
	if !reflect.DeepEqual(golden.PublicStatuses, wantStatuses) {
		t.Fatalf("public statuses = %#v, want %#v", golden.PublicStatuses, wantStatuses)
	}
	wantApply := [][2]RunStatus{{RunStatusRunning, RunStatusCompleted}, {RunStatusRunning, RunStatusFailed}, {RunStatusRunning, RunStatusCanceled}}
	wantNoop := [][2]RunStatus{{RunStatusRunning, RunStatusRunning}, {RunStatusCompleted, RunStatusCompleted}, {RunStatusFailed, RunStatusFailed}, {RunStatusCanceled, RunStatusCanceled}}
	wantReject := [][2]RunStatus{
		{RunStatusCompleted, RunStatusRunning}, {RunStatusCompleted, RunStatusFailed}, {RunStatusCompleted, RunStatusCanceled},
		{RunStatusFailed, RunStatusRunning}, {RunStatusFailed, RunStatusCompleted}, {RunStatusFailed, RunStatusCanceled},
		{RunStatusCanceled, RunStatusRunning}, {RunStatusCanceled, RunStatusCompleted}, {RunStatusCanceled, RunStatusFailed},
	}
	if !reflect.DeepEqual(golden.ApplyTransitions, wantApply) || !reflect.DeepEqual(golden.NoopTransitions, wantNoop) || !reflect.DeepEqual(golden.RejectedTransitions, wantReject) {
		t.Fatal("golden transition matrix drifted")
	}
	assertTransitionDecisions(t, golden.ApplyTransitions, TransitionApply)
	assertTransitionDecisions(t, golden.NoopTransitions, TransitionNoop)
	assertTransitionDecisions(t, golden.RejectedTransitions, TransitionReject)
	for _, status := range golden.TerminalStatuses {
		if !IsTerminal(status) {
			t.Errorf("golden status %q must be terminal", status)
		}
	}
	if err := ValidateRunTaskPayload(golden.ExamplePayload); err != nil {
		t.Fatalf("valid golden payload rejected: %v", err)
	}
	gotKey, err := UniqueKey(golden.ExamplePayload)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := gotKey, "model-check:run:mcr_contract_1"; got != want {
		t.Fatalf("unique key = %q, want %q", got, want)
	}
	gotFingerprint, err := RequestFingerprint(golden.ExamplePayload)
	if err != nil {
		t.Fatal(err)
	}
	if got := gotFingerprint; got != golden.ExampleRequestFingerprint {
		t.Fatalf("request fingerprint = %q", got)
	}

	encoded, err := EncodeRunTaskPayload(golden.ExamplePayload)
	if err != nil {
		t.Fatal(err)
	}
	var payloadKeys map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payloadKeys); err != nil {
		t.Fatal(err)
	}
	for _, key := range golden.ProhibitedPayloadKeys {
		if _, exists := payloadKeys[key]; exists {
			t.Errorf("durable task payload must not contain sensitive or mutable field %q", key)
		}
	}

	wantStages := []string{"create_run", "checkpoint_probe_attempts", "upsert_items", "append_observations", "finish_run"}
	if !reflect.DeepEqual(golden.WriteStages, wantStages) {
		t.Fatalf("write stages = %#v, want %#v", golden.WriteStages, wantStages)
	}
	for _, stage := range wantStages {
		if strings.TrimSpace(golden.Idempotency[stage]) == "" {
			t.Errorf("missing idempotency rule for %q", stage)
		}
	}
	if len(golden.Idempotency) != len(wantStages) {
		t.Errorf("idempotency rules contain unexpected stages: %#v", golden.Idempotency)
	}
	if len(golden.NodeDivergenceReasons) < 4 {
		t.Fatalf("expected recorded Go-native divergence reasons, got %d", len(golden.NodeDivergenceReasons))
	}
}

func TestRunTaskPayloadStrictRoundTrip(t *testing.T) {
	input := RunTaskPayload{
		Version: PayloadVersionV1, RunID: " run-1 ", SystemAccountID: " owner-1 ", ActorSystemAccountID: " actor-1 ",
		TargetType: " account ", TargetID: " account-1 ", TargetConfigRevision: 7, Model: " gpt-5.4 ",
		Profile: " full ", ProbeSetVersion: " probe-v1 ", TraceID: " trace-1 ",
		RequestedAt: time.Date(2026, 7, 22, 20, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)),
	}
	encoded, err := EncodeRunTaskPayload(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRunTaskPayload(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RunID != "run-1" || decoded.Model != "gpt-5.4" || decoded.RequestedAt.Location() != time.UTC {
		t.Fatalf("decoded payload was not canonical: %#v", decoded)
	}
}

func TestDecodeRunTaskPayloadRejectsUnknownTrailingAndOversizedData(t *testing.T) {
	valid := `{"version":1,"runId":"run-1","systemAccountId":"owner-1","actorSystemAccountId":"actor-1","targetType":"account","targetId":"account-1","targetConfigRevision":1,"model":"gpt-5.4","profile":"full","probeSetVersion":"probe-v1","trustedComparison":false,"traceId":"trace-1","requestedAt":"2026-07-22T12:00:00Z"}`
	for _, raw := range [][]byte{
		[]byte(strings.TrimSuffix(valid, "}") + `,"apiKey":"secret"}`),
		[]byte(strings.Replace(valid, `"runId":"run-1"`, `"runId":"run-1","runId":"run-2"`, 1)),
		[]byte(strings.Replace(valid, `"runId":"run-1"`, `"runId":" run-1 "`, 1)),
		[]byte(valid + ` {}`),
		bytes.Repeat([]byte("x"), MaxPayloadBytes+1),
	} {
		if _, err := DecodeRunTaskPayload(raw); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("DecodeRunTaskPayload() error = %v, want ErrInvalidPayload", err)
		}
	}
}

func TestValidateRunTaskPayload(t *testing.T) {
	valid := RunTaskPayload{
		Version:                         PayloadVersionV1,
		RunID:                           "mcr_contract_1",
		SystemAccountID:                 "sys_owner",
		ActorSystemAccountID:            "sys_actor",
		TargetType:                      TargetTypeAccount,
		TargetID:                        "account_1",
		TargetConfigRevision:            7,
		Model:                           "gpt-5.4",
		Profile:                         ProfileFull,
		ProbeSetVersion:                 "multi-provider-model-check-v4-gpt56-preview",
		TrustedComparison:               true,
		TrustedComparisonAccountID:      "account_2",
		TrustedComparisonConfigRevision: 9,
		TraceID:                         "trace_contract_1",
		RequestedAt:                     time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name   string
		mutate func(*RunTaskPayload)
		want   string
	}{
		{name: "version", mutate: func(p *RunTaskPayload) { p.Version = 2 }, want: "payload version"},
		{name: "run id", mutate: func(p *RunTaskPayload) { p.RunID = " " }, want: "run id"},
		{name: "run id whitespace", mutate: func(p *RunTaskPayload) { p.RunID = " run " }, want: "surrounding whitespace"},
		{name: "run id control", mutate: func(p *RunTaskPayload) { p.RunID = "run\x00id" }, want: "control characters"},
		{name: "owner", mutate: func(p *RunTaskPayload) { p.SystemAccountID = "" }, want: "system account id"},
		{name: "actor", mutate: func(p *RunTaskPayload) { p.ActorSystemAccountID = "" }, want: "actor system account id"},
		{name: "target type", mutate: func(p *RunTaskPayload) { p.TargetType = "group" }, want: "target type"},
		{name: "target id", mutate: func(p *RunTaskPayload) { p.TargetID = "" }, want: "target id"},
		{name: "target revision", mutate: func(p *RunTaskPayload) { p.TargetConfigRevision = 0 }, want: "target config revision"},
		{name: "model", mutate: func(p *RunTaskPayload) { p.Model = strings.Repeat("x", MaxModelBytes+1) }, want: "model"},
		{name: "profile", mutate: func(p *RunTaskPayload) { p.Profile = "quick" }, want: "profile"},
		{name: "probe version", mutate: func(p *RunTaskPayload) { p.ProbeSetVersion = "" }, want: "probe set version"},
		{name: "comparison id", mutate: func(p *RunTaskPayload) { p.TrustedComparisonAccountID = "" }, want: "trusted comparison account id"},
		{name: "comparison revision", mutate: func(p *RunTaskPayload) { p.TrustedComparisonConfigRevision = 0 }, want: "trusted comparison config revision"},
		{name: "self comparison", mutate: func(p *RunTaskPayload) { p.TrustedComparisonAccountID = p.TargetID }, want: "must differ"},
		{name: "trace id", mutate: func(p *RunTaskPayload) { p.TraceID = "" }, want: "trace id"},
		{name: "requested at", mutate: func(p *RunTaskPayload) { p.RequestedAt = time.Time{} }, want: "requested at"},
	}

	if err := ValidateRunTaskPayload(valid); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			err := ValidateRunTaskPayload(input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestTerminalTransitionsAreImmutableButIdempotent(t *testing.T) {
	for _, terminal := range []RunStatus{RunStatusCompleted, RunStatusFailed, RunStatusCanceled} {
		if DecideTransition(terminal, terminal) != TransitionNoop {
			t.Errorf("same terminal transition %q must be a no-op", terminal)
		}
		for _, other := range []RunStatus{RunStatusRunning, RunStatusCompleted, RunStatusFailed, RunStatusCanceled} {
			if other != terminal && DecideTransition(terminal, other) != TransitionReject {
				t.Errorf("terminal transition %q -> %q must be immutable", terminal, other)
			}
		}
	}
}

func TestRequestFingerprintBindsConfigurationRevision(t *testing.T) {
	payload := RunTaskPayload{
		Version:                         PayloadVersionV1,
		RunID:                           "run-1",
		SystemAccountID:                 "owner-1",
		ActorSystemAccountID:            "actor-1",
		TargetType:                      TargetTypeAccount,
		TargetID:                        "account-1",
		TargetConfigRevision:            7,
		Model:                           "gpt-5.4",
		Profile:                         ProfileFull,
		ProbeSetVersion:                 "multi-provider-model-check-v4-gpt56-preview",
		TrustedComparison:               true,
		TrustedComparisonAccountID:      "account-2",
		TrustedComparisonConfigRevision: 9,
		TraceID:                         "trace-1",
		RequestedAt:                     time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
	}
	before, err := RequestFingerprint(payload)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*RunTaskPayload)
	}{
		{name: "owner", mutate: func(p *RunTaskPayload) { p.SystemAccountID = "owner-2" }},
		{name: "actor", mutate: func(p *RunTaskPayload) { p.ActorSystemAccountID = "actor-2" }},
		{name: "target revision", mutate: func(p *RunTaskPayload) { p.TargetConfigRevision++ }},
		{name: "model", mutate: func(p *RunTaskPayload) { p.Model = "gpt-5.4-mini" }},
		{name: "probe set", mutate: func(p *RunTaskPayload) { p.ProbeSetVersion = "model-check-v-next" }},
		{name: "comparison revision", mutate: func(p *RunTaskPayload) { p.TrustedComparisonConfigRevision++ }},
		{name: "trace", mutate: func(p *RunTaskPayload) { p.TraceID = "trace-2" }},
		{name: "requested at", mutate: func(p *RunTaskPayload) { p.RequestedAt = p.RequestedAt.Add(time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := payload
			test.mutate(&changed)
			after, err := RequestFingerprint(changed)
			if err != nil {
				t.Fatal(err)
			}
			if after == before {
				t.Fatal("immutable request fact must change the fingerprint")
			}
		})
	}
}

func TestNormalizeRunTaskPayloadCanonicalizesBeforeIdentity(t *testing.T) {
	payload := RunTaskPayload{
		Version:              PayloadVersionV1,
		RunID:                " run-1 ",
		SystemAccountID:      " owner-1 ",
		ActorSystemAccountID: " actor-1 ",
		TargetType:           " account ",
		TargetID:             " account-1 ",
		TargetConfigRevision: 1,
		Model:                " gpt-5.4 ",
		Profile:              " full ",
		ProbeSetVersion:      " multi-provider-model-check-v4-gpt56-preview ",
		TraceID:              " trace-1 ",
		RequestedAt:          time.Date(2026, 7, 22, 20, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)),
	}
	normalized, err := NormalizeRunTaskPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.RunID != "run-1" || normalized.Model != "gpt-5.4" || normalized.RequestedAt.Location() != time.UTC {
		t.Fatalf("payload was not canonicalized: %#v", normalized)
	}
}

func assertTransitionDecisions(t *testing.T, transitions [][2]RunStatus, want TransitionDecision) {
	t.Helper()
	for _, transition := range transitions {
		if got := DecideTransition(transition[0], transition[1]); got != want {
			t.Errorf("transition %q -> %q = %q, want %q", transition[0], transition[1], got, want)
		}
	}
}
