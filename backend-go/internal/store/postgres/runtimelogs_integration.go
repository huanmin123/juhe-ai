//go:build integration

package postgres

import "juhe-ai/backend-go/internal/store/port"

// BuildManagementRuntimeLogListQueryForIntegration exposes the production SQL shape to real PostgreSQL plan checks.
func BuildManagementRuntimeLogListQueryForIntegration(
	input port.ManagementRuntimeLogListInput,
	rowLimit int,
	rowOffset int,
) (string, []any) {
	return runtimeLogListQuery(input, rowLimit, rowOffset)
}
