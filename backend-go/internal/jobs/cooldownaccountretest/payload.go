package cooldownaccountretest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"juhe-ai/backend-go/internal/accounthealth"
	"juhe-ai/backend-go/internal/store/port"
)

const (
	TaskType       = "cooldown-account-retest:probe"
	QueueName      = "account-probes"
	PayloadVersion = 3

	maxPauseMinutesHeader  = "juhe-ai-cooldown-retest-max-pause-minutes"
	maxRecoveryHoursHeader = "juhe-ai-cooldown-retest-max-recovery-hours"
	maxPauseMinutesValue   = 1440
	maxRecoveryHoursValue  = 24 * 30
)

var ErrInvalidPayload = errors.New("invalid cooldown account retest task payload")

type TaskPayload struct {
	Version int             `json:"version"`
	Fence   RetestTaskFence `json:"fence"`
}

type RetestTaskFence struct {
	AccountID            string     `json:"accountId"`
	ConfigRevision       int        `json:"configRevision"`
	DispatchRevision     int        `json:"dispatchRevision"`
	ObservationStartedAt *time.Time `json:"observationStartedAt"`
	Generation           string     `json:"generation"`
	SourceConfigRevision *int       `json:"sourceConfigRevision,omitempty"`
}

func EncodeTask(task port.CooldownAccountRetestTask) ([]byte, map[string]string, error) {
	task = canonicalTask(task)
	if err := validateTask(task); err != nil {
		return nil, nil, err
	}
	payload, err := json.Marshal(TaskPayload{Version: PayloadVersion, Fence: taskFence(task)})
	if err != nil {
		return nil, nil, err
	}
	return payload, map[string]string{
		maxPauseMinutesHeader:  strconv.Itoa(task.MaxPauseMinutes),
		maxRecoveryHoursHeader: strconv.Itoa(task.MaxRecoveryHours),
	}, nil
}

func DecodeTask(data []byte, headers map[string]string) (port.CooldownAccountRetestTask, error) {
	var payload TaskPayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return port.CooldownAccountRetestTask{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return port.CooldownAccountRetestTask{}, err
	}
	if payload.Version != PayloadVersion {
		return port.CooldownAccountRetestTask{}, fmt.Errorf("%w: unsupported version %d", ErrInvalidPayload, payload.Version)
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return port.CooldownAccountRetestTask{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if !bytes.Equal(data, canonical) {
		return port.CooldownAccountRetestTask{}, fmt.Errorf("%w: payload is not canonical", ErrInvalidPayload)
	}
	maxPauseMinutes, err := decodeStrategyHeader(headers, maxPauseMinutesHeader)
	if err != nil {
		return port.CooldownAccountRetestTask{}, err
	}
	maxRecoveryHours, err := decodeStrategyHeader(headers, maxRecoveryHoursHeader)
	if err != nil {
		return port.CooldownAccountRetestTask{}, err
	}
	task := port.CooldownAccountRetestTask{
		AccountID: payload.Fence.AccountID, ConfigRevision: payload.Fence.ConfigRevision,
		DispatchRevision: payload.Fence.DispatchRevision, ObservationStartedAt: payload.Fence.ObservationStartedAt,
		Generation: payload.Fence.Generation, SourceConfigRevision: payload.Fence.SourceConfigRevision,
		MaxPauseMinutes: maxPauseMinutes, MaxRecoveryHours: maxRecoveryHours,
	}
	if err := validateTask(task); err != nil {
		return port.CooldownAccountRetestTask{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	return task, nil
}

func canonicalTask(task port.CooldownAccountRetestTask) port.CooldownAccountRetestTask {
	task.AccountID = strings.TrimSpace(task.AccountID)
	task.Generation = accounthealth.NormalizeCooldownRetestGeneration(task.Generation)
	if task.ObservationStartedAt != nil {
		observation := task.ObservationStartedAt.UTC()
		task.ObservationStartedAt = &observation
	}
	return task
}

func taskFence(task port.CooldownAccountRetestTask) RetestTaskFence {
	return RetestTaskFence{
		AccountID: task.AccountID, ConfigRevision: task.ConfigRevision, DispatchRevision: task.DispatchRevision,
		ObservationStartedAt: task.ObservationStartedAt, Generation: task.Generation,
		SourceConfigRevision: task.SourceConfigRevision,
	}
}

func decodeStrategyHeader(headers map[string]string, name string) (int, error) {
	raw, ok := headers[name]
	if !ok || raw == "" {
		return 0, fmt.Errorf("%w: missing %s header", ErrInvalidPayload, name)
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || strconv.Itoa(value) != raw {
		return 0, fmt.Errorf("%w: invalid %s header", ErrInvalidPayload, name)
	}
	return value, nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON value", ErrInvalidPayload)
		}
		return fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	return nil
}

func UniqueKey(task port.CooldownAccountRetestTask) string {
	observation := ""
	if task.ObservationStartedAt != nil {
		observation = task.ObservationStartedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	sourceRevision := "owner"
	if task.SourceConfigRevision != nil {
		sourceRevision = fmt.Sprintf("%d", *task.SourceConfigRevision)
	}
	raw := fmt.Sprintf("%s|%d|%d|%s|%s|%s", strings.TrimSpace(task.AccountID), task.ConfigRevision,
		task.DispatchRevision, observation, accounthealth.NormalizeCooldownRetestGeneration(task.Generation), sourceRevision)
	sum := sha256.Sum256([]byte(raw))
	return "cooldown-account-retest:" + hex.EncodeToString(sum[:])
}

func validateTask(task port.CooldownAccountRetestTask) error {
	if strings.TrimSpace(task.AccountID) == "" || strings.TrimSpace(task.AccountID) != task.AccountID {
		return errors.New("account id is required")
	}
	if task.ConfigRevision < 1 {
		return errors.New("config revision must be positive")
	}
	if task.DispatchRevision < 1 {
		return errors.New("dispatch revision must be positive")
	}
	if task.ObservationStartedAt == nil || task.ObservationStartedAt.IsZero() {
		return errors.New("observation start is required")
	}
	_, observationOffset := task.ObservationStartedAt.Zone()
	if observationOffset != 0 {
		return errors.New("observation start must use UTC")
	}
	normalizedGeneration := accounthealth.NormalizeCooldownRetestGeneration(task.Generation)
	if normalizedGeneration == "" || normalizedGeneration != task.Generation {
		return errors.New("generation is required")
	}
	if task.SourceConfigRevision != nil && *task.SourceConfigRevision < 1 {
		return errors.New("source config revision must be positive")
	}
	if task.MaxPauseMinutes < 1 || task.MaxPauseMinutes > maxPauseMinutesValue {
		return errors.New("max pause minutes is outside the allowed range")
	}
	if task.MaxRecoveryHours < 1 || task.MaxRecoveryHours > maxRecoveryHoursValue {
		return errors.New("max recovery hours is outside the allowed range")
	}
	return nil
}
