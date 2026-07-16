import assert from 'node:assert/strict'

import { ChatCacheBroadcast, isChatCacheBroadcastPayload } from '../../views/chat/chatCacheBroadcast'

class FakeChannel {
  static last?: FakeChannel
  readonly sent: unknown[] = []
  onmessage: ((event: MessageEvent) => void) | null = null
  closed = false
  constructor(readonly name: string) { FakeChannel.last = this }
  postMessage(value: unknown): void { this.sent.push(structuredClone(value)) }
  close(): void { this.closed = true }
  emit(value: unknown): void { this.onmessage?.({ data: value } as MessageEvent) }
}

assert.equal(isChatCacheBroadcastPayload({ systemAccountId: 'account_1', conversationId: 'conv-1', messageRevision: 1 }), true)
for (const value of [
  { systemAccountId: '', conversationId: 'c', messageRevision: 1 },
  { systemAccountId: 'a', conversationId: '../c', messageRevision: 1 },
  { systemAccountId: 'a', conversationId: 'c', messageRevision: 1.5 },
  { systemAccountId: 'a', conversationId: 'c', messageRevision: 1, content: 'secret' }
]) assert.equal(isChatCacheBroadcastPayload(value), false)

const received: unknown[] = []
const broadcast = new ChatCacheBroadcast({ channelFactory: (name) => new FakeChannel(name) })
broadcast.subscribe((payload) => { received.push(payload); throw new Error('listener isolation') })
broadcast.subscribe((payload) => received.push(payload))
broadcast.publish({ systemAccountId: 'account_1', conversationId: 'conv_1', messageRevision: 2, content: 'must drop' } as never)
assert.deepEqual(FakeChannel.last?.sent, [{ systemAccountId: 'account_1', conversationId: 'conv_1', messageRevision: 2 }])
FakeChannel.last?.emit({ systemAccountId: 'account_1', conversationId: 'conv_1', messageRevision: 3 })
assert.equal(received.length, 2)
FakeChannel.last?.emit({ systemAccountId: 'account_1', conversationId: 'conv_1', messageRevision: 2, content: 'secret' })
assert.equal(received.length, 2)
broadcast.close(); broadcast.close()
assert.equal(FakeChannel.last?.closed, true)

const unsupported = new ChatCacheBroadcast({ channelFactory: undefined })
assert.equal(unsupported.enabled, false)
unsupported.publish({ systemAccountId: 'a', conversationId: 'c', messageRevision: 1 })
unsupported.close()
const failed = new ChatCacheBroadcast({ channelFactory: () => { throw Object.assign(new Error('denied'), { name: 'SecurityError' }) } })
assert.equal(failed.enabled, false)

console.log('AI 问答缓存广播回归通过')
