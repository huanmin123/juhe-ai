import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { openAIOAuthTokenResponseMaxBytes } from '../../modules/openai-oauth/openai-oauth.service.js'

assert.equal(openAIOAuthTokenResponseMaxBytes, 256 * 1024, 'OAuth token 响应体上限应固定为 256KB')

const source = readFileSync(resolve('src/modules/openai-oauth/openai-oauth.service.ts'), 'utf8')
assert.match(source, /new BoundedBufferCollector\(openAIOAuthTokenResponseMaxBytes\)/, 'OAuth token 响应必须使用有界 buffer 收集')
assert.match(source, /body\.truncated[\s\S]*request\.destroy\(new Error\('OpenAI OAuth 令牌响应体过大'\)\)/, 'OAuth token 响应超限时必须主动中断请求')
assert.doesNotMatch(source, /const chunks: Buffer\[\]/, 'OAuth token 响应不能无界保存 chunk 数组')
assert.doesNotMatch(source, /Buffer\.concat\(chunks\)/, 'OAuth token 响应不能无界拼接完整响应体')

console.log('OpenAI OAuth token 响应边界回归通过：token endpoint 响应体按 256KB 收集，超限主动中断')
