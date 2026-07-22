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

console.log('流式事件 token 估算边界回归通过：输出估算达到字段上限后停止，不会遍历完整大对象')

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
