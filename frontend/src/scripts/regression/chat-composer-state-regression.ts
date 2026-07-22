import assert from 'node:assert/strict'

import { createChatComposerSubmission } from '../../views/chat/composer/chatComposerSubmission'

const original = { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: '保留草稿' }] }] }
const submission = createChatComposerSubmission(original)

assert.deepEqual(submission.snapshot, original)
assert.notEqual(submission.snapshot, original, '发送快照必须深拷贝，不能继续引用编辑器文档')
assert.equal(submission.shouldRestore('failed'), true)
assert.equal(submission.shouldRestore('canceled'), false)
assert.equal(submission.shouldRestore('completed'), false)

console.log('AIComposer 发送状态回归通过')
