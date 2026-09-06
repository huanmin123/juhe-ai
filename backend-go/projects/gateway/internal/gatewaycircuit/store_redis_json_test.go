package gatewaycircuit

import "testing"

func TestDecodeStrictRejectsTrailingJSON(t *testing.T) {
	var payload map[string]any
	if err := decodeStrict(`{"status":"ok"} {"status":"unexpected"}`, &payload); err == nil {
		t.Fatal("decodeStrict must reject trailing JSON")
	}
}
