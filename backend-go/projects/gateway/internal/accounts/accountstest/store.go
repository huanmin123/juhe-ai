package accountstest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// Manual account test task/session persistence: the gateway-facing port of
// storage/account-test-tasks.repository.ts (session lifecycle, task creation
// and the status/cancel reads). The worker-side lifecycle (mark running,
// complete, fail, maintenance) lives in jobs/internal/manualtestrepo over the
// same three tables — gateway and jobs share the tables with a split read/
// write surface, mirroring the accountkeystates precedent. The diagnostics
// execution itself is dispatched through TestDispatchEffects.
//
// SQL and message copy follow the Node repository verbatim; the dual-mode
// dialect differences are the store-wide ones (PostgreSQL qualified
// juhe_business tables + TRUE/FALSE literals, SQLite plain tables + 1/0).

const (
	// testTaskRetentionHours mirrors accountTestTaskRetentionHours.
	testTaskRetentionHours = 24 * time.Hour
	// testSessionIdleCompleteMS mirrors accountTestSessionIdleCompleteMs.
	testSessionIdleCompleteMS = 15 * time.Second
	// testCleanupBatchSize mirrors accountTestCleanupBatchSize.
	testCleanupBatchSize = 200
	// testQueuedMaxWaitMS mirrors runtimeConfig.background.accountTestQueuedMaxWaitMs
	// (Node default 10 minutes; the deadline stamp uses the same default and the
	// jobs-side queue maintenance reads the same env knob for its sweeps).
	testQueuedMaxWaitMS = 10 * 60_000
	// testTaskListMaxIDs mirrors queryTextList(req.query.ids, 200).
	testTaskListMaxIDs = 200
	// testDraftMaxBytes mirrors encryptedDraftAccount (plaintext JSON budget).
	testDraftMaxBytes = 64 * 1024
)

// Session statuses (accountTestSessionStatus normalization: unknown → expired).
const (
	TestSessionRunning   = "running"
	TestSessionCompleted = "completed"
	TestSessionCanceled  = "canceled"
	TestSessionExpired   = "expired"
)

// Task statuses (accountTestTaskStatus normalization: unknown → failed).
const (
	TestTaskQueued   = "queued"
	TestTaskRunning  = "running"
	TestTaskSuccess  = "success"
	TestTaskFailed   = "failed"
	TestTaskCanceled = "canceled"
)

// AccountTestSession mirrors AccountTestSession.
type AccountTestSession struct {
	ID                string  `json:"id"`
	Status            string  `json:"status"`
	Message           *string `json:"message,omitempty"`
	LastHeartbeatAt   string  `json:"lastHeartbeatAt"`
	CancelRequestedAt *string `json:"cancelRequestedAt,omitempty"`
	FinishedAt        *string `json:"finishedAt,omitempty"`
	CreatedAt         string  `json:"createdAt"`
	UpdatedAt         string  `json:"updatedAt"`
}

// AccountTestResult is the pass-through AccountTestResult payload the worker
// stores in result_json. The gateway validates the envelope (object with
// accountId + message strings — accountTestResult) and round-trips the raw
// JSON verbatim.
type AccountTestResult json.RawMessage

// MarshalJSON renders the validated payload verbatim.
func (r AccountTestResult) MarshalJSON() ([]byte, error) { return json.RawMessage(r).MarshalJSON() }

// UnmarshalJSON validates the envelope; anything else is rejected the way
// Node renders the shape undefined.
func (r *AccountTestResult) UnmarshalJSON(raw []byte) error {
	var probe struct {
		AccountID *string `json:"accountId"`
		Message   *string `json:"message"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return errors.New("账户测试结果无效")
	}
	if probe.AccountID == nil || probe.Message == nil {
		return errors.New("账户测试结果无效")
	}
	*r = append((*r)[0:0], raw...)
	return nil
}

// AccountTestTask mirrors AccountTestTask.
type AccountTestTask struct {
	ID                        string            `json:"id"`
	SessionID                 *string           `json:"sessionId,omitempty"`
	AccountID                 string            `json:"accountId"`
	AccountName               string            `json:"accountName"`
	ProviderCode              string            `json:"providerCode"`
	ProviderProtocolProfileID string            `json:"providerProtocolProfileId"`
	ProtocolCode              string            `json:"protocolCode"`
	ProtocolVersion           string            `json:"protocolVersion"`
	Type                      string            `json:"type"`
	Status                    string            `json:"status"`
	Message                   *string           `json:"message,omitempty"`
	Model                     *string           `json:"model,omitempty"`
	TestEndpointMode          *string           `json:"testEndpointMode,omitempty"`
	Result                    AccountTestResult `json:"result,omitempty"`
	CancelRequested           bool              `json:"cancelRequested"`
	CreatedAt                 string            `json:"createdAt"`
	QueuedAt                  string            `json:"queuedAt"`
	QueuedDeadlineAt          string            `json:"queuedDeadlineAt"`
	StartedAt                 *string           `json:"startedAt,omitempty"`
	FinishedAt                *string           `json:"finishedAt,omitempty"`
	UpdatedAt                 string            `json:"updatedAt"`
}

// TestDraftSnapshot mirrors AccountTestDraftSnapshot (the encrypted
// draft_account_encrypted payload; the jobs-side executor decrypts it with
// the shared secret).
type TestDraftSnapshot struct {
	ID                        string                      `json:"id"`
	StateTargetAccountID      *string                     `json:"stateTargetAccountId,omitempty"`
	OwnerSystemAccountID      string                      `json:"ownerSystemAccountId"`
	GroupID                   string                      `json:"groupId"`
	GroupName                 *string                     `json:"groupName,omitempty"`
	ProviderCode              string                      `json:"providerCode"`
	ProviderProtocolProfileID *string                     `json:"providerProtocolProfileId,omitempty"`
	ProtocolCode              *string                     `json:"protocolCode,omitempty"`
	ProtocolVersion           *string                     `json:"protocolVersion,omitempty"`
	Name                      string                      `json:"name"`
	Type                      string                      `json:"type"`
	Credentials               map[string]any              `json:"credentials"`
	ConcurrencyLimit          int                         `json:"concurrencyLimit"`
	Priority                  int                         `json:"priority"`
	SuperPriorityEnabled      bool                        `json:"superPriorityEnabled"`
	FallbackEnabled           bool                        `json:"fallbackEnabled"`
	ClientCompatibility       string                      `json:"clientCompatibility"`
	SupportedModels           []string                    `json:"supportedModels,omitempty"`
	HealthCheckModel          string                      `json:"healthCheckModel"`
	HealthCheckEndpointMode   string                      `json:"healthCheckEndpointMode"`
	ModelMappings             []accountscore.ModelMapping `json:"modelMappings,omitempty"`
	ProxyProfileID            *string                     `json:"proxyProfileId,omitempty"`
	AccountExpiresAt          *string                     `json:"accountExpiresAt,omitempty"`
	AvailabilitySchedule      map[string]any              `json:"availabilitySchedule,omitempty"`
	AvailabilityScheduleJSON  *string                     `json:"availabilityScheduleJson,omitempty"`
	Notes                     *string                     `json:"notes,omitempty"`
}

// TestTaskCreateInput mirrors CreateAccountTestTaskInput.
type TestTaskCreateInput struct {
	AccountID                 string
	AccountName               string
	ProviderCode              string
	ProviderProtocolProfileID string
	ProtocolCode              string
	ProtocolVersion           string
	AccountType               string
	Access                    accountscore.AccessScope
	Diagnostics               string // full | limited
	SessionID                 string
	Model                     string
	TestEndpointMode          string
	Draft                     *TestDraftSnapshot
}

// TestSessionCancel mirrors the cancelAccountTestSession return shape.
type TestSessionCancel struct {
	Session AccountTestSession
	TaskIDs []string
}

// ---- task rows ----

// TestTaskRow mirrors the account_test_tasks join row scan target
// （原根包私有类型 testTaskRow，字段随子域化导出）.
type TestTaskRow struct {
	ID                         string
	SessionID                  sql.NullString
	AccountID                  string
	AccountName                string
	ProviderCode               string
	ProviderProfileID          string
	ProtocolCode               string
	ProtocolVersion            string
	AccountType                string
	RequestSystemAccountID     string
	RequestRole                string
	RequestSystemAccountFilter sql.NullString
	Status                     string
	StatusMessage              sql.NullString
	Model                      sql.NullString
	TestEndpointMode           sql.NullString
	ResultJSON                 sql.NullString
	CancelRequested            bool
	CreatedAt                  string
	QueuedAt                   string
	QueuedDeadlineAt           sql.NullString
	StartedAt                  sql.NullString
	FinishedAt                 sql.NullString
	UpdatedAt                  string
	ErrorMessage               sql.NullString
}

const testTaskSelectColumns = `t.id, st.session_id, t.account_id, t.account_name, t.provider_code,
	t.provider_protocol_profile_id, t.protocol_code, t.protocol_version, t.account_type,
	t.request_system_account_id, t.request_role, t.request_system_account_filter_id,
	t.status, t.status_message, t.model, t.test_endpoint_mode, t.result_json,
	t.cancel_requested, t.created_at, t.queued_at, t.queued_deadline_at, t.started_at,
	t.finished_at, t.updated_at, t.error_message`

func (s *Service) scanTestTaskRow(scan func(...any) error) (*TestTaskRow, error) {
	var row TestTaskRow
	if err := scan(&row.ID, &row.SessionID, &row.AccountID, &row.AccountName, &row.ProviderCode,
		&row.ProviderProfileID, &row.ProtocolCode, &row.ProtocolVersion, &row.AccountType,
		&row.RequestSystemAccountID, &row.RequestRole, &row.RequestSystemAccountFilter,
		&row.Status, &row.StatusMessage, &row.Model, &row.TestEndpointMode, &row.ResultJSON,
		&row.CancelRequested, &row.CreatedAt, &row.QueuedAt, &row.QueuedDeadlineAt, &row.StartedAt,
		&row.FinishedAt, &row.UpdatedAt, &row.ErrorMessage); err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *TestTaskRow) ToTask() *AccountTestTask {
	queuedAt := r.QueuedAt
	deadline := ""
	if r.QueuedDeadlineAt.Valid && r.QueuedDeadlineAt.String != "" {
		deadline = r.QueuedDeadlineAt.String
	} else {
		deadline = TestQueuedDeadlineAt(queuedAt)
	}
	task := &AccountTestTask{
		ID:                        r.ID,
		SessionID:                 accountscore.NullPtrString(r.SessionID),
		AccountID:                 r.AccountID,
		AccountName:               r.AccountName,
		ProviderCode:              r.ProviderCode,
		ProviderProtocolProfileID: r.ProviderProfileID,
		ProtocolCode:              r.ProtocolCode,
		ProtocolVersion:           r.ProtocolVersion,
		Type:                      r.AccountType,
		Status:                    TestTaskStatus(r.Status),
		Model:                     accountscore.NullPtrString(r.Model),
		TestEndpointMode:          TestEndpointModeOrNull(r.TestEndpointMode),
		CancelRequested:           r.CancelRequested,
		CreatedAt:                 r.CreatedAt,
		QueuedAt:                  queuedAt,
		QueuedDeadlineAt:          deadline,
		StartedAt:                 TestOptionalTimestamp(r.StartedAt),
		FinishedAt:                TestOptionalTimestamp(r.FinishedAt),
		UpdatedAt:                 r.UpdatedAt,
	}
	if r.StatusMessage.Valid && r.StatusMessage.String != "" {
		message := r.StatusMessage.String
		task.Message = &message
	} else if r.ErrorMessage.Valid && r.ErrorMessage.String != "" {
		message := r.ErrorMessage.String
		task.Message = &message
	}
	if r.ResultJSON.Valid && r.ResultJSON.String != "" {
		var result AccountTestResult
		if err := json.Unmarshal([]byte(r.ResultJSON.String), &result); err == nil {
			task.Result = result
		}
	}
	return task
}

// testQueuedDeadlineAt mirrors accountTestTaskQueuedDeadlineAt (queued_at +
// accountTestQueuedMaxWaitMs).
func TestQueuedDeadlineAt(queuedAt string) string {
	parsed, err := time.Parse(time.RFC3339Nano, queuedAt)
	if err != nil {
		return queuedAt
	}
	return accountscore.IsoMillis(parsed.Add(testQueuedMaxWaitMS * time.Millisecond))
}

func TestTaskStatus(value string) string {
	switch value {
	case TestTaskQueued, TestTaskRunning, TestTaskSuccess, TestTaskFailed, TestTaskCanceled:
		return value
	}
	return TestTaskFailed
}

func TestSessionStatus(value string) string {
	switch value {
	case TestSessionRunning, TestSessionCanceled, TestSessionExpired, TestSessionCompleted:
		return value
	}
	return TestSessionExpired
}

// testEndpointModeOrNull mirrors accountTestEndpointMode.
func TestEndpointModeOrNull(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	switch value.String {
	case "chat_json", "chat_sse", "responses_json", "responses_sse",
		"messages_json", "messages_sse", "message_token_counting",
		"generate_content_json", "generate_content_sse", "count_tokens", "embed_content":
		mode := value.String
		return &mode
	}
	return nil
}

func TestOptionalTimestamp(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	text := value.String
	return &text
}

func TestTrimmedOrNull(value string) any {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil
	}
	return text
}

// ---- access checks (canReadAccountTestTask / canReadAccountTestSession) ----

func CanReadTestTask(requestSystemAccountID string, requestFilterID sql.NullString, access *accountscore.AccessScope) bool {
	if access == nil {
		return true
	}
	if requestSystemAccountID != access.ViewerID {
		return false
	}
	return strings.TrimSpace(requestFilterID.String) == strings.TrimSpace(access.FilterID)
}

func CanReadTestSession(requestSystemAccountID string, requestFilterID sql.NullString, access *accountscore.AccessScope) bool {
	if access == nil {
		return true
	}
	if requestSystemAccountID != access.ViewerID {
		return false
	}
	rowFilter := strings.TrimSpace(requestFilterID.String)
	return rowFilter == "" || rowFilter == strings.TrimSpace(access.FilterID)
}

// ---- sessions ----

// testRequestRole renders the request_role stamp from the Go scope (the route
// layer knows the concrete role; the store stamps the effective role split).
func TestRequestRole(access accountscore.AccessScope) string {
	if access.IsAdmin {
		return "admin"
	}
	return "user"
}

// CreateTestSession mirrors createAccountTestSessionAsync.
func (s *Service) CreateTestSession(ctx context.Context, access accountscore.AccessScope) (*AccountTestSession, error) {
	if access.ViewerID == "" {
		return nil, &accountscore.ValidationError{Message: "缺少系统账户上下文"}
	}
	ctx = accountscore.EnsureCtx(ctx)
	now := accountscore.IsoMillis(s.store.Now())
	id := s.store.NewID("acctsess")
	if _, err := s.store.DB().ExecContext(ctx, s.store.Bind(`INSERT INTO `+s.store.Table("account_test_sessions")+`
		(id, request_system_account_id, request_role, request_system_account_filter_id,
		 status, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'running', ?, ?, ?)`),
		id, access.ViewerID, TestRequestRole(access), TestTrimmedOrNull(access.FilterID),
		now, now, now); err != nil {
		return nil, err
	}
	session, err := s.GetTestSession(ctx, id, &access)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("账户测试会话创建失败")
	}
	return session, nil
}

// TestSessionRow mirrors the account_test_sessions row scan target
// （原根包私有类型 testSessionRow，字段随子域化导出）.
type TestSessionRow struct {
	ID                string
	RequestAccountID  string
	RequestRole       string
	RequestFilterID   sql.NullString
	Status            string
	CancelReason      sql.NullString
	LastHeartbeatAt   string
	CancelRequestedAt sql.NullString
	FinishedAt        sql.NullString
	CreatedAt         string
	UpdatedAt         string
}

func (s *Service) scanTestSessionRow(scan func(...any) error) (*TestSessionRow, error) {
	var row TestSessionRow
	if err := scan(&row.ID, &row.RequestAccountID, &row.RequestRole, &row.RequestFilterID,
		&row.Status, &row.CancelReason, &row.LastHeartbeatAt, &row.CancelRequestedAt,
		&row.FinishedAt, &row.CreatedAt, &row.UpdatedAt); err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *TestSessionRow) toSession() *AccountTestSession {
	session := &AccountTestSession{
		ID:              r.ID,
		Status:          TestSessionStatus(r.Status),
		LastHeartbeatAt: r.LastHeartbeatAt,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
	if r.CancelReason.Valid && r.CancelReason.String != "" {
		message := r.CancelReason.String
		session.Message = &message
	}
	if r.CancelRequestedAt.Valid && r.CancelRequestedAt.String != "" {
		value := r.CancelRequestedAt.String
		session.CancelRequestedAt = &value
	}
	if r.FinishedAt.Valid && r.FinishedAt.String != "" {
		value := r.FinishedAt.String
		session.FinishedAt = &value
	}
	return session
}

const testSessionSelectColumns = `id, request_system_account_id, request_role,
	request_system_account_filter_id, status, cancel_reason, last_heartbeat_at,
	cancel_requested_at, finished_at, created_at, updated_at`

func (s *Service) getTestSessionRow(ctx context.Context, q accountscore.Queryer, id string) (*TestSessionRow, error) {
	normalized := strings.TrimSpace(id)
	if normalized == "" {
		return nil, nil
	}
	row, err := s.scanTestSessionRow(func(target ...any) error {
		return q.QueryRowContext(ctx, s.store.Bind(`SELECT `+testSessionSelectColumns+`
			FROM `+s.store.Table("account_test_sessions")+` WHERE id = ? LIMIT 1`), normalized).Scan(target...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

// GetTestSession mirrors getAccountTestSessionAsync.
func (s *Service) GetTestSession(ctx context.Context, sessionID string, access *accountscore.AccessScope) (*AccountTestSession, error) {
	ctx = accountscore.EnsureCtx(ctx)
	row, err := s.getTestSessionRow(ctx, s.store.DB(), sessionID)
	if err != nil || row == nil {
		return nil, err
	}
	if !CanReadTestSession(row.RequestAccountID, row.RequestFilterID, access) {
		return nil, nil
	}
	return row.toSession(), nil
}

// GetTestSessionDetail mirrors getAccountTestSessionDetailAsync (session +
// ordered tasks in one call for GET /test-sessions/{id}/tasks).
func (s *Service) GetTestSessionDetail(ctx context.Context, sessionID string, access *accountscore.AccessScope) (*AccountTestSession, []AccountTestTask, error) {
	ctx = accountscore.EnsureCtx(ctx)
	row, err := s.getTestSessionRow(ctx, s.store.DB(), sessionID)
	if err != nil || row == nil {
		return nil, nil, err
	}
	if !CanReadTestSession(row.RequestAccountID, row.RequestFilterID, access) {
		return nil, nil, nil
	}
	tasks, err := s.ListSessionTasks(ctx, s.store.DB(), row.ID, access)
	if err != nil {
		return nil, nil, err
	}
	return row.toSession(), tasks, nil
}

// ListSessionTasks mirrors the ordered per-session task read
// （原根包私有方法 listSessionTasks，根包测试经由 Store 转发访问）.
func (s *Service) ListSessionTasks(ctx context.Context, q accountscore.Queryer, sessionID string, access *accountscore.AccessScope) ([]AccountTestTask, error) {
	rows, err := q.QueryContext(ctx, s.store.Bind(`SELECT `+testTaskSelectColumns+`
		FROM `+s.store.Table("account_test_session_tasks")+` st
		JOIN `+s.store.Table("account_test_tasks")+` t ON t.id = st.task_id
		WHERE st.session_id = ?
		ORDER BY t.queued_at ASC, t.id ASC`), sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []AccountTestTask{}
	for rows.Next() {
		scanned, scanErr := s.scanTestTaskRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		if CanReadTestTask(scanned.RequestSystemAccountID, scanned.RequestSystemAccountFilter, access) {
			tasks = append(tasks, *scanned.ToTask())
		}
	}
	return tasks, rows.Err()
}

// HeartbeatTestSession mirrors heartbeatAccountTestSessionAsync.
func (s *Service) HeartbeatTestSession(ctx context.Context, sessionID string, access *accountscore.AccessScope) (*AccountTestSession, error) {
	ctx = accountscore.EnsureCtx(ctx)
	row, err := s.getTestSessionRow(ctx, s.store.DB(), sessionID)
	if err != nil || row == nil {
		return nil, err
	}
	if !CanReadTestSession(row.RequestAccountID, row.RequestFilterID, access) {
		return nil, nil
	}
	if row.Status == TestSessionRunning {
		now := accountscore.IsoMillis(s.store.Now())
		if _, err := s.store.DB().ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("account_test_sessions")+`
			SET last_heartbeat_at = ?, updated_at = ?
			WHERE id = ? AND status = 'running'`), now, now, row.ID); err != nil {
			return nil, err
		}
	}
	return s.GetTestSession(ctx, row.ID, access)
}

// CompleteTestSession mirrors completeAccountTestSessionAsync (settle-only:
// completed when no queued/running task remains).
func (s *Service) CompleteTestSession(ctx context.Context, sessionID string, access *accountscore.AccessScope) (*AccountTestSession, error) {
	ctx = accountscore.EnsureCtx(ctx)
	row, err := s.getTestSessionRow(ctx, s.store.DB(), sessionID)
	if err != nil || row == nil {
		return nil, err
	}
	if !CanReadTestSession(row.RequestAccountID, row.RequestFilterID, access) {
		return nil, nil
	}
	if err := s.completeTestSessionIfSettled(ctx, s.store.DB(), row.ID); err != nil {
		return nil, err
	}
	return s.GetTestSession(ctx, row.ID, access)
}

func (s *Service) completeTestSessionIfSettled(ctx context.Context, q accountscore.Queryer, sessionID string) error {
	now := accountscore.IsoMillis(s.store.Now())
	_, err := q.ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("account_test_sessions")+`
		SET status = 'completed', finished_at = COALESCE(finished_at, ?), updated_at = ?
		WHERE id = ? AND status = 'running'
		AND NOT EXISTS (
			SELECT 1
			FROM `+s.store.Table("account_test_session_tasks")+` st
			JOIN `+s.store.Table("account_test_tasks")+` t ON t.id = st.task_id
			WHERE st.session_id = ? AND t.status IN ('queued', 'running')
		)`), now, now, sessionID, sessionID)
	return err
}

// CancelTestSession mirrors cancelAccountTestSessionAsync: running → canceled,
// queued tasks cancel immediately, running tasks flag cancel_requested and
// their ids ride back to the route for the worker cancel dispatch.
func (s *Service) CancelTestSession(ctx context.Context, sessionID string, access *accountscore.AccessScope, message string) (*TestSessionCancel, error) {
	ctx = accountscore.EnsureCtx(ctx)
	row, err := s.getTestSessionRow(ctx, s.store.DB(), sessionID)
	if err != nil || row == nil {
		return nil, err
	}
	if !CanReadTestSession(row.RequestAccountID, row.RequestFilterID, access) {
		return nil, nil
	}
	taskIDs, err := s.cancelTestSessionByRow(ctx, s.store.DB(), row, message)
	if err != nil {
		return nil, err
	}
	session, err := s.GetTestSession(ctx, row.ID, access)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, nil
	}
	return &TestSessionCancel{Session: *session, TaskIDs: taskIDs}, nil
}

func (s *Service) cancelTestSessionByRow(ctx context.Context, q accountscore.Queryer, row *TestSessionRow, message string) ([]string, error) {
	if strings.TrimSpace(message) == "" {
		message = "已停止测试"
	}
	now := accountscore.IsoMillis(s.store.Now())
	// Task ids to cancel-signal on the worker (queued + running, pre-update).
	taskRows, err := q.QueryContext(ctx, s.store.Bind(`SELECT t.id, t.status
		FROM `+s.store.Table("account_test_session_tasks")+` st
		JOIN `+s.store.Table("account_test_tasks")+` t ON t.id = st.task_id
		WHERE st.session_id = ? AND t.status IN ('queued', 'running')
		ORDER BY t.queued_at ASC, t.id ASC`), row.ID)
	if err != nil {
		return nil, err
	}
	taskIDs := []string{}
	for taskRows.Next() {
		var id, status string
		if err := taskRows.Scan(&id, &status); err != nil {
			taskRows.Close()
			return nil, err
		}
		taskIDs = append(taskIDs, id)
	}
	taskRows.Close()
	if err := taskRows.Err(); err != nil {
		return nil, err
	}

	status := TestSessionCanceled
	if row.Status != TestSessionRunning {
		status = TestSessionStatus(row.Status)
	}
	if _, err := q.ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("account_test_sessions")+`
		SET status = ?, cancel_reason = ?, cancel_requested_at = COALESCE(cancel_requested_at, ?),
		    finished_at = COALESCE(finished_at, ?), updated_at = ?
		WHERE id = ? AND status = 'running'`),
		status, message, now, now, now, row.ID); err != nil {
		return nil, err
	}
	if _, err := q.ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("account_test_tasks")+`
		SET status = 'canceled', status_message = ?, cancel_requested = `+s.store.BoolTrueLiteral()+`,
		    finished_at = COALESCE(finished_at, ?), updated_at = ?
		WHERE id IN (
			SELECT task_id FROM `+s.store.Table("account_test_session_tasks")+` WHERE session_id = ?
		) AND status = 'queued'`), message, now, now, row.ID); err != nil {
		return nil, err
	}
	if _, err := q.ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("account_test_tasks")+`
		SET cancel_requested = `+s.store.BoolTrueLiteral()+`, status_message = ?, updated_at = ?
		WHERE id IN (
			SELECT task_id FROM `+s.store.Table("account_test_session_tasks")+` WHERE session_id = ?
		) AND status = 'running'`), message, now, row.ID); err != nil {
		return nil, err
	}
	return taskIDs, nil
}

// ---- tasks ----

// CreateTestTask mirrors createAccountTestTaskAsync (cleanup → session assert
// → insert task (+ session link) → read back).
func (s *Service) CreateTestTask(ctx context.Context, input TestTaskCreateInput) (*AccountTestTask, error) {
	ctx = accountscore.EnsureCtx(ctx)
	if err := s.CleanupExpiredTestTasks(ctx); err != nil {
		return nil, err
	}
	now := accountscore.IsoMillis(s.store.Now())
	deadline := TestQueuedDeadlineAt(now)
	id := s.store.NewID("accttest")
	sessionID := strings.TrimSpace(input.SessionID)

	draftEncrypted := sql.NullString{}
	if input.Draft != nil {
		plaintext, marshalErr := json.Marshal(input.Draft)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if len(plaintext) > testDraftMaxBytes {
			return nil, &accountscore.ValidationError{Message: "账户测试草稿过大"}
		}
		sealed, encErr := accountscore.EncryptJSON(s.store.Secret(), input.Draft)
		if encErr != nil {
			return nil, encErr
		}
		draftEncrypted = sql.NullString{String: sealed, Valid: true}
	}

	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if sessionID != "" {
		if err := s.assertUsableTestSession(ctx, tx, sessionID, &input.Access); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, s.store.Bind(`INSERT INTO `+s.store.Table("account_test_tasks")+`
		(id, account_id, account_name, provider_code, provider_protocol_profile_id,
		 protocol_code, protocol_version, account_type,
		 request_system_account_id, request_role, request_system_account_filter_id,
		 diagnostics, model, test_endpoint_mode, draft_account_encrypted, status, status_message,
		 cancel_requested, queued_at, queued_deadline_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', '等待后台测试', `+s.store.BoolFalseLiteral()+`, ?, ?, ?, ?)`),
		id, input.AccountID, input.AccountName, input.ProviderCode, input.ProviderProtocolProfileID,
		input.ProtocolCode, input.ProtocolVersion, input.AccountType,
		input.Access.ViewerID, TestRequestRole(input.Access), TestTrimmedOrNull(input.Access.FilterID),
		input.Diagnostics, TestTrimmedOrNull(input.Model), TestTrimmedOrNull(input.TestEndpointMode),
		draftEncrypted, now, deadline, now, now); err != nil {
		return nil, err
	}
	if sessionID != "" {
		if _, err := tx.ExecContext(ctx, s.store.Bind(`INSERT INTO `+s.store.Table("account_test_session_tasks")+`
			(session_id, task_id, created_at) VALUES (?, ?, ?)`), sessionID, id, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	task, err := s.GetTestTask(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, errors.New("账户测试任务创建失败")
	}
	return task, nil
}

// assertUsableTestSession mirrors assertUsableAccountTestSession: readable,
// no cancel reason and at most one task per session.
func (s *Service) assertUsableTestSession(ctx context.Context, q accountscore.Queryer, sessionID string, access *accountscore.AccessScope) error {
	row, err := s.getTestSessionRow(ctx, q, sessionID)
	if err != nil {
		return err
	}
	if row == nil || !CanReadTestSession(row.RequestAccountID, row.RequestFilterID, access) {
		return &accountscore.ValidationError{Message: "账户测试会话不存在"}
	}
	if reason := SessionCancelReason(row); reason != "" {
		status := TestSessionStatus(row.Status)
		if row.Status == TestSessionRunning {
			status = TestSessionExpired
		}
		now := accountscore.IsoMillis(s.store.Now())
		if _, err := q.ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("account_test_sessions")+`
			SET status = ?, cancel_reason = ?, cancel_requested_at = COALESCE(cancel_requested_at, ?),
			    finished_at = COALESCE(finished_at, ?), updated_at = ?
			WHERE id = ? AND status = 'running'`),
			status, reason, now, now, now, row.ID); err != nil {
			return err
		}
		return &accountscore.ValidationError{Message: reason}
	}
	var taskID sql.NullString
	if err := q.QueryRowContext(ctx, s.store.Bind(`SELECT task_id
		FROM `+s.store.Table("account_test_session_tasks")+` WHERE session_id = ? LIMIT 1`), row.ID).Scan(&taskID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if taskID.Valid && taskID.String != "" {
		return &accountscore.ValidationError{Message: "账户测试会话只能包含一个账户任务"}
	}
	return nil
}

// SessionCancelReason mirrors accountTestSessionCancelReason
// （原根包私有函数 testSessionCancelReason，根包测试经由转发访问）.
func SessionCancelReason(row *TestSessionRow) string {
	switch row.Status {
	case TestSessionCanceled:
		if row.CancelReason.Valid && row.CancelReason.String != "" {
			return row.CancelReason.String
		}
		return "已停止测试"
	case TestSessionExpired:
		if row.CancelReason.Valid && row.CancelReason.String != "" {
			return row.CancelReason.String
		}
		return "账户测试会话已过期"
	}
	if row.Status != TestSessionRunning {
		if row.CancelReason.Valid && row.CancelReason.String != "" {
			return row.CancelReason.String
		}
		return "账户测试会话已结束"
	}
	return ""
}

// testTaskRowSelect 必须经方法构造：表名要走 Table() 方言前缀（PG 为
// juhe_business.*），const 表达式无法调用方法曾导致裸表名在 PG 42P01
// （2026-09-25 生产/测试「测试账号连接」500 根因）。
func (s *Service) testTaskRowSelect() string {
	return `SELECT ` + testTaskSelectColumns + `
	FROM ` + s.store.Table("account_test_tasks") + ` t
	LEFT JOIN ` + s.store.Table("account_test_session_tasks") + ` st ON st.task_id = t.id
	WHERE t.id = ? LIMIT 1`
}

func (s *Service) getTestTaskRow(ctx context.Context, q accountscore.Queryer, taskID string) (*TestTaskRow, error) {
	normalized := strings.TrimSpace(taskID)
	if normalized == "" {
		return nil, nil
	}
	row, err := s.scanTestTaskRow(func(target ...any) error {
		return q.QueryRowContext(ctx, s.store.Bind(s.testTaskRowSelect()), normalized).Scan(target...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

// GetTestTask mirrors getAccountTestTaskAsync.
func (s *Service) GetTestTask(ctx context.Context, taskID string, access *accountscore.AccessScope) (*AccountTestTask, error) {
	ctx = accountscore.EnsureCtx(ctx)
	row, err := s.getTestTaskRow(ctx, s.store.DB(), taskID)
	if err != nil || row == nil {
		return nil, err
	}
	if !CanReadTestTask(row.RequestSystemAccountID, row.RequestSystemAccountFilter, access) {
		return nil, nil
	}
	return row.ToTask(), nil
}

// ListTestTasks mirrors listAccountTestTasksAsync: stable id order, scope
// filtered, unknown ids dropped.
func (s *Service) ListTestTasks(ctx context.Context, ids []string, access *accountscore.AccessScope) ([]AccountTestTask, error) {
	ctx = accountscore.EnsureCtx(ctx)
	seen := map[string]bool{}
	normalized := []string{}
	for _, id := range ids {
		text := strings.TrimSpace(id)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		normalized = append(normalized, text)
		if len(normalized) >= testTaskListMaxIDs {
			break
		}
	}
	if len(normalized) == 0 {
		return []AccountTestTask{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(normalized)), ", ")
	rows, err := s.store.DB().QueryContext(ctx, s.store.Bind(`SELECT `+testTaskSelectColumns+`
		FROM `+s.store.Table("account_test_tasks")+` t
		LEFT JOIN `+s.store.Table("account_test_session_tasks")+` st ON st.task_id = t.id
		WHERE t.id IN (`+placeholders+`)`), accountscore.AnySlice(normalized)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]*AccountTestTask{}
	for rows.Next() {
		scanned, scanErr := s.scanTestTaskRow(rows.Scan)
		if scanErr != nil {
			return nil, scanErr
		}
		if !CanReadTestTask(scanned.RequestSystemAccountID, scanned.RequestSystemAccountFilter, access) {
			continue
		}
		byID[scanned.ID] = scanned.ToTask()
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tasks := []AccountTestTask{}
	for _, id := range normalized {
		if task, ok := byID[id]; ok {
			tasks = append(tasks, *task)
		}
	}
	return tasks, nil
}

// CancelTestTask mirrors cancelAccountTestTaskAsync: queued → canceled
// immediately, running → cancel_requested flag (worker finalizes).
func (s *Service) CancelTestTask(ctx context.Context, taskID string, access *accountscore.AccessScope) (*AccountTestTask, error) {
	ctx = accountscore.EnsureCtx(ctx)
	row, err := s.getTestTaskRow(ctx, s.store.DB(), taskID)
	if err != nil || row == nil {
		return nil, err
	}
	if !CanReadTestTask(row.RequestSystemAccountID, row.RequestSystemAccountFilter, access) {
		return nil, nil
	}
	now := accountscore.IsoMillis(s.store.Now())
	switch row.Status {
	case TestTaskQueued:
		if _, err := s.store.DB().ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("account_test_tasks")+`
			SET status = 'canceled',
			    status_message = CASE
			      WHEN cancel_requested = `+s.store.BoolTrueLiteral()+` AND status_message IS NOT NULL AND TRIM(status_message) != '' THEN status_message
			      ELSE '已停止测试'
			    END,
			    cancel_requested = `+s.store.BoolTrueLiteral()+`,
			    finished_at = COALESCE(finished_at, ?),
			    updated_at = ?
			WHERE id = ? AND status IN ('queued', 'running')`), now, now, row.ID); err != nil {
			return nil, err
		}
	case TestTaskRunning:
		if _, err := s.store.DB().ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("account_test_tasks")+`
			SET cancel_requested = `+s.store.BoolTrueLiteral()+`, status_message = '正在停止测试', updated_at = ?
			WHERE id = ? AND status = 'running'`), now, row.ID); err != nil {
			return nil, err
		}
	}
	return s.GetTestTask(ctx, row.ID, access)
}

// FailTestTask mirrors failAccountTestTaskAsync for the route dispatch
// failure path (queued|running → failed with the message copy).
func (s *Service) FailTestTask(ctx context.Context, taskID string, message string) error {
	ctx = accountscore.EnsureCtx(ctx)
	normalized := strings.TrimSpace(taskID)
	if normalized == "" {
		return nil
	}
	now := accountscore.IsoMillis(s.store.Now())
	_, err := s.store.DB().ExecContext(ctx, s.store.Bind(`UPDATE `+s.store.Table("account_test_tasks")+`
		SET status = 'failed', status_message = ?, error_message = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('queued', 'running') AND cancel_requested = `+s.store.BoolFalseLiteral()),
		message, message, now, now, normalized)
	return err
}

// CleanupExpiredTestTasks mirrors cleanupExpiredAccountTestTasksAsync
// （原根包私有方法 cleanupExpiredTestTasks，根包测试经由 Store 转发访问）.
func (s *Service) CleanupExpiredTestTasks(ctx context.Context) error {
	cutoff := accountscore.IsoMillis(s.store.Now().Add(-testTaskRetentionHours))
	tasks := s.store.Table("account_test_tasks")
	sessions := s.store.Table("account_test_sessions")
	sessionTasks := s.store.Table("account_test_session_tasks")
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.store.Bind(`DELETE FROM `+tasks+`
		WHERE id IN (
			SELECT id FROM `+tasks+`
			WHERE finished_at IS NOT NULL AND finished_at < ?
			ORDER BY finished_at ASC, id ASC
			LIMIT ?
		)`), cutoff, testCleanupBatchSize); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.store.Bind(`DELETE FROM `+sessions+`
		WHERE id IN (
			SELECT s.id FROM `+sessions+` s
			WHERE s.updated_at < ?
				AND NOT EXISTS (
					SELECT 1
					FROM `+sessionTasks+` st
					JOIN `+tasks+` t ON t.id = st.task_id
					WHERE st.session_id = s.id AND t.status IN ('queued', 'running')
				)
			ORDER BY s.updated_at ASC, s.id ASC
			LIMIT ?
		)`), cutoff, testCleanupBatchSize); err != nil {
		return err
	}
	return tx.Commit()
}
