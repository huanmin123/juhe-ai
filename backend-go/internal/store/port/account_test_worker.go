package port

import "context"

type AccountTestWorkerFinishInput struct {
	TaskID  string
	Status  string
	Message string
	Result  map[string]any
}

type AccountTestWorkerStore interface {
	ClaimAccountTestTask(context.Context, string) (ManagementAccountTestTask, bool, error)
	FinishAccountTestTask(context.Context, AccountTestWorkerFinishInput) error
}
