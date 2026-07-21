package operationlog

import (
	"encoding/json"
	"errors"
	"fmt"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	TaskTypeWrite  = "operation-log:write"
	QueueName      = "operation-logs"
	PayloadVersion = 1
)

var ErrInvalidPayload = errors.New("invalid operation log write task payload")

type WriteTaskPayload struct {
	Version     int                    `json:"version"`
	Correlation *TaskCorrelation       `json:"correlation,omitempty"`
	Log         port.OperationLogInput `json:"log"`
}

type TaskCorrelation struct {
	TraceID   string `json:"traceId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

func EncodeWriteTaskPayload(input port.OperationLogInput) ([]byte, error) {
	return EncodeWriteTaskPayloadWithCorrelation(input, TaskCorrelation{})
}

func EncodeWriteTaskPayloadWithCorrelation(input port.OperationLogInput, correlation TaskCorrelation) ([]byte, error) {
	if err := validateOperationLogTaskInput(input); err != nil {
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
		return nil, fmt.Errorf("encode operation log write task payload: %w", err)
	}
	return data, nil
}

func DecodeWriteTaskPayload(data []byte) (port.OperationLogInput, error) {
	payload, err := DecodeWriteTaskEnvelope(data)
	if err != nil {
		return port.OperationLogInput{}, err
	}
	return payload.Log, nil
}

func DecodeWriteTaskEnvelope(data []byte) (WriteTaskPayload, error) {
	var payload WriteTaskPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return WriteTaskPayload{}, fmt.Errorf("%w: decode operation log write task payload: %w", ErrInvalidPayload, err)
	}
	if payload.Version != PayloadVersion {
		return WriteTaskPayload{}, fmt.Errorf("%w: unsupported operation log write task payload version: %d", ErrInvalidPayload, payload.Version)
	}
	if err := validateOperationLogTaskInput(payload.Log); err != nil {
		return payload, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	return payload, nil
}

func validateOperationLogTaskInput(input port.OperationLogInput) error {
	if input.ID == "" {
		return fmt.Errorf("operation log write task id is required")
	}
	if input.ActorSystemAccountID == "" {
		return fmt.Errorf("operation log write task actor_system_account_id is required")
	}
	if input.ActorRole == "" {
		return fmt.Errorf("operation log write task actor_role is required")
	}
	if input.Module == "" {
		return fmt.Errorf("operation log write task module is required")
	}
	if input.Action == "" {
		return fmt.Errorf("operation log write task action is required")
	}
	if input.OperationKey == "" {
		return fmt.Errorf("operation log write task operation_key is required")
	}
	if input.ResourceType == "" {
		return fmt.Errorf("operation log write task resource_type is required")
	}
	if input.Summary == "" {
		return fmt.Errorf("operation log write task summary is required")
	}
	if input.CreatedAt.IsZero() {
		return fmt.Errorf("operation log write task created_at is required")
	}
	return nil
}
