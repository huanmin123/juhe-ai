package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
	"juhe-ai/backend-go/internal/modules/managementpublicapilogs"
)

func TestRunPublicAPILogRetentionCleanupWorkerRequiresPostgresURL(t *testing.T) {
	err := RunPublicAPILogRetentionCleanupWorker(
		context.Background(),
		config.Config{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		PublicAPILogRetentionCleanupWorkerOptions{},
	)
	if err == nil || !strings.Contains(err.Error(), "JUHE_AI_POSTGRES_URL") {
		t.Fatalf("RunPublicAPILogRetentionCleanupWorker() error = %v, want missing postgres url", err)
	}
}

func TestRunPublicAPILogRetentionCleanupLogsPartialResultBeforeReturningError(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	wantErr := errors.New("second batch failed")

	err := runPublicAPILogRetentionCleanup(context.Background(), logger, func(context.Context) (managementpublicapilogs.RetentionCleanupResult, error) {
		return managementpublicapilogs.RetentionCleanupResult{
			Deleted: 2,
			Batches: 1,
			Phase:   managementpublicapilogs.RetentionCleanupPhasePublicAPILogs,
			Partial: true,
		}, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runPublicAPILogRetentionCleanup() error = %v, want %v", err, wantErr)
	}
	for _, want := range []string{"level=ERROR", "deleted=2", "batches=1", "phase=public_api_logs", "partial=true", `error="second batch failed"`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("log = %q, want contains %q", output.String(), want)
		}
	}
}

func TestPublicAPILogRetentionCleanupWorkerOptionsAreRunOnceOnly(t *testing.T) {
	typeOfOptions := reflect.TypeOf(PublicAPILogRetentionCleanupWorkerOptions{})
	for _, forbidden := range []string{"RunOnce", "Interval", "InitialDelay"} {
		if _, found := typeOfOptions.FieldByName(forbidden); found {
			t.Fatalf("PublicAPILogRetentionCleanupWorkerOptions exposes scheduler field %q", forbidden)
		}
	}
}
