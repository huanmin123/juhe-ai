// Package safego is the process-wide panic barrier for self-started
// goroutines. The Go runtime terminates the whole process when any goroutine
// panics without a recover on its own stack, and a recover on the main
// goroutine cannot reach them. Every goroutine that runs business logic, I/O,
// or upstream requests must either start through Go or defer a barrier on the
// first line of its body. Pure in-memory goroutines (channel send/recv,
// cancel, WaitGroup bookkeeping) are exempt.
//
// recover() only works when it is called directly from the function that the
// runtime is executing as a deferred function, so both barriers below keep
// the recover() call in their own (or the returned closure's) frame. Wrapping
// them in an extra function — `defer func() { safego.Recover("x") }()` — is
// silently inert and must never be used.
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Recover is the simple goroutine panic barrier. Use as
//
//	defer safego.Recover("pkg.role")
//
// directly on the goroutine body (or any function whose panic must not
// escape). A panic is logged with the component label and stack; the
// goroutine then ends as if it had returned.
func Recover(name string) {
	recovered := recover()
	if recovered == nil {
		return
	}
	logPanic(name, recovered)
}

// Handle is Recover with a callback that translates the panic into the
// goroutine's own failure path (error result channel, status update, ...).
// Use as
//
//	defer safego.Handle("pkg.role", func(recovered any) {
//		results <- errFromPanic(recovered)
//	})
//
// The deferred call is Handle itself and recover() runs in its own frame;
// the callback fires only on a real panic, after the panic is already
// absorbed, so a panic raised inside the callback propagates normally.
func Handle(name string, onPanic func(recovered any)) {
	recovered := recover()
	if recovered == nil {
		return
	}
	logPanic(name, recovered)
	if onPanic != nil {
		onPanic(recovered)
	}
}

// Go starts fn in a new goroutine behind a panic barrier. The label names the
// goroutine's role for the recovery log.
func Go(name string, fn func()) {
	go func() {
		defer Recover(name)
		fn()
	}()
}

func logPanic(name string, recovered any) {
	slog.Default().Error("goroutine panic recovered; process keeps running",
		"event", "goroutine_panic_recovered",
		"component", name,
		"panic", recovered,
		"stack", string(debug.Stack()),
	)
}
