package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Manual account test task/session persistence: the gateway-facing port of
// storage/account-test-tasks.repository.ts (session lifecycle, task creation
// and the status/cancel reads). The worker-side lifecycle (mark running,
// complete, fail, maintenance) lives in jobs/internal/manualtestrepo over the
// same three tables — gateway and jobs share the tables with a split read/
// write surface, mirroring the accountkeystates precedent. The diagnostics
// execution itself is dispatched through TestDispatchEffects (test_effects.go).
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
	ID                        string         `json:"id"`
	StateTargetAccountID      *string        `json:"stateTargetAccountId,omitempty"`
	OwnerSystemAccountID      string         `json:"ownerSystemAccountId"`
	GroupID                   string         `json:"groupId"`
	GroupName                 *string        `json:"groupName,omitempty"`
	ProviderCode              string         `json:"providerCode"`
	ProviderProtocolProfileID *string        `json:"providerProtocolProfileId,omitempty"`
	ProtocolCode              *string        `json:"protocolCode,omitempty"`
	ProtocolVersion           *string        `json:"protocolVersion,omitempty"`
	Name                      string         `json:"name"`
	Type                      string         `json:"type"`
	Credentials               map[string]any `json:"credentials"`
	ConcurrencyLimit          int            `json:"concurrencyLimit"`
	Priority                  int            `json:"priority"`
	SuperPriorityEnabled      bool           `json:"superPriorityEnabled"`
	FallbackEnabled           bool           `json:"fallbackEnabled"`
	ClientCompatibility       string         `json:"clientCompatibility"`
	SupportedModels           []string       `json:"supportedModels,omitempty"`
	HealthCheckModel          string         `json:"healthCheckModel"`
	HealthCheckEndpointMode   string         `json:"healthCheckEndpointMode"`
	ModelMappings             []ModelMapping `json:"modelMappings,omitempty"`
	ProxyProfileID            *string        `json:"proxyProfileId,omitempty"`
	AccountExpiresAt          *string        `json:"accountExpiresAt,omitempty"`
	AvailabilitySchedule      map[string]any `json:"availabilitySchedule,omitempty"`
	AvailabilityScheduleJSON  *string        `json:"availabilityScheduleJson,omitempty"`
	Notes                     *string        `json:"notes,omitempty"`
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
	Access                    AccessScope
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

type testTaskRow struct {
	id                         string
	sessionID                  sql.NullString
	accountID                  string
	accountName                string
	providerCode               string
	providerProfileID          string
	protocolCode               string
	protocolVersion            string
	accountType                string
	requestSystemAccountID     string
	requestRole                string
	requestSystemAccountFilter sql.NullString
	status                     string
	statusMessage              sql.NullString
	model                      sql.NullString
	testEndpointMode           sql.NullString
	resultJSON                 sql.NullString
	cancelRequested            bool
	createdAt                  string
	queuedAt                   string
	queuedDeadlineAt           sql.NullString
	startedAt                  sql.NullString
	finishedAt                 sql.NullString
	updatedAt                  string
	errorMessage               sql.NullString
}

const testTaskSelectColumns = `t.id, st.session_id, t.account_id, t.account_name, t.provider_code,
	t.provider_protocol_profile_id, t.protocol_code, t.protocol_version, t.account_type,
	t.request_system_account_id, t.request_role, t.request_system_account_filter_id,
	t.status, t.status_message, t.model, t.test_endpoint_mode, t.result_json,
	t.cancel_requested, t.created_at, t.queued_at, t.queued_deadline_at, t.started_at,
	t.finished_at, t.updated_at, t.error_message`

func (s *Store) scanTestTaskRow(scan func(...any) error) (*testTaskRow, error) {
	var row testTaskRow
	if err := scan(&row.id, &row.sessionID, &row.accountID, &row.accountName, &row.providerCode,
		&row.providerProfileID, &row.protocolCode, &row.protocolVersion, &row.accountType,
		&row.requestSystemAccountID, &row.requestRole, &row.requestSystemAccountFilter,
		&row.status, &row.statusMessage, &row.model, &row.testEndpointMode, &row.resultJSON,
		&row.cancelRequested, &row.createdAt, &row.queuedAt, &row.queuedDeadlineAt, &row.startedAt,
		&row.finishedAt, &row.updatedAt, &row.errorMessage); err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *testTaskRow) toTask() *AccountTestTask {
	queuedAt := r.queuedAt
	deadline := ""
	if r.queuedDeadlineAt.Valid && r.queuedDeadlineAt.String != "" {
		deadline = r.queuedDeadlineAt.String
	} else {
		deadline = testQueuedDeadlineAt(queuedAt)
	}
	task := &AccountTestTask{
		ID:                        r.id,
		SessionID:                 nullPtrString(r.sessionID),
		AccountID:                 r.accountID,
		AccountName:               r.accountName,
		ProviderCode:              r.providerCode,
		ProviderProtocolProfileID: r.providerProfileID,
		ProtocolCode:              r.protocolCode,
		ProtocolVersion:           r.protocolVersion,
		Type:                      r.accountType,
		Status:                    testTaskStatus(r.status),
		Model:                     nullPtrString(r.model),
		TestEndpointMode:          testEndpointModeOrNull(r.testEndpointMode),
		CancelRequested:           r.cancelRequested,
		CreatedAt:                 r.createdAt,
		QueuedAt:                  queuedAt,
		QueuedDeadlineAt:          deadline,
		StartedAt:                 testOptionalTimestamp(r.startedAt),
		FinishedAt:                testOptionalTimestamp(r.finishedAt),
		UpdatedAt:                 r.updatedAt,
	}
	if r.statusMessage.Valid && r.statusMessage.String != "" {
		message := r.statusMessage.String
		task.Message = &message
	} else if r.errorMessage.Valid && r.errorMessage.String != "" {
		message := r.errorMessage.String
		task.Message = &message
	}
	if r.resultJSON.Valid && r.resultJSON.String != "" {
		var result AccountTestResult
		if err := json.Unmarshal([]byte(r.resultJSON.String), &result); err == nil {
			task.Result = result
		}
	}
	return task
}

// testQueuedDeadlineAt mirrors accountTestTaskQueuedDeadlineAt (queued_at +
// accountTestQueuedMaxWaitMs).
func testQueuedDeadlineAt(queuedAt string) string {
	parsed, err := time.Parse(time.RFC3339Nano, queuedAt)
	if err != nil {
		return queuedAt
	}
	return isoMillis(parsed.Add(testQueuedMaxWaitMS * time.Millisecond))
}

func testTaskStatus(value string) string {
	switch value {
	case TestTaskQueued, TestTaskRunning, TestTaskSuccess, TestTaskFailed, TestTaskCanceled:
		return value
	}
	return TestTaskFailed
}

func testSessionStatus(value string) string {
	switch value {
	case TestSessionRunning, TestSessionCanceled, TestSessionExpired, TestSessionCompleted:
		return value
	}
	return TestSessionExpired
}

// testEndpointModeOrNull mirrors accountTestEndpointMode.
func testEndpointModeOrNull(value sql.NullString) *string {
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

func (s *Store) boolTrueLiteral() string {
	if s.pg {
		return "TRUE"
	}
	return "1"
}

func (s *Store) boolFalseLiteral() string {
	if s.pg {
		return "FALSE"
	}
	return "0"
}

func testOptionalTimestamp(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	text := value.String
	return &text
}

func testTrimmedOrNull(value string) any {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil
	}
	return text
}

// ---- access checks (canReadAccountTestTask / canReadAccountTestSession) ----

func canReadTestTask(requestSystemAccountID string, requestFilterID sql.NullString, access *AccessScope) bool {
	if access == nil {
		return true
	}
	if requestSystemAccountID != access.ViewerID {
		return false
	}
	return strings.TrimSpace(requestFilterID.String) == strings.TrimSpace(access.FilterID)
}

func canReadTestSession(requestSystemAccountID string, requestFilterID sql.NullString, access *AccessScope) bool {
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
func testRequestRole(access AccessScope) string {
	if access.IsAdmin {
		return "admin"
	}
	return "user"
}

// CreateTestSession mirrors createAccountTestSessionAsync.
func (s *Store) CreateTestSession(ctx context.Context, access AccessScope) (*AccountTestSession, error) {
	if access.ViewerID == "" {
		return nil, &ValidationError{Message: "缺少系统账户上下文"}
	}
	ctx = ensureCtx(ctx)
	now := isoMillis(s.now())
	id := s.newI("acctsess")
	if _, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_test_sessions")+`
		(id, request_system_account_id, request_role, request_system_account_filter_id,
		 status, last_heartbeat_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'running', ?, ?, ?)`),
		id, access.ViewerID, testRequestRole(access), testTrimmedOrNull(access.FilterID),
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

type testSessionRow struct {
	id                string
	requestAccountID  string
	requestRole       string
	requestFilterID   sql.NullString
	status            string
	cancelReason      sql.NullString
	lastHeartbeatAt   string
	cancelRequestedAt sql.NullString
	finishedAt        sql.NullString
	createdAt         string
	updatedAt         string
}

func (s *Store) scanTestSessionRow(scan func(...any) error) (*testSessionRow, error) {
	var row testSessionRow
	if err := scan(&row.id, &row.requestAccountID, &row.requestRole, &row.requestFilterID,
		&row.status, &row.cancelReason, &row.lastHeartbeatAt, &row.cancelRequestedAt,
		&row.finishedAt, &row.createdAt, &row.updatedAt); err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *testSessionRow) toSession() *AccountTestSession {
	session := &AccountTestSession{
		ID:              r.id,
		Status:          testSessionStatus(r.status),
		LastHeartbeatAt: r.lastHeartbeatAt,
		CreatedAt:       r.createdAt,
		UpdatedAt:       r.updatedAt,
	}
	if r.cancelReason.Valid && r.cancelReason.String != "" {
		message := r.cancelReason.String
		session.Message = &message
	}
	if r.cancelRequestedAt.Valid && r.cancelRequestedAt.String != "" {
		value := r.cancelRequestedAt.String
		session.CancelRequestedAt = &value
	}
	if r.finishedAt.Valid && r.finishedAt.String != "" {
		value := r.finishedAt.String
		session.FinishedAt = &value
	}
	return session
}

const testSessionSelectColumns = `id, request_system_account_id, request_role,
	request_system_account_filter_id, status, cancel_reason, last_heartbeat_at,
	cancel_requested_at, finished_at, created_at, updated_at`

func (s *Store) getTestSessionRow(ctx context.Context, q queryer, id string) (*testSessionRow, error) {
	normalized := strings.TrimSpace(id)
	if normalized == "" {
		return nil, nil
	}
	row, err := s.scanTestSessionRow(func(target ...any) error {
		return q.QueryRowContext(ctx, s.bind(`SELECT `+testSessionSelectColumns+`
			FROM `+s.table("account_test_sessions")+` WHERE id = ? LIMIT 1`), normalized).Scan(target...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

// GetTestSession mirrors getAccountTestSessionAsync.
func (s *Store) GetTestSession(ctx context.Context, sessionID string, access *AccessScope) (*AccountTestSession, error) {
	ctx = ensureCtx(ctx)
	row, err := s.getTestSessionRow(ctx, s.db, sessionID)
	if err != nil || row == nil {
		return nil, err
	}
	if !canReadTestSession(row.requestAccountID, row.requestFilterID, access) {
		return nil, nil
	}
	return row.toSession(), nil
}

// GetTestSessionDetail mirrors getAccountTestSessionDetailAsync (session +
// ordered tasks in one call for GET /test-sessions/{id}/tasks).
func (s *Store) GetTestSessionDetail(ctx context.Context, sessionID string, access *AccessScope) (*AccountTestSession, []AccountTestTask, error) {
	ctx = ensureCtx(ctx)
	row, err := s.getTestSessionRow(ctx, s.db, sessionID)
	if err != nil || row == nil {
		return nil, nil, err
	}
	if !canReadTestSession(row.requestAccountID, row.requestFilterID, access) {
		return nil, nil, nil
	}
	tasks, err := s.listSessionTasks(ctx, s.db, row.id, access)
	if err != nil {
		return nil, nil, err
	}
	return row.toSession(), tasks, nil
}

func (s *Store) listSessionTasks(ctx context.Context, q queryer, sessionID string, access *AccessScope) ([]AccountTestTask, error) {
	rows, err := q.QueryContext(ctx, s.bind(`SELECT `+testTaskSelectColumns+`
		FROM `+s.table("account_test_session_tasks")+` st
		JOIN `+s.table("account_test_tasks")+` t ON t.id = st.task_id
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
		if canReadTestTask(scanned.requestSystemAccountID, scanned.requestSystemAccountFilter, access) {
			tasks = append(tasks, *scanned.toTask())
		}
	}
	return tasks, rows.Err()
}

// HeartbeatTestSession mirrors heartbeatAccountTestSessionAsync.
func (s *Store) HeartbeatTestSession(ctx context.Context, sessionID string, access *AccessScope) (*AccountTestSession, error) {
	ctx = ensureCtx(ctx)
	row, err := s.getTestSessionRow(ctx, s.db, sessionID)
	if err != nil || row == nil {
		return nil, err
	}
	if !canReadTestSession(row.requestAccountID, row.requestFilterID, access) {
		return nil, nil
	}
	if row.status == TestSessionRunning {
		now := isoMillis(s.now())
		if _, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_test_sessions")+`
			SET last_heartbeat_at = ?, updated_at = ?
			WHERE id = ? AND status = 'running'`), now, now, row.id); err != nil {
			return nil, err
		}
	}
	return s.GetTestSession(ctx, row.id, access)
}

// CompleteTestSession mirrors completeAccountTestSessionAsync (settle-only:
// completed when no queued/running task remains).
func (s *Store) CompleteTestSession(ctx context.Context, sessionID string, access *AccessScope) (*AccountTestSession, error) {
	ctx = ensureCtx(ctx)
	row, err := s.getTestSessionRow(ctx, s.db, sessionID)
	if err != nil || row == nil {
		return nil, err
	}
	if !canReadTestSession(row.requestAccountID, row.requestFilterID, access) {
		return nil, nil
	}
	if err := s.completeTestSessionIfSettled(ctx, s.db, row.id); err != nil {
		return nil, err
	}
	return s.GetTestSession(ctx, row.id, access)
}

func (s *Store) completeTestSessionIfSettled(ctx context.Context, q queryer, sessionID string) error {
	now := isoMillis(s.now())
	_, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_test_sessions")+`
		SET status = 'completed', finished_at = COALESCE(finished_at, ?), updated_at = ?
		WHERE id = ? AND status = 'running'
		AND NOT EXISTS (
			SELECT 1
			FROM `+s.table("account_test_session_tasks")+` st
			JOIN `+s.table("account_test_tasks")+` t ON t.id = st.task_id
			WHERE st.session_id = ? AND t.status IN ('queued', 'running')
		)`), now, now, sessionID, sessionID)
	return err
}

// CancelTestSession mirrors cancelAccountTestSessionAsync: running → canceled,
// queued tasks cancel immediately, running tasks flag cancel_requested and
// their ids ride back to the route for the worker cancel dispatch.
func (s *Store) CancelTestSession(ctx context.Context, sessionID string, access *AccessScope, message string) (*TestSessionCancel, error) {
	ctx = ensureCtx(ctx)
	row, err := s.getTestSessionRow(ctx, s.db, sessionID)
	if err != nil || row == nil {
		return nil, err
	}
	if !canReadTestSession(row.requestAccountID, row.requestFilterID, access) {
		return nil, nil
	}
	taskIDs, err := s.cancelTestSessionByRow(ctx, s.db, row, message)
	if err != nil {
		return nil, err
	}
	session, err := s.GetTestSession(ctx, row.id, access)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, nil
	}
	return &TestSessionCancel{Session: *session, TaskIDs: taskIDs}, nil
}

func (s *Store) cancelTestSessionByRow(ctx context.Context, q queryer, row *testSessionRow, message string) ([]string, error) {
	if strings.TrimSpace(message) == "" {
		message = "已停止测试"
	}
	now := isoMillis(s.now())
	// Task ids to cancel-signal on the worker (queued + running, pre-update).
	taskRows, err := q.QueryContext(ctx, s.bind(`SELECT t.id, t.status
		FROM `+s.table("account_test_session_tasks")+` st
		JOIN `+s.table("account_test_tasks")+` t ON t.id = st.task_id
		WHERE st.session_id = ? AND t.status IN ('queued', 'running')
		ORDER BY t.queued_at ASC, t.id ASC`), row.id)
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
	if row.status != TestSessionRunning {
		status = testSessionStatus(row.status)
	}
	if _, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_test_sessions")+`
		SET status = ?, cancel_reason = ?, cancel_requested_at = COALESCE(cancel_requested_at, ?),
		    finished_at = COALESCE(finished_at, ?), updated_at = ?
		WHERE id = ? AND status = 'running'`),
		status, message, now, now, now, row.id); err != nil {
		return nil, err
	}
	if _, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_test_tasks")+`
		SET status = 'canceled', status_message = ?, cancel_requested = `+s.boolTrueLiteral()+`,
		    finished_at = COALESCE(finished_at, ?), updated_at = ?
		WHERE id IN (
			SELECT task_id FROM `+s.table("account_test_session_tasks")+` WHERE session_id = ?
		) AND status = 'queued'`), message, now, now, row.id); err != nil {
		return nil, err
	}
	if _, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_test_tasks")+`
		SET cancel_requested = `+s.boolTrueLiteral()+`, status_message = ?, updated_at = ?
		WHERE id IN (
			SELECT task_id FROM `+s.table("account_test_session_tasks")+` WHERE session_id = ?
		) AND status = 'running'`), message, now, row.id); err != nil {
		return nil, err
	}
	return taskIDs, nil
}

// ---- tasks ----

// CreateTestTask mirrors createAccountTestTaskAsync (cleanup → session assert
// → insert task (+ session link) → read back).
func (s *Store) CreateTestTask(ctx context.Context, input TestTaskCreateInput) (*AccountTestTask, error) {
	ctx = ensureCtx(ctx)
	if err := s.cleanupExpiredTestTasks(ctx); err != nil {
		return nil, err
	}
	now := isoMillis(s.now())
	deadline := testQueuedDeadlineAt(now)
	id := s.newI("accttest")
	sessionID := strings.TrimSpace(input.SessionID)

	draftEncrypted := sql.NullString{}
	if input.Draft != nil {
		plaintext, marshalErr := json.Marshal(input.Draft)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if len(plaintext) > testDraftMaxBytes {
			return nil, &ValidationError{Message: "账户测试草稿过大"}
		}
		sealed, encErr := EncryptJSON(s.secret, input.Draft)
		if encErr != nil {
			return nil, encErr
		}
		draftEncrypted = sql.NullString{String: sealed, Valid: true}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if sessionID != "" {
		if err := s.assertUsableTestSession(ctx, tx, sessionID, &input.Access); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_test_tasks")+`
		(id, account_id, account_name, provider_code, provider_protocol_profile_id,
		 protocol_code, protocol_version, account_type,
		 request_system_account_id, request_role, request_system_account_filter_id,
		 diagnostics, model, test_endpoint_mode, draft_account_encrypted, status, status_message,
		 cancel_requested, queued_at, queued_deadline_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', '等待后台测试', `+s.boolFalseLiteral()+`, ?, ?, ?, ?)`),
		id, input.AccountID, input.AccountName, input.ProviderCode, input.ProviderProtocolProfileID,
		input.ProtocolCode, input.ProtocolVersion, input.AccountType,
		input.Access.ViewerID, testRequestRole(input.Access), testTrimmedOrNull(input.Access.FilterID),
		input.Diagnostics, testTrimmedOrNull(input.Model), testTrimmedOrNull(input.TestEndpointMode),
		draftEncrypted, now, deadline, now, now); err != nil {
		return nil, err
	}
	if sessionID != "" {
		if _, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("account_test_session_tasks")+`
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
func (s *Store) assertUsableTestSession(ctx context.Context, q queryer, sessionID string, access *AccessScope) error {
	row, err := s.getTestSessionRow(ctx, q, sessionID)
	if err != nil {
		return err
	}
	if row == nil || !canReadTestSession(row.requestAccountID, row.requestFilterID, access) {
		return &ValidationError{Message: "账户测试会话不存在"}
	}
	if reason := testSessionCancelReason(row); reason != "" {
		status := testSessionStatus(row.status)
		if row.status == TestSessionRunning {
			status = TestSessionExpired
		}
		now := isoMillis(s.now())
		if _, err := q.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_test_sessions")+`
			SET status = ?, cancel_reason = ?, cancel_requested_at = COALESCE(cancel_requested_at, ?),
			    finished_at = COALESCE(finished_at, ?), updated_at = ?
			WHERE id = ? AND status = 'running'`),
			status, reason, now, now, now, row.id); err != nil {
			return err
		}
		return &ValidationError{Message: reason}
	}
	var taskID sql.NullString
	if err := q.QueryRowContext(ctx, s.bind(`SELECT task_id
		FROM `+s.table("account_test_session_tasks")+` WHERE session_id = ? LIMIT 1`), row.id).Scan(&taskID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if taskID.Valid && taskID.String != "" {
		return &ValidationError{Message: "账户测试会话只能包含一个账户任务"}
	}
	return nil
}

// testSessionCancelReason mirrors accountTestSessionCancelReason.
func testSessionCancelReason(row *testSessionRow) string {
	switch row.status {
	case TestSessionCanceled:
		if row.cancelReason.Valid && row.cancelReason.String != "" {
			return row.cancelReason.String
		}
		return "已停止测试"
	case TestSessionExpired:
		if row.cancelReason.Valid && row.cancelReason.String != "" {
			return row.cancelReason.String
		}
		return "账户测试会话已过期"
	}
	if row.status != TestSessionRunning {
		if row.cancelReason.Valid && row.cancelReason.String != "" {
			return row.cancelReason.String
		}
		return "账户测试会话已结束"
	}
	return ""
}

const testTaskRowSelect = `SELECT ` + testTaskSelectColumns + `
	FROM account_test_tasks t
	LEFT JOIN account_test_session_tasks st ON st.task_id = t.id
	WHERE t.id = ? LIMIT 1`

func (s *Store) getTestTaskRow(ctx context.Context, q queryer, taskID string) (*testTaskRow, error) {
	normalized := strings.TrimSpace(taskID)
	if normalized == "" {
		return nil, nil
	}
	row, err := s.scanTestTaskRow(func(target ...any) error {
		return q.QueryRowContext(ctx, s.bind(testTaskRowSelect), normalized).Scan(target...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

// GetTestTask mirrors getAccountTestTaskAsync.
func (s *Store) GetTestTask(ctx context.Context, taskID string, access *AccessScope) (*AccountTestTask, error) {
	ctx = ensureCtx(ctx)
	row, err := s.getTestTaskRow(ctx, s.db, taskID)
	if err != nil || row == nil {
		return nil, err
	}
	if !canReadTestTask(row.requestSystemAccountID, row.requestSystemAccountFilter, access) {
		return nil, nil
	}
	return row.toTask(), nil
}

// ListTestTasks mirrors listAccountTestTasksAsync: stable id order, scope
// filtered, unknown ids dropped.
func (s *Store) ListTestTasks(ctx context.Context, ids []string, access *AccessScope) ([]AccountTestTask, error) {
	ctx = ensureCtx(ctx)
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
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+testTaskSelectColumns+`
		FROM `+s.table("account_test_tasks")+` t
		LEFT JOIN `+s.table("account_test_session_tasks")+` st ON st.task_id = t.id
		WHERE t.id IN (`+placeholders+`)`), anySlice(normalized)...)
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
		if !canReadTestTask(scanned.requestSystemAccountID, scanned.requestSystemAccountFilter, access) {
			continue
		}
		byID[scanned.id] = scanned.toTask()
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
func (s *Store) CancelTestTask(ctx context.Context, taskID string, access *AccessScope) (*AccountTestTask, error) {
	ctx = ensureCtx(ctx)
	row, err := s.getTestTaskRow(ctx, s.db, taskID)
	if err != nil || row == nil {
		return nil, err
	}
	if !canReadTestTask(row.requestSystemAccountID, row.requestSystemAccountFilter, access) {
		return nil, nil
	}
	now := isoMillis(s.now())
	switch row.status {
	case TestTaskQueued:
		if _, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_test_tasks")+`
			SET status = 'canceled',
			    status_message = CASE
			      WHEN cancel_requested = `+s.boolTrueLiteral()+` AND status_message IS NOT NULL AND TRIM(status_message) != '' THEN status_message
			      ELSE '已停止测试'
			    END,
			    cancel_requested = `+s.boolTrueLiteral()+`,
			    finished_at = COALESCE(finished_at, ?),
			    updated_at = ?
			WHERE id = ? AND status IN ('queued', 'running')`), now, now, row.id); err != nil {
			return nil, err
		}
	case TestTaskRunning:
		if _, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_test_tasks")+`
			SET cancel_requested = `+s.boolTrueLiteral()+`, status_message = '正在停止测试', updated_at = ?
			WHERE id = ? AND status = 'running'`), now, row.id); err != nil {
			return nil, err
		}
	}
	return s.GetTestTask(ctx, row.id, access)
}

// FailTestTask mirrors failAccountTestTaskAsync for the route dispatch
// failure path (queued|running → failed with the message copy).
func (s *Store) FailTestTask(ctx context.Context, taskID string, message string) error {
	ctx = ensureCtx(ctx)
	normalized := strings.TrimSpace(taskID)
	if normalized == "" {
		return nil
	}
	now := isoMillis(s.now())
	_, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("account_test_tasks")+`
		SET status = 'failed', status_message = ?, error_message = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('queued', 'running') AND cancel_requested = `+s.boolFalseLiteral()),
		message, message, now, now, normalized)
	return err
}

// cleanupExpiredTestTasks mirrors cleanupExpiredAccountTestTasksAsync.
func (s *Store) cleanupExpiredTestTasks(ctx context.Context) error {
	cutoff := isoMillis(s.now().Add(-testTaskRetentionHours))
	tasks := s.table("account_test_tasks")
	sessions := s.table("account_test_sessions")
	sessionTasks := s.table("account_test_session_tasks")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+tasks+`
		WHERE id IN (
			SELECT id FROM `+tasks+`
			WHERE finished_at IS NOT NULL AND finished_at < ?
			ORDER BY finished_at ASC, id ASC
			LIMIT ?
		)`), cutoff, testCleanupBatchSize); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+sessions+`
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
