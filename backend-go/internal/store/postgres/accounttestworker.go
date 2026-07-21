package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"juhe-ai/backend-go/internal/store/port"
)

const claimAccountTestTaskSQL = `
UPDATE juhe_business.account_test_tasks SET status='running', started_at=COALESCE(started_at,now()), updated_at=now()
WHERE id=$1 AND status='queued' AND cancel_requested=false
RETURNING id, '', account_id, account_name, provider_code, COALESCE(provider_protocol_profile_id,''), COALESCE(protocol_code,''),
 COALESCE(protocol_version,''), account_type, status, COALESCE(status_message,''), COALESCE(model,''), COALESCE(test_endpoint_mode,''),
 COALESCE(result_json,''), cancel_requested, created_at, queued_at, started_at, finished_at, updated_at`

const finishAccountTestTaskSQL = `
UPDATE juhe_business.account_test_tasks SET status=$2, status_message=$3, result_json=NULLIF($4,''),
 finished_at=COALESCE(finished_at,now()), updated_at=now()
WHERE id=$1 AND status='running'`

func (s *Store) ClaimAccountTestTask(ctx context.Context, id string) (port.ManagementAccountTestTask, bool, error) {
	var row taskRow
	err := s.pool.QueryRow(ctx, claimAccountTestTaskSQL, strings.TrimSpace(id)).Scan(taskArgs(&row)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementAccountTestTask{}, false, nil
	}
	if err != nil {
		return port.ManagementAccountTestTask{}, false, fmt.Errorf("claim account test task: %w", err)
	}
	return row.task(), true, nil
}

func (s *Store) FinishAccountTestTask(ctx context.Context, input port.AccountTestWorkerFinishInput) error {
	if input.Status != "success" && input.Status != "failed" && input.Status != "canceled" {
		return fmt.Errorf("invalid account test finish status %q", input.Status)
	}
	resultJSON := ""
	if input.Result != nil {
		payload, err := json.Marshal(input.Result)
		if err != nil {
			return fmt.Errorf("encode account test result: %w", err)
		}
		resultJSON = string(payload)
	}
	tag, err := s.pool.Exec(ctx, finishAccountTestTaskSQL, strings.TrimSpace(input.TaskID), input.Status, strings.TrimSpace(input.Message), resultJSON)
	if err != nil {
		return fmt.Errorf("finish account test task: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("account test task is not running")
	}
	return nil
}

var _ port.AccountTestWorkerStore = (*Store)(nil)
