package main

// J3b 零配置自动认领（2026-09-20）在未配置 Redis 时的 key-model 前台准入
// 回退：进程内 memory store，适配 gatewaydispatch.KeyModelGate 的 ctx 形签
// 名。单进程 owner 重启后短屏蔽状态即重置；配置
// JUHE_AI_J3B_CIRCUIT_REDIS_URL（或 JUHE_AI_REDIS_STATE_URL）后组合根改用
// Redis store，本实现不再参与。

import (
	"context"
	"time"

	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
)

type j3bMemoryKeyModelGate struct {
	store *keymodelruntime.MemoryStore
}

func newJ3bMemoryKeyModelGate() *j3bMemoryKeyModelGate {
	return &j3bMemoryKeyModelGate{store: keymodelruntime.NewMemoryStore()}
}

func (g *j3bMemoryKeyModelGate) AdmitForeground(_ context.Context, capability keymodelruntime.Capability, attemptID string) (keymodelruntime.ForegroundDecision, keymodelruntime.ForegroundPermit, uint64, error) {
	return g.store.AdmitForeground(capability, attemptID, time.Now().UTC())
}

func (g *j3bMemoryKeyModelGate) ReleaseForeground(_ context.Context, permit keymodelruntime.ForegroundPermit) (bool, error) {
	return g.store.ReleaseForeground(permit), nil
}

func (g *j3bMemoryKeyModelGate) RenewForeground(_ context.Context, permit keymodelruntime.ForegroundPermit) (keymodelruntime.ForegroundPermit, bool, error) {
	renewed, ok := g.store.RenewForeground(permit, time.Now().UTC())
	return renewed, ok, nil
}

func (g *j3bMemoryKeyModelGate) RecordFailureIntent(_ context.Context, intent keymodelruntime.FailureIntent) (keymodelruntime.MutationStatus, keymodelruntime.State, error) {
	observedAt := intent.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	return g.store.RecordFailure(intent.Capability, observedAt)
}
