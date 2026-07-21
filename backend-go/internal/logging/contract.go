package logging

import (
	"fmt"
	"strings"
	"time"
)

const EventVersion = 1

type EventInput struct {
	Level, Service, Role, Event                string
	TraceID, RequestID, JobID, ParentID        string
	Stage, Outcome, FailureClass               string
	DurationMS, StartedOffsetMS, EndedOffsetMS int64
}

type EventEnvelope struct {
	Time            string `json:"time"`
	Version         int    `json:"version"`
	Level           string `json:"level"`
	Service         string `json:"service"`
	Role            string `json:"role"`
	Event           string `json:"event"`
	TraceID         string `json:"traceId,omitempty"`
	RequestID       string `json:"requestId,omitempty"`
	JobID           string `json:"jobId,omitempty"`
	ParentID        string `json:"parentId,omitempty"`
	Stage           string `json:"stage,omitempty"`
	Outcome         string `json:"outcome,omitempty"`
	FailureClass    string `json:"failureClass,omitempty"`
	DurationMS      int64  `json:"durationMs,omitempty"`
	StartedOffsetMS int64  `json:"startedOffsetMs,omitempty"`
	EndedOffsetMS   int64  `json:"endedOffsetMs,omitempty"`
}

func BuildEventEnvelope(input EventInput) (EventEnvelope, error) {
	for name, value := range map[string]string{"level": input.Level, "service": input.Service, "role": input.Role, "event": input.Event} {
		if strings.TrimSpace(value) == "" {
			return EventEnvelope{}, fmt.Errorf("日志字段不能为空: %s", name)
		}
	}
	if input.DurationMS < 0 || input.StartedOffsetMS < 0 || input.EndedOffsetMS < input.StartedOffsetMS || input.EndedOffsetMS-input.StartedOffsetMS != input.DurationMS {
		return EventEnvelope{}, fmt.Errorf("日志耗时字段不一致")
	}
	if input.FailureClass != "" && input.FailureClass != "expected" && input.FailureClass != "unexpected" && input.FailureClass != "aborted" && input.FailureClass != "infrastructure" {
		return EventEnvelope{}, fmt.Errorf("无效失败分类: %s", input.FailureClass)
	}
	return EventEnvelope{Time: time.Now().UTC().Format(time.RFC3339Nano), Version: EventVersion, Level: input.Level, Service: input.Service, Role: input.Role, Event: input.Event, TraceID: input.TraceID, RequestID: input.RequestID, JobID: input.JobID, ParentID: input.ParentID, Stage: input.Stage, Outcome: input.Outcome, FailureClass: input.FailureClass, DurationMS: input.DurationMS, StartedOffsetMS: input.StartedOffsetMS, EndedOffsetMS: input.EndedOffsetMS}, nil
}
