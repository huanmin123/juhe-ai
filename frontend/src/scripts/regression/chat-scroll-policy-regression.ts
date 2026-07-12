import assert from 'node:assert/strict'
import { chatDistanceFromBottom, shouldBreakChatFollowOnWheel, shouldFollowChatBottom, shouldShowChatJumpButton } from '../../views/chat/chatScrollPolicy'

assert.equal(chatDistanceFromBottom({ scrollHeight: 1000, scrollTop: 600, clientHeight: 400 }), 0)
assert.equal(shouldFollowChatBottom(72), true)
assert.equal(shouldFollowChatBottom(73), false)
assert.equal(shouldShowChatJumpButton(72), false)
assert.equal(shouldShowChatJumpButton(73), true)
assert.equal(shouldBreakChatFollowOnWheel(-1), true)
assert.equal(shouldBreakChatFollowOnWheel(1), false)
console.log('chat scroll policy regression passed')
