package httpapi

import "sync"

const managementDiagnosticTaskMaxInFlight = 8

type diagnosticTaskLimiter interface {
	TryAcquire() (release func(), acquired bool)
}

type boundedDiagnosticTaskLimiter struct {
	slots chan struct{}
}

var managementDiagnosticTaskLimiter = newDiagnosticTaskLimiter(managementDiagnosticTaskMaxInFlight)

func sharedManagementDiagnosticTaskLimiter() diagnosticTaskLimiter {
	return managementDiagnosticTaskLimiter
}

func newDiagnosticTaskLimiter(maxInFlight int) diagnosticTaskLimiter {
	if maxInFlight <= 0 {
		maxInFlight = 1
	}
	return &boundedDiagnosticTaskLimiter{
		slots: make(chan struct{}, maxInFlight),
	}
}

func (l *boundedDiagnosticTaskLimiter) TryAcquire() (func(), bool) {
	select {
	case l.slots <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-l.slots
			})
		}, true
	default:
		return nil, false
	}
}
