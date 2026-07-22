package gatewayaudit

import (
	"errors"
	"fmt"
	"math"
	"sync"
)

var ErrCaptureOverflow = errors.New("gateway audit capture overflow")

const (
	MaxResidentBytes          int64 = 64 * 1024 * 1024
	ResidentItemOverheadBytes int64 = 512
)

type CaptureStatus string

const (
	CaptureStatusComplete CaptureStatus = "complete"
	CaptureStatusOverflow CaptureStatus = "overflow"
)

// ResidentItem accounts for an in-memory capture fragment without owning raw
// payload bytes or any persistence behavior.
type ResidentItem struct {
	Kind          string `json:"kind"`
	ExternalBytes int64  `json:"externalBytes"`
}

type Snapshot struct {
	Status           CaptureStatus  `json:"status"`
	ResidentBytes    int64          `json:"residentBytes"`
	MaxResidentBytes int64          `json:"maxResidentBytes"`
	Items            []ResidentItem `json:"items"`
}

type Capture struct {
	mu               sync.RWMutex
	maxResidentBytes int64
	residentBytes    int64
	status           CaptureStatus
	items            []ResidentItem
}

func NewCapture(maxResidentBytes int64) (*Capture, error) {
	if maxResidentBytes <= 0 {
		return nil, fmt.Errorf("max resident bytes must be positive")
	}
	maxResidentBytes = min(maxResidentBytes, MaxResidentBytes)
	return &Capture{
		maxResidentBytes: maxResidentBytes,
		status:           CaptureStatusComplete,
	}, nil
}

func (capture *Capture) Add(item ResidentItem) error {
	if capture == nil {
		return fmt.Errorf("capture is required")
	}
	if item.ExternalBytes < 0 {
		return fmt.Errorf("external bytes must not be negative")
	}

	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.status == CaptureStatusOverflow {
		return ErrCaptureOverflow
	}

	itemBytes := saturatingAdd(ResidentItemOverheadBytes, int64(len(item.Kind)))
	itemBytes = saturatingAdd(itemBytes, item.ExternalBytes)
	next := saturatingAdd(capture.residentBytes, itemBytes)
	if next > capture.maxResidentBytes {
		capture.status = CaptureStatusOverflow
		capture.residentBytes = next
		capture.items = nil
		return ErrCaptureOverflow
	}
	capture.residentBytes = next
	capture.items = append(capture.items, item)
	return nil
}

func (capture *Capture) Snapshot() Snapshot {
	if capture == nil {
		return Snapshot{}
	}
	capture.mu.RLock()
	defer capture.mu.RUnlock()
	items := append([]ResidentItem(nil), capture.items...)
	return Snapshot{
		Status:           capture.status,
		ResidentBytes:    capture.residentBytes,
		MaxResidentBytes: capture.maxResidentBytes,
		Items:            items,
	}
}

func saturatingAdd(left, right int64) int64 {
	if right > math.MaxInt64-left {
		return math.MaxInt64
	}
	return left + right
}
