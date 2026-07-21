package publicapilog

import (
	"encoding/json"
	"errors"
	"fmt"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	TaskTypeWrite  = "public-api-log:write"
	QueueName      = "public-api-logs"
	PayloadVersion = 1
)

var ErrInvalidPayload = errors.New("invalid public api log write task payload")

type WriteTaskPayload struct {
	Version     int                    `json:"version"`
	Correlation *TaskCorrelation       `json:"correlation,omitempty"`
	Log         port.PublicAPILogInput `json:"log"`
}

type TaskCorrelation struct {
	TraceID   string `json:"traceId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

func EncodeWriteTaskPayload(input port.PublicAPILogInput) ([]byte, error) {
	return EncodeWriteTaskPayloadWithCorrelation(input, TaskCorrelation{})
}

func EncodeWriteTaskPayloadWithCorrelation(input port.PublicAPILogInput, correlation TaskCorrelation) ([]byte, error) {
	if err := validatePublicAPILogTaskInput(input); err != nil {
		return nil, err
	}
	payload := WriteTaskPayload{
		Version: PayloadVersion,
		Log:     input,
	}
	if correlation.TraceID != "" || correlation.RequestID != "" {
		payload.Correlation = &correlation
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode public api log write task payload: %w", err)
	}
	return data, nil
}

func DecodeWriteTaskPayload(data []byte) (port.PublicAPILogInput, error) {
	payload, err := DecodeWriteTaskEnvelope(data)
	if err != nil {
		return port.PublicAPILogInput{}, err
	}
	return payload.Log, nil
}

func DecodeWriteTaskEnvelope(data []byte) (WriteTaskPayload, error) {
	var payload WriteTaskPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return WriteTaskPayload{}, fmt.Errorf("%w: decode public api log write task payload: %w", ErrInvalidPayload, err)
	}
	if payload.Version != PayloadVersion {
		return WriteTaskPayload{}, fmt.Errorf("%w: unsupported public api log write task payload version: %d", ErrInvalidPayload, payload.Version)
	}
	if err := validatePublicAPILogTaskInput(payload.Log); err != nil {
		return payload, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	return payload, nil
}

func validatePublicAPILogTaskInput(input port.PublicAPILogInput) error {
	if input.ID == "" {
		return fmt.Errorf("public api log write task id is required")
	}
	if input.Method == "" {
		return fmt.Errorf("public api log write task method is required")
	}
	if input.Path == "" {
		return fmt.Errorf("public api log write task path is required")
	}
	if input.StartedAt.IsZero() {
		return fmt.Errorf("public api log write task started_at is required")
	}
	if input.EndedAt.IsZero() {
		return fmt.Errorf("public api log write task ended_at is required")
	}
	return nil
}
