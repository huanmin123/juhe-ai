package statsagg

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAggregateUsageStatsBatchFailsOnTimezoneDrift(t *testing.T) {
	env := newPGTestEnv(t)
	ctx := context.Background()
	first := StaticTimezoneSource{Location: time.UTC}
	aggregator := &Aggregator{DB: env.db, Dialect: env.dialect, Clock: first, Now: func() time.Time { return env.now }}
	if _, err := aggregator.AggregateUsageStatsBatch(ctx, AggregateOptions{BatchSize: 10}); err != nil {
		t.Fatalf("首次聚合必须成功: %v", err)
	}
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	second := StaticTimezoneSource{Location: shanghai}
	aggregator.Clock = second
	if _, err := aggregator.AggregateUsageStatsBatch(ctx, AggregateOptions{BatchSize: 10}); err == nil || !strings.Contains(err.Error(), "统计时区指纹不一致") {
		t.Fatalf("时区漂移必须 fail-closed，got %v", err)
	}
}
