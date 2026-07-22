package managementopenaioauth

import (
	"errors"
	"strings"
	"time"
)

type SessionStatus string

const (
	SessionPending    SessionStatus = "pending"
	SessionProcessing SessionStatus = "processing"
	SessionExchanged  SessionStatus = "exchanged"
	SessionConsumed   SessionStatus = "consumed"
)

type SessionLifecycle struct {
	Status     SessionStatus
	LeaseToken string
	LeaseUntil time.Time
}

type SessionAcquire struct {
	// ResumeExchanged tells the service to continue database persistence with
	// the sealed token result instead of replaying an authorization code.
	ResumeExchanged bool
}

func AcquireSession(current SessionLifecycle, leaseToken string, now time.Time, leaseTTL time.Duration) (SessionLifecycle, SessionAcquire, error) {
	leaseToken = strings.TrimSpace(leaseToken)
	if leaseToken == "" || leaseTTL <= 0 {
		return current, SessionAcquire{}, NewError(ErrorCodeRequestInvalid, errors.New("invalid OAuth session lease arguments"))
	}
	switch current.Status {
	case SessionPending:
		return withLease(current, SessionProcessing, leaseToken, now, leaseTTL), SessionAcquire{}, nil
	case SessionProcessing:
		if current.LeaseToken != "" && current.LeaseUntil.After(now) {
			return current, SessionAcquire{}, NewError(ErrorCodeSessionProcessing, nil)
		}
		return withLease(current, SessionProcessing, leaseToken, now, leaseTTL), SessionAcquire{}, nil
	case SessionExchanged:
		if current.LeaseToken != "" && current.LeaseUntil.After(now) {
			return current, SessionAcquire{}, NewError(ErrorCodeSessionProcessing, nil)
		}
		return withLease(current, SessionExchanged, leaseToken, now, leaseTTL), SessionAcquire{ResumeExchanged: true}, nil
	case SessionConsumed:
		return current, SessionAcquire{}, NewError(ErrorCodeSessionConsumed, nil)
	default:
		return current, SessionAcquire{}, NewError(ErrorCodeRequestInvalid, errors.New("invalid OAuth session lifecycle status"))
	}
}

func MarkSessionExchanged(current SessionLifecycle, leaseToken string, now time.Time) (SessionLifecycle, error) {
	if current.Status != SessionProcessing {
		return current, NewError(ErrorCodeSessionProcessing, errors.New("OAuth session is not processing"))
	}
	if err := requireLease(current, leaseToken, now); err != nil {
		return current, err
	}
	current.Status = SessionExchanged
	return current, nil
}

func ReleaseSessionRetryable(current SessionLifecycle, leaseToken string, now time.Time) (SessionLifecycle, error) {
	if current.Status != SessionProcessing && current.Status != SessionExchanged {
		return current, NewError(ErrorCodeSessionProcessing, errors.New("OAuth session has no releasable lease"))
	}
	if err := requireLease(current, leaseToken, now); err != nil {
		return current, err
	}
	if current.Status == SessionProcessing {
		current.Status = SessionPending
	}
	return clearLease(current), nil
}

func ConsumeSession(current SessionLifecycle, leaseToken string, now time.Time) (SessionLifecycle, error) {
	if current.Status == SessionConsumed {
		return current, NewError(ErrorCodeSessionConsumed, nil)
	}
	if current.Status != SessionProcessing && current.Status != SessionExchanged {
		return current, NewError(ErrorCodeSessionProcessing, errors.New("OAuth session is not leased"))
	}
	if err := requireLease(current, leaseToken, now); err != nil {
		return current, err
	}
	current.Status = SessionConsumed
	return clearLease(current), nil
}

func withLease(current SessionLifecycle, status SessionStatus, leaseToken string, now time.Time, leaseTTL time.Duration) SessionLifecycle {
	current.Status = status
	current.LeaseToken = leaseToken
	current.LeaseUntil = now.Add(leaseTTL).UTC()
	return current
}

func clearLease(current SessionLifecycle) SessionLifecycle {
	current.LeaseToken = ""
	current.LeaseUntil = time.Time{}
	return current
}

func requireLease(current SessionLifecycle, leaseToken string, now time.Time) error {
	if strings.TrimSpace(leaseToken) == "" || current.LeaseToken == "" || current.LeaseToken != leaseToken {
		return NewError(ErrorCodeSessionProcessing, errors.New("OAuth session lease token mismatch"))
	}
	if !current.LeaseUntil.After(now) {
		return NewError(ErrorCodeSessionProcessing, errors.New("OAuth session lease expired"))
	}
	return nil
}
