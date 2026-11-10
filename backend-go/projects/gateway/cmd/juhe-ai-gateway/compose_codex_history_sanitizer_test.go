package main

import (
	"os"
	"strings"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch/gatewayoauthcodex"
)

func TestComposeWiresCodexHistorySanitizer(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "wireCodexHistorySanitizer()") {
		t.Fatal("main must wire the Codex history sanitizer (D-93)")
	}
}

func TestWireCodexHistorySanitizerInstallsHook(t *testing.T) {
	previous := gatewayoauthcodex.SanitizeCodexHistory
	t.Cleanup(func() { gatewayoauthcodex.SanitizeCodexHistory = previous })
	wireCodexHistorySanitizer()
	if gatewayoauthcodex.SanitizeCodexHistory == nil {
		t.Fatal("production hook must be installed")
	}
}
