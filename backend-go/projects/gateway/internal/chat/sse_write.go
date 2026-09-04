package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// SSE response plumbing ported from chat-sse-subscriber.ts. Byte layout is
// exactly `event: <type>\ndata: <json>\n\n` with eventVersion merged into the
// data payload; heartbeats are `: heartbeat\n\n` every 5s.

// chatSSEWriter wraps an http.ResponseWriter with the Node ChatSseResponse
// contract (destroyed ~ request close, writableEnded ~ end()).
type chatSSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher

	mu          sync.Mutex
	closed      bool // downstream connection closed (request context done)
	ended       bool // End() called
	writeFailed bool
}

func newChatSSEWriter(w http.ResponseWriter, requestContext context.Context, onClose func()) *chatSSEWriter {
	writer := &chatSSEWriter{w: w}
	if flusher, ok := w.(http.Flusher); ok {
		writer.flusher = flusher
	}
	if requestContext != nil {
		go func() {
			<-requestContext.Done()
			writer.mu.Lock()
			already := writer.closed
			writer.closed = true
			writer.mu.Unlock()
			if !already && onClose != nil {
				onClose()
			}
		}()
	}
	return writer
}

// WriteEvent mirrors writeChatSseEvent; returns false when the response is no
// longer writable.
func (s *chatSSEWriter) WriteEvent(eventType string, data any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ended || s.writeFailed {
		return false
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return false
	}
	chunk := "event: " + eventType + "\ndata: " + string(payload) + "\n\n"
	defer func() {
		if recover() != nil {
			s.writeFailed = true
		}
	}()
	if _, err := s.w.Write([]byte(chunk)); err != nil {
		s.writeFailed = true
		return false
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return true
}

// WriteComment writes a raw SSE comment line (heartbeat).
func (s *chatSSEWriter) WriteComment(comment string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ended || s.writeFailed {
		return false
	}
	if _, err := s.w.Write([]byte(comment)); err != nil {
		s.writeFailed = true
		return false
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return true
}

// End mirrors res.end(): subsequent writes fail and the response finishes.
func (s *chatSSEWriter) End() {
	s.mu.Lock()
	already := s.ended
	s.ended = true
	s.mu.Unlock()
	if already {
		return
	}
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

// Writable mirrors !destroyed && !writableEnded.
func (s *chatSSEWriter) Writable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !s.ended && !s.writeFailed
}

// sseSubscriber mirrors createChatSseSubscriber: on terminal events it ends
// the response and detaches from the registry.
type sseSubscriber struct {
	writer   *chatSSEWriter
	detach   func()
	mu       sync.Mutex
	detached bool
}

func (s *sseSubscriber) detachOnce() {
	s.mu.Lock()
	if s.detached {
		s.mu.Unlock()
		return
	}
	s.detached = true
	s.mu.Unlock()
	if s.detach != nil {
		func() {
			defer func() { _ = recover() }()
			s.detach()
		}()
	}
	s.writer.End()
}

// TrySend implements ChatGenerationSubscriber.
func (s *sseSubscriber) TrySend(event ChatGenerationEvent) bool {
	s.mu.Lock()
	detached := s.detached
	s.mu.Unlock()
	if detached || !s.writer.Writable() {
		s.detachOnce()
		return false
	}
	data := map[string]any{}
	for key, value := range event.Data {
		data[key] = value
	}
	data["eventVersion"] = event.EventVersion
	if !s.writer.WriteEvent(event.Type, data) {
		s.detachOnce()
		return false
	}
	if event.Type == "message.completed" || event.Type == "message.failed" || event.Type == "message.canceled" {
		s.detachOnce()
	}
	return true
}

// startChatSSEHeartbeat mirrors startChatSseHeartbeat. intervalMs <= 0 uses
// the Node default (5000). Returns the stop function.
func startChatSSEHeartbeat(writer *chatSSEWriter, intervalMs int, onUnwritable func()) (stop func()) {
	if intervalMs <= 0 {
		intervalMs = 5000
	}
	stopped := false
	var mu sync.Mutex
	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	done := make(chan struct{})
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				mu.Lock()
				if stopped {
					mu.Unlock()
					continue
				}
				mu.Unlock()
				if !writer.Writable() || !writer.WriteComment(": heartbeat\n\n") {
					mu.Lock()
					already := stopped
					stopped = true
					mu.Unlock()
					if !already && onUnwritable != nil {
						func() {
							defer func() { _ = recover() }()
							onUnwritable()
						}()
					}
				}
			}
		}
	}()
	return func() {
		mu.Lock()
		if stopped {
			mu.Unlock()
			close(done)
			return
		}
		stopped = true
		mu.Unlock()
		close(done)
	}
}

// prepareSSEResponse mirrors prepareSseResponse.
func prepareSSEResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
