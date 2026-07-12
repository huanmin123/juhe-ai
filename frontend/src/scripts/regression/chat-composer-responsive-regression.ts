import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../../views/chat/composer/AIComposer.vue', import.meta.url), 'utf8')

assert.match(source, /@media \(max-width: 520px\)/)
assert.match(source, /\.ai-composer-model-controls\s*\{[^}]*flex-wrap:\s*wrap;/s)
assert.match(source, /\.ai-composer-model-controls\s*\{[^}]*overflow-x:\s*visible;/s)
console.log('chat composer responsive regression passed')
