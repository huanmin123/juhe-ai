// Package gatewayusage is the G17 slice of the Node→Go gateway migration:
// the usage (使用记录) record assembly + write-dispatch boundary and the
// gateway audit (审计) capture domain, mirroring:
//
//	backend/src/modules/gateway/usage/
//	  types.ts                        → (covered by internal/gatewayproto
//	                                    ParsedUsage; re-exported here as an
//	                                    alias)
//	  service-tier.ts                 → capability.go
//	  reasoning-effort.ts             → capability.go
//	  traffic-source.ts               → trafficsource.go
//	  snapshots.ts                    → snapshots.go
//	  records.ts                      → service.go (+ metrics/attribution)
//	  failure-finalization.service.ts → finalization.go
//	  record-queue.service.ts         → records.go (normalizeUsageRecordInput,
//	                                    snapshot bounding, byte estimation;
//	                                    the queue/flush/Redis-Stream/IPC
//	                                    machinery itself stays on the Node
//	                                    writer side and is re-owned by the
//	                                    J-F/G20 writer assembly)
//	  usage-record-spool.ts           → spool.go
//	backend/src/modules/gateway/audit/
//	  capture.service.ts              → audit_capture.go
//	  metadata.ts                     → audit_capture.go
//	                                  (ResponseInspectionDecisionAuditMetadata)
//	backend/src/modules/audit-logs/
//	  audit-log-settings.ts           → audit_settings.go
//	  audit-payload-summary.ts        → audit_summary.go
//
// plus the imported helpers the Node files reach inline:
//
//	response/upstream-failure-classifier.ts → classifier.go
//	diagnostics/diagnostic-sanitizer.ts     → diagnostics.go
//	shared/queue-size.ts                    → size.go
//	shared/rfc3339.ts                       → rfc3339.go
//	request-context URL sanitizers          → snapshots.go
//
// # Ports frozen for the writer assembly (J-F / G20)
//
// Node routes every usage record through enqueueUsageRecord
// (record-queue.service.ts) and every gateway audit through
// dispatchAuditLogToGo (audit-log-go-input.service.ts). This package freezes
// those call surfaces as ports and converges "write delivery" onto them:
//
//   - UsageRecorder.EnqueueUsageRecord mirrors enqueueUsageRecord(input):
//     one async delivery of one normalized UsageRecordInput. The in-tree
//     MemoryUsageRecorder is the in-memory mock; the real Redis Stream / IPC
//     / spool-backed writer is assembled by the J-F/G20 writer slice.
//   - AuditDispatcher.DispatchAuditLog mirrors dispatchAuditLogToGo(input):
//     one-shot best-effort delivery of one AuditLogInput (no error to the
//     caller, mirroring the Node void contract). The HMAC loopback input
//     server adapter (or a direct operationlog-producer-style store write)
//     is assembled by G20.
//
// The remaining external capabilities Node reaches through module
// singletons are ports as well: UsageModelResolver
// (providers/drivers/registry resolveGatewayUsageModel), PricingCatalog
// (model-catalog.service synchronous estimators, gated by the
// cacheDriver!=='redis' rule), UpstreamFailureMetricRecorder
// (prometheus-metrics), AuditLogSettingsSource (readAuditLogSettings /
// runtimeConfig), Logger (pino request logger) and the ID generators
// (randomUUID / generateUsageRecordId).
//
// All gateway-facing Chinese copy ("非法网关流量来源：…"，
// "下游连接关闭"，"网关上游尝试失败"，"网关请求失败"， truncation suffixes，
// audit sample reasons and capture statuses) is byte-identical with the Node
// source. Snapshot bounding limits (64 KiB / 16 KiB strings / 50 array items
// / 80 object keys / depth 6), the 4 KiB log-error truncation contract and
// the audit payload summary (head/tail base64 windows + text preview) mirror
// Node exactly.
//
// Every time source is an injected clock and tests run against in-memory
// mocks only; no DB, Redis, HTTP or filesystem access outside t.TempDir().
package gatewayusage
