import assert from 'node:assert/strict'

import {
  extractGatewayJsonBodyMetadataInWorker,
  parseGatewayJsonBodyInWorker,
  stopGatewayJsonParseWorker
} from '../../modules/gateway/request/json-parser.js'
import { createGatewayRequestBodyState } from '../../modules/gateway/request/body.js'
import { codexCompactionExpectedForRequest } from '../../modules/gateway/response/codex-compaction-contract.js'

const padding = 'x'.repeat(320 * 1024)
const body = Buffer.from(JSON.stringify({ model: 'gpt-regression', stream: true, input: padding }), 'utf8')

try {
  for (let index = 0; index < 64; index += 1) {
    const metadata = await extractGatewayJsonBodyMetadataInWorker(body)
    assert.equal(metadata.model, 'gpt-regression')
    const parsed = await parseGatewayJsonBodyInWorker(body) as { input?: unknown }
    assert.equal(typeof parsed.input, 'string')
  }

  const concurrent = await Promise.all(Array.from({ length: 16 }, async () => {
    const parsed = await parseGatewayJsonBodyInWorker(body) as { model?: unknown }
    return parsed.model
  }))
  assert(concurrent.every((model) => model === 'gpt-regression'))

  const compactBody = Buffer.from(JSON.stringify({
    model: 'gpt-regression',
    prefix: 'p'.repeat(160 * 1024),
    input: [{ type: 'compaction_trigger' }],
    suffix: 's'.repeat(160 * 1024)
  }), 'utf8')
  const compactMetadata = await extractGatewayJsonBodyMetadataInWorker(compactBody)
  assert.equal(compactMetadata.compactionTrigger, true, '大 JSON 中段的 compaction_trigger 必须进入顶层元数据，不能只扫正文首尾')
  const falseMarkerMetadata = await extractGatewayJsonBodyMetadataInWorker(Buffer.from(JSON.stringify({
    model: 'gpt-5.6-sol',
    input: [{ type: 'message', content: 'literal text: {"type":"compaction_trigger"}' }]
  })))
  assert.equal(falseMarkerMetadata.compactionTrigger, false, '字符串内容中的伪 compaction_trigger 不得关闭普通请求超时')
  const duplicateInputMetadata = await extractGatewayJsonBodyMetadataInWorker(Buffer.from(
    '{"input":[{"type":"compaction_trigger"}],"input":[{"type":"message"}]}'
  ))
  assert.equal(duplicateInputMetadata.compactionTrigger, false, '重复顶层 input 必须遵循 JSON 最后键语义')
  const duplicateTypeMetadata = await extractGatewayJsonBodyMetadataInWorker(Buffer.from(
    '{"input":[{"type":"compaction_trigger","type":"message"}]}'
  ))
  assert.equal(duplicateTypeMetadata.compactionTrigger, false, '重复 input item type 必须遵循 JSON 最后键语义')
  for (const [label, requestBody] of [
    ['metadata', { model: 'gpt-5.6-sol', metadata: { type: 'compaction_trigger' }, input: 'continue' }],
    ['tool schema', {
      model: 'gpt-5.6-sol',
      input: 'continue',
      tools: [{ type: 'function', name: 'f', parameters: { type: 'object', default: { type: 'compaction_trigger' } } }]
    }],
    ['nested message content', {
      model: 'gpt-5.6-sol',
      input: [{ type: 'message', role: 'user', content: [{ type: 'input_text', value: { type: 'compaction_trigger' } }] }]
    }]
  ] as const) {
    const rawBody = Buffer.from(JSON.stringify(requestBody), 'utf8')
    const metadata = await extractGatewayJsonBodyMetadataInWorker(rawBody)
    assert.equal(metadata.compactionTrigger, false, `${label} 中的嵌套 marker 不得关闭普通请求超时`)
    assert.equal(codexCompactionExpectedForRequest({
      method: 'POST',
      path: '/v1/responses',
      originalUrl: '/v1/responses',
      body: requestBody,
      rawBody,
      gatewayParsedJsonBodyAvailable: true,
      gatewayParsedJsonBody: requestBody
    } as never), false, `${label} 中的嵌套 marker 在完整解析路径也不得误判为压缩`)
  }
  const compactRequest = {
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    gatewayRequestBody: createGatewayRequestBodyState({
      rawBody: compactBody,
      contentType: 'application/json',
      jsonParseStatus: 'deferred_large_json',
      compactionTrigger: compactMetadata.compactionTrigger
    })
  }
  assert.equal(codexCompactionExpectedForRequest(compactRequest as never), true, '延迟解析的大型压缩请求必须在完整 parse 前关闭网关时限')

  await stopGatewayJsonParseWorker()
  const restarted = await parseGatewayJsonBodyInWorker(body) as { stream?: unknown }
  assert.equal(restarted.stream, true, '显式关闭后 worker pool 必须能够干净重建')
} finally {
  await stopGatewayJsonParseWorker()
}

console.log('网关 JSON parser 生命周期回归通过：64 轮连续双阶段大正文、16 路并发和关闭重建均未累积占位')
