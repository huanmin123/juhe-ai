import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

import { ChatGenerationRegistry } from '../../modules/chat/chat-generation-registry.js'
import {
  ChatGenerationRunner,
  type ChatGenerationEvent,
  type ChatGenerationSubscriber
} from '../../modules/chat/chat-generation-runner.js'
import { collectChatResponsesSse } from '../../modules/chat/chat-responses-sse.js'
import { createChatSseSubscriber, writeChatSseEvent } from '../../modules/chat/chat-sse-subscriber.js'

type JsonRecord = Record<string, unknown>

const repoRoot = fileURLToPath(new URL('../../../../', import.meta.url))
const goldenPath = fileURLToPath(new URL('../../../../testdata/ai-chat-contract/v1/sse.jsonl', import.meta.url))
const frontendTypesSource = readFileSync(`${repoRoot}/frontend/src/types/domain/chat.ts`, 'utf8')
const frontendStreamSource = readFileSync(`${repoRoot}/frontend/src/views/chat/chatStream.ts`, 'utf8')
const frontendRuntimeSource = readFileSync(`${repoRoot}/frontend/src/views/chat/chatGenerationRuntime.ts`, 'utf8')
const frontendSyncSource = readFileSync(`${repoRoot}/frontend/src/views/chat/chatConversationSync.ts`, 'utf8')
const runnerSource = readFileSync(`${repoRoot}/backend/src/modules/chat/chat-generation-runner.ts`, 'utf8')
const routesSource = readFileSync(`${repoRoot}/backend/src/modules/chat/chat.routes.ts`, 'utf8')
const subscriberSource = readFileSync(`${repoRoot}/backend/src/modules/chat/chat-sse-subscriber.ts`, 'utf8')
const responsesSource = readFileSync(`${repoRoot}/backend/src/modules/chat/chat-responses-sse.ts`, 'utf8')
const frontendTypesAst = parseTypeScript(frontendTypesSource, 'frontend/src/types/domain/chat.ts')
const frontendSyncAst = parseTypeScript(frontendSyncSource, 'frontend/src/views/chat/chatConversationSync.ts')
const frontendRuntimeAst = parseTypeScript(frontendRuntimeSource, 'frontend/src/views/chat/chatGenerationRuntime.ts')
const frontendStreamAst = parseTypeScript(frontendStreamSource, 'frontend/src/views/chat/chatStream.ts')
const responsesAst = parseTypeScript(responsesSource, 'backend/src/modules/chat/chat-responses-sse.ts')
const routesAst = parseTypeScript(routesSource, 'backend/src/modules/chat/chat.routes.ts')
const subscriberAst = parseTypeScript(subscriberSource, 'backend/src/modules/chat/chat-sse-subscriber.ts')

const actual: JsonRecord[] = [
  {
    kind: 'meta',
    contract: 'ai-chat-sse',
    version: 1,
    productionDynamicValueFieldsNotFrozen: [
      'lastSemanticActivityAt',
      'serverTime',
      'completedAt',
      'traceId',
      'userMessage.createdAt',
      'userMessage.expiresAt',
      'assistantMessage.createdAt',
      'assistantMessage.expiresAt'
    ]
  },
  runnerEventSetRecord(),
  ...extractFrontendEventSchemas(),
  frontendPayloadContractRecord(),
  ...extractResponsesEventSchemas(),
  submissionContractRecord(),
  syncContractRecord(),
  runtimeContractRecord(),
  nodeBoundaryRecord(),
  await subscriberBoundaryRecord(),
  await responsesBridgeRecord(),
  await responsesFailedBoundaryRecord(),
  ...(await completedSseRecords()),
  ...(await failedSseRecords()),
  ...(await stoppedSseRecords()),
  await attachLifecycleRecord(),
  await slowSubscriberRecord()
]

const expected = readJsonLines(goldenPath)
assert.equal(actual.length, expected.length, `Chat SSE golden 记录数漂移：expected=${expected.length}, actual=${actual.length}`)
for (let index = 0; index < actual.length; index += 1) {
  assert.deepEqual(actual[index], expected[index], `Chat SSE golden 第 ${index + 1} 行漂移`)
}

const frontendEventCount = actual.filter((record) => record.kind === 'frontend-event-schema').length
const runnerEventCount = (actual.find((record) => record.kind === 'runner-event-set')?.events as unknown[]).length
console.log(`Chat SSE golden regression passed: ${actual.length} records, ${frontendEventCount} frontend events, ${runnerEventCount} runner events`)

function runnerEventSetRecord(): JsonRecord {
  const events = new Set<string>()
  for (const match of runnerSource.matchAll(/['`](message\.[a-z_]+|content_block\.[a-z_]+)['`]/gu)) events.add(match[1]!)
  if (/this\.emitEvent\(`message\.\$\{result\.status\}`/u.test(runnerSource)) {
    for (const status of ['completed', 'failed', 'canceled']) events.add(`message.${status}`)
  }
  return { kind: 'runner-event-set', events: [...events].sort() }
}

function extractFrontendEventSchemas(): JsonRecord[] {
  const records: JsonRecord[] = []
  for (const variant of unionVariants(typeAlias(frontendTypesAst, 'ChatStreamEvent').type)) {
    const literal = requireTypeLiteral(variant, 'ChatStreamEvent variant')
    const events = propertyStringValues(literal, 'type')
    const data = requireTypeLiteral(requireProperty(literal, 'data').type, 'ChatStreamEvent data')
    for (const event of events) {
      records.push({
        kind: 'frontend-event-schema',
        event,
        fields: propertyNames(data),
        fieldSignatures: propertySignatures(data, frontendTypesAst)
      })
    }
  }
  return records
}

function frontendPayloadContractRecord(): JsonRecord {
  const blockAlias = typeAlias(frontendTypesAst, 'ChatMessageContentBlock')
  return {
    kind: 'frontend-payload-contract',
    interfaces: ['ChatMessage', 'ChatToolEvent', 'ChatStreamAssistantSnapshot'].map((name) => {
      const declaration = interfaceDeclaration(frontendTypesAst, name)
      return { name, fields: propertySignatures(declaration, frontendTypesAst) }
    }),
    contentBlockVariants: unionVariants(blockAlias.type).map((variant) => {
      const literal = requireTypeLiteral(variant, 'ChatMessageContentBlock variant')
      return { type: propertyStringValues(literal, 'type')[0], fields: propertySignatures(literal, frontendTypesAst) }
    }),
    aliases: ['ChatMessageStatus', 'ChatProcessStatus', 'ChatToolStatus'].map((name) => ({
      name,
      expression: normalizeTypeText(typeAlias(frontendTypesAst, name).type.getText(frontendTypesAst))
    }))
  }
}

function extractResponsesEventSchemas(): JsonRecord[] {
  const records: JsonRecord[] = []
  for (const variant of unionVariants(typeAlias(responsesAst, 'ChatResponsesEvent').type)) {
    const literal = requireTypeLiteral(variant, 'ChatResponsesEvent variant')
    const event = propertyStringValues(literal, 'type')[0]
    assert.ok(event, 'ChatResponsesEvent 必须有 type 字面量')
    const fields = literal.members.filter((member): member is ts.PropertySignature => ts.isPropertySignature(member) && member.name.getText(responsesAst) !== 'type')
    records.push({
      kind: 'responses-event-schema',
      event,
      fields: fields.map((field) => propertyName(field)),
      fieldSignatures: fields.map((field) => propertySignature(field, responsesAst))
    })
  }
  return records
}

function submissionContractRecord(): JsonRecord {
  return {
    kind: 'submission-contract',
    variants: unionVariants(typeAlias(frontendTypesAst, 'ChatSubmissionStatus').type).map((variant) => {
      const literal = requireTypeLiteral(variant, 'ChatSubmissionStatus variant')
      return {
        state: propertyStringValues(literal, 'state')[0],
        fields: propertySignatures(literal, frontendTypesAst)
      }
    })
  }
}

function syncContractRecord(): JsonRecord {
  return {
    kind: 'sync-contract',
    decisions: unionVariantSignatures(frontendSyncAst, 'ChatConversationSyncDecision', 'type'),
    outcomes: unionVariantSignatures(frontendSyncAst, 'ChatConversationSyncOutcome', 'state'),
    sharedResultFields: propertySignatures(interfaceDeclaration(frontendSyncAst, 'ChatConversationSynchronizationResult'), frontendSyncAst)
  }
}

function runtimeContractRecord(): JsonRecord {
  return {
    kind: 'runtime-contract',
    statuses: exportedStringUnion(frontendRuntimeSource, 'ChatGenerationRuntimeStatus'),
    reconciliationReasons: exportedStringUnion(frontendRuntimeSource, 'ChatGenerationReconciliationReason'),
    livenessStates: exportedStringUnion(frontendRuntimeSource, 'ChatGenerationLivenessState'),
    snapshotAllowsEqualBarrier: functionText(frontendStreamAst, 'acceptEventVersion').includes('allowSnapshot ? next < current : next <= current'),
    incrementalRequiresContiguousVersion: functionText(frontendStreamAst, 'acceptEventVersion').includes('next !== current + 1'),
    terminalProjectionReplaysWithoutAttach: /if \(isTerminal\(initialStatus\)\) this\.markTerminal\(turn, initialStatus\)[\s\S]{0,120}else this\.scheduleWatchdog/u.test(methodText(frontendRuntimeAst, 'ChatGenerationRuntime', 'attach'))
      && /if \(!isTerminal\(initialStatus\)\) void this\.runAttach/u.test(methodText(frontendRuntimeAst, 'ChatGenerationRuntime', 'attach')),
    successfulStopConvergesCanceled: /if \(this\.turns\.get\(key\) === turn && \(turn\.status === 'preparing' \|\| turn\.status === 'running'\)\) \{[\s\S]{0,120}this\.markTerminal\(turn, 'canceled'\)/u.test(methodText(frontendRuntimeAst, 'ChatGenerationRuntime', 'stop'))
  }
}

function nodeBoundaryRecord(): JsonRecord {
  const initialHandler = routeHandlerText(routesAst, 'post', '/conversations/:conversationId/stream')
  const attachHandler = routeHandlerText(routesAst, 'get', '/conversations/:conversationId/streams/:turnId')
  const subscriber = functionText(subscriberAst, 'createChatSseSubscriber')
  const initialStartedIndex = initialHandler.indexOf("writeChatSseEvent(res, 'message.started'")
  const initialSubscribeIndex = initialHandler.indexOf('registry.subscribe(identity, subscriber)')
  const attachSubscribeIndex = attachHandler.indexOf('registry.subscribe(')
  const attachWritesBeforeSubscribe = ['writeChatSseEvent(', 'res.write(']
    .map((call) => attachHandler.indexOf(call))
    .some((index) => index >= 0 && index < attachSubscribeIndex)
  return {
    kind: 'node-boundary',
    initialStartedBeforeSnapshot: initialStartedIndex >= 0 && initialSubscribeIndex >= 0 && initialStartedIndex < initialSubscribeIndex,
    attachBeginsWithRegistrySnapshot: attachSubscribeIndex >= 0 && !attachWritesBeforeSubscribe,
    subscriberRunnerVersionOverridesPayload: subscriber.includes('{ ...event.data, eventVersion: event.eventVersion }'),
    subscriberTerminalEvents: ['message.completed', 'message.failed', 'message.canceled'].filter((event) => subscriber.includes(`event.type === '${event}'`))
  }
}

async function subscriberBoundaryRecord(): Promise<JsonRecord> {
  const chunks: string[] = []
  let detachCount = 0
  let endCount = 0
  const response = {
    destroyed: false,
    writableEnded: false,
    write(chunk: string): boolean { chunks.push(chunk); return true },
    end(): void { endCount += 1; this.writableEnded = true }
  }
  const subscriber = createChatSseSubscriber({ response, detach: () => { detachCount += 1 } })
  assert.equal(subscriber.trySend({
    type: 'message.delta',
    eventVersion: 7,
    data: { messageId: 'assistant-boundary', delta: 'versioned', eventVersion: 999 }
  }), true)
  assert.equal(subscriber.trySend({
    type: 'message.completed',
    eventVersion: 8,
    data: { messageId: 'assistant-boundary' }
  }), true)
  await tick()
  const events = chunks.map(parseSseChunk)
  return {
    kind: 'subscriber-boundary',
    events,
    runnerVersionOverridesPayload: (events[0]?.data as JsonRecord).eventVersion === 7,
    detachCount,
    endCount
  }
}

async function responsesBridgeRecord(): Promise<JsonRecord> {
  const text = [
    sse('response.reasoning_summary_text.delta', { type: 'response.reasoning_summary_text.delta', delta: '思考' }),
    sse('response.output_item.done', { type: 'response.output_item.done', item: { type: 'reasoning', id: 'reasoning-1', summary: [] } }),
    sse('response.output_item.added', { type: 'response.output_item.added', item: { type: 'web_search_call', id: 'tool-1' } }),
    sse('response.function_call_arguments.delta', { type: 'response.function_call_arguments.delta', delta: '{"q":"合同"}' }),
    sse('response.output_item.done', { type: 'response.output_item.done', item: { type: 'web_search_call', id: 'tool-1', status: 'completed' } }),
    sse('response.output_item.added', { type: 'response.output_item.added', item: { type: 'image_generation_call', id: 'image-failed', status: 'started' } }),
    sse('response.image_generation_call.failed', { type: 'response.image_generation_call.failed', item: { type: 'image_generation_call', id: 'image-failed', status: 'failed' } }),
    sse('response.output_item.added', { type: 'response.output_item.added', item: { type: 'image_generation_call', id: 'image-ok', status: 'started' } }),
    sse('response.image_generation_call.in_progress', { type: 'response.image_generation_call.in_progress', item: { type: 'image_generation_call', id: 'image-ok', status: 'in_progress', partial_image: 'not-on-event' } }),
    sse('response.image_generation_call.completed', { type: 'response.image_generation_call.completed', item: { type: 'image_generation_call', id: 'image-ok', status: 'completed', result: 'iVBORw==', revised_prompt: 'cat' } }),
    sse('response.output_text.delta', { type: 'response.output_text.delta', delta: '完成' }),
    sse('response.completed', { type: 'response.completed', response: { status: 'completed', usage: { input_tokens: 7, output_tokens: 3 } } })
  ].join('')
  const events: Array<{ type: string; callId?: unknown; status?: unknown; itemKeys?: string[] }> = []
  let sinkCallId = ''
  let sinkBytes = 0
  const result = await collectChatResponsesSse(oneChunk(text), (event) => {
    if ('item' in event) {
      events.push({
        type: event.type,
        callId: event.item.callId,
        status: event.item.status,
        itemKeys: Object.keys(event.item).sort()
      })
      return
    }
    events.push({ type: event.type })
  }, undefined, undefined, async (input) => {
    sinkCallId = input.callId
    for await (const chunk of input.chunks) sinkBytes += chunk.length
  })
  return {
    kind: 'responses-bridge',
    eventTypes: events.map((event) => event.type),
    imageEvents: events.filter((event) => event.type.startsWith('image_')),
    content: result.content,
    usage: { inputTokens: result.inputTokens, outputTokens: result.outputTokens },
    imageSink: { callId: sinkCallId, bytes: sinkBytes }
  }
}

async function responsesFailedBoundaryRecord(): Promise<JsonRecord> {
  const events: JsonRecord[] = []
  let rejection = ''
  try {
    await collectChatResponsesSse(oneChunk(sse('response.failed', {
      type: 'response.failed',
      response: { status: 'failed', error: { code: 'upstream_failed', message: 'contract failure' } }
    })), (event) => {
      events.push(event.type === 'failed' ? { type: event.type, error: event.error } : { type: event.type })
    })
  } catch (error) {
    rejection = error instanceof Error ? error.message : String(error)
  }
  assert.equal(events[0]?.type, 'failed', 'response.failed 必须投影 failed 事件')
  assert.match(rejection, /缺少 response\.completed/u, 'response.failed 当前仍必须以缺少 completed 拒绝收集结果')
  return { kind: 'responses-failed-boundary', events, rejection }
}

async function completedSseRecords(): Promise<JsonRecord[]> {
  const identity = { ownerId: 'owner-golden', conversationId: 'conversation-complete', turnId: 'turn-complete', assistantMessageId: 'assistant-complete' }
  const runner = new ChatGenerationRunner({
    identity,
    execute: async ({ publish }) => {
      publish('message.delta', { messageId: identity.assistantMessageId, delta: '正文 A' }, { contentTextDelta: '正文 A' })
      publish('message.delta', { messageId: identity.assistantMessageId, delta: '正文 B' }, { contentTextDelta: '正文 B' })
      publish('reasoning.delta', { messageId: identity.assistantMessageId, delta: '思考 1' }, { reasoningTextDelta: '思考 1' })
      publish('reasoning.delta', { messageId: identity.assistantMessageId, delta: '思考 2' }, { reasoningTextDelta: '思考 2' })
      publish('reasoning.completed', { messageId: identity.assistantMessageId }, { reasoningCompleted: true })
      publish('tool.started', { messageId: identity.assistantMessageId }, { toolEvent: { id: 'search-1', toolType: 'web_search', status: 'started', item: { query: '合同' } } })
      publish('tool.updated', { messageId: identity.assistantMessageId }, { toolEvent: { id: 'search-1', toolType: 'web_search', status: 'updated', item: { phase: 'searching' } } })
      publish('tool.completed', { messageId: identity.assistantMessageId }, { toolEvent: { id: 'search-1', toolType: 'web_search', status: 'completed', item: { result: 'done' } } })
      publish('image.started', { messageId: identity.assistantMessageId }, { imageEvent: { id: 'image-1', status: 'started', item: { assetId: 'asset-1' } } })
      publish('image.updated', { messageId: identity.assistantMessageId }, { imageEvent: { id: 'image-1', status: 'updated', item: { assetId: 'asset-1', mimeType: 'image/png', width: 1024, height: 1024, revisedPrompt: 'cat' } } })
      publish('image.completed', { messageId: identity.assistantMessageId }, { imageEvent: { id: 'image-1', status: 'completed', item: { assetId: 'asset-1', mimeType: 'image/png', width: 1024, height: 1024, revisedPrompt: 'cat' } } })
      publish('tool.started', { messageId: identity.assistantMessageId }, { toolEvent: { id: 'image-tool-1', toolType: 'generate_image', status: 'started', item: { prompt: 'cat' } } })
      publish('tool.completed', { messageId: identity.assistantMessageId }, { toolEvent: { id: 'image-tool-1', toolType: 'generate_image', status: 'completed', item: { assetId: 'asset-1' } } })
      publish('message.delta', { messageId: identity.assistantMessageId, delta: '正文 C' }, { contentTextDelta: '正文 C' })
      return { status: 'completed', data: { messageId: identity.assistantMessageId, finishReason: 'stop', traceId: 'trace-contract' } }
    }
  })
  const registry = new ChatGenerationRegistry()
  const capture = sseCapture()
  assert.equal(registry.start(runner), true)
  assert.equal(writeChatSseEvent(capture.response, 'message.started', startedPayload(identity)), true)
  let subscriber!: ChatGenerationSubscriber
  subscriber = createChatSseSubscriber({ response: capture.response, detach: () => registry.unsubscribe(identity, subscriber) })
  assert.equal(registry.subscribe(identity, subscriber), true)
  await runner.completion
  await tick()
  const events = capture.events()
  assert.deepEqual(events.slice(1).map((event) => (event.data as JsonRecord).eventVersion), Array.from({ length: 16 }, (_, index) => index), 'completed SSE eventVersion 必须从 snapshot 0 严格连续到 terminal 15')
  const terminal = registry.snapshot(identity)
  return [
    { kind: 'sse-scenario', scenario: 'completed-content-tool-image', events },
    {
      kind: 'terminal-status-replay',
      scenario: 'completed',
      snapshot: terminal.state === 'missing' ? terminal : {
        state: terminal.state,
        eventVersion: terminal.eventVersion,
        assistantMessageId: terminal.assistantMessageId,
        hasLastSemanticActivityAt: typeof terminal.lastSemanticActivityAt === 'string'
      },
      subscribeAfterTerminal: registry.subscribe(identity, { trySend: () => true })
    }
  ]
}

async function failedSseRecords(): Promise<JsonRecord[]> {
  const identity = { ownerId: 'owner-golden', conversationId: 'conversation-failed', turnId: 'turn-failed', assistantMessageId: 'assistant-failed' }
  const runner = new ChatGenerationRunner({
    identity,
    execute: async ({ publish }) => {
      publish('reasoning.delta', {}, { reasoningTextDelta: 'unfinished' })
      publish('tool.started', {}, { toolEvent: { id: 'failed-tool', toolType: 'web_search', status: 'started' } })
      return { status: 'failed', data: { messageId: identity.assistantMessageId, code: 'upstream_stream_failed', message: '模型响应中断，请重新发送' } }
    }
  })
  const registry = new ChatGenerationRegistry()
  const capture = sseCapture()
  registry.start(runner)
  let subscriber!: ChatGenerationSubscriber
  subscriber = createChatSseSubscriber({ response: capture.response, detach: () => registry.unsubscribe(identity, subscriber) })
  registry.subscribe(identity, subscriber)
  await runner.completion
  await tick()
  const terminal = registry.snapshot(identity)
  return [
    { kind: 'sse-scenario', scenario: 'failed-terminalizes-active-blocks', events: capture.events() },
    {
      kind: 'terminal-status-replay',
      scenario: 'failed',
      snapshot: terminal.state === 'missing' ? terminal : {
        state: terminal.state,
        eventVersion: terminal.eventVersion,
        assistantMessageId: terminal.assistantMessageId,
        hasLastSemanticActivityAt: typeof terminal.lastSemanticActivityAt === 'string'
      },
      subscribeAfterTerminal: registry.subscribe(identity, { trySend: () => true })
    }
  ]
}

async function stoppedSseRecords(): Promise<JsonRecord[]> {
  const identity = { ownerId: 'owner-golden', conversationId: 'conversation-stopped', turnId: 'turn-stopped', assistantMessageId: 'assistant-stopped' }
  const runner = new ChatGenerationRunner({
    identity,
    execute: async ({ signal }) => {
      if (!signal.aborted) await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve(), { once: true }))
      return { status: 'canceled', data: { messageId: identity.assistantMessageId, traceId: 'trace-stopped' } }
    }
  })
  const registry = new ChatGenerationRegistry()
  const capture = sseCapture()
  registry.start(runner)
  let subscriber!: ChatGenerationSubscriber
  subscriber = createChatSseSubscriber({ response: capture.response, detach: () => registry.unsubscribe(identity, subscriber) })
  registry.subscribe(identity, subscriber)
  const stopped = registry.stop(identity)
  const repeatedStop = registry.stop(identity)
  const mismatchedStop = registry.stop({ ...identity, turnId: 'other-turn' })
  await runner.completion
  await tick()
  const terminal = registry.snapshot(identity)
  return [
    { kind: 'sse-scenario', scenario: 'stop-canceled', events: capture.events() },
    {
      kind: 'stop-lifecycle',
      stopped,
      repeatedStop,
      mismatchedStop,
      runnerState: runner.state,
      terminalEventVersion: terminal.state === 'missing' ? null : terminal.eventVersion
    },
    {
      kind: 'terminal-status-replay',
      scenario: 'canceled',
      snapshot: terminal.state === 'missing' ? terminal : {
        state: terminal.state,
        eventVersion: terminal.eventVersion,
        assistantMessageId: terminal.assistantMessageId,
        hasLastSemanticActivityAt: typeof terminal.lastSemanticActivityAt === 'string'
      },
      subscribeAfterTerminal: registry.subscribe(identity, { trySend: () => true })
    }
  ]
}

async function attachLifecycleRecord(): Promise<JsonRecord> {
  const gate = deferred<void>()
  const identity = { ownerId: 'owner-golden', conversationId: 'conversation-attach', turnId: 'turn-attach', assistantMessageId: 'assistant-attach' }
  const runner = new ChatGenerationRunner({
    identity,
    execute: async ({ publish }) => {
      publish('message.delta', {}, { contentTextDelta: 'first' })
      await gate.promise
      publish('message.delta', {}, { contentTextDelta: 'second' })
      return { status: 'completed', data: { messageId: identity.assistantMessageId } }
    }
  })
  const registry = new ChatGenerationRegistry()
  const initial = eventCollector()
  const attached = eventCollector()
  registry.start(runner)
  registry.subscribe(identity, initial.subscriber)
  await tick()
  registry.subscribe(identity, attached.subscriber)
  gate.resolve()
  await runner.completion
  return {
    kind: 'attach-lifecycle',
    initial: eventKeys(initial.events),
    attached: eventKeys(attached.events),
    attachedSnapshotText: attached.events[0]?.data.assistant.contentText,
    attachedSnapshotBlocks: attached.events[0]?.data.assistant.contentBlocks.map((block: { type: string }) => block.type)
  }
}

async function slowSubscriberRecord(): Promise<JsonRecord> {
  const gate = deferred<void>()
  const identity = { ownerId: 'owner-golden', conversationId: 'conversation-slow', turnId: 'turn-slow', assistantMessageId: 'assistant-slow' }
  const runner = new ChatGenerationRunner({
    identity,
    execute: async ({ publish }) => {
      publish('message.delta', {}, { contentTextDelta: 'before-detach' })
      await gate.promise
      publish('message.delta', {}, { contentTextDelta: 'after-detach' })
      return { status: 'completed', data: { messageId: identity.assistantMessageId } }
    }
  })
  const registry = new ChatGenerationRegistry()
  const writes: string[] = []
  let detachCount = 0
  let endCount = 0
  const response = {
    destroyed: false,
    writableEnded: false,
    write(chunk: string): boolean { writes.push(chunk); return !chunk.startsWith('event: content_block.started') },
    end(): void { endCount += 1; this.writableEnded = true }
  }
  registry.start(runner)
  let slow!: ChatGenerationSubscriber
  slow = createChatSseSubscriber({
    response,
    detach: () => { detachCount += 1; registry.unsubscribe(identity, slow) }
  })
  registry.subscribe(identity, slow)
  await tick()
  const attached = eventCollector()
  registry.subscribe(identity, attached.subscriber)
  gate.resolve()
  await runner.completion
  return {
    kind: 'slow-subscriber',
    writeAttempts: writes.map((chunk) => parseSseChunk(chunk).event),
    detachCount,
    endCount,
    attached: eventKeys(attached.events),
    attachedSnapshotText: attached.events[0]?.data.assistant.contentText,
    runnerState: runner.state
  }
}

function startedPayload(identity: { conversationId: string; turnId: string; assistantMessageId: string }): JsonRecord {
  const common = {
    conversationId: identity.conversationId,
    turnId: identity.turnId,
    model: 'contract-model',
    createdAt: '2026-07-22T00:00:00.000Z',
    expiresAt: '2026-07-29T00:00:00.000Z'
  }
  return {
    turnId: identity.turnId,
    userMessage: { id: 'user-complete', ...common, sequenceNo: 1, role: 'user', status: 'completed', contentText: '开始' },
    assistantMessage: { id: identity.assistantMessageId, ...common, sequenceNo: 2, role: 'assistant', status: 'streaming', contentText: '' }
  }
}

function sseCapture(): { response: { destroyed: boolean; writableEnded: boolean; write(chunk: string): boolean; end(): void }; events(): JsonRecord[] } {
  const chunks: string[] = []
  const response = {
    destroyed: false,
    writableEnded: false,
    write(chunk: string): boolean { chunks.push(chunk); return true },
    end(): void { this.writableEnded = true }
  }
  return { response, events: () => chunks.map(parseSseChunk) }
}

function parseSseChunk(chunk: string): JsonRecord {
  const event = requireMatch(chunk, /^event:\s*([^\r\n]+)/mu, 'SSE event')[1]!.trim()
  const data = JSON.parse(requireMatch(chunk, /^data:\s*(.+)$/mu, 'SSE data')[1]!) as JsonRecord
  return { event, data }
}

function eventCollector(): { events: ChatGenerationEvent[]; subscriber: ChatGenerationSubscriber } {
  const events: ChatGenerationEvent[] = []
  return { events, subscriber: { trySend: (event) => { events.push(event); return true } } }
}

function eventKeys(events: ChatGenerationEvent[]): string[] {
  return events.map((event) => `${event.type}@${event.eventVersion}`)
}

function parseTypeScript(source: string, fileName: string): ts.SourceFile {
  return ts.createSourceFile(fileName, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS)
}

function typeAlias(sourceFile: ts.SourceFile, name: string): ts.TypeAliasDeclaration {
  const declaration = sourceFile.statements.find((statement): statement is ts.TypeAliasDeclaration => (
    ts.isTypeAliasDeclaration(statement) && statement.name.text === name
  ))
  assert.ok(declaration, `无法提取 type ${name}`)
  return declaration
}

function interfaceDeclaration(sourceFile: ts.SourceFile, name: string): ts.InterfaceDeclaration {
  const declaration = sourceFile.statements.find((statement): statement is ts.InterfaceDeclaration => (
    ts.isInterfaceDeclaration(statement) && statement.name.text === name
  ))
  assert.ok(declaration, `无法提取 interface ${name}`)
  return declaration
}

function unionVariants(node: ts.TypeNode): ts.TypeNode[] {
  const unwrapped = ts.isParenthesizedTypeNode(node) ? node.type : node
  return ts.isUnionTypeNode(unwrapped) ? [...unwrapped.types] : [unwrapped]
}

function requireTypeLiteral(node: ts.TypeNode | undefined, label: string): ts.TypeLiteralNode {
  assert.ok(node, `无法提取 ${label}`)
  const unwrapped = ts.isParenthesizedTypeNode(node) ? node.type : node
  assert.ok(ts.isTypeLiteralNode(unwrapped), `${label} 不是 type literal`)
  return unwrapped
}

function requireProperty(node: ts.TypeLiteralNode, name: string): ts.PropertySignature {
  const property = node.members.find((member): member is ts.PropertySignature => (
    ts.isPropertySignature(member) && propertyName(member) === name
  ))
  assert.ok(property, `无法提取字段 ${name}`)
  return property
}

function propertyStringValues(node: ts.TypeLiteralNode, name: string): string[] {
  const property = requireProperty(node, name)
  assert.ok(property.type, `字段 ${name} 缺少类型`)
  return unionVariants(property.type).map((variant) => {
    assert.ok(ts.isLiteralTypeNode(variant) && ts.isStringLiteral(variant.literal), `字段 ${name} 必须是字符串字面量`)
    return variant.literal.text
  })
}

function propertyNames(node: { members: ts.NodeArray<ts.TypeElement> }): string[] {
  return node.members.filter(ts.isPropertySignature).map(propertyName)
}

function propertySignatures(node: { members: ts.NodeArray<ts.TypeElement> }, sourceFile: ts.SourceFile): string[] {
  return node.members.filter(ts.isPropertySignature).map((property) => propertySignature(property, sourceFile))
}

function propertyName(property: ts.PropertySignature): string {
  return `${property.name.getText()}${property.questionToken ? '?' : ''}`
}

function propertySignature(property: ts.PropertySignature, sourceFile: ts.SourceFile): string {
  assert.ok(property.type, `字段 ${property.name.getText(sourceFile)} 缺少类型`)
  return `${propertyName(property)}: ${normalizeTypeText(property.type.getText(sourceFile))}`
}

function normalizeTypeText(value: string): string {
  return value.replace(/\s+/gu, ' ').trim()
}

function unionVariantSignatures(sourceFile: ts.SourceFile, aliasName: string, discriminator: string): JsonRecord[] {
  return unionVariants(typeAlias(sourceFile, aliasName).type).map((variant) => {
    const literals = findTypeLiterals(variant)
    const discriminating = literals.map((literal) => {
      try { return propertyStringValues(literal, discriminator)[0] } catch { return undefined }
    }).find((value) => value !== undefined)
    assert.ok(discriminating, `${aliasName} variant 缺少 ${discriminator}`)
    return {
      [discriminator]: discriminating,
      signature: normalizeTypeText(variant.getText(sourceFile)),
      fields: literals.flatMap((literal) => propertySignatures(literal, sourceFile))
    }
  })
}

function findTypeLiterals(node: ts.TypeNode): ts.TypeLiteralNode[] {
  const unwrapped = ts.isParenthesizedTypeNode(node) ? node.type : node
  if (ts.isTypeLiteralNode(unwrapped)) return [unwrapped]
  if (ts.isIntersectionTypeNode(unwrapped)) return unwrapped.types.flatMap(findTypeLiterals)
  return []
}

function functionText(sourceFile: ts.SourceFile, name: string): string {
  const declaration = sourceFile.statements.find((statement): statement is ts.FunctionDeclaration => (
    ts.isFunctionDeclaration(statement) && statement.name?.text === name
  ))
  assert.ok(declaration, `无法提取 function ${name}`)
  return declaration.getText(sourceFile)
}

function methodText(sourceFile: ts.SourceFile, className: string, methodName: string): string {
  const declaration = sourceFile.statements.find((statement): statement is ts.ClassDeclaration => (
    ts.isClassDeclaration(statement) && statement.name?.text === className
  ))
  assert.ok(declaration, `无法提取 class ${className}`)
  const method = declaration.members.find((member): member is ts.MethodDeclaration => (
    ts.isMethodDeclaration(member) && member.name.getText(sourceFile) === methodName
  ))
  assert.ok(method, `无法提取 ${className}.${methodName}`)
  return method.getText(sourceFile)
}

function routeHandlerText(sourceFile: ts.SourceFile, methodName: string, path: string): string {
  let handler: ts.Expression | undefined
  const visit = (node: ts.Node): void => {
    if (handler) return
    if (ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression)
      && node.expression.name.text === methodName
      && ts.isStringLiteral(node.arguments[0]) && node.arguments[0].text === path) {
      handler = node.arguments[1]
      return
    }
    ts.forEachChild(node, visit)
  }
  visit(sourceFile)
  assert.ok(handler, `无法提取路由 ${methodName.toUpperCase()} ${path}`)
  return handler.getText(sourceFile)
}

function readJsonLines(path: string): JsonRecord[] {
  return readFileSync(path, 'utf8').split(/\r?\n/u).filter((line) => line.trim().length > 0).map((line, index) => {
    try { return JSON.parse(line) as JsonRecord } catch (error) { throw new Error(`Chat SSE golden 第 ${index + 1} 行不是合法 JSON`, { cause: error }) }
  })
}

function quotedValues(source: string): string[] {
  return [...source.matchAll(/'([^']+)'/gu)].map((match) => match[1]!)
}

function exportedStringUnion(source: string, name: string): string[] {
  const expression = requireMatch(source, new RegExp(`export type ${name} = ([^\\r\\n]+)`, 'u'), name)[1]!
  return quotedValues(expression)
}

function requireMatch(source: string, pattern: RegExp, label: string): RegExpMatchArray {
  const match = source.match(pattern)
  assert.ok(match, `无法提取 ${label}`)
  return match
}

function sse(event: string, data: unknown): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
}

async function* oneChunk(text: string): AsyncGenerator<Uint8Array> {
  yield new TextEncoder().encode(text)
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T | PromiseLike<T>): void } {
  let resolve!: (value: T | PromiseLike<T>) => void
  return { promise: new Promise<T>((next) => { resolve = next }), resolve }
}

function tick(): Promise<void> {
  return new Promise((resolve) => setImmediate(resolve))
}
