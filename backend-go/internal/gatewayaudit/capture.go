package gatewayaudit

import (
	"errors"
	"fmt"
	"math"
	"sync"
)

var (
	ErrCaptureOverflow  = errors.New("gateway audit capture overflow")
	ErrCaptureFinalized = errors.New("gateway audit capture finalized")
)

const (
	MaxResidentBytes          int64 = 64 * 1024 * 1024
	MinResidentBytes          int64 = 1024
	ResidentItemOverheadBytes int64 = 512
)

type CaptureStatus string

const (
	CaptureStatusComplete CaptureStatus = "complete"
	CaptureStatusOverflow CaptureStatus = "overflow"
)

type Snapshot struct {
	Status            CaptureStatus `json:"status"`
	ResidentBytes     int64         `json:"residentBytes"`
	PeakResidentBytes int64         `json:"peakResidentBytes"`
	MaxResidentBytes  int64         `json:"maxResidentBytes"`
	DTO               CaptureDTO    `json:"dto"`
}

// Capture owns and accounts for every retained DTO field. It never accepts a
// caller-supplied size for data that it stores.
type Capture struct {
	mu                sync.Mutex
	maxResidentBytes  int64
	residentBytes     int64
	peakResidentBytes int64
	status            CaptureStatus
	dto               CaptureDTO
	finalized         bool
}

func NewCapture(maxResidentBytes int64, metadata ...MetadataDTO) (*Capture, error) {
	if maxResidentBytes < MinResidentBytes {
		return nil, fmt.Errorf("max resident bytes must be at least %d", MinResidentBytes)
	}
	if len(metadata) > 1 {
		return nil, fmt.Errorf("at most one metadata value is allowed")
	}
	maxResidentBytes = min(maxResidentBytes, MaxResidentBytes)
	capture := &Capture{
		maxResidentBytes: maxResidentBytes,
		status:           CaptureStatusComplete,
	}
	capture.dto.Terminal = normalizeTerminal(ResolveTerminal(TerminalInput{}))
	if len(metadata) == 1 {
		capture.dto.Metadata = normalizeMetadata(metadata[0])
	}
	capture.residentBytes = estimateMetadataBytes(capture.dto.Metadata) + estimateTerminalBytes(capture.dto.Terminal)
	capture.peakResidentBytes = capture.residentBytes
	if capture.residentBytes > capture.maxResidentBytes {
		capture.markOverflow(capture.residentBytes)
	}
	return capture, nil
}

func (capture *Capture) AddAttempt(attempt AttemptInput) error {
	if capture == nil {
		return fmt.Errorf("capture is required")
	}
	normalized, err := normalizeAttempt(attempt)
	if err != nil {
		return err
	}
	return capture.add(estimateAttemptBytes(normalized), func() {
		capture.dto.Attempts = append(capture.dto.Attempts, normalized)
	})
}

func (capture *Capture) AddFragment(fragment FragmentDescriptorDTO) error {
	if capture == nil {
		return fmt.Errorf("capture is required")
	}
	normalized := normalizeFragment(fragment)
	return capture.add(estimateFragmentBytes(normalized), func() {
		capture.dto.Fragments = append(capture.dto.Fragments, normalized)
	})
}

func (capture *Capture) SetTerminal(input TerminalInput) error {
	if capture == nil {
		return fmt.Errorf("capture is required")
	}
	normalized := normalizeTerminal(ResolveTerminal(input))
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.finalized {
		return ErrCaptureFinalized
	}
	previousBytes := estimateTerminalBytes(capture.dto.Terminal)
	next := saturatingAdd(capture.residentBytes-previousBytes, estimateTerminalBytes(normalized))
	capture.peakResidentBytes = max(capture.peakResidentBytes, next)
	if capture.status == CaptureStatusOverflow {
		capture.dto.Terminal = compactTerminal(normalized)
		capture.residentBytes = estimateMetadataBytes(capture.dto.Metadata) + estimateTerminalBytes(capture.dto.Terminal)
		return ErrCaptureOverflow
	}
	if next > capture.maxResidentBytes {
		capture.dto.Terminal = normalized
		capture.markOverflow(next)
		return ErrCaptureOverflow
	}
	capture.residentBytes = next
	capture.dto.Terminal = normalized
	return nil
}

func (capture *Capture) add(itemBytes int64, appendItem func()) error {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.finalized {
		return ErrCaptureFinalized
	}
	if capture.status == CaptureStatusOverflow {
		return ErrCaptureOverflow
	}
	next := saturatingAdd(capture.residentBytes, itemBytes)
	capture.peakResidentBytes = max(capture.peakResidentBytes, next)
	if next > capture.maxResidentBytes {
		capture.markOverflow(next)
		return ErrCaptureOverflow
	}
	capture.residentBytes = next
	appendItem()
	return nil
}

func (capture *Capture) markOverflow(attemptedBytes int64) {
	capture.status = CaptureStatusOverflow
	capture.peakResidentBytes = max(capture.peakResidentBytes, attemptedBytes)
	capture.dto.Metadata = MetadataDTO{}
	capture.dto.Terminal = compactTerminal(capture.dto.Terminal)
	capture.dto.Attempts = nil
	capture.dto.Fragments = nil
	capture.residentBytes = estimateMetadataBytes(capture.dto.Metadata) + estimateTerminalBytes(capture.dto.Terminal)
}

func compactTerminal(input Terminal) Terminal {
	return Terminal{Outcome: input.Outcome, Success: input.Success}
}

// TakeSnapshot transfers DTO ownership exactly once. The capture releases its
// retained DTO so repeated handoff cannot multiply the bounded resident memory.
func (capture *Capture) TakeSnapshot() (Snapshot, error) {
	if capture == nil {
		return Snapshot{}, fmt.Errorf("capture is required")
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if capture.finalized {
		return Snapshot{}, ErrCaptureFinalized
	}
	capture.finalized = true
	snapshot := Snapshot{
		Status:            capture.status,
		ResidentBytes:     capture.residentBytes,
		PeakResidentBytes: capture.peakResidentBytes,
		MaxResidentBytes:  capture.maxResidentBytes,
		DTO:               capture.dto,
	}
	capture.dto = CaptureDTO{}
	capture.residentBytes = 0
	return snapshot, nil
}

func saturatingAdd(left, right int64) int64 {
	if left < 0 {
		left = 0
	}
	if right < 0 {
		right = 0
	}
	if right > math.MaxInt64-left {
		return math.MaxInt64
	}
	return left + right
}
