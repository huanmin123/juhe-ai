package managementopenaioauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestStableErrorCatalog(t *testing.T) {
	want := map[ErrorCode]int{
		ErrorCodeRequestInvalid:          http.StatusBadRequest,
		ErrorCodeStateInvalid:            http.StatusBadRequest,
		ErrorCodeAccountStateInvalid:     http.StatusBadRequest,
		ErrorCodeAccountNotFound:         http.StatusNotFound,
		ErrorCodeSessionExpired:          http.StatusConflict,
		ErrorCodeSessionProcessing:       http.StatusConflict,
		ErrorCodeSessionConsumed:         http.StatusConflict,
		ErrorCodeGrantInvalid:            http.StatusConflict,
		ErrorCodeAccountConflict:         http.StatusConflict,
		ErrorCodeUpstreamUnavailable:     http.StatusBadGateway,
		ErrorCodeSessionStoreUnavailable: http.StatusServiceUnavailable,
	}
	for code, status := range want {
		if !code.Valid() {
			t.Fatalf("code %q must be valid", code)
		}
		err := NewError(code, errors.New("secret refresh_token=do-not-leak"))
		if err.Code != code || err.StatusCode != status || strings.TrimSpace(err.Message) == "" {
			t.Fatalf("error for %q = %#v", code, err)
		}
		if strings.Contains(err.Error(), "do-not-leak") {
			t.Fatalf("safe Error() leaked cause for %q", code)
		}
		if !errors.Is(err, err.Unwrap()) {
			t.Fatalf("error for %q must preserve cause for internal classification", code)
		}
		body, marshalErr := json.Marshal(err.Response())
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(string(body), "do-not-leak") || !strings.Contains(string(body), string(code)) {
			t.Fatalf("unsafe response for %q: %s", code, body)
		}
	}
	if ErrorCode("made_up").Valid() {
		t.Fatal("arbitrary codes must not become public protocol values")
	}
}

func TestNewErrorFailsClosedForUnknownCode(t *testing.T) {
	err := NewError(ErrorCode("made_up"), nil)
	if err.Code != ErrorCodeUpstreamUnavailable || err.StatusCode != http.StatusBadGateway {
		t.Fatalf("unknown error = %#v", err)
	}
}
