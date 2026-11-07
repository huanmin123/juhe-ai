package main

import (
	"context"
	"errors"
	"testing"
)

func TestRunRetentionOwnerWithLeaseRunsAndCloses(t *testing.T) {
	closed := false
	ran := false
	err := runRetentionOwnerWithLease(context.Background(), "test", nil,
		func(context.Context) (int, bool, error) { return 7, true, nil },
		func(_ context.Context, keeper int) error {
			ran = keeper == 7
			return nil
		},
		func(int) { closed = true })
	if err != nil || !ran || !closed {
		t.Fatalf("err=%v ran=%v closed=%v", err, ran, closed)
	}
}

func TestRunRetentionOwnerWithLeasePropagatesRunError(t *testing.T) {
	want := errors.New("retention failed")
	closed := false
	err := runRetentionOwnerWithLease(context.Background(), "test", nil,
		func(context.Context) (int, bool, error) { return 1, true, nil },
		func(context.Context, int) error { return want },
		func(int) { closed = true })
	if !errors.Is(err, want) || !closed {
		t.Fatalf("err=%v closed=%v", err, closed)
	}
}

func TestRunRetentionOwnerWithLeaseCancellationWhileWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runRetentionOwnerWithLease(ctx, "test", nil,
		func(context.Context) (int, bool, error) { return 0, false, nil },
		func(context.Context, int) error { t.Fatal("run must not be called"); return nil },
		func(int) { t.Fatal("close must not be called") })
	if err != nil {
		t.Fatalf("cancelled wait must return nil, got %v", err)
	}
}
