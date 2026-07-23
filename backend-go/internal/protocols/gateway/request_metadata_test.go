package gateway

import (
	"strings"
	"testing"
)

func TestScanRequestMetadataStoreStates(t *testing.T) {
	tests := []struct {
		name string
		body string
		want StoreRequestState
	}{
		{name: "absent", body: `{"model":"gpt-5"}`, want: StoreAbsent},
		{name: "false", body: `{"store":false}`, want: StoreExplicitFalse},
		{name: "true", body: `{"store":true}`, want: StoreExplicitTrue},
		{name: "null", body: `{"store":null}`, want: StoreNull},
		{name: "wrong type", body: `{"store":"false"}`, want: StoreInvalid},
		{name: "duplicate", body: `{"store":false,"store":true}`, want: StoreInvalid},
		{name: "trailing", body: `{"store":false}{}`, want: StoreInvalid},
		{name: "truncated", body: `{"nested":[1,2}`, want: StoreInvalid},
		{name: "array", body: `[{"store":false}]`, want: StoreInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ScanRequestMetadata([]byte(tt.body), 1<<20)
			if got.Store.State != tt.want {
				t.Fatalf("state = %q, want %q", got.Store.State, tt.want)
			}
		})
	}
}

func TestScanRequestMetadataIsBoundedAndReadsCompleteDocument(t *testing.T) {
	body := `{"nested":{"items":[1,{"value":"` + strings.Repeat("x", 4096) + `"}]},"store":false}`
	if got := ScanRequestMetadata([]byte(body), len(body)); got.Store.State != StoreExplicitFalse {
		t.Fatalf("exact limit state = %q", got.Store.State)
	}
	if got := ScanRequestMetadata([]byte(body), len(body)-1); got.Store.State != StoreScanLimitExceeded {
		t.Fatalf("over limit state = %q", got.Store.State)
	}
	invalidUTF8 := []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}
	if got := ScanRequestMetadata(invalidUTF8, len(invalidUTF8)); got.Store.State != StoreInvalid {
		t.Fatalf("invalid utf8 state = %q", got.Store.State)
	}
	shape := ScanRequestMetadata([]byte(`{"store":false}`), 64).Apply(RequestShape{Method: "POST", Path: "/v1/responses"})
	if !ClassifyReplay(shape, nil).Allowed {
		t.Fatalf("applied shape was not replayable: %+v", shape)
	}
}
