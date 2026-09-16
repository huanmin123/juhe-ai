import { describe, expect, it } from 'vitest'

import { message } from './antd'

describe('antd message 再导出', () => {
  it('透出 ant-design-vue message 的常用方法', () => {
    expect(message).toBeTypeOf('object')
    for (const method of ['success', 'error', 'warning', 'info', 'loading'] as const) {
      expect(typeof message[method], `message.${method}`).toBe('function')
    }
  })
})
