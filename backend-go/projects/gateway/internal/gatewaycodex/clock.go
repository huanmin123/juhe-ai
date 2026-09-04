package gatewaycodex

import (
	"crypto/rand"
	"encoding"
	"fmt"
	"time"
)

// Clock injects time; tests use a fixed clock. It mirrors the Node
// Date.now()/new Date() reads in the migrated services.
type Clock interface {
	Now() time.Time
}

// SystemClock is the default wall clock.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// NowMs returns the clock reading in unix milliseconds.
func NowMs(clock Clock) int64 {
	if clock == nil {
		return time.Now().UnixMilli()
	}
	return clock.Now().UnixMilli()
}

// IDGenerator mirrors the node:crypto randomUUID reads of the migrated
// services (source fence ids, compact ids).
type IDGenerator func() string

// RandomUUID mirrors randomUUID() of node:crypto through the standard
// library. The v4 formatting matches the Node output consumed by
// isSourceFenceId.
func RandomUUID() string {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		// crypto/rand never fails on the supported platforms; mirror the Node
		// throw path with a panic so callers cannot silently continue with a
		// non-unique fence id.
		panic(fmt.Errorf("randomUUID: %w", err))
	}
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// compile-time: time.Time stays the only instant representation crossing the
// package boundary.
var _ encoding.TextMarshaler = time.Time{}
