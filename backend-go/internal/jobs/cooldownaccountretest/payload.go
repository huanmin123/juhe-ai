package cooldownaccountretest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"juhe-ai/backend-go/internal/store/port"
)

const (
	TaskType       = "cooldown-account-retest:probe"
	QueueName      = "account-probes"
	PayloadVersion = 1
)

var ErrInvalidPayload = errors.New("invalid cooldown account retest task payload")

type TaskPayload struct {
	Version int                            `json:"version"`
	Task    port.CooldownAccountRetestTask `json:"task"`
}

func EncodeTask(task port.CooldownAccountRetestTask) ([]byte, error) {
	if err := validateTask(task); err != nil {
		return nil, err
	}
	return json.Marshal(TaskPayload{Version: PayloadVersion, Task: task})
}

func DecodeTask(data []byte) (port.CooldownAccountRetestTask, error) {
	var payload TaskPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return port.CooldownAccountRetestTask{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if payload.Version != PayloadVersion {
		return port.CooldownAccountRetestTask{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidPayload, payload.Version)
	}
	if err := validateTask(payload.Task); err != nil {
		return port.CooldownAccountRetestTask{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	return payload.Task, nil
}

func UniqueKey(task port.CooldownAccountRetestTask) string {
	observation := ""
	if task.ObservationStartedAt != nil {
		observation = task.ObservationStartedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	raw := fmt.Sprintf("%s|%d|%s", strings.TrimSpace(task.AccountID), task.ConfigRevision, observation)
	sum := sha256.Sum256([]byte(raw))
	return "cooldown-account-retest:" + hex.EncodeToString(sum[:])
}

func validateTask(task port.CooldownAccountRetestTask) error {
	if strings.TrimSpace(task.AccountID) == "" {
		return errors.New("account id is required")
	}
	if task.ConfigRevision < 1 {
		return errors.New("config revision must be positive")
	}
	if task.MaxPauseMinutes < 0 || task.MaxRecoveryHours < 0 {
		return errors.New("recovery limits must not be negative")
	}
	return nil
}
