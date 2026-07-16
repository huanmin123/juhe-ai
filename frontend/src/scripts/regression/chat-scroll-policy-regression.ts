import assert from 'node:assert/strict'
import { chatDistanceFromBottom, resolveChatFollowState, shouldBreakChatFollowOnScroll, shouldBreakChatFollowOnWheel, shouldFollowChatBottom, shouldShowChatJumpButton } from '../../views/chat/chatScrollPolicy'

assert.equal(chatDistanceFromBottom({ scrollHeight: 1000, scrollTop: 600, clientHeight: 400 }), 0)
assert.equal(shouldFollowChatBottom(72), true)
assert.equal(shouldFollowChatBottom(73), false)
assert.equal(shouldShowChatJumpButton(72), false)
assert.equal(shouldShowChatJumpButton(73), true)
assert.equal(shouldBreakChatFollowOnWheel(-1), true)
assert.equal(shouldBreakChatFollowOnWheel(1), false)
assert.equal(shouldBreakChatFollowOnScroll(100, 98), true, '键盘或滚动条造成的向上位移必须退出自动跟随')
assert.equal(shouldBreakChatFollowOnScroll(100, 100), false)
assert.equal(shouldBreakChatFollowOnScroll(100, 120), false)
assert.deepEqual(resolveChatFollowState({ distance: 40, userDetached: true }), { followLatest: false, userDetached: true }, '用户在底部附近主动上滚后不能被流式输出重新拉回')
assert.deepEqual(resolveChatFollowState({ distance: 3, userDetached: true }), { followLatest: true, userDetached: false }, '用户真正回到底部后应恢复自动跟随')
console.log('chat scroll policy regression passed')
