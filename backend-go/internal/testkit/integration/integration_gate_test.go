//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

const requireIntegrationEnv = "JUHE_AI_REQUIRE_INTEGRATION"

func TestMain(m *testing.M) {
	required, err := parseIntegrationRequirement(os.Getenv(requireIntegrationEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s 配置无效: %v\n", requireIntegrationEnv, err)
		os.Exit(2)
	}
	if required {
		if err := testcontainersProviderHealth(); err != nil {
			fmt.Fprintf(os.Stderr, "%s=1，但 testcontainers provider 不可用: %v\n", requireIntegrationEnv, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func parseIntegrationRequirement(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	default:
		return false, fmt.Errorf("只接受 1/0、true/false、yes/no 或 on/off")
	}
}

func testcontainersProviderHealth() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("provider health panic: %v", recovered)
		}
	}()

	provider, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		return err
	}
	return provider.Health(context.Background())
}

func TestParseIntegrationRequirement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    bool
		wantErr bool
	}{
		{name: "unset", raw: "", want: false},
		{name: "disabled", raw: "off", want: false},
		{name: "enabled numeric", raw: "1", want: true},
		{name: "enabled text", raw: " TRUE ", want: true},
		{name: "invalid", raw: "required", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseIntegrationRequirement(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatal("parseIntegrationRequirement() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIntegrationRequirement() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("parseIntegrationRequirement() = %t, want %t", got, tc.want)
			}
		})
	}
}
