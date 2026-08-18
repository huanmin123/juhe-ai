package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMatchesAccountBalanceManualSecret(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	request := httptest.NewRequest(http.MethodPost, "/account-balance/manual", nil)
	if matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge must reject a missing bearer secret")
	}
	request.Header.Set("Authorization", "Bearer wrong")
	if matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge must reject a wrong bearer secret")
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	if !matchesAccountBalanceManualSecret(request, secret) {
		t.Fatal("manual bridge must accept its configured bearer secret")
	}
}
