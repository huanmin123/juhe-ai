import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h } from 'vue'

import { useSubmitAction } from './useSubmitAction'

beforeEach(() => {
  vi.spyOn(console, 'warn').mockImplementation(() => {})
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void; reject: (reason?: unknown) => void } {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

describe('useSubmitAction', () => {
  it('动作执行期间同 key 提交被拒绝，完成后可再次提交', async () => {
    const { submitAction } = useSubmitAction('scope-a')
    const gate = deferred<number>()
    const action = vi.fn(() => gate.promise)
    const wrapped = submitAction('save', action)

    const first = wrapped()
    const second = wrapped()
    expect(await second).toBeUndefined()
    expect(action).toHaveBeenCalledTimes(1)

    gate.resolve(42)
    await expect(first).resolves.toBe(42)

    const again = submitAction('save', vi.fn().mockResolvedValue('next'))()
    await expect(again).resolves.toBe('next')
    expect(action).toHaveBeenCalledTimes(1)
  })

  it('动作失败后仍释放提交锁并透传错误', async () => {
    const { submitAction } = useSubmitAction('scope-b')
    const failure = new Error('提交失败')
    const wrapped = submitAction('save', () => Promise.reject(failure))
    await expect(wrapped()).rejects.toBe(failure)
    await expect(submitAction('save', vi.fn().mockResolvedValue('ok'))()).resolves.toBe('ok')
  })

  it('isSubmitting / submittingRef 反映提交状态', async () => {
    vi.useFakeTimers()
    const { submitAction, isSubmitting, submittingRef } = useSubmitAction('scope-c')
    const gate = deferred<string>()
    const pending = submitAction('save', () => gate.promise)()
    expect(isSubmitting('save')).toBe(true)
    expect(submittingRef('save').value).toBe(true)
    expect(isSubmitting('other')).toBe(false)
    gate.resolve('done')
    await pending
    expect(isSubmitting('save')).toBe(false)
    expect(submittingRef('save').value).toBe(false)
  })

  it('不同 scope 或不同 key 相互独立', async () => {
    const scopeOne = useSubmitAction('scope-one')
    const scopeTwo = useSubmitAction('scope-two')
    const gate = deferred<string>()
    const pendingOne = scopeOne.submitAction('save', () => gate.promise)()
    // 不同 scope、不同 key 不被拦截。
    await expect(scopeTwo.submitAction('save', vi.fn().mockResolvedValue('two'))()).resolves.toBe('two')
    await expect(scopeOne.submitAction('other', vi.fn().mockResolvedValue('other'))()).resolves.toBe('other')
    gate.resolve('one')
    await expect(pendingOne).resolves.toBe('one')
  })

  it('releaseDelayMs 到期前保持提交锁，到期后释放', async () => {
    vi.useFakeTimers()
    const { submitAction, isSubmitting } = useSubmitAction('scope-d')
    const wrapped = submitAction('save', vi.fn().mockResolvedValue('ok'), { releaseDelayMs: 500 })
    await expect(wrapped()).resolves.toBe('ok')
    expect(isSubmitting('save')).toBe(true)
    vi.advanceTimersByTime(499)
    expect(isSubmitting('save')).toBe(true)
    vi.advanceTimersByTime(1)
    expect(isSubmitting('save')).toBe(false)
  })

  it('组件卸载时清理提交锁与延迟释放计时器', async () => {
    vi.useFakeTimers()
    let api: ReturnType<typeof useSubmitAction> | undefined
    const app = createApp(defineComponent({
      setup() {
        api = useSubmitAction('scope-unmount')
        return () => h('div')
      }
    }))
    const container = document.createElement('div')
    document.body.appendChild(container)
    app.mount(container)

    const gate = deferred<string>()
    const pending = api!.submitAction('save', () => gate.promise)()
    gate.resolve('done')
    await pending

    app.unmount()
    expect(api!.isSubmitting('save')).toBe(false)
    // 卸载后不再有遗留计时器效果。
    vi.advanceTimersByTime(5_000)
    expect(api!.isSubmitting('save')).toBe(false)
    container.remove()
  })
})
