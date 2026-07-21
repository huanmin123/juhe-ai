package logging

import "testing"

func TestBuildEventEnvelopeRequiresStableTimingAndFailureClass(t *testing.T) {

	event, err := BuildEventEnvelope(EventInput{
		Level:           "info",
		Service:         "gateway",
		Role:            "go",
		Event:           "gateway.request.stage",
		TraceID:         "trace-contract-1",
		RequestID:       "request-contract-1",
		Stage:           "route.resolve",
		Outcome:         "success",
		DurationMS:      4,
		StartedOffsetMS: 2,
		EndedOffsetMS:   6,
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Version == 0 || event.EndedOffsetMS-event.StartedOffsetMS != event.DurationMS {
		t.Fatalf("invalid timing envelope: %+v", event)
	}
	if _, err := BuildEventEnvelope(EventInput{FailureClass: "unknown"}); err == nil {
		t.Fatal("expected invalid failure class")
	}
}
