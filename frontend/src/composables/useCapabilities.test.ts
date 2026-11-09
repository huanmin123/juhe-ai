import { describe, expect, it } from 'vitest'

import { deploymentCapabilityEnabled } from './useCapabilities'

describe('deploymentCapabilityEnabled', () => {
  it('capabilities 缺失（旧后端/旧会话快照）按开启处理', () => {
    expect(deploymentCapabilityEnabled(undefined, 'auditLogs')).toBe(true)
  })

  it('能力位显式 false 时关闭', () => {
    expect(deploymentCapabilityEnabled({ auditLogs: false }, 'auditLogs')).toBe(false)
  })

  it('能力位 true 或未声明时保持开启', () => {
    expect(deploymentCapabilityEnabled({ auditLogs: true }, 'auditLogs')).toBe(true)
    expect(deploymentCapabilityEnabled({}, 'auditLogs')).toBe(true)
  })
})
