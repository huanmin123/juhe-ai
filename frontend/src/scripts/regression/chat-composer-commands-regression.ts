import assert from 'node:assert/strict'

import { chatComposerCommandQueryRange, filterChatComposerCommands, moveChatComposerCommandIndex } from '../../views/chat/composer/chatComposerCommands'

assert.deepEqual(filterChatComposerCommands('代码').map((item) => item.key), ['code'])
assert.equal(moveChatComposerCommandIndex(0, -1, 4), 3)
assert.equal(moveChatComposerCommandIndex(3, 1, 4), 0)
assert.equal(moveChatComposerCommandIndex(0, 1, 0), 0)
assert.deepEqual(chatComposerCommandQueryRange(8, 'code'), { from: 3, to: 8 })

console.log('chat composer commands regression passed')
