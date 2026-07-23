package gatewaydeadline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrFirstByteDeadline = errors.New("网关上游首个可见字节超时")

// Controller owns one attempt-scoped first-visible deadline. The child context
// is attached to the real HTTP request so expiry interrupts response headers
// and body reads. MarkVisible stops the timer without canceling the request.
type Controller struct {
	ctx    context.Context
	cancel context.CancelCauseFunc

	mu      sync.Mutex
	timer   *time.Timer
	visible bool
	closed  bool
}

func New(parent context.Context, deadline time.Time) (*Controller, error) {
	if parent == nil {
		return nil, fmt.Errorf("gateway deadline parent context is required")
	}
	ctx, cancel := context.WithCancelCause(parent)
	controller := &Controller{ctx: ctx, cancel: cancel}
	if !deadline.IsZero() {
		delay := time.Until(deadline)
		if delay < 0 {
			delay = 0
		}
		controller.timer = time.AfterFunc(delay, controller.expire)
	}
	return controller, nil
}

func (c *Controller) Context() context.Context { return c.ctx }

func (c *Controller) MarkVisible() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.visible {
		return
	}
	c.visible = true
	if c.timer != nil {
		c.timer.Stop()
	}
}

func (c *Controller) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if c.timer != nil {
		c.timer.Stop()
	}
	c.mu.Unlock()
	c.cancel(context.Canceled)
}

func (c *Controller) expire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.visible {
		return
	}
	c.cancel(ErrFirstByteDeadline)
}
