package port

import "context"

type ManagementAccountTestStatusReader interface {
	GetManagementAccountTestSession(context.Context, string, ManagementAccountTestAccess) (ManagementAccountTestSession, bool, error)
	ListManagementAccountTestSessionTasks(context.Context, string, ManagementAccountTestAccess, int) ([]ManagementAccountTestTask, bool, error)
	GetManagementAccountTestTask(context.Context, string, ManagementAccountTestAccess) (ManagementAccountTestTask, bool, error)
	ListManagementAccountTestTasks(context.Context, []string, ManagementAccountTestAccess) ([]ManagementAccountTestTask, error)
}
