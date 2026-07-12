import assert from 'node:assert/strict'

import {
  chatMessagePartitionBounds,
  chatMessagePartitionDateKeyFromIso,
  postgresChatMessagePartitionName
} from '../../storage/postgres-chat-message-partitions.js'

assert.equal(chatMessagePartitionDateKeyFromIso('2026-07-12T23:59:59.000Z'), '20260712')
assert.equal(chatMessagePartitionDateKeyFromIso('invalid'), undefined)
assert.equal(postgresChatMessagePartitionName('20260712'), 'chat_messages_20260712')
assert.deepEqual(chatMessagePartitionBounds('20260712'), {
  startDate: '2026-07-12',
  endDate: '2026-07-13'
})
assert.throws(() => postgresChatMessagePartitionName('2026-07-12'), /日期无效/)

console.log('AI 问答 PostgreSQL 日分区回归通过')
