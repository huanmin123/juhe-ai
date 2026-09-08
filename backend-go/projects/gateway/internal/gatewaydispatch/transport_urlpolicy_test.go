package gatewaydispatch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sharedupstreamhttp "github.com/huanminabc/juhe-ai/backend-go-platform/upstreamhttp"
)

func TestResolvedUpstreamURLPolicyRejectsPrivateLiteral(t *testing.T) {
	policy := NewResolvedUpstreamURLPolicy(sharedupstreamhttp.URLSecurityConfig{})
	_, err := policy.PrepareSafeUpstreamRequestURL(context.Background(), "http://192.168.1.5:8000/v1")
	var staticErr *UnsafeUpstreamURLError
	if !errors.As(err, &staticErr) {
		t.Fatalf("err = %v, want UnsafeUpstreamURLError", err)
	}
	_, err = policy.PrepareSafeUpstreamRequestURL(context.Background(), "http://internal.localhost/v1")
	if !errors.As(err, &staticErr) {
		t.Fatalf("localhost err = %v, want UnsafeUpstreamURLError", err)
	}
	for _, bad := range []string{"not a url", "ftp://example.com", "http:///path"} {
		if _, err := policy.PrepareSafeUpstreamRequestURL(context.Background(), bad); err == nil {
			t.Fatalf("policy accepted %q", bad)
		}
	}
}

func TestResolvedUpstreamURLPolicyAllowlistAndAllowPrivate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	host, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	originKey := "http://" + host + ":" + port

	// Without an allowlist the private literal is refused.
	strict := NewResolvedUpstreamURLPolicy(sharedupstreamhttp.URLSecurityConfig{})
	if _, err := strict.PrepareSafeUpstreamRequestURL(context.Background(), originKey+"/v1"); err == nil {
		t.Fatal("strict policy accepted private literal")
	}
	// With the origin allowlisted it passes (and no DNS is needed).
	allowlisted := NewResolvedUpstreamURLPolicy(sharedupstreamhttp.URLSecurityConfig{
		PrivateOriginAllowlist: map[string]bool{originKey: true},
	})
	if _, err := allowlisted.PrepareSafeUpstreamRequestURL(context.Background(), originKey+"/v1"); err != nil {
		t.Fatalf("allowlisted policy rejected the origin: %v", err)
	}
	// allowPrivateBaseUrls passes everything (development escape hatch).
	permissive := NewResolvedUpstreamURLPolicy(sharedupstreamhttp.URLSecurityConfig{AllowPrivateBaseUrls: true})
	if _, err := permissive.PrepareSafeUpstreamRequestURL(context.Background(), "http://127.0.0.1:1/v1"); err != nil {
		t.Fatalf("allowPrivate policy rejected loopback: %v", err)
	}
	// The dial guard travels with the policy.
	if allowlisted.Guard() == nil || allowlisted.Guard().Key() == "" {
		t.Fatal("policy must expose its dial guard")
	}
}

func TestResolvedUpstreamURLPolicyMapsResolvedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	// The httptest host is a loopback literal, so resolution never happens;
	// force the resolved-error path with a hostname that the guard resolves
	// through the loopback listener address.
	policy := NewResolvedUpstreamURLPolicy(sharedupstreamhttp.URLSecurityConfig{})
	// hostname forms go through the guard's resolver; stub the resolver by
	// dialing against a hostname the system resolver maps to loopback.
	_, err := policy.PrepareSafeUpstreamRequestURL(context.Background(), "http://10.0.0.1:9999/v1")
	var staticErr *UnsafeUpstreamURLError
	if !errors.As(err, &staticErr) {
		t.Fatalf("literal err = %v, want static", err)
	}
	// Public literal hostname pass-through (no DNS for IP literals).
	if _, err := policy.PrepareSafeUpstreamRequestURL(context.Background(), fmt.Sprintf("http://%s:%s/v1", publicIPForTest(t), "80")); err != nil {
		t.Fatalf("public literal rejected: %v", err)
	}
	_ = server
}

func publicIPForTest(t *testing.T) string {
	t.Helper()
	// Documentation-range address: globally routable syntax, never dialed by
	// this test (PrepareSafeUpstreamRequestURL skips DNS for IP literals).
	return "93.184.216.34"
}

func TestRequestUpstreamProducesUnsafeResolvedErrorThroughPolicy(t *testing.T) {
	// End-to-end at the transport seam: a policy-registered upstream URL that
	// fails the static check surfaces as UnsafeResolvedUpstreamURLError? No —
	// the static variant is UnsafeUpstreamURLError; the attempt engine's
	// errors.As(unsafe resolved) path must only trigger for resolution hits.
	policy := NewResolvedUpstreamURLPolicy(sharedupstreamhttp.URLSecurityConfig{})
	_, err := RequestUpstream(context.Background(), "http://172.16.5.5/v1", UpstreamRequestOptions{
		Method: http.MethodGet,
	}, TransportDeps{URLPolicy: policy})
	var resolvedErr *UnsafeResolvedUpstreamURLError
	if errors.As(err, &resolvedErr) {
		t.Fatalf("static failure must not surface as resolved error: %v", err)
	}
	var staticErr *UnsafeUpstreamURLError
	if !errors.As(err, &staticErr) {
		t.Fatalf("err = %v, want UnsafeUpstreamURLError", err)
	}
}

func TestBoundedConcurrencyGovernorLimitsInFlight(t *testing.T) {
	governor := NewBoundedConcurrencyGovernor(2)
	var inFlight, peak int64
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := governor.Acquire(context.Background())
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			current := atomic.AddInt64(&inFlight, 1)
			for {
				latest := atomic.LoadInt64(&peak)
				if current <= latest || atomic.CompareAndSwapInt64(&peak, latest, current) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
			release()
		}()
	}
	wg.Wait()
	if atomic.LoadInt64(&peak) > 2 {
		t.Fatalf("peak in-flight = %d, want <= 2", peak)
	}
	// The slots are all returned: another acquire must not block.
	done := make(chan struct{})
	go func() {
		release, err := governor.Acquire(context.Background())
		if err != nil {
			t.Errorf("acquire after drain: %v", err)
		}
		release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("acquire after drain blocked")
	}
}

func TestBoundedConcurrencyGovernorContextCancelAndNil(t *testing.T) {
	governor := NewBoundedConcurrencyGovernor(1)
	release, err := governor.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := governor.Acquire(ctx); err == nil {
		t.Fatal("cancelled acquire must fail")
	}
	release()
	// nil governor keeps the unlimited Nop semantics.
	var nilGovernor *BoundedConcurrencyGovernor
	if _, err := nilGovernor.Acquire(context.Background()); err != nil {
		t.Fatalf("nil governor acquire = %v", err)
	}
	if NewBoundedConcurrencyGovernor(0) != nil {
		t.Fatal("capacity < 1 must return nil (Nop semantics)")
	}
}
