package app

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
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

func TestPublicAPILogRetentionCleanupWorkerOptionsAreRunOnceOnly(t *testing.T) {
	typeOfOptions := reflect.TypeOf(PublicAPILogRetentionCleanupWorkerOptions{})
	for _, forbidden := range []string{"RunOnce", "Interval", "InitialDelay"} {
		if _, found := typeOfOptions.FieldByName(forbidden); found {
			t.Fatalf("PublicAPILogRetentionCleanupWorkerOptions exposes scheduler field %q", forbidden)
		}
	}
}
