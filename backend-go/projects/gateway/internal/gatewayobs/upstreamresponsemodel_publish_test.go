package gatewayobs

import (
	"io"
	"strings"
	"testing"
)

// D-97 (BUG-0175 wave 4 W4-E): the publishing wrapper feeds the observed
// upstream model into the response snapshot before the usage finalization
// reads it, mirroring the Node lazy `upstreamResponseModelObservation?.model`
// getter timing.

func TestPublishingBodyPublishesModelOnEOF(t *testing.T) {
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{
		Protocol: UpstreamResponseModelProtocolOpenAI,
		SSE:      true,
	})
	var published string
	reader := ObserveUpstreamResponseModelBodyPublishing(
		strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5\"}}\n\n"),
		observation,
		func(model string) { published = model },
	)
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read err = %v", err)
	}
	if len(body) == 0 {
		t.Fatal("body must pass through untouched")
	}
	if published != "gpt-5" {
		t.Fatalf("published model = %q, want gpt-5", published)
	}
	if observation.Model() != "gpt-5" {
		t.Fatalf("observation model = %q", observation.Model())
	}
}

func TestPublishingBodyPublishesEmptyWhenNothingObserved(t *testing.T) {
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{
		Protocol: UpstreamResponseModelProtocolOpenAI,
	})
	var published string
	reader := ObserveUpstreamResponseModelBodyPublishing(strings.NewReader("not json"), observation,
		func(model string) { published = model })
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("read err = %v", err)
	}
	if published != "" {
		t.Fatalf("published model = %q, want empty", published)
	}
}

func TestPublishingBodyClosePublishesObservedStateOnce(t *testing.T) {
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{
		Protocol: UpstreamResponseModelProtocolOpenAI,
		SSE:      true,
	})
	published := ""
	reader := ObserveUpstreamResponseModelBodyPublishing(
		strings.NewReader("data: {\"model\":\"gpt-5-mini\"}\n\ndata: {\"model\":\"gpt-5"),
		observation,
		func(model string) { published += model },
	)
	// Partial read, then close before EOF: the observed-so-far state
	// publishes exactly once (Node's getter answers with whatever was seen).
	buffer := make([]byte, 64)
	if _, err := reader.Read(buffer); err != nil {
		t.Fatalf("read err = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close err = %v", err)
	}
	if published != "gpt-5-mini" {
		t.Fatalf("published = %q, want gpt-5-mini once", published)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second close err = %v", err)
	}
	if published != "gpt-5-mini" {
		t.Fatalf("second close re-published: %q", published)
	}
}

func TestPublishingBodyNilPublishStillObserves(t *testing.T) {
	observation := CreateUpstreamResponseModelObservation(UpstreamResponseModelObserverOptions{
		Protocol: UpstreamResponseModelProtocolAnthropic,
		SSE:      true,
	})
	reader := ObserveUpstreamResponseModelBodyPublishing(
		strings.NewReader("event: message_start\ndata: {\"message\":{\"model\":\"claude-x\"}}\n\n"),
		observation,
		nil,
	)
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("read err = %v", err)
	}
	if observation.Model() != "claude-x" {
		t.Fatalf("observation model = %q", observation.Model())
	}
}
