import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/** deployMode 在模块加载时固化取值，用 resetModules + 动态 import 逐场景重新求值。 */
async function loadDeployMode(): Promise<typeof import('./deployMode')> {
  return await import('./deployMode')
}

beforeEach(() => {
  vi.resetModules()
})

afterEach(() => {
  vi.unstubAllEnvs()
})

describe('frontendDeployMode', () => {
  it('未设置环境变量时默认 node 模式', async () => {
    vi.stubEnv('VITE_JUHE_AI_DEPLOY_MODE', '')
    const { frontendDeployMode, isGoBackendMode } = await loadDeployMode()
    expect(frontendDeployMode).toBe('node')
    expect(isGoBackendMode).toBe(false)
  })

  it('设置为 go 时解析为 go 模式', async () => {
    vi.stubEnv('VITE_JUHE_AI_DEPLOY_MODE', 'go')
    const { frontendDeployMode, isGoBackendMode } = await loadDeployMode()
    expect(frontendDeployMode).toBe('go')
    expect(isGoBackendMode).toBe(true)
  })

  it('大小写与首尾空白不敏感', async () => {
    vi.stubEnv('VITE_JUHE_AI_DEPLOY_MODE', '  GO  ')
    const { frontendDeployMode, isGoBackendMode } = await loadDeployMode()
    expect(frontendDeployMode).toBe('go')
    expect(isGoBackendMode).toBe(true)
  })

  it('未知取值回退 node 模式', async () => {
    vi.stubEnv('VITE_JUHE_AI_DEPLOY_MODE', 'rust')
    const { frontendDeployMode, isGoBackendMode } = await loadDeployMode()
    expect(frontendDeployMode).toBe('node')
    expect(isGoBackendMode).toBe(false)
  })
})
