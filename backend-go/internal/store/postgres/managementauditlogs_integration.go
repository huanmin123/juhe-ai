//go:build integration

package postgres

import "juhe-ai/backend-go/internal/store/port"

// BuildManagementAuditErrorGroupListQueryForIntegration exposes the production error-group SQL shape to integration checks.
func BuildManagementAuditErrorGroupListQueryForIntegration(
	input port.ManagementAuditErrorGroupListInput,
	rowLimit int,
	rowOffset int,
) (string, []any) {
	return managementAuditErrorGroupListQuery(input, rowLimit, rowOffset)
}

// BuildManagementAuditLogListQueryForIntegration exposes the production audit-log SQL shape to integration checks.
func BuildManagementAuditLogListQueryForIntegration(
	input port.ManagementAuditLogListInput,
	rowLimit int,
	rowOffset int,
) (string, []any) {
	return managementAuditLogListQuery(input, rowLimit, rowOffset)
}
