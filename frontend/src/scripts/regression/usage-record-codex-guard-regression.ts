import assert from 'node:assert/strict'

import type { UsageRecordSummary } from '../../types/domain/usage-records'
import { usageRecordCodexGuardStatus } from '../../views/usage-records/usageRecordFormatters'

const base: UsageRecordSummary = {
  id: 'usage-codex-guard',
  traceId: 'trace-codex-guard',
  trafficSource: 'gateway',
  stream: false,
  success: true,
  createdAt: '2026-07-23T00:00:00.000Z'
}

assert.equal(usageRecordCodexGuardStatus(base), undefined)
assert.deepEqual(usageRecordCodexGuardStatus({
  ...base,
  responseSnapshot: {
    codexResponsesGuard: {
      outcome: 'repaired_safe',
      repairRuleIds: ['codex.r0.response.replace_item_id'],
      diagnosticCodes: ['response_item_id_prefix_invalid']
    }
  }
}), {
  label: '已修复',
  detail: '响应已在网关复制后完成安全修复；修复规则：codex.r0.response.replace_item_id；诊断码：response_item_id_prefix_invalid'
})
assert.equal(usageRecordCodexGuardStatus({
  ...base,
  responseSnapshot: { codexResponsesGuard: { outcome: 'observed_unknown', diagnosticCodes: ['future_item_type'] } }
})?.label, '协议异常')
assert.equal(usageRecordCodexGuardStatus({
  ...base,
  success: false,
  responseSnapshot: { codexResponsesGuard: { outcome: 'repaired_safe' } }
}), undefined, '失败记录仍使用失败红色状态')

console.log('usage record Codex guard status regression passed')
