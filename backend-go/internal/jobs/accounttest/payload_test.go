package accounttest

import (
	"encoding/json"
	"testing"
)

func TestEncodePayloadUsesVersionOneAndTaskID(t *testing.T) {
	payload, err := Encode(EnqueuePayload{TaskID: "accttest_1"})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	var decoded EnqueuePayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if decoded.Version != 1 || decoded.TaskID != "accttest_1" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestEncodeRejectsBlankTaskIDAndUnsupportedVersion(t *testing.T) {
	for _, input := range []EnqueuePayload{{}, {Version: 2, TaskID: "accttest_1"}} {
		if _, err := Encode(input); err == nil {
			t.Fatalf("Encode(%+v) error = nil", input)
		}
	}
}
