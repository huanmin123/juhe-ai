import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'

import { attachDownstreamResponseErrorBoundary } from '../../shared/downstream-response-error-boundary.js'

const response = new EventEmitter()
let errorCode: string | undefined
let unwritableCalls = 0
const detach = attachDownstreamResponseErrorBoundary({
  response,
  onError: (error) => { errorCode = (error as NodeJS.ErrnoException).code },
  onUnwritable: () => { unwritableCalls += 1 }
})

response.emit('error', Object.assign(new Error('broken pipe'), { code: 'EPIPE' }))
assert.equal(errorCode, 'EPIPE', '响应边界必须保留原始错误代码')
assert.equal(unwritableCalls, 1, '响应错误必须收口为当前流不可写')
assert.equal(response.listenerCount('error'), 1, '响应结束前必须保留 listener，避免重复 error 升级为未捕获异常')

response.emit('error', Object.assign(new Error('second pipe error'), { code: 'EPIPE' }))
assert.equal(unwritableCalls, 1, '已处理的同一响应错误不得重复收口')
detach()
assert.equal(response.listenerCount('error'), 0, '主动清理后必须释放 listener')

const unsupported = attachDownstreamResponseErrorBoundary({
  response: {},
  onError: () => assert.fail('没有事件接口时不得伪造下游错误'),
  onUnwritable: () => assert.fail('没有事件接口时不得伪造不可写')
})
unsupported()

console.log('downstream response error boundary regression passed')
