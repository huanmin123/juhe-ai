package cooldownaccountretest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"juhe-ai/backend-go/internal/store/port"
)

func validCooldownRetestTask() port.CooldownAccountRetestTask {
	started := time.Date(2026, 7, 20, 9, 0, 0, 123, time.UTC)
	sourceRevision := 11
	return port.CooldownAccountRetestTask{
		AccountID: "acct_1", ConfigRevision: 7, DispatchRevision: 9,
		ObservationStartedAt: &started, Generation: "generation-1", SourceConfigRevision: &sourceRevision,
		MaxPauseMinutes: 30, MaxRecoveryHours: 24,
	}
}

func TestTaskPayloadRoundTripAndUniqueBoundary(t *testing.T) {
	task := validCooldownRetestTask()
	payload, headers, err := EncodeTask(task)
	if err != nil {
		t.Fatalf("EncodeTask() error = %v", err)
	}
	decoded, err := DecodeTask(payload, headers)
	if err != nil {
		t.Fatalf("DecodeTask() error = %v", err)
	}
	if decoded.AccountID != task.AccountID || decoded.ConfigRevision != 7 || decoded.DispatchRevision != 9 ||
		decoded.ObservationStartedAt == nil || !decoded.ObservationStartedAt.Equal(*task.ObservationStartedAt) ||
		decoded.Generation != task.Generation || decoded.SourceConfigRevision == nil || *decoded.SourceConfigRevision != 11 ||
		decoded.MaxPauseMinutes != task.MaxPauseMinutes || decoded.MaxRecoveryHours != task.MaxRecoveryHours {
		t.Fatalf("decoded = %+v", decoded)
	}
	key := UniqueKey(task)
	changedRevision := task
	changedRevision.ConfigRevision++
	changedDispatch := task
	changedDispatch.DispatchRevision++
	changedObservation := task
	later := task.ObservationStartedAt.Add(time.Second)
	changedObservation.ObservationStartedAt = &later
	changedGeneration := task
	changedGeneration.Generation = "generation-2"
	changedSource := task
	otherSource := 12
	changedSource.SourceConfigRevision = &otherSource
	owner := task
	owner.SourceConfigRevision = nil
	for name, changed := range map[string]port.CooldownAccountRetestTask{
		"config revision": changedRevision, "dispatch revision": changedDispatch,
		"observation": changedObservation, "generation": changedGeneration,
		"source revision": changedSource, "owner boundary": owner,
	} {
		if key == UniqueKey(changed) {
			t.Fatalf("unique key must include %s", name)
		}
	}
}

func TestTaskPayloadDeduplicatesSameFenceAcrossStrategyChanges(t *testing.T) {
	first := validCooldownRetestTask()
	second := first
	second.MaxPauseMinutes = 45
	second.MaxRecoveryHours = 72

	firstPayload, firstHeaders, err := EncodeTask(first)
	if err != nil {
		t.Fatal(err)
	}
	secondPayload, secondHeaders, err := EncodeTask(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstPayload, secondPayload) {
		t.Fatalf("same fence payloads differ:\nfirst=%s\nsecond=%s", firstPayload, secondPayload)
	}
	if reflect.DeepEqual(firstHeaders, secondHeaders) {
		t.Fatalf("strategy headers must differ: %v", firstHeaders)
	}
	if bytes.Contains(firstPayload, []byte("maxPause")) || bytes.Contains(firstPayload, []byte("maxRecovery")) {
		t.Fatalf("strategy leaked into Asynq unique payload: %s", firstPayload)
	}
	if UniqueKey(first) != UniqueKey(second) {
		t.Fatal("strategy changes must not change the five-fence unique key")
	}
}

func TestTaskPayloadCanonicalizesEquivalentFenceRepresentations(t *testing.T) {
	first := validCooldownRetestTask()
	second := first
	second.AccountID = "  " + first.AccountID + "  "
	second.Generation = "  " + first.Generation + "  "
	local := first.ObservationStartedAt.In(time.FixedZone("offset", 8*60*60))
	second.ObservationStartedAt = &local
	firstPayload, _, err := EncodeTask(first)
	if err != nil {
		t.Fatal(err)
	}
	secondPayload, _, err := EncodeTask(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstPayload, secondPayload) {
		t.Fatalf("equivalent fences must have one canonical payload:\n%s\n%s", firstPayload, secondPayload)
	}
}

func TestTaskPayloadUsesECMAScriptGenerationWhitespace(t *testing.T) {
	base := validCooldownRetestTask()
	withECMAScriptWhitespace := base
	withECMAScriptWhitespace.Generation = "\ufeff\u00a0" + base.Generation + "\u00a0\ufeff"
	withNEL := base
	withNEL.Generation = "\u0085" + base.Generation + "\u0085"

	basePayload, _, err := EncodeTask(base)
	if err != nil {
		t.Fatal(err)
	}
	trimmedPayload, _, err := EncodeTask(withECMAScriptWhitespace)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(basePayload, trimmedPayload) {
		t.Fatalf("ECMAScript whitespace must canonicalize to one payload:\n%s\n%s", basePayload, trimmedPayload)
	}
	nelPayload, nelHeaders, err := EncodeTask(withNEL)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTask(nelPayload, nelHeaders)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Generation != withNEL.Generation || bytes.Equal(basePayload, nelPayload) {
		t.Fatalf("NEL generation was not preserved: decoded=%q payload=%s", decoded.Generation, nelPayload)
	}
}

func TestDecodeTaskRejectsMissingOrMalformedStrategyHeaders(t *testing.T) {
	payload, headers, err := EncodeTask(validCooldownRetestTask())
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]map[string]string{
		"missing all":        nil,
		"missing pause":      {maxRecoveryHoursHeader: "24"},
		"missing recovery":   {maxPauseMinutesHeader: "30"},
		"negative":           {maxPauseMinutesHeader: "-1", maxRecoveryHoursHeader: "24"},
		"zero pause":         {maxPauseMinutesHeader: "0", maxRecoveryHoursHeader: "24"},
		"pause above max":    {maxPauseMinutesHeader: "1441", maxRecoveryHoursHeader: "24"},
		"zero recovery":      {maxPauseMinutesHeader: "30", maxRecoveryHoursHeader: "0"},
		"recovery above max": {maxPauseMinutesHeader: "30", maxRecoveryHoursHeader: "721"},
		"leading zero":       {maxPauseMinutesHeader: "030", maxRecoveryHoursHeader: "24"},
		"surrounding spaces": {maxPauseMinutesHeader: "30", maxRecoveryHoursHeader: " 24 "},
		"not integer":        {maxPauseMinutesHeader: "thirty", maxRecoveryHoursHeader: "24"},
	}
	for name, invalidHeaders := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTask(payload, invalidHeaders); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("DecodeTask() error = %v, want invalid payload", err)
			}
		})
	}
	if _, err := DecodeTask(payload, headers); err != nil {
		t.Fatalf("valid headers rejected: %v", err)
	}
}

func TestDecodeTaskRejectsInvalidPayload(t *testing.T) {
	valid := validCooldownRetestTask()
	zeroTime := time.Time{}
	zero := 0
	tests := map[string]port.CooldownAccountRetestTask{
		"account id":             func() port.CooldownAccountRetestTask { v := valid; v.AccountID = " "; return v }(),
		"config revision":        func() port.CooldownAccountRetestTask { v := valid; v.ConfigRevision = 0; return v }(),
		"dispatch revision":      func() port.CooldownAccountRetestTask { v := valid; v.DispatchRevision = 0; return v }(),
		"observation missing":    func() port.CooldownAccountRetestTask { v := valid; v.ObservationStartedAt = nil; return v }(),
		"observation zero":       func() port.CooldownAccountRetestTask { v := valid; v.ObservationStartedAt = &zeroTime; return v }(),
		"generation":             func() port.CooldownAccountRetestTask { v := valid; v.Generation = " "; return v }(),
		"source config revision": func() port.CooldownAccountRetestTask { v := valid; v.SourceConfigRevision = &zero; return v }(),
	}
	for name, task := range tests {
		t.Run(name, func(t *testing.T) {
			payload, err := json.Marshal(TaskPayload{Version: PayloadVersion, Fence: taskFence(task)})
			if err != nil {
				t.Fatal(err)
			}
			_, headers, encodeErr := EncodeTask(valid)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			if _, err := DecodeTask(payload, headers); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("DecodeTask() error = %v, want invalid payload", err)
			}
		})
	}
	_, headers, encodeErr := EncodeTask(valid)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	for _, version := range []int{1, 2} {
		legacy, err := json.Marshal(TaskPayload{Version: version, Fence: taskFence(valid)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeTask(legacy, headers); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("v%d DecodeTask() error = %v, want invalid payload", version, err)
		}
	}
}

func TestDecodeTaskRejectsUnknownAndTrailingJSON(t *testing.T) {
	validPayload, headers, err := EncodeTask(validCooldownRetestTask())
	if err != nil {
		t.Fatal(err)
	}
	var payloadParts struct {
		Fence json.RawMessage `json:"fence"`
	}
	if err := json.Unmarshal(validPayload, &payloadParts); err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string][]byte{
		"unknown":          []byte(fmt.Sprintf(`{"version":%d,"fence":%s,"unexpected":true}`, PayloadVersion, payloadParts.Fence)),
		"trailing":         append(append([]byte(nil), validPayload...), []byte(` {}`)...),
		"extra whitespace": append([]byte(" "), validPayload...),
		"duplicate version": []byte(fmt.Sprintf(
			`{"version":%d,"version":%d,"fence":%s}`, PayloadVersion, PayloadVersion, payloadParts.Fence,
		)),
		"duplicate fence": []byte(fmt.Sprintf(
			`{"version":%d,"fence":%s,"fence":%s}`, PayloadVersion, payloadParts.Fence, payloadParts.Fence,
		)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTask(payload, headers); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("DecodeTask() error = %v, want invalid payload", err)
			}
		})
	}
}
