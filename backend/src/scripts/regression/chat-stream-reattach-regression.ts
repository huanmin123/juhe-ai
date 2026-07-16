import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { ChatGenerationRunner, type ChatGenerationEvent } from '../../modules/chat/chat-generation-runner.js'
import { ChatGenerationRegistry } from '../../modules/chat/chat-generation-registry.js'

const source = readFileSync('src/modules/chat/chat.routes.ts', 'utf8')
assert.match(source, /ChatGenerationRegistry/, 'chat 路由必须使用服务端 generation registry')
assert.match(source, /streams\/:turnId/, '必须提供活动轮次重附着 SSE 路由')
assert.match(source, /registry\.start\(/, 'accept 成功后必须同步登记 runner')
assert.doesNotMatch(source, /res\.once\('close'[\s\S]{0,180}controller\.abort\(\)/, 'accept 后 response close 不得 abort 服务端 runner')

let releaseExecution!: () => void
const executionGate = new Promise<void>((resolve) => { releaseExecution = resolve })
let upstreamCalls = 0
const registry = new ChatGenerationRegistry()
const runner = new ChatGenerationRunner({
  identity: { ownerId: 'owner', conversationId: 'conversation', turnId: 'turn', assistantMessageId: 'assistant' },
  execute: async ({ publish }) => {
    upstreamCalls += 1
    publish('message.delta', { messageId: 'assistant', delta: 'first' }, { contentTextDelta: 'first' })
    await executionGate
    publish('message.delta', { messageId: 'assistant', delta: 'second' }, { contentTextDelta: 'second' })
    return { status: 'completed', data: { messageId: 'assistant' } }
  }
})
assert.equal(registry.start(runner), true)
const detachedEvents: ChatGenerationEvent[] = []
registry.subscribe(runner.identity, { trySend: (event) => { detachedEvents.push(event); return event.type !== 'message.delta' } })
await new Promise<void>((resolve) => setImmediate(resolve))
const attachedEvents: ChatGenerationEvent[] = []
assert.equal(registry.subscribe(runner.identity, { trySend: (event) => { attachedEvents.push(event) } }), true)
assert.equal(attachedEvents[0]?.type, 'message.snapshot')
assert.equal(attachedEvents[0]?.data.assistant.contentText, 'first')
releaseExecution()
await runner.completion
assert.equal(upstreamCalls, 1, '重新附着不得重复调用上游')
assert(attachedEvents.some((event) => event.type === 'message.delta' && event.data.delta === 'second'))
assert.equal(attachedEvents.at(-1)?.type, 'message.completed')
console.log('chat stream reattach regression contract passed')
