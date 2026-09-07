import { strict as assert } from 'node:assert'

import {
  classifyOpenAIStreamEvent,
  type ParsedOpenAIStreamEvent
} from '../../modules/gateway/protocols/openai-v1/stream-events.js'

const output = buildTrapOutput()
const classification = classifyOpenAIStreamEvent({
  eventName: 'response.output_item.done',
  dataText: '{}',
  dataParseError: false,
  eventType: 'response.output_item.done',
  data: {
    type: 'response.output_item.done',
    item: {
      type: 'message',
      content: output
    }
  }
} satisfies ParsedOpenAIStreamEvent)

assert.equal(classification.visibleOutput, true, '有限字段内的文本输出仍应被识别')
assert(classification.estimatedOutputTokens > 0, '有限字段内的文本输出仍应估算 token')

const ordinaryErrorField = classifyOpenAIStreamEvent({
  eventName: 'response.metadata',
  dataText: '{}',
  dataParseError: false,
  eventType: 'response.metadata',
  data: {
    type: 'response.metadata',
    error: { code: 'vendor_invented_error', message: 'diagnostic only' }
  }
})
assert.equal(ordinaryErrorField.failed, false, '普通事件中的 error 字段不得被猜测为协议失败终态')
assert.equal(ordinaryErrorField.terminal, false, '普通事件中的 error 字段不得提前终止流')

for (const code of ['401', '429', '500', 'vendor_invented_error']) {
  const declaredFailure = classifyOpenAIStreamEvent({
    eventName: 'error',
    dataText: '{}',
    dataParseError: false,
    eventType: 'error',
    data: { type: 'error', code, message: 'untrusted' }
  })
  assert.equal(declaredFailure.failed, true, 'event:error 是可观察协议失败结构')
  assert.equal(declaredFailure.terminal, true, 'event:error 应终止当前失败流')
}

console.log('流式事件边界回归通过：token 估算有界，失败只由协议事件结构决定而不解释错误字段或状态码')

function buildTrapOutput(): Record<string, unknown> {
  const value: Record<string, unknown> = {}
  value.first = { text: 'hello stream output' }
  for (let index = 1; index < 120; index += 1) {
    value[`field_${index}`] = { text: `visible ${index}` }
  }
  Object.defineProperty(value, 'field_120_trap', {
    enumerable: true,
    get() {
      throw new Error('流式 token 估算不应读取超过字段上限后的属性')
    }
  })
  return value
}
