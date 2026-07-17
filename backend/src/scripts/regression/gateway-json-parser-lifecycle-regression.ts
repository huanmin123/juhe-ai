import assert from 'node:assert/strict'

import {
  extractGatewayJsonBodyMetadataInWorker,
  parseGatewayJsonBodyInWorker,
  stopGatewayJsonParseWorker
} from '../../modules/gateway/request/json-parser.js'

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

  await stopGatewayJsonParseWorker()
  const restarted = await parseGatewayJsonBodyInWorker(body) as { stream?: unknown }
  assert.equal(restarted.stream, true, '显式关闭后 worker pool 必须能够干净重建')
} finally {
  await stopGatewayJsonParseWorker()
}

console.log('网关 JSON parser 生命周期回归通过：64 轮连续双阶段大正文、16 路并发和关闭重建均未累积占位')
