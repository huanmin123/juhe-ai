import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const { useMock, initMock } = vi.hoisted(() => ({
  useMock: vi.fn(),
  initMock: vi.fn()
}))

// echarts 真实 init 需要 canvas，单测只验证 passive 监听补丁逻辑，全部 mock 掉 echarts 子模块。
vi.mock('echarts/core', () => ({ use: useMock, init: initMock }))
vi.mock('echarts/charts', () => ({ BarChart: {}, LineChart: {}, PieChart: {} }))
vi.mock('echarts/components', () => ({ GridComponent: {}, LegendComponent: {}, TooltipComponent: {} }))
vi.mock('echarts/features', () => ({ LabelLayout: {} }))
vi.mock('echarts/renderers', () => ({ CanvasRenderer: {} }))

type EchartsModule = typeof import('./echarts')

/** 先 spy 原生 addEventListener 再加载模块，使模块内捕获的 nativeAddEventListener 即 spy。 */
async function loadEcharts(): Promise<{ module: EchartsModule; addEventListenerSpy: ReturnType<typeof vi.spyOn> }> {
  const addEventListenerSpy = vi.spyOn(EventTarget.prototype, 'addEventListener')
  const module = await import('./echarts')
  return { module, addEventListenerSpy }
}

beforeEach(() => {
  vi.resetModules()
  useMock.mockReset()
  initMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('echarts 被动滚动监听补丁', () => {
  it('模块加载时注册图表组件，init 透传参数并返回实例', async () => {
    const fakeChart = { id: 'chart-1' }
    initMock.mockReturnValue(fakeChart)
    const { module } = await loadEcharts()
    expect(useMock).toHaveBeenCalledTimes(1)
    const container = document.createElement('div')
    const result = module.init(container, 'dark', { renderer: 'canvas' })
    expect(result).toBe(fakeChart)
    expect(initMock).toHaveBeenCalledWith(container, 'dark', { renderer: 'canvas' })
  })

  it('init 期间替换 EventTarget.prototype.addEventListener，结束后恢复', async () => {
    let patchedDuringInit: unknown
    initMock.mockImplementation(() => {
      patchedDuringInit = EventTarget.prototype.addEventListener
      return {}
    })
    const { module, addEventListenerSpy } = await loadEcharts()
    expect(EventTarget.prototype.addEventListener).toBe(addEventListenerSpy)
    module.init(document.createElement('div'))
    expect(patchedDuringInit).not.toBe(addEventListenerSpy)
    expect(patchedDuringInit).toBeTypeOf('function')
    expect(EventTarget.prototype.addEventListener).toBe(addEventListenerSpy)
  })

  it('滚动阻断类事件的 options 被追加 passive: true，其余事件不受影响', async () => {
    const target = document.createElement('div')
    const listener = () => undefined
    initMock.mockImplementation(() => {
      // happy-dom 元素原型链中间层覆盖 addEventListener，这里显式走 EventTarget.prototype
      // 上被替换的补丁函数（与 zrender 在 EventTarget 层挂载监听等价）。
      const patched = EventTarget.prototype.addEventListener as unknown as (
        type: string,
        listener: EventListener,
        options?: boolean | AddEventListenerOptions
      ) => void
      patched.call(target, 'wheel', listener)
      patched.call(target, 'click', listener)
      patched.call(target, 'touchstart', listener, true)
      patched.call(target, 'mousewheel', listener, { capture: true })
      patched.call(target, 'touchmove', listener, { passive: false })
      patched.call(target, 'scroll', listener, { capture: true })
      return {}
    })
    const { module, addEventListenerSpy } = await loadEcharts()
    module.init(target)
    const calls = (addEventListenerSpy.mock.calls as Array<[string, EventListener, boolean | AddEventListenerOptions | undefined]>)
      .map(([type, , options]) => [type, options] as const)
    expect(calls).toEqual([
      ['wheel', { passive: true }],
      ['click', undefined],
      ['touchstart', { capture: true, passive: true }],
      ['mousewheel', { capture: true, passive: true }],
      ['touchmove', { passive: false }],
      ['scroll', { capture: true }]
    ])
  })

  it('嵌套 init 期间补丁只安装一次、最外层结束时恢复', async () => {
    const { module, addEventListenerSpy } = await loadEcharts()
    const container = document.createElement('div')
    let outerPatched: unknown
    let innerPatched: unknown
    initMock.mockImplementationOnce(() => {
      outerPatched = EventTarget.prototype.addEventListener
      initMock.mockImplementationOnce(() => {
        innerPatched = EventTarget.prototype.addEventListener
        return {}
      })
      // 外层 echartsInit 内部再触发一次模块 init，模拟嵌套调用
      module.init(container)
      return {}
    })
    module.init(container)
    expect(outerPatched).toBe(innerPatched)
    expect(outerPatched).not.toBe(addEventListenerSpy)
    expect(EventTarget.prototype.addEventListener).toBe(addEventListenerSpy)
  })

  it('echarts init 抛错时补丁仍被恢复且错误透传', async () => {
    const failure = new Error('echarts init failed')
    initMock.mockImplementation(() => {
      throw failure
    })
    const { module, addEventListenerSpy } = await loadEcharts()
    expect(() => module.init(document.createElement('div'))).toThrow(failure)
    expect(EventTarget.prototype.addEventListener).toBe(addEventListenerSpy)
  })
})
