import { describe, expect, it } from 'vitest'

import type { ChatMessage } from '@/types/domain/chat'
import { ChatLocalCache, cloneVisibleChatMessage, type ChatCachePutContext, type ChatCachePutResult, type ChatCacheSyncSnapshot, type ChatCacheConversationHead, type ChatCacheEvictionCursor, type ChatCacheSyncCommitResult, type ChatRunningTurn, type ChatLocalCacheStorageAdapter } from './chatLocalCache'

class MemoryStorageAdapter implements ChatLocalCacheStorageAdapter {
  readonly heads = new Map<string, ChatCacheConversationHead>()
  readonly messages = new Map<string, ChatMessage>()

  private key(account: string, conversation: string): string { return `${account}\u0000${conversation}\u0000` }

  async readConversation(account: string, conversation: string) {
    const prefix = this.key(account, conversation)
    return {
      head: this.heads.get(`${account}\u0000${conversation}`),
      messages: [...this.messages.entries()].filter(([id]) => id.startsWith(prefix)).map(([, value]) => structuredClone(value)).sort((a, b) => a.sequenceNo - b.sequenceNo),
      runningTurn: undefined
    }
  }

  async putHead(head: ChatCacheConversationHead): Promise<void> { this.heads.set(`${head.systemAccountId}\u0000${head.conversationId}`, structuredClone(head)) }

  async putMessages(account: string, conversation: string, messages: ChatMessage[], context: ChatCachePutContext): Promise<ChatCachePutResult> {
    const head: ChatCacheConversationHead = this.heads.get(`${account}\u0000${conversation}`) ?? { systemAccountId: account, conversationId: conversation, messageRevision: 0, lastAccessAt: context.now, byteSize: 0 }
    for (const message of messages) {
      this.messages.set(`${this.key(account, conversation)}${message.sequenceNo}`, structuredClone(message))
      head.byteSize += JSON.stringify(message).length
    }
    this.heads.set(`${account}\u0000${conversation}`, head)
    return { head, totalBytes: head.byteSize }
  }

  async commitSyncSnapshot(snapshot: ChatCacheSyncSnapshot): Promise<ChatCacheSyncCommitResult> {
    const prefix = this.key(snapshot.systemAccountId, snapshot.conversationId)
    for (const id of [...this.messages.keys()]) if (id.startsWith(prefix)) this.messages.delete(id)
    const result = await this.putMessages(snapshot.systemAccountId, snapshot.conversationId, snapshot.messages, { now: snapshot.now })
    return { ...result, committed: true }
  }

  async deleteFromSequence(): Promise<void> {}
  async deleteConversation(account: string, conversation: string): Promise<void> {
    const prefix = this.key(account, conversation)
    for (const id of [...this.messages.keys()]) if (id.startsWith(prefix)) this.messages.delete(id)
  }
  async putRunningTurn(): Promise<void> {}
  async removeRunningTurn(): Promise<void> {}
  async touch(): Promise<void> {}
  async clearAccount(): Promise<void> { this.heads.clear(); this.messages.clear() }
  async getTotalBytes(): Promise<number> { return [...this.heads.values()].reduce((total, head) => total + head.byteSize, 0) }
  async listEvictionCandidates(): Promise<ChatCacheConversationHead[]> { return [] }
  async cleanupExpired(): Promise<{ conversations: number; messages: number }> { return { conversations: 0, messages: 0 } }
  close(): void {}
}

function message(sequenceNo: number, text: string): ChatMessage {
  return { id: `m${sequenceNo}`, conversationId: 'c1', turnId: `t${sequenceNo}`, sequenceNo, role: sequenceNo % 2 ? 'user' : 'assistant', status: 'completed', contentText: text, model: 'm', createdAt: '2026-07-16T00:00:00.000Z', expiresAt: '2026-07-20T00:00:00.000Z' }
}

describe('chatLocalCache 克隆层字段保留', () => {
  it('tool_call 与 toolEvents 的 item 必须逐字段保留', () => {
    const item = { query: '北京天气', results: [{ title: '天气网', url: 'https://example.com/weather' }], errorCode: 'search_timeout', errorMessage: '上游超时' }
    const eventItem = { action: { query: '北京天气', type: 'search' } }
    const cloned = cloneVisibleChatMessage({
      ...message(2, '工具调用'),
      contentBlocks: [{ type: 'tool_call', blockId: 'block_tool', order: 1, id: 'tool_1', callId: 'tool_1', toolType: 'web_search_call', status: 'completed', item }],
      toolEvents: [{ id: 'tool_1', type: 'web_search_call', status: 'completed', item: eventItem }]
    })
    expect(cloned?.contentBlocks?.[0]).toEqual({ type: 'tool_call', blockId: 'block_tool', order: 1, id: 'tool_1', callId: 'tool_1', toolType: 'web_search_call', status: 'completed', item })
    expect(cloned?.toolEvents).toEqual([{ id: 'tool_1', type: 'web_search_call', status: 'completed', item: eventItem }])
  })

  it('output_image 的 mimeType/width/height/revisedPrompt 必须逐字段保留', () => {
    const cloned = cloneVisibleChatMessage({
      ...message(2, '生成图片'),
      contentBlocks: [{ type: 'output_image', blockId: 'block_image', order: 2, assetId: 'asset_generated', status: 'completed', mimeType: 'image/png', width: 1024, height: 768, revisedPrompt: '绿色圆形' }]
    })
    expect(cloned?.contentBlocks?.[0]).toEqual({ type: 'output_image', blockId: 'block_image', order: 2, assetId: 'asset_generated', status: 'completed', mimeType: 'image/png', width: 1024, height: 768, revisedPrompt: '绿色圆形' })
  })

  it('缺失新增字段的旧缓存消息仍可克隆且字段保持缺省', () => {
    const cloned = cloneVisibleChatMessage({
      ...message(2, '旧缓存'),
      contentBlocks: [
        { type: 'tool_call', blockId: 'block_tool', order: 1, callId: 'tool_legacy', toolType: 'web_search_call', status: 'completed' },
        { type: 'output_image', blockId: 'block_image', order: 2, assetId: 'asset_legacy', status: 'completed' }
      ],
      toolEvents: [{ id: 'tool_legacy', type: 'web_search_call', status: 'completed' }]
    })
    expect(cloned).toBeDefined()
    expect('item' in (cloned!.contentBlocks?.[0] ?? {})).toBe(false)
    expect('mimeType' in (cloned!.contentBlocks?.[1] ?? {})).toBe(false)
    expect('width' in (cloned!.contentBlocks?.[1] ?? {})).toBe(false)
    expect('height' in (cloned!.contentBlocks?.[1] ?? {})).toBe(false)
    expect('revisedPrompt' in (cloned!.contentBlocks?.[1] ?? {})).toBe(false)
    expect('item' in (cloned!.toolEvents?.[0] ?? {})).toBe(false)
  })

  it('缓存写入读取往返后字段仍保留且不与输入共享引用', async () => {
    const adapter = new MemoryStorageAdapter()
    const cache = new ChatLocalCache({ adapter, clock: () => 10, estimate: async () => undefined })
    const item = { query: '北京天气', nested: { findings: ['结论一'] } }
    const input: ChatMessage = {
      ...message(2, '往返'),
      contentBlocks: [
        { type: 'tool_call', blockId: 'block_tool', order: 1, callId: 'tool_1', toolType: 'web_search_call', status: 'completed', item },
        { type: 'output_image', blockId: 'block_image', order: 2, assetId: 'asset_generated', status: 'completed', mimeType: 'image/png', width: 512, height: 512, revisedPrompt: '绿色圆形' }
      ],
      toolEvents: [{ id: 'tool_1', type: 'web_search_call', status: 'completed', item: { action: { query: '北京天气' } } }]
    }
    const written = await cache.putMessages('A', 'c1', [input])
    expect(written.ok).toBe(true)
    const restored = (await cache.readConversation('A', 'c1')).value?.messages.find((entry) => entry.sequenceNo === 2)
    expect((restored?.contentBlocks?.[0] as { item?: unknown }).item).toEqual(item)
    expect((restored?.contentBlocks?.[0] as { item?: unknown }).item).not.toBe(item)
    expect(restored?.contentBlocks?.[1]).toEqual({ type: 'output_image', blockId: 'block_image', order: 2, assetId: 'asset_generated', status: 'completed', mimeType: 'image/png', width: 512, height: 512, revisedPrompt: '绿色圆形' })
    expect(restored?.toolEvents).toEqual([{ id: 'tool_1', type: 'web_search_call', status: 'completed', item: { action: { query: '北京天气' } } }])
  })

  it('item 内 data:/base64 或超长字符串必须触发整页放弃', async () => {
    const adapter = new MemoryStorageAdapter()
    const cache = new ChatLocalCache({ adapter, clock: () => 10, estimate: async () => undefined })
    const dataUrlMessage = { ...message(2, 'x'), contentBlocks: [{ type: 'tool_call', callId: 't1', toolType: 'search', status: 'completed', item: { raw: 'data:image/png;base64,AAAA' } }] }
    expect((await cache.putMessages('A', 'c1', [dataUrlMessage])).ok).toBe(false)
    expect(adapter.messages.size).toBe(0)
    const oversizeMessage = { ...message(2, 'x'), toolEvents: [{ id: 't2', type: 'search', status: 'completed', item: { payload: 'x'.repeat(2 * 1024 * 1024 + 1) } }] }
    expect((await cache.putMessages('A', 'c1', [oversizeMessage])).ok).toBe(false)
    expect(adapter.messages.size).toBe(0)
  })
})
