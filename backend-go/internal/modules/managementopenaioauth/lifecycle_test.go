package managementopenaioauth

import (
	"testing"
	"time"
)

func TestSessionLifecycleRetryableExchangeFlow(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	pending := SessionLifecycle{Status: SessionPending}

	processing, acquire, err := AcquireSession(pending, "lease-1", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if processing.Status != SessionProcessing || processing.LeaseToken != "lease-1" || !processing.LeaseUntil.Equal(now.Add(30*time.Second)) || acquire.ResumeExchanged {
		t.Fatalf("processing = %#v, acquire = %#v", processing, acquire)
	}
	if _, _, err = AcquireSession(processing, "lease-2", now.Add(time.Second), 30*time.Second); ErrorCodeOf(err) != ErrorCodeSessionProcessing {
		t.Fatalf("active lease acquire error = %v", err)
	}

	released, err := ReleaseSessionRetryable(processing, "lease-1", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != SessionPending || released.LeaseToken != "" || !released.LeaseUntil.IsZero() {
		t.Fatalf("released = %#v", released)
	}

	processing, _, err = AcquireSession(released, "lease-2", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	exchanged, err := MarkSessionExchanged(processing, "lease-2", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if exchanged.Status != SessionExchanged {
		t.Fatalf("exchanged = %#v", exchanged)
	}

	retryExchanged, err := ReleaseSessionRetryable(exchanged, "lease-2", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if retryExchanged.Status != SessionExchanged || retryExchanged.LeaseToken != "" {
		t.Fatalf("retry exchanged = %#v", retryExchanged)
	}
	resumed, acquire, err := AcquireSession(retryExchanged, "lease-3", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != SessionExchanged || !acquire.ResumeExchanged {
		t.Fatalf("resumed = %#v, acquire = %#v", resumed, acquire)
	}
	consumed, err := ConsumeSession(resumed, "lease-3", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if consumed.Status != SessionConsumed || consumed.LeaseToken != "" {
		t.Fatalf("consumed = %#v", consumed)
	}
	if _, _, err = AcquireSession(consumed, "lease-4", now, 30*time.Second); ErrorCodeOf(err) != ErrorCodeSessionConsumed {
		t.Fatalf("consumed acquire error = %v", err)
	}
}

func TestSessionLifecycleAllowsExpiredLeaseTakeoverButRejectsStaleWriter(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	stale := SessionLifecycle{Status: SessionProcessing, LeaseToken: "old", LeaseUntil: now.Add(-time.Nanosecond)}
	taken, _, err := AcquireSession(stale, "new", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if taken.LeaseToken != "new" {
		t.Fatalf("lease token = %q", taken.LeaseToken)
	}
	if _, err = MarkSessionExchanged(taken, "old", now); ErrorCodeOf(err) != ErrorCodeSessionProcessing {
		t.Fatalf("stale writer error = %v", err)
	}
	if _, err = MarkSessionExchanged(taken, "new", now.Add(time.Minute)); ErrorCodeOf(err) != ErrorCodeSessionProcessing {
		t.Fatalf("expired current lease error = %v", err)
	}
}

func TestSessionLifecycleRejectsInvalidLeaseArguments(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name  string
		token string
		ttl   time.Duration
	}{
		{name: "empty token", ttl: time.Second},
		{name: "non-positive ttl", token: "lease"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := AcquireSession(SessionLifecycle{Status: SessionPending}, tc.token, now, tc.ttl)
			if ErrorCodeOf(err) != ErrorCodeRequestInvalid {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
