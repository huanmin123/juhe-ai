import assert from 'node:assert/strict'
import { createApp, computed } from 'vue'

import { api } from '../../api/client.js'
import { useAccountProxyOptions } from '../../views/accounts/useAccountProxyOptions.js'

const mutableApi = api as unknown as {
  proxies: { options: (...args: unknown[]) => Promise<unknown> }
}
const originalOptions = mutableApi.proxies.options
const calls: Array<Record<string, unknown> | undefined> = []

async function waitFor(predicate: () => boolean, label: string, timeoutMs = 1500): Promise<void> {
  const started = Date.now()
  while (!predicate()) {
    if (Date.now() - started > timeoutMs) throw new Error(label)
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
}

async function createHarness() {
  const app = createApp({ render: () => null })
  return app.runWithContext(() => useAccountProxyOptions({
    searchDelayMs: 250,
    scope: () => ({ selectedIds: ['selected-proxy'] })
  }))
}

async function main() {
  mutableApi.proxies.options = async (params?: Record<string, unknown>) => {
    calls.push(params)
    return [{ id: 'selected-proxy', name: '已选代理', type: 'http', enabled: true }]
  }

  const harness = await createHarness()
  assert.equal(calls.length, 0, '创建 composable 时不得请求代理 options')

  harness.handleDropdown(true)
  await waitFor(() => calls.length === 1, '展开下拉后应请求代理 options')
  assert.deepEqual(calls[0]?.selectedIds, ['selected-proxy'])

  harness.handleSearch('alpha')
  await new Promise((resolve) => setTimeout(resolve, 200))
  assert.equal(calls.length, 1, 'debounce 250ms 前不得二次请求')
  await waitFor(() => calls.length === 2, 'debounce 后应搜索请求')
  assert.equal(calls[1]?.keyword, 'alpha')

  console.log('账户代理 options 按需加载回归通过')
}

main()
  .catch((error) => {
    console.error(error)
    process.exitCode = 1
  })
  .finally(() => {
    mutableApi.proxies.options = originalOptions
  })
