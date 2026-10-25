package accounthealth

import (
	"context"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"testing"
	"time"
)

func TestProbeTimeoutLadder(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want []time.Duration
	}{
		{"default", 65 * time.Second, []time.Duration{20 * time.Second, 30 * time.Second, 40 * time.Second}},
		{"short", 5 * time.Second, []time.Duration{5 * time.Second}},
		{"mid", 25 * time.Second, []time.Duration{20 * time.Second, 25 * time.Second}},
		{"long", 35 * time.Second, []time.Duration{20 * time.Second, 30 * time.Second, 35 * time.Second}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := probeTimeoutLadder(test.in)
			if len(got) != len(test.want) {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("got=%v want=%v", got, test.want)
				}
			}
		})
	}
}

func TestExecuteInputProbeUsesInjectedClockForOutcome(t *testing.T) {
	fixed := time.Date(2026, 8, 17, 3, 4, 5, 678000000, time.FixedZone("UTC+8", 8*60*60))
	input := exactkeyprobe.Input{AccountID: "account", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1}
	outcome, err := ExecuteInputProbe(context.Background(), nil, OwnerLease{}, input, ProbeRequest{RequestID: "request", AccountID: "other"}, exactkeyprobe.ProbeOptions{Now: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	want := fixed.UTC()
	if !outcome.ObservedAt.Equal(want) || outcome.ObservedAt.Location() != time.UTC {
		t.Fatalf("outcome observedAt=%s, want injected UTC clock %s", outcome.ObservedAt, want)
	}
}

func staleLeaseFor(t *testing.T, store *Store, ownerID string) OwnerLease {
	t.Helper()
	if _, acquired, err := store.AcquireOwnerLease(context.Background(), ownerID, time.Minute); err != nil || !acquired {
		t.Fatalf("acquire lease acquired=%t err=%v", acquired, err)
	}
	return OwnerLease{OwnerID: ownerID, FenceToken: 999999}
}

