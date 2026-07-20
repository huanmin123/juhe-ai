package logging

import (
	"sync"
	"testing"
	"time"
)

type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestNewLoggerDoesNotWaitForDestinationWrite(t *testing.T) {
	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	logger, err := New("info", w)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		logger.Info("stage completed", "event", "gateway.request.stage")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		close(w.release)
		t.Fatal("logger call waited for destination write")
	}
	close(w.release)
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("queued record was not delivered")
	}
}
