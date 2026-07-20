package accountbalancesnapshotcleanup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	cleanupservice "juhe-ai/backend-go/internal/modules/accountbalancesnapshotcleanup"
)

const (
	TaskType       = "account-balance-snapshot-cleanup"
	QueueName      = "account-balance-snapshot-cleanup"
	PayloadVersion = 1
)

var ErrInvalidPayload = errors.New("invalid account balance snapshot cleanup task payload")

type Task struct {
	AccountID       string    `json:"accountId"`
	SystemAccountID string    `json:"systemAccountId"`
	UpdatedBefore   time.Time `json:"updatedBefore"`
	Reason          string    `json:"reason"`
}

type taskPayload struct {
	Version int `json:"version"`
	Task
}

func Encode(task Task) ([]byte, error) {
	if err := validateTask(task); err != nil {
		return nil, err
	}
	data, err := json.Marshal(taskPayload{Version: PayloadVersion, Task: task})
	if err != nil {
		return nil, fmt.Errorf("encode account balance snapshot cleanup task payload: %w", err)
	}
	return data, nil
}

func Decode(data []byte) (Task, error) {
	var payload taskPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return Task{}, fmt.Errorf("%w: decode payload: %w", ErrInvalidPayload, err)
	}
	if payload.Version != PayloadVersion {
		return Task{}, fmt.Errorf("%w: unsupported payload version: %d", ErrInvalidPayload, payload.Version)
	}
	if err := validateTask(payload.Task); err != nil {
		return Task{}, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	return payload.Task, nil
}

func UniqueKey(task Task) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		strings.TrimSpace(task.SystemAccountID),
		strings.TrimSpace(task.AccountID),
		task.UpdatedBefore.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(task.Reason),
	}, "\x00")))
	return TaskType + ":" + hex.EncodeToString(sum[:])
}

func validateTask(task Task) error {
	if strings.TrimSpace(task.AccountID) == "" {
		return fmt.Errorf("account_id is required")
	}
	if strings.TrimSpace(task.SystemAccountID) == "" {
		return fmt.Errorf("system_account_id is required")
	}
	if task.UpdatedBefore.IsZero() {
		return fmt.Errorf("updated_before is required")
	}
	if !cleanupservice.IsValidReason(cleanupservice.Reason(strings.TrimSpace(task.Reason))) {
		return fmt.Errorf("unsupported cleanup reason: %q", task.Reason)
	}
	return nil
}
