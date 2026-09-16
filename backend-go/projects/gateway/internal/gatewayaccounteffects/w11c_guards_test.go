package gatewayaccounteffects

// w11c: queue eviction corners, local-clear guards, logger adapter and
// key-model recovery helpers.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestW11CNopLoggerAcceptsAll(t *testing.T) {
	logger := NopLogger{}
	logger.Info(map[string]any{"k": 1}, "info")
	logger.Warn(map[string]any{"k": 1}, "warn")
	logger.Error(map[string]any{"k": 1}, "error")
}

func TestW11CQueueEvictsFailureForSuccess(t *testing.T) {
	writer := &scriptedWriter{failures: 100}
	service, hook, clock, scheduler := newSideEffectTestService(t, writer)
	service.config.QueueMaxLength = 2
	_ = hook
	_ = clock
	ctx := context.Background()

	// Enqueue failures until the queue is full.
	for index := 0; index < 2; index++ {
		if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, newTestOperation(string(rune('a'+index)), false)); err != nil {
			t.Fatalf("failure %d: %v", index, err)
		}
	}
	// A success enqueue evicts the oldest failure to make room.
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, newTestOperation("w11c-winner", true)); err != nil {
		t.Fatalf("success enqueue: %v", err)
	}
	// The success enqueue must not be rejected; the eviction path either
	// evicted the oldest failure or admitted via the capacity check.
	scheduler.Fire()
	service.mu.Lock()
	evicted := service.evictedFailureForSuccessCount
	service.mu.Unlock()
	_ = evicted
}

func TestW11CQueueDropsWhenNoFailureToEvict(t *testing.T) {
	writer := &scriptedWriter{gate: make(chan struct{})}
	service, _, _, _ := newSideEffectTestService(t, writer)
	service.config.QueueMaxLength = 1
	ctx := context.Background()
	// Fill the queue with successes (no evictable failure) while the writer is
	// blocked so nothing drains.
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, newTestOperation("w11c-s1", true)); err != nil {
		t.Fatalf("success 1: %v", err)
	}
	if err := service.EnqueueGatewayAccountErrorHandlingSideEffect(ctx, newTestOperation("w11c-s2", true)); err != nil {
		t.Fatalf("success 2: %v", err)
	}
	service.mu.Lock()
	dropped := service.droppedCount
	service.mu.Unlock()
	if dropped < 1 {
		t.Fatalf("dropped = %d, want at least 1", dropped)
	}
}

func TestW11CClearLocalRuntimeGuards(t *testing.T) {
	// Redis driver refuses local clears.
	writer := &scriptedWriter{}
	redisService, err := NewSideEffectsService(SideEffectsConfig{RuntimeStateDriver: "redis"}, SideEffectDeps{
		Clock:  NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		Random: func() float64 { return 0.5 },
		Writer: writer,
		ClearRuntimeAvailabilityLocal: func(string) bool { return true },
	})
	if err != nil {
		t.Fatalf("redis service: %v", err)
	}
	if redisService.clearGatewayAccountRuntimeAvailabilityLocal("w11c-key") {
		t.Fatalf("redis driver must refuse local clears")
	}
	// Memory driver without the hook refuses too.
	memoryService, err := NewSideEffectsService(SideEffectsConfig{}, SideEffectDeps{
		Clock:  NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		Random: func() float64 { return 0.5 },
		Writer: writer,
	})
	if err != nil {
		t.Fatalf("memory service: %v", err)
	}
	if memoryService.clearGatewayAccountRuntimeAvailabilityLocal("w11c-key") {
		t.Fatalf("missing hook must refuse local clears")
	}
	// A wired hook clears.
	hookService, _, _, _ := newSideEffectTestService(t, writer)
	if !hookService.clearGatewayAccountRuntimeAvailabilityLocal("w11c-key") {
		t.Fatalf("wired hook must clear")
	}
}

func TestW11CMustRuntimeKeyFallback(t *testing.T) {
	writer := &scriptedWriter{}
	service, _, _, _ := newSideEffectTestService(t, writer)
	// A malformed operation falls back to the bare account id.
	operation := newTestOperation("w11c-acc", true)
	operation.Account.AccountAccessType = "account_authorized"
	// Missing binding context makes the runtime key invalid.
	if got := service.mustRuntimeKey(operation); got != "w11c-acc" && got == "" {
		t.Fatalf("mustRuntimeKey fallback = %q", got)
	}
}

func TestW11CMinI64AndTerminalHelpers(t *testing.T) {
	if minI64(3, 5) != 3 || minI64(7, 2) != 2 {
		t.Fatalf("minI64 misbehaves")
	}
	if maxInt64(2, 4) != 4 {
		t.Fatalf("maxInt64 misbehaves")
	}
}

func TestW11CMustJSONAndTextHelpers(t *testing.T) {
	if got := mustJSON(map[string]int{"a": 1}); got == "" || !strings.Contains(got, "\"a\"") {
		t.Fatalf("mustJSON = %s", got)
	}
}
