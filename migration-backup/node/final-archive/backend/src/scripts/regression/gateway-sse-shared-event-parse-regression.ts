import assert from 'node:assert/strict'

import { AnthropicStreamInspector } from '../../modules/gateway/protocols/anthropic-v1/stream-inspection.js'
import { GeminiStreamInspector } from '../../modules/gateway/protocols/gemini-v1beta/stream-inspection.js'
import type { GatewayStreamInspector } from '../../modules/gateway/protocols/_shared/types.js'
import {
  OpenAIResponseInspectionBuffer
} from '../../modules/gateway/protocols/openai-v1/response-inspection-buffer.js'
import { OpenAIStreamInspector } from '../../modules/gateway/protocols/openai-v1/stream-inspection.js'
import { pushGatewayStreamInspectorChunk } from '../../modules/gateway/response/stream.js'

function countJsonParses(run: () => void): number {
  const originalParse = JSON.parse
  let count = 0
  JSON.parse = ((text: string, reviver?: (this: unknown, key: string, value: unknown) => unknown) => {
    count += 1
    return originalParse(text, reviver)
  }) as typeof JSON.parse
  try {
    run()
  } finally {
    JSON.parse = originalParse
  }
  return count
}

function inspectOutput(
  interceptor: OpenAIResponseInspectionBuffer,
  inspector: GatewayStreamInspector,
  chunks: Buffer[]
): void {
  for (const chunk of chunks) {
    pushGatewayStreamInspectorChunk(inspector, chunk, interceptor)
  }
}

function testOpenAiMultilineCrLfEventIsParsedOnce(): void {
  const interceptor = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'responses'
  })
  const inspector = new OpenAIStreamInspector()
  const event = Buffer.from([
    'event: response.completed\r\n',
    'data: {"type":"response.completed",\r\n',
    'data: "response":{"status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}\r\n',
    '\r\n'
  ].join(''), 'utf8')

  const parseCount = countJsonParses(() => {
    const split = event.length - 3
    const first = interceptor.pushChunk(event.subarray(0, split))
    assert.equal(first.chunks.length, 0, '未闭合的 CRLF 事件不得提前释放')
    const second = interceptor.pushChunk(event.subarray(split))
    inspectOutput(interceptor, inspector, second.chunks)
  })

  const inspection = inspector.finish()
  assert.equal(parseCount, 1, 'OpenAI 响应策略与协议 inspector 必须共享同一次 JSON 解码')
  assert.equal(inspection.eventCount, 1)
  assert.equal(inspection.terminalReceived, true)
  assert.equal(inspection.usage.inputTokens, 7)
  assert.equal(inspection.usage.outputTokens, 3)
}

function testAnthropicAndGeminiInspectorsReuseParsedEvents(): void {
  const cases: Array<{
    name: string
    endpointFamily: 'messages' | 'generate_content'
    event: Buffer
    inspector: GatewayStreamInspector
    verify: () => void
  }> = []

  const anthropic = new AnthropicStreamInspector()
  const anthropicObserved: unknown[] = []
  anthropic.setParsedEventObserver((event) => anthropicObserved.push(event))
  cases.push({
    name: 'Anthropic',
    endpointFamily: 'messages',
    event: Buffer.from('event: message_stop\ndata: {"type":"message_stop"}\n\n', 'utf8'),
    inspector: anthropic,
    verify: () => {
      assert.equal(anthropic.finish().terminalReceived, true)
      assert.equal(anthropicObserved.length, 1, 'Anthropic inspector 必须发布同一个 parsed event 给诊断消费者')
    }
  })

  const gemini = new GeminiStreamInspector()
  const geminiObserved: unknown[] = []
  gemini.setParsedEventObserver((event) => geminiObserved.push(event))
  cases.push({
    name: 'Gemini',
    endpointFamily: 'generate_content',
    event: Buffer.from('data: {"event_type":"interaction.completed","interaction":{"id":"interactions/shared-parse","status":"completed"}}\n\n', 'utf8'),
    inspector: gemini,
    verify: () => {
      const inspection = gemini.finish()
      assert.equal(inspection.terminalReceived, true)
      assert.equal(inspection.responseResourceId, 'interactions/shared-parse')
      assert.equal(geminiObserved.length, 1, 'Gemini inspector 必须发布同一个 parsed event 给诊断消费者')
    }
  })

  for (const testCase of cases) {
    const interceptor = new OpenAIResponseInspectionBuffer({
      clientRetryEnabled: true,
      endpointFamily: testCase.endpointFamily
    })
    const parseCount = countJsonParses(() => {
      const result = interceptor.pushChunk(testCase.event)
      inspectOutput(interceptor, testCase.inspector, result.chunks)
    })
    assert.equal(parseCount, 1, `${testCase.name} 响应策略与协议 inspector 必须共享同一次 JSON 解码`)
    testCase.verify()
  }
}

function testEofIncompleteEventKeepsFallbackSemanticsAndSharesParse(): void {
  const interceptor = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'responses'
  })
  const inspector = new OpenAIStreamInspector()
  const incomplete = Buffer.from('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}', 'utf8')

  const parseCount = countJsonParses(() => {
    const pending = interceptor.pushChunk(incomplete)
    assert.equal(pending.pendingEvent, true)
    assert.equal(pending.chunks.length, 0)
    const eof = interceptor.flushPendingOnEof()
    assert.equal(eof.pendingEvent, false)
    assert.equal(Buffer.concat(eof.chunks).toString('utf8'), `${incomplete.toString('utf8')}\n\n`)
    inspectOutput(interceptor, inspector, eof.chunks)
  })

  assert.equal(parseCount, 1, 'EOF 补边界后的事件仍只能解码一次')
  assert.equal(inspector.finish().terminalReceived, true)
}

function testEmptyDataAndOversizedEventsKeepInspectorBoundaries(): void {
  const emptyInterceptor = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'responses'
  })
  const emptyInspector = new OpenAIStreamInspector()
  const emptyResult = emptyInterceptor.pushChunk(Buffer.from('event: response.completed\n\n', 'utf8'))
  assert.equal(emptyInterceptor.parsedEventForChunk(emptyResult.chunks[0]!), undefined, '无 data 事件不得走解析对象复用')
  inspectOutput(emptyInterceptor, emptyInspector, emptyResult.chunks)
  const emptyInspection = emptyInspector.finish()
  assert.equal(emptyInspection.eventCount, 0)
  assert.equal(emptyInspection.terminalReceived, false)

  const oversizedInterceptor = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'responses'
  })
  const oversizedInspector = new OpenAIStreamInspector()
  const oversized = Buffer.from(`event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"${'x'.repeat(300 * 1024)}"}\n\n`, 'utf8')
  const oversizedResult = oversizedInterceptor.pushChunk(oversized)
  assert.equal(oversizedResult.parserSkipped, true)
  assert.equal(oversizedInterceptor.parsedEventForChunk(oversizedResult.chunks[0]!), undefined, '超限事件必须保留 inspector 原有限长回退路径')
  inspectOutput(oversizedInterceptor, oversizedInspector, oversizedResult.chunks)
  const completedAfterSkip = Buffer.from('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed"}}\n\n', 'utf8')
  const parseCountAfterSkip = countJsonParses(() => {
    const passthrough = oversizedInterceptor.pushChunk(completedAfterSkip)
    assert.equal(oversizedInterceptor.parsedEventForChunk(passthrough.chunks[0]!), undefined, '响应策略 parserSkipped 后必须持续原样透传')
    inspectOutput(oversizedInterceptor, oversizedInspector, passthrough.chunks)
  })
  const oversizedInspection = oversizedInspector.finish()
  assert.equal(parseCountAfterSkip, 1, 'parserSkipped 后只应由协议 inspector 解码后续事件')
  assert.equal(oversizedInspection.eventCount, 2)
  assert.equal(oversizedInspection.terminalReceived, true)
}

function testDecodedByteExpansionAndBareCrKeepLegacyFallback(): void {
  const invalidUtf8 = Buffer.concat([
    Buffer.from('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"', 'utf8'),
    Buffer.alloc(90 * 1024, 0xff),
    Buffer.from('"}]}]}}\n\n', 'utf8')
  ])
  const expansionInterceptor = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'responses'
  })
  const expansionResult = expansionInterceptor.pushChunk(invalidUtf8)
  assert.equal(expansionResult.parserSkipped, false, '原始字节未超过响应策略缓冲上限')
  assert.equal(expansionInterceptor.parsedEventForChunk(expansionResult.chunks[0]!), undefined, 'UTF-8 替换字符膨胀后超过 inspector 上限时必须回退')
  const directExpansionInspector = new OpenAIStreamInspector()
  directExpansionInspector.pushChunk(invalidUtf8)
  const sharedExpansionInspector = new OpenAIStreamInspector()
  inspectOutput(expansionInterceptor, sharedExpansionInspector, expansionResult.chunks)
  assert.deepEqual(sharedExpansionInspector.finish(), directExpansionInspector.finish(), 'UTF-8 解码膨胀事件必须保持原 inspector 限长语义')

  const bareCrEvent = Buffer.from('event: response.completed\rdata: {"type":"response.completed","response":{"status":"completed"}}\r\r', 'utf8')
  const bareCrInterceptor = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'responses'
  })
  const bareCrResult = bareCrInterceptor.pushChunk(bareCrEvent)
  assert.equal(bareCrInterceptor.parsedEventForChunk(bareCrResult.chunks[0]!), undefined, '裸 CR 分帧必须保留协议 inspector 的既有回退行为')
  const directBareCrInspector = new OpenAIStreamInspector()
  directBareCrInspector.pushChunk(bareCrEvent)
  const sharedBareCrInspector = new OpenAIStreamInspector()
  inspectOutput(bareCrInterceptor, sharedBareCrInspector, bareCrResult.chunks)
  assert.deepEqual(sharedBareCrInspector.finish(), directBareCrInspector.finish(), '裸 CR 事件共享路径不得改变既有 inspector 语义')
}

function testDeferredEventsKeepSharedParseIdentity(): void {
  const noopInterceptor = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'chat_completions'
  })
  const noopInspector = new OpenAIStreamInspector()
  const noop = Buffer.from('data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}\n\n', 'utf8')
  const output = Buffer.from('data: {"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"}}]}\n\n', 'utf8')
  const noopParseCount = countJsonParses(() => {
    assert.equal(noopInterceptor.pushChunk(noop).chunks.length, 0, 'leading noop 应继续暂存')
    const released = noopInterceptor.pushChunk(output)
    assert.equal(released.chunks.length, 2, '真实输出到达后应按原顺序释放 noop 与输出事件')
    inspectOutput(noopInterceptor, noopInspector, released.chunks)
  })
  const noopInspection = noopInspector.finish()
  assert.equal(noopParseCount, 2, '两个 deferred 事件应各自只解码一次')
  assert.equal(noopInspection.eventCount, 2)
  assert.equal(noopInspection.outputReceived, true)

  const compactionInterceptor = new OpenAIResponseInspectionBuffer({
    clientRetryEnabled: true,
    endpointFamily: 'responses',
    context: {
      clientProfile: 'codex',
      accountClientCompatibility: 'codex_responses',
      codexCompactionExpected: true
    }
  })
  const compactionInspector = new OpenAIStreamInspector()
  const item = Buffer.from('event: response.output_item.done\ndata: {"type":"response.output_item.done","output_index":0,"item":{"id":"item_compaction","type":"compaction","status":"completed","encrypted_content":"ctx"}}\n\n', 'utf8')
  const completed = Buffer.from('event: response.completed\ndata: {"type":"response.completed","response":{"id":"resp_compaction","status":"completed"}}\n\n', 'utf8')
  const compactionParseCount = countJsonParses(() => {
    assert.equal(compactionInterceptor.pushChunk(item).chunks.length, 0, 'compact item 应等待完成事件')
    const released = compactionInterceptor.pushChunk(completed)
    assert.equal(released.chunks.length, 2, 'compact 完成后应释放全部原始事件')
    inspectOutput(compactionInterceptor, compactionInspector, released.chunks)
  })
  const compactionInspection = compactionInspector.finish()
  assert.equal(compactionParseCount, 2, 'Codex compaction 多事件释放后不得重新解码')
  assert.equal(compactionInspection.eventCount, 2)
  assert.equal(compactionInspection.terminalReceived, true)
}

testOpenAiMultilineCrLfEventIsParsedOnce()
testAnthropicAndGeminiInspectorsReuseParsedEvents()
testEofIncompleteEventKeepsFallbackSemanticsAndSharesParse()
testEmptyDataAndOversizedEventsKeepInspectorBoundaries()
testDecodedByteExpansionAndBareCrKeepLegacyFallback()
testDeferredEventsKeepSharedParseIdentity()

console.log('gateway SSE shared event parse regression passed')
