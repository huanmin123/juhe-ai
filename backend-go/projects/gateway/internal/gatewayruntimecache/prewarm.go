// prewarm.go 移植 Node prewarmGatewayApiKeyValidationCacheAsync
// （storage/gateway-api-key.repository.ts:300）与 server.ts:181 的启动接线语义：
// 启动期把全部可校验 API Key 的运行时读预热进 runtimeCache，消除冷启动后首波
// /v1 请求的缓存 miss（Node 语义：仅缓存校验通过（活跃 + 绑定非空）的 Key，
// 失效代次中止，失败由调用方告警不阻断启动）。
//
// Go 架构对照：Node 的 gatewayApiKeyProcessCache（validateGatewayApiKey 校验
// 缓存）在 Go 并入 runtimeCache 的完整 GatewayRuntime 条目（populateGatewayRuntimeCaches
// 是自然读路径的唯一缓存写入面），因此预热按同一自然读路径逐 Key 填充完整
// 运行时（含分组访问与账户集），保证预热条目与惰性加载条目逐字段一致——只写
// 半份条目会让首个请求命中不完整快照，属正确性破坏，故不采用。
package gatewayruntimecache

import "context"

// prewarmChunkSize mirrors the Node prewarm batch width (rows sliced 8 at a
// time, Promise.all per slice); the Go loader walks sequentially, the chunking
// only bounds per-pass work between generation checks.
const prewarmChunkSize = 8

// PrewarmGatewayAPIKeyValidationCache mirrors prewarmGatewayApiKeyValidationCacheAsync:
// forced cross-instance invalidation sync (Node syncGatewayCacheInvalidationsFromRuntimeState
// when the runtime-state driver is redis), a generation snapshot, then every
// active key hash loaded through the natural read path and cached under its
// hash key. Returns the warmed key count; a stale generation returns early
// with what has been warmed (Node `isGatewayApiKeyValidationGenerationCurrent`
// branches). Models without the GatewayAPIKeyPrewarmer seam degrade to a no-op
// (the Node control-replica skip at server.ts:180).
func (s *Service) PrewarmGatewayAPIKeyValidationCache(ctx context.Context) (int, error) {
	prewarmer, ok := s.models.(GatewayAPIKeyPrewarmer)
	if !ok {
		return 0, nil
	}
	// Node opens with the forced redis invalidation sync before snapshotting
	// the generation.
	s.syncInvalidationsForRuntime(ctx)
	generation := s.currentAPIKeyRuntimeGeneration()
	hashes, err := prewarmer.ListActiveGatewayAPIKeyHashes(ctx)
	if err != nil {
		return 0, err
	}
	warmed := 0
	for start := 0; start < len(hashes); start += prewarmChunkSize {
		end := start + prewarmChunkSize
		if end > len(hashes) {
			end = len(hashes)
		}
		for _, keyHash := range hashes[start:end] {
			// Node re-checks the generation after every awaited read and stops
			// the loop when an invalidation landed mid-prewarm.
			if !s.isAPIKeyRuntimeGenerationCurrent(generation) {
				return warmed, nil
			}
			runtime, loadErr := prewarmer.ReadGatewayRuntimeByKeyHash(ctx, keyHash)
			if loadErr != nil {
				return warmed, loadErr
			}
			// Node: rows without active bindings warm nothing (no negative
			// entries — the lazy path owns those).
			if runtime.APIKey == nil {
				continue
			}
			if !s.isAPIKeyRuntimeGenerationCurrent(generation) {
				return warmed, nil
			}
			if err := s.populateGatewayRuntimeCaches(keyHash, runtime); err != nil {
				return warmed, err
			}
			warmed++
		}
	}
	return warmed, nil
}
