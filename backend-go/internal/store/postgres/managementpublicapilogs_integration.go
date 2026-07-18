//go:build integration

package postgres

import "juhe-ai/backend-go/internal/store/port"

// BuildManagementPublicAPILogListQueryForIntegration exposes the production SQL shape to real PostgreSQL plan checks.
func BuildManagementPublicAPILogListQueryForIntegration(
	input port.ManagementPublicAPILogListInput,
	rowLimit int,
	rowOffset int,
) (string, []any) {
	return managementPublicAPILogListQuery(input, rowLimit, rowOffset)
}
