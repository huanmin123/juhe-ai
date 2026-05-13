import type { RuntimeLogGrepItem } from '@/types/domain'

export const runtimeLogEventTextMap: Record<string, string> = {
  audit_log_queue_dropped: '审计日志队列丢弃',
  audit_log_queue_flush_failed: '审计日志队列写入失败',
  background_account_quality_refresh_completed: '后台账户质量刷新完成',
  background_account_quality_refresh_failed: '后台账户质量刷新失败',
  background_cooldown_account_retest_failed: '后台冷却账户复测失败',
  background_group_account_stats_refresh_failed: '后台分组账户统计刷新失败',
  background_job_failed: '后台任务执行失败',
  background_job_skipped_running: '后台任务因运行中跳过',
  background_openai_oauth_access_token_refresh_completed: '后台 OpenAI OAuth Token 刷新完成',
  background_openai_oauth_access_token_refresh_failed: '后台 OpenAI OAuth Token 刷新失败',
  background_proxy_latency_refresh_failed: '后台代理延迟刷新失败',
  background_resource_authorization_expiry_sweep_failed: '后台资源授权过期扫描失败',
  background_runtime_log_index_maintenance_failed: '后台运行日志索引维护失败',
  background_system_metrics_sample_failed: '后台系统指标采样失败',
  background_usage_stats_aggregation_failed: '后台用量统计聚合失败',
  background_worker_exited: '后台 Worker 已退出',
  background_worker_spawn_failed: '后台 Worker 启动失败',
  background_worker_spawned: '后台 Worker 已拉起',
  background_worker_started: '后台 Worker 已启动',
  data_retention_cleanup_completed: '数据保留清理完成',
  data_retention_cleanup_failed: '数据保留清理失败',
  db_service_cache_invalidation_failed: 'DB Service 缓存失效通知失败',
  db_service_exited: 'DB Service 已退出',
  db_service_fallback_to_local: 'DB Service 回退到本地访问',
  db_service_spawn_failed: 'DB Service 启动失败',
  db_service_spawned: 'DB Service 已拉起',
  db_service_started: 'DB Service 已启动',
  gateway_account_local_suppressed: '网关本地抑制账户',
  gateway_account_side_effect_expired: '网关账户副作用已过期',
  gateway_account_side_effect_retry_scheduled: '网关账户副作用重试已调度',
  gateway_auth_failed: '网关鉴权失败',
  gateway_codex_usage_snapshot_side_effect_failed: '网关 Codex 用量快照副作用失败',
  gateway_local_account_suppression_applied: '网关本地账户抑制已应用',
  gateway_non_stream_response_capture_truncated: '网关非流式响应截断记录',
  gateway_raw_body_rejected: '网关原始请求体被拒绝',
  gateway_request_failed: '网关请求失败',
  gateway_response_backpressure_drained: '网关响应背压已恢复',
  gateway_response_backpressure_started: '网关响应背压开始',
  gateway_response_drain_waiting: '网关响应等待写入排空',
  gateway_stream_aborted: '网关流式请求已中断',
  gateway_stream_completed_with_parser_skipped: '网关流式完成但跳过解析',
  gateway_stream_error_ignored_after_terminal: '网关流式终止后忽略错误',
  gateway_stream_failure_event_skipped: '网关流式失败事件已跳过',
  gateway_stream_failure_event_writing: '网关流式失败事件写入中',
  gateway_stream_failure_event_written: '网关流式失败事件已写入',
  gateway_stream_finished_failed: '网关流式结束失败',
  gateway_stream_finished_success: '网关流式结束成功',
  gateway_stream_inspector_skipped: '网关流式检查已跳过',
  gateway_stream_missing_terminal: '网关流式缺少结束事件',
  gateway_stream_missing_terminal_failure_event_skipped: '网关缺少结束事件的失败事件已跳过',
  gateway_stream_missing_terminal_failure_event_written: '网关缺少结束事件的失败事件已写入',
  gateway_stream_pipe_error: '网关流式管道错误',
  gateway_stream_pipe_started: '网关流式管道已启动',
  gateway_stream_progress: '网关流式进度',
  gateway_stream_response_backpressure: '网关流式响应背压',
  gateway_upstream_attempt_failed: '网关上游尝试失败',
  gateway_upstream_error_body_truncated: '网关上游错误响应体截断',
  gateway_upstream_request_failed: '网关上游请求失败',
  gateway_upstream_request_started: '网关上游请求开始',
  gateway_upstream_response_failed: '网关上游响应失败',
  gateway_upstream_response_received: '网关上游响应已收到',
  gateway_upstream_retry_error_body_truncated: '网关上游重试错误响应体截断',
  http_request_closed: 'HTTP 请求连接关闭',
  http_request_completed: 'HTTP 请求完成',
  http_request_unhandled_error: 'HTTP 请求未处理异常',
  openai_oauth_access_token_refresh_account_failed: 'OpenAI OAuth 账户 Token 刷新失败',
  openai_oauth_access_token_refresh_race_recovered: 'OpenAI OAuth Token 并发刷新已恢复',
  openai_oauth_access_token_refresh_retry_with_latest_refresh_token: 'OpenAI OAuth 使用最新 Refresh Token 重试',
  process_uncaught_exception: '进程未捕获异常',
  process_unhandled_rejection: '进程未处理 Promise 拒绝',
  server_listen_failed: '服务监听失败',
  server_started: '服务已启动',
  usage_record_queue_flush_failed: '使用记录队列写入失败',
  usage_record_queue_soft_limit_exceeded: '使用记录队列接近上限'
}

export function eventText(value?: string): string {
  if (!value) return '-'
  return runtimeLogEventTextMap[value] ?? value
}

export function levelText(value: string): string {
  return value.toLowerCase()
}

export function levelColor(value: string): string {
  const level = value.toLowerCase()
  if (level === 'fatal' || level === 'error') return 'red'
  if (level === 'warn') return 'orange'
  if (level === 'debug' || level === 'trace') return 'blue'
  return 'green'
}

export function prettyRawJson(rawJson: string): string {
  try {
    return JSON.stringify(JSON.parse(rawJson), null, 2)
  } catch {
    return rawJson
  }
}

export function splitGrepKeywords(value: string): string[] {
  const seen = new Set<string>()
  const keywords: string[] = []
  for (const part of value.split(/[\s,;，；]+/)) {
    const keyword = part.trim()
    if (!keyword) continue
    const key = keyword.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    keywords.push(keyword)
  }
  return keywords
}

export function grepLinePositionText(record: RuntimeLogGrepItem): string {
  return record.lineNumber ? `第 ${record.lineNumber} 行` : '-'
}
