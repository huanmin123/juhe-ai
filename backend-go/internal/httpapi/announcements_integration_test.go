package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"

	"juhe-ai/backend-go/internal/config"
	operationlogjob "juhe-ai/backend-go/internal/jobs/operationlog"
	"juhe-ai/backend-go/internal/jobs/queue"
	"juhe-ai/backend-go/internal/modules/announcements"
	"juhe-ai/backend-go/internal/modules/managementauth"
	postgresstore "juhe-ai/backend-go/internal/store/postgres"
)

func TestAnnouncementManagementHTTPIntegration(t *testing.T) {
	postgresURL := strings.TrimSpace(os.Getenv("JUHE_AI_TEST_POSTGRES_URL"))
	redisURL := strings.TrimSpace(os.Getenv("JUHE_AI_TEST_REDIS_URL"))
	if postgresURL == "" || redisURL == "" {
		if os.Getenv("JUHE_AI_REQUIRE_INTEGRATION") == "1" {
			t.Fatal("JUHE_AI_TEST_POSTGRES_URL and JUHE_AI_TEST_REDIS_URL are required when JUHE_AI_REQUIRE_INTEGRATION=1")
		}
		t.Skip("announcement management integration dependencies are not configured")
	}

	ctx := t.Context()
	store, err := postgresstore.Open(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(store.Close)
	pool, err := pgxpool.New(ctx, postgresURL)
	if err != nil {
		t.Fatalf("open postgres fixture pool: %v", err)
	}
	t.Cleanup(pool.Close)
	redisOptions, err := queue.ParseRedisURL(redisURL)
	if err != nil {
		t.Fatalf("parse redis URL: %v", err)
	}
	queueClient := queue.NewClient(redisOptions)
	t.Cleanup(func() { _ = queueClient.Close() })
	inspector := queue.NewInspector(redisOptions)
	t.Cleanup(func() { _ = inspector.Close() })
	if err := queueClient.Ping(); err != nil {
		t.Fatalf("ping operation log queue: %v", err)
	}
	recordedQueue := &announcementIntegrationQueueRecorder{delegate: queueClient}

	marker := strings.ReplaceAll(uuid.NewString(), "-", "")
	actorID := "sys_ann_http_" + marker
	sessionID := "sess_ann_http_" + marker
	sessionToken := "ann-http-token-" + marker
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO juhe_business.system_accounts
		  (id, username, display_name, role, status, password_hash, created_at, updated_at)
		VALUES ($1, $2, '公告 HTTP 管理员', 'admin', 'active', 'integration-only', $3, $3)`,
		actorID, "ann_http_"+marker, now); err != nil {
		t.Fatalf("insert admin fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO juhe_business.system_sessions
		  (id, system_account_id, token_hash, expires_at, created_at, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		sessionID, actorID, managementauth.HashSessionToken(sessionToken), now.Add(time.Hour), now, now.Add(-2*time.Minute)); err != nil {
		t.Fatalf("insert session fixture: %v", err)
	}
	var announcementID string
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if announcementID != "" {
			if _, err := pool.Exec(cleanupCtx, "DELETE FROM juhe_business.announcements WHERE id = $1", announcementID); err != nil {
				t.Errorf("cleanup announcement fixture: %v", err)
			}
		}
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM juhe_business.system_accounts WHERE id = $1", actorID); err != nil {
			t.Errorf("cleanup system account fixture: %v", err)
		}
		for _, task := range recordedQueue.snapshot() {
			if err := inspector.DeleteTask(task.info.Queue, task.info.ID); err != nil && !errors.Is(err, asynq.ErrTaskNotFound) {
				t.Errorf("cleanup operation log task %s: %v", task.info.ID, err)
			}
		}
	})

	authenticator := managementauth.NewAuthenticator(managementauth.AuthenticatorOptions{Store: store})
	service := announcements.NewService(store)
	handler := NewAnnouncementManagementHandlerWithOptions(
		service,
		ManagementOperationLogOptions{Client: recordedQueue},
		nil,
	)
	router := NewRouter(RouterOptions{
		Config:                           config.Config{ManagementAPIEnabled: true},
		ManagementAPIAuthMiddleware:      NewManagementAPIAuthMiddleware(authenticator),
		ManagementAPIAuthTouchMiddleware: NewManagementAPIAuthTouchMiddleware(authenticator),
		ManagementAnnouncementsHandler:   handler,
	})

	create := httptest.NewRequest(http.MethodPost, "/__aisys__/api/announcements", strings.NewReader(`{"title":"真实链路公告","content":"真实 PostgreSQL 与认证链路","status":"published"}`))
	create.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	createRecorder := httptest.NewRecorder()
	router.ServeHTTP(createRecorder, create)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var createBody struct {
		Data struct {
			ID            string `json:"id"`
			CreatedByName string `json:"createdByName"`
			UpdatedByName string `json:"updatedByName"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &createBody); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	announcementID = createBody.Data.ID
	if announcementID == "" || createBody.Data.CreatedByName != "公告 HTTP 管理员" || createBody.Data.UpdatedByName != "公告 HTTP 管理员" {
		t.Fatalf("create response data = %+v", createBody.Data)
	}

	var touchedAt time.Time
	if err := pool.QueryRow(ctx, "SELECT last_seen_at FROM juhe_business.system_sessions WHERE id = $1", sessionID).Scan(&touchedAt); err != nil {
		t.Fatalf("read touched session: %v", err)
	}
	if !touchedAt.After(now.Add(-time.Minute)) {
		t.Fatalf("write auth did not touch session: %s", touchedAt)
	}

	list := httptest.NewRequest(http.MethodGet, "/__aisys__/api/announcements?page=1&pageSize=10", nil)
	list.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	listRecorder := httptest.NewRecorder()
	router.ServeHTTP(listRecorder, list)
	if listRecorder.Code != http.StatusOK || !strings.Contains(listRecorder.Body.String(), announcementID) {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}

	recordedTasks := recordedQueue.snapshot()
	if len(recordedTasks) != 1 || recordedTasks[0].info.Queue != operationlogjob.QueueName || recordedTasks[0].info.ID == "" || recordedTasks[0].info.State != "pending" {
		t.Fatalf("recorded operation log tasks = %+v", recordedTasks)
	}
	logInput, err := operationlogjob.DecodeWriteTaskPayload(recordedTasks[0].payload)
	if err != nil {
		t.Fatalf("decode operation log payload: %v", err)
	}
	if logInput.OperationKey != "announcements.create" || logInput.ResourceID != announcementID || logInput.StatusCode == nil || *logInput.StatusCode != http.StatusCreated {
		t.Fatalf("operation log input = %+v", logInput)
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/__aisys__/api/announcements/%s", announcementID), nil)
	deleteRequest.Header.Set("Cookie", managementauth.SessionCookieName+"="+sessionToken)
	deleteRecorder := httptest.NewRecorder()
	router.ServeHTTP(deleteRecorder, deleteRequest)
	if deleteRecorder.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	recordedTasks = recordedQueue.snapshot()
	if len(recordedTasks) != 2 {
		t.Fatalf("recorded tasks after delete = %+v", recordedTasks)
	}
	deleteLog, err := operationlogjob.DecodeWriteTaskPayload(recordedTasks[1].payload)
	if err != nil {
		t.Fatalf("decode delete operation log payload: %v", err)
	}
	if deleteLog.OperationKey != "announcements.delete" || deleteLog.ResourceID != announcementID || deleteLog.StatusCode == nil || *deleteLog.StatusCode != http.StatusNoContent {
		t.Fatalf("delete operation log input = %+v", deleteLog)
	}
}

type announcementIntegrationRecordedTask struct {
	info    queue.TaskInfo
	payload []byte
}

type announcementIntegrationQueueRecorder struct {
	delegate *queue.Client
	mu       sync.Mutex
	tasks    []announcementIntegrationRecordedTask
}

func (r *announcementIntegrationQueueRecorder) Enqueue(ctx context.Context, taskType string, payload []byte, opts queue.EnqueueOptions) (queue.TaskInfo, error) {
	info, err := r.delegate.Enqueue(ctx, taskType, payload, opts)
	if err != nil {
		return queue.TaskInfo{}, err
	}
	r.mu.Lock()
	r.tasks = append(r.tasks, announcementIntegrationRecordedTask{info: info, payload: append([]byte(nil), payload...)})
	r.mu.Unlock()
	return info, nil
}

func (r *announcementIntegrationQueueRecorder) snapshot() []announcementIntegrationRecordedTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]announcementIntegrationRecordedTask, len(r.tasks))
	copy(result, r.tasks)
	return result
}
