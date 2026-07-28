package accountprobe

import (
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/modules/gatewaycandidatewindow"
)

func TestCandidateProxyURLBuildsBoundedPrivateCredentialURL(t *testing.T) {
	tests := []struct {
		name  string
		proxy *gatewaycandidatewindow.ProxyRuntime
		want  string
	}{
		{name: "direct", want: ""},
		{name: "HTTP auth", proxy: &gatewaycandidatewindow.ProxyRuntime{
			ID: "proxy", Type: "http", Host: "proxy.example", Port: 8080, Username: "user name",
			Credentials: gatewaycandidatewindow.NewCredentialSet(map[string]any{"password": "p@ss word"}), Enabled: true, Available: true,
		}, want: "http://user%20name:p%40ss%20word@proxy.example:8080"},
		{name: "SOCKS normalized", proxy: &gatewaycandidatewindow.ProxyRuntime{ID: "proxy", Type: "socks5", Host: "2001:db8::1", Port: 1080, Enabled: true, Available: true}, want: "socks5h://[2001:db8::1]:1080"},
		{name: "password ignored without username", proxy: &gatewaycandidatewindow.ProxyRuntime{
			ID: "proxy", Type: "https", Host: "proxy.example", Port: 8443,
			Credentials: gatewaycandidatewindow.NewCredentialSet(map[string]any{"password": "secret"}), Enabled: true, Available: true,
		}, want: "https://proxy.example:8443"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := candidateProxyURL(test.proxy)
			if err != nil || got != test.want {
				t.Fatalf("candidateProxyURL() = %q, %v", got, err)
			}
		})
	}
}

func TestCandidateProxyURLFailsClosedBeforeTransport(t *testing.T) {
	tests := []*gatewaycandidatewindow.ProxyRuntime{
		{ID: "disabled", Type: "http", Host: "proxy.example", Port: 8080, Available: true},
		{ID: "missing", Type: "http", Host: "proxy.example", Port: 8080, Enabled: true, UnavailableReason: "missing"},
		{ID: "scheme", Type: "ftp", Host: "proxy.example", Port: 21, Enabled: true, Available: true},
		{ID: "host", Type: "http", Port: 8080, Enabled: true, Available: true},
	}
	for _, proxy := range tests {
		if got, err := candidateProxyURL(proxy); err == nil || got != "" || strings.Contains(err.Error(), "secret") {
			t.Fatalf("candidateProxyURL(%+v) = %q, %v", proxy, got, err)
		}
	}
}

func TestTransportFactoryCreatesDirectAndProxyClients(t *testing.T) {
	factory := TransportFactory{}
	if transport, err := factory.New(gatewaycandidatewindow.Candidate{}); err != nil || transport == nil {
		t.Fatalf("direct transport=%v error=%v", transport, err)
	}
	candidate := gatewaycandidatewindow.Candidate{Proxy: &gatewaycandidatewindow.ProxyRuntime{
		ID: "proxy", Type: "http", Host: "127.0.0.1", Port: 8080, Enabled: true, Available: true,
	}}
	if transport, err := factory.New(candidate); err != nil || transport == nil {
		t.Fatalf("proxy transport=%v error=%v", transport, err)
	}
}
