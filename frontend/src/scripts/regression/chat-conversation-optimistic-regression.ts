import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { ChatConversationMutationQueue } from '../../views/chat/chatConversationMutations'

const source = readFileSync(new URL('../../views/chat/ChatView.vue', import.meta.url), 'utf8')
const renameBlock = source.slice(source.indexOf('async function renameConversation'), source.indexOf('async function togglePinned'))
const pinBlock = source.slice(source.indexOf('async function togglePinned'), source.indexOf('async function confirmDeleteConversation'))
const clearBlock = source.slice(source.indexOf('async function handleConversationAction'), source.indexOf('async function retryLatestTurn'))
const deleteBlock = source.slice(source.indexOf('async function removeConversation'), source.indexOf('async function refreshSelectedModelsIfExpired'))

assert(renameBlock.indexOf('replaceConversation(optimistic)') < renameBlock.indexOf('conversationMutationQueue.enqueue'), '重命名必须先更新可见标题')
assert.match(renameBlock, /catch \(error\)[\s\S]{0,600}confirmedTitle[\s\S]{0,180}previous\.title/, '重命名失败必须按最后确认标题回滚，不能覆盖并发置顶投影')
assert(pinBlock.indexOf('replaceConversation(optimistic)') < pinBlock.indexOf('conversationMutationQueue.enqueue'), '置顶必须先更新可见排序')
assert.match(pinBlock, /conversationMutationVersions\.set\(mutationKey, mutationVersion\)/, '置顶必须登记每会话字段请求版本')
assert.match(pinBlock, /conversationMutationVersions\.get\(mutationKey\) !== mutationVersion/, '置顶旧响应不得覆盖用户最后一次选择')
assert.match(pinBlock, /catch \(error\)[\s\S]{0,720}confirmedPinned[\s\S]{0,220}previous\.isPinned[\s\S]{0,180}sortConversations\(\)/, '置顶失败必须按最后确认值恢复置顶字段和排序')
assert(clearBlock.indexOf('await chatApi.clearConversation') < clearBlock.indexOf('messages.value = []'), '清空仍必须等待服务端成功，不能加入乐观白名单')
assert.match(clearBlock, /conversationMutationQueue\.enqueue\(conversation\.id, \(\) => chatApi\.clearConversation/, '清空必须排在同会话已提交的重命名/置顶之后，不能被迟到 PATCH 覆盖标题')
assert(deleteBlock.indexOf('await chatApi.deleteConversation') < deleteBlock.indexOf('applyDeletedChatConversation'), '删除会话仍必须等待服务端成功')

const firstMutation = Promise.withResolvers<void>()
const mutationOrder: string[] = []
const queue = new ChatConversationMutationQueue()
const rename = queue.enqueue('conv_1', async () => { mutationOrder.push('rename:start'); await firstMutation.promise; mutationOrder.push('rename:end') })
const pin = queue.enqueue('conv_1', async () => { mutationOrder.push('pin:start'); mutationOrder.push('pin:end') })
await Promise.resolve()
await Promise.resolve()
assert.deepEqual(mutationOrder, ['rename:start'], '同一会话的重命名和置顶请求必须串行，避免服务端乱序写入')
firstMutation.resolve()
await Promise.all([rename, pin])
assert.deepEqual(mutationOrder, ['rename:start', 'rename:end', 'pin:start', 'pin:end'])
assert.equal(queue.size, 0, '会话 mutation 队列完成后不得泄漏')
assert.match(source, /conversationMutationQueue\.enqueue\(item\.id/, '重命名和置顶必须共用按会话串行队列')
assert.match(renameBlock, /\.\.\.current, title: updated\.title/, '重命名响应只能合并 title，不能覆盖并发置顶投影')
assert.match(pinBlock, /\.\.\.current, isPinned: updated\.isPinned/, '置顶响应只能合并 isPinned，不能覆盖并发重命名投影')
assert.match(source, /conversationMutationConfirmedValues/, '连续同字段请求失败时必须保留最后一次服务端确认值，不能回滚到中间乐观值')
assert.match(pinBlock, /conversationMutationConfirmedValues\.set\(mutationKey, updated\.isPinned\)[\s\S]{0,900}confirmedPinned/, '置顶成功必须推进确认水位，最终失败按该水位回滚')

console.log('AI 问答重命名和置顶可回滚乐观更新回归通过')
