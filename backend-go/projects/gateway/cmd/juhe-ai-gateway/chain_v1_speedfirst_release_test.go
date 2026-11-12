package main

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhotquality"
)

// newCutoverReservationForReleaseTest 构造一个在目标账户上已占槽的切号预留，
// 通过 fake acquirer 统计底层槽的释放次数。
func newCutoverReservationForReleaseTest(t *testing.T, released *atomic.Int32) *gatewayhotquality.SpeedFirstCutoverReservation {
	t.Helper()
	t.Cleanup(gatewayhotquality.ClearSpeedFirstCutoverReservationsForTest)
	gatewayhotquality.SetSpeedFirstCutoverSlotAcquirerForTest(func(_ context.Context, accountID string, _ int, request gatewayhotquality.AccountConcurrencyAcquireRequest) (gatewayhotquality.AccountConcurrencySlot, bool, error) {
		return gatewayhotquality.AccountConcurrencySlot{
			Key:     accountID,
			Lane:    request.Lane,
			Release: func() { released.Add(1) },
		}, true, nil
	})
	reservation, err := gatewayhotquality.ReserveSpeedFirstCutoverTarget(context.Background(), gatewayhotquality.SpeedFirstCutoverReservationInput{
		Targets: []gatewayhotquality.GatewayAccountConcurrencyLimitIdentity{
			{ID: "acc_target", ConcurrencyLimit: 20},
		},
		Lane: gatewayhotquality.AccountConcurrencyLaneText,
	})
	if err != nil {
		t.Fatalf("ReserveSpeedFirstCutoverTarget: %v", err)
	}
	if reservation == nil {
		t.Fatal("ReserveSpeedFirstCutoverTarget returned nil reservation")
	}
	return reservation
}

// 本轮候选窗口没有预留目标账户（引擎未 take）时，预留必须被确定性释放，
// 否则目标账户并发计数永久 +1。
func TestReleaseUnconsumedCutoverReservationReleasesUntakenReservation(t *testing.T) {
	var released atomic.Int32
	reservation := newCutoverReservationForReleaseTest(t, &released)

	releaseUnconsumedCutoverReservation(reservation)

	if got := released.Load(); got != 1 {
		t.Fatalf("underlying slot releases = %d, want 1", got)
	}
	// Release 与 unconsume 路径都幂等：重复调用不得二次归还。
	releaseUnconsumedCutoverReservation(reservation)
	if got := released.Load(); got != 1 {
		t.Fatalf("underlying slot releases after duplicate call = %d, want 1", got)
	}
}

// 引擎 TakeForAccount 成功后槽所有权归响应链，收口函数不得提前释放。
func TestReleaseUnconsumedCutoverReservationKeepsTakenSlot(t *testing.T) {
	var released atomic.Int32
	reservation := newCutoverReservationForReleaseTest(t, &released)

	slot, ok := reservation.TakeForAccount(gatewayhotquality.GatewayAccountConcurrencyLimitIdentity{ID: "acc_target"})
	if !ok {
		t.Fatal("TakeForAccount rejected the reserved target")
	}
	if !reservation.Consumed() {
		t.Fatal("reservation should be consumed after TakeForAccount")
	}

	releaseUnconsumedCutoverReservation(reservation)
	if got := released.Load(); got != 0 {
		t.Fatalf("underlying slot released before response chain finished: %d", got)
	}

	slot.Release()
	if got := released.Load(); got != 1 {
		t.Fatalf("underlying slot releases = %d, want 1", got)
	}
}
