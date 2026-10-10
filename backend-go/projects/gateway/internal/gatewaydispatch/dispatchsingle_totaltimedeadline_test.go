package gatewaydispatch

// 普通路由速度优先总时间兜底截止（设计 6.3 / 调度内核通用化设计 5.2）：档位
// 选档纯函数的分支覆盖。档位（TotalTimeLane：normal / extended）由链面以超
// 时豁免布尔 + 估算输入 token 与 SpeedFirstLargeInputTokenThreshold 比较算好
// 后传入（大输入维度的链面判定由 chain 侧 speedFirstTotalTimeLaneOf 测试锁
// 定）；本函数只按档位取 preauth 配置对应阈值，两字段为 nil 时回落内联默认
// （与 chain 侧 chainSpeedFirstRuntimeConfigOf 一致）。该函数同时被 attempt
// 装配与 chain 层完成观测消费。

import (
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func int64PtrOf(value int64) *int64 { return &value }

func TestResolveNormalRouteTotalTimeDeadline(t *testing.T) {
	normalConfig := &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{
		TotalTimeDeadlineMs:           int64PtrOf(60_000),
		CompactionTotalTimeDeadlineMs: int64PtrOf(600_000),
	}
	tests := []struct {
		name               string
		config             *gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig
		lane               TotalTimeLane
		attemptStartedAtMs int64
		wantOK             bool
		wantThresholdMs    int64
		wantLane           TotalTimeLane
		wantDeadlineAtMs   int64
	}{
		{
			// preauth 配置缺席 = 不装配总时间维度。
			name:   "配置缺席不装配",
			config: nil,
			wantOK: false,
		},
		{
			// 链面判 extended（超时豁免或大输入）→ extended 档阈值。
			name:               "extended档取压缩档阈值",
			config:             normalConfig,
			lane:               TotalTimeLaneExtended,
			attemptStartedAtMs: 1_000,
			wantOK:             true,
			wantThresholdMs:    600_000,
			wantLane:           TotalTimeLaneExtended,
			wantDeadlineAtMs:   601_000,
		},
		{
			// 链面判 normal → 普通档阈值（原样回带档位）。
			name:               "normal档取普通档阈值",
			config:             normalConfig,
			lane:               TotalTimeLaneNormal,
			attemptStartedAtMs: 3_000,
			wantOK:             true,
			wantThresholdMs:    60_000,
			wantLane:           TotalTimeLaneNormal,
			wantDeadlineAtMs:   63_000,
		},
		{
			// preauth 两阈值为 nil（存量策略缺省提交）→ 内联默认 120s/300s；
			// Lane 零值 = normal 档（未携带档位的调用方保持普通档）。
			name:               "阈值缺省回落默认",
			config:             &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{},
			attemptStartedAtMs: 5_000,
			wantOK:             true,
			wantThresholdMs:    120_000,
			wantLane:           TotalTimeLaneNormal,
			wantDeadlineAtMs:   125_000,
		},
		{
			name:               "extended阈值缺省回落默认",
			config:             &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{},
			lane:               TotalTimeLaneExtended,
			attemptStartedAtMs: 5_000,
			wantOK:             true,
			wantThresholdMs:    300_000,
			wantLane:           TotalTimeLaneExtended,
			wantDeadlineAtMs:   305_000,
		},
		{
			// 运行态解码零值（显式非法）同样回落默认，不产生非正阈值。
			name:            "阈值非法回落默认",
			config:          &gatewaypreauth.NormalRouteSpeedFirstRuntimeConfig{TotalTimeDeadlineMs: int64PtrOf(0)},
			wantOK:          true,
			wantThresholdMs: 120_000,
			wantLane:        TotalTimeLaneNormal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deadline, ok := ResolveNormalRouteTotalTimeDeadline(NormalRouteTotalTimeDeadlineInput{
				Config:             tt.config,
				Lane:               tt.lane,
				AttemptStartedAtMs: tt.attemptStartedAtMs,
			})
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if deadline.ThresholdMs != tt.wantThresholdMs {
				t.Fatalf("thresholdMs = %d, want %d", deadline.ThresholdMs, tt.wantThresholdMs)
			}
			if deadline.Lane != tt.wantLane {
				t.Fatalf("lane = %v, want %v", deadline.Lane, tt.wantLane)
			}
			if tt.wantDeadlineAtMs != 0 && deadline.DeadlineAtMs != tt.wantDeadlineAtMs {
				t.Fatalf("deadlineAtMs = %d, want %d", deadline.DeadlineAtMs, tt.wantDeadlineAtMs)
			}
		})
	}
}
