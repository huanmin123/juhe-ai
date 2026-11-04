package gatewayruntimecache

// 时间计划闸门与账户状态解耦（docs/functions/时间计划闸门与账户状态解耦设计.md
// §5.2）：API Key 有效但 gate=0 时必须与「API Key 无效」区分——装载层返回
// ErrAPIKeyOutsideSchedule 哨兵，鉴权层据此渲染 401 api_key_outside_schedule。

import (
	"context"
	"errors"
	"testing"
)

func TestGatewayAPIKeyRuntimeUsableScheduleGate(t *testing.T) {
	now := int64(1_800_000_000_000)
	zero, one := int64(0), int64(1)
	cases := []struct {
		name    string
		status  string
		gate    *int64
		usable  bool
		outside bool
	}{
		{name: "无计划放行", status: "active", gate: nil, usable: true},
		{name: "时段内放行", status: "active", gate: &one, usable: true},
		{name: "时段外拒绝", status: "active", gate: &zero, outside: true},
		{name: "人工停用按无效 Key 处理", status: "disabled", gate: &zero},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			usable, err := isGatewayAPIKeyRuntimeUsableAt(&GatewayAPIKeyRow{
				Status: testCase.status, AvailabilityScheduleGate: testCase.gate,
			}, now)
			if usable != testCase.usable {
				t.Fatalf("usable = %v，应为 %v", usable, testCase.usable)
			}
			if testCase.outside {
				if !errors.Is(err, ErrAPIKeyOutsideSchedule) {
					t.Fatalf("时段外必须返回 ErrAPIKeyOutsideSchedule，得到 %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("非时段外分支不得报错：%v", err)
			}
		})
	}
}

func TestReadCachedGatewayRuntimeOutsideScheduleSentinel(t *testing.T) {
	clock := newManualClock()
	models := newFakeModels()
	zero, one := int64(0), int64(1)

	closedRow := testAPIKeyRow("key_gate_closed", "normal", "g1")
	closedRow.AvailabilityScheduleGate = &zero
	models.runtimes["sk-gate-closed"] = staticRuntime(t, models, closedRow, nil, []OpenAIAccountSecret{})

	openRow := testAPIKeyRow("key_gate_open", "normal", "g1")
	openRow.AvailabilityScheduleGate = &one
	models.runtimes["sk-gate-open"] = staticRuntime(t, models, openRow, nil, []OpenAIAccountSecret{})

	nilRow := testAPIKeyRow("key_gate_nil", "normal", "g1")
	models.runtimes["sk-gate-nil"] = staticRuntime(t, models, nilRow, nil, []OpenAIAccountSecret{})

	service := newTestService(t, models, clock, nil)
	ctx := context.Background()

	// gate=0：返回哨兵错误（可区分「当前不在可用时段」），不落「API Key 无效」。
	if _, err := service.ReadCachedGatewayRuntimeAsync(ctx, "sk-gate-closed"); !errors.Is(err, ErrAPIKeyOutsideSchedule) {
		t.Fatalf("gate=0 必须返回 ErrAPIKeyOutsideSchedule，得到 %v", err)
	}
	// gate=1 / NULL：正常放行。
	for _, key := range []string{"sk-gate-open", "sk-gate-nil"} {
		runtime, err := service.ReadCachedGatewayRuntimeAsync(ctx, key)
		if err != nil {
			t.Fatalf("%s 读取失败：%v", key, err)
		}
		if runtime.APIKey == nil {
			t.Fatalf("%s 必须放行", key)
		}
	}
}
