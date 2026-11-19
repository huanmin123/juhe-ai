package gatewaymedia

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/mediapricing"
)

// 异步媒体任务落库面契约（非对话媒体统一计费设计 §6.1/§14.4）：
// 创建即 completed 时 cost/usage/价格快照随 INSERT 一次落行；重试分类只
// 覆盖可重试 DB 错误，唯一键冲突按幂等成功处理。

func TestMediaJobsRepoInsertWritesTerminalCostAndPricingSnapshot(t *testing.T) {
	db := openJobsRepoPG(t)
	repo := NewMediaJobsRepo(db, time.Now)
	ctx := context.Background()

	template := &mediapricing.Pricing{
		Version:         1,
		Unit:            mediapricing.UnitSecond,
		BaseUsd:         0.05,
		BaseTier:        mediapricing.VideoTier720P,
		TierMultipliers: map[string]float64{"480p": 0.5, "720p": 1, "1080p": 2, "1440p": 3, "2160p": 4},
	}
	seconds := 10.0
	record := MediaJobRecord{
		ID:                   "video_completed_on_create",
		Kind:                 JobKindVideo,
		APIKeyID:             "key_1",
		OwnerSystemAccountID: "sys_1",
		AccountID:            "acc_1",
		UpstreamJobID:        "up_1",
		Status:               JobStatusCompleted,
		RequestSnapshot: MediaJobRequestSnapshot{
			Model:          "veo-3.1-generate-preview",
			MediaPricing:   template,
			ResolutionTier: "1080p",
		},
		Usage: MediaJobUsage{OutputVideoSeconds: &seconds},
		// 10s × 0.05 × 2 = 1.00（创建时已由统一引擎算好）
		CostUsd: 1.0,
	}
	if err := repo.Insert(ctx, record); err != nil {
		t.Fatalf("insert: %v", err)
	}
	loaded, err := repo.GetByIDAndOwner(ctx, record.ID, "sys_1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != JobStatusCompleted || loaded.CostUsd != 1.0 {
		t.Fatalf("status/cost = %s/%v, want completed/1.0", loaded.Status, loaded.CostUsd)
	}
	if loaded.Usage.OutputVideoSeconds == nil || *loaded.Usage.OutputVideoSeconds != 10 {
		t.Fatalf("usage 秒未落库: %#v", loaded.Usage)
	}
	if loaded.RequestSnapshot.MediaPricing == nil || loaded.RequestSnapshot.MediaPricing.BaseUsd != 0.05 {
		t.Fatalf("mediaPricing 快照未落库: %#v", loaded.RequestSnapshot.MediaPricing)
	}
	if loaded.RequestSnapshot.ResolutionTier != "1080p" {
		t.Fatalf("resolutionTier = %q, want 1080p", loaded.RequestSnapshot.ResolutionTier)
	}
}

func TestMediaJobsRepoInsertRejectsInvalidCost(t *testing.T) {
	repo := NewMediaJobsRepo(nil, time.Now)
	err := repo.Insert(context.Background(), MediaJobRecord{ID: "bad", CostUsd: -1})
	if err == nil {
		t.Fatal("负成本必须拒绝")
	}
}

type mediaJobFakeSQLStateError struct{ state string }

func (e mediaJobFakeSQLStateError) Error() string    { return "fake sqlstate " + e.state }
func (e mediaJobFakeSQLStateError) SQLState() string { return e.state }

func TestMediaJobInsertRetryClassification(t *testing.T) {
	retryable := []error{
		fmt.Errorf("wrap: %w", driver.ErrBadConn),
		mediaJobFakeSQLStateError{state: "08006"},
		mediaJobFakeSQLStateError{state: "40001"},
		mediaJobFakeSQLStateError{state: "53300"},
		mediaJobFakeSQLStateError{state: "57P01"},
	}
	for _, err := range retryable {
		if !isRetryableMediaJobInsertError(err) {
			t.Fatalf("%v 应可重试", err)
		}
	}
	nonRetryable := []error{
		context.Canceled,
		context.DeadlineExceeded,
		mediaJobFakeSQLStateError{state: "23505"},
		mediaJobFakeSQLStateError{state: "23503"},
		errors.New("plain error"),
	}
	for _, err := range nonRetryable {
		if isRetryableMediaJobInsertError(err) {
			t.Fatalf("%v 不应重试", err)
		}
	}
	if !isMediaJobDuplicateKeyError(mediaJobFakeSQLStateError{state: "23505"}) {
		t.Fatal("23505 应识别为幂等成功")
	}
	if isMediaJobDuplicateKeyError(mediaJobFakeSQLStateError{state: "23503"}) {
		t.Fatal("23503 不是唯一键冲突")
	}
}
