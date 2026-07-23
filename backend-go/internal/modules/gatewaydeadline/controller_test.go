package gatewaydeadline

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestControllerExpiresFromAbsoluteDeadline(t *testing.T) {
	controller, err := New(context.Background(), time.Now().Add(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	select {
	case <-controller.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("deadline did not cancel request context")
	}
	if !errors.Is(context.Cause(controller.Context()), ErrFirstByteDeadline) {
		t.Fatalf("cause = %v", context.Cause(controller.Context()))
	}
}

func TestControllerMarkVisibleStopsDeadline(t *testing.T) {
	controller, err := New(context.Background(), time.Now().Add(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	controller.MarkVisible()
	time.Sleep(25 * time.Millisecond)
	if err := controller.Context().Err(); err != nil {
		t.Fatalf("visible request canceled: %v", err)
	}
}
