package port

import "context"

type ManagementAccountTestSessionStore interface {
	CreateManagementAccountTestSession(context.Context, string, ManagementAccountTestAccess) (ManagementAccountTestSession, error)
	HeartbeatManagementAccountTestSession(context.Context, string, ManagementAccountTestAccess) (ManagementAccountTestSession, bool, error)
	CompleteManagementAccountTestSession(context.Context, string, ManagementAccountTestAccess) (ManagementAccountTestSession, bool, error)
	CancelManagementAccountTestSession(context.Context, string, ManagementAccountTestAccess) (ManagementAccountTestSession, []string, bool, error)
	CancelManagementAccountTestTask(context.Context, string, ManagementAccountTestAccess) (ManagementAccountTestTask, bool, error)
}
