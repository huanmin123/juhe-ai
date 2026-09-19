package circuitstate

import "encoding/json"

// ProbeSourceFence mirrors AvailabilityProbeSourceFence.
//
// REFACTOR-0008：两侧（gatewaycircuit/probe.go 与 circuitstore/probestate.go）
// 的结构体与编码函数逐字节相同，下潜为本包词汇；fence 的解码/校验两侧已
// 漂移（gateway decodeSourceFence + uuidPattern、jobs NormalizeSourceFence），
// 按对账结论留守各自包。
type ProbeSourceFence struct {
	StateKey         string
	AccountID        string
	SourceGeneration int64
	SourceFenceID    string
}

// EncodeSourceFence mirrors availability-probe-coordinator 的编码
// （JSON [stateKey, accountId, sourceGeneration, sourceFenceId]）。
func EncodeSourceFence(fence ProbeSourceFence) string {
	encoded, _ := json.Marshal([]any{fence.StateKey, fence.AccountID, fence.SourceGeneration, fence.SourceFenceID})
	return string(encoded)
}
