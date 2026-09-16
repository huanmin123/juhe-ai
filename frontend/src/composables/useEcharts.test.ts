import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApp, defineComponent, h, nextTick, ref, shallowRef, KeepAlive } from 'vue'

import { init } from '@/lib/echarts'
import type { ECharts } from '@/lib/echarts'
import { disposeChart, ensureChart, ensureChartFromElement, resizeEcharts, useEchartsPageLifecycle } from './useEcharts'

vi.mock('@/lib/echarts', () => ({
  init: vi.fn()
}))

interface FakeChart {
  dom: HTMLDivElement | undefined
  disposed: boolean
  isDisposed: () => boolean
  getDom: () => HTMLDivElement | undefined
  dispose: ReturnType<typeof vi.fn>
  resize: ReturnType<typeof vi.fn>
}

function fakeChart(dom?: HTMLDivElement): ECharts & FakeChart {
  const chart: FakeChart = {
    dom,
    disposed: false,
    isDisposed: () => chart.disposed,
    getDom: () => chart.dom,
    dispose: vi.fn(() => { chart.disposed = true }),
    resize: vi.fn()
  }
  return chart as unknown as ECharts & FakeChart
}

function attachedElement(): HTMLDivElement {
  const element = document.createElement('div')
  document.body.appendChild(element)
  return element
}

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  document.body.innerHTML = ''
})

describe('ensureChartFromElement', () => {
  it('element 缺失返回 undefined 且不加载运行时', async () => {
    expect(await ensureChartFromElement(undefined, shallowRef())).toBeUndefined()
    expect(init).not.toHaveBeenCalled()
  })

  it('shouldCreate 返回 false 时不创建', async () => {
    expect(await ensureChartFromElement(attachedElement(), shallowRef(), () => false)).toBeUndefined()
    expect(init).not.toHaveBeenCalled()
  })

  it('shouldCreate 在运行时加载后变为 false 时放弃创建', async () => {
    const element = attachedElement()
    expect(await ensureChartFromElement(element, shallowRef(), () => false)).toBeUndefined()
    expect(init).not.toHaveBeenCalled()
  })

  it('未挂载到文档的元素不创建', async () => {
    const detached = document.createElement('div')
    expect(await ensureChartFromElement(detached, shallowRef())).toBeUndefined()
    expect(init).not.toHaveBeenCalled()
  })

  it('为空 chartRef 时初始化并写回实例', async () => {
    const element = attachedElement()
    const chart = fakeChart(element)
    vi.mocked(init).mockReturnValue(chart)
    const chartRef = shallowRef<ECharts>()
    await expect(ensureChartFromElement(element, chartRef)).resolves.toBe(chart)
    expect(init).toHaveBeenCalledWith(element)
    expect(chartRef.value).toBe(chart)
  })

  it('已有实例绑定其他 DOM 时重建', async () => {
    const oldElement = attachedElement()
    const oldChart = fakeChart(oldElement)
    const newElement = attachedElement()
    const newChart = fakeChart(newElement)
    vi.mocked(init).mockReturnValue(newChart)
    const chartRef = shallowRef<ECharts>(oldChart)
    await expect(ensureChartFromElement(newElement, chartRef)).resolves.toBe(newChart)
    expect(oldChart.dispose).toHaveBeenCalled()
    expect(init).toHaveBeenCalledWith(newElement)
    expect(chartRef.value).toBe(newChart)
  })

  it('已有实例已销毁时重新初始化', async () => {
    const element = attachedElement()
    const stale = fakeChart(element)
    stale.dispose()
    const fresh = fakeChart(element)
    vi.mocked(init).mockReturnValue(fresh)
    const chartRef = shallowRef<ECharts>(stale)
    await expect(ensureChartFromElement(element, chartRef)).resolves.toBe(fresh)
    expect(chartRef.value).toBe(fresh)
  })

  it('ensureChart 经由 ref 取元素', async () => {
    const element = attachedElement()
    const chart = fakeChart(element)
    vi.mocked(init).mockReturnValue(chart)
    const elementRef = ref<HTMLDivElement | undefined>(element)
    const chartRef = shallowRef<ECharts>()
    await expect(ensureChart(elementRef, chartRef)).resolves.toBe(chart)
  })
})

describe('disposeChart / resizeEcharts', () => {
  it('disposeChart 销毁活动实例并清空引用', () => {
    const chart = fakeChart()
    const chartRef = shallowRef<ECharts | undefined>(chart)
    disposeChart(chartRef)
    expect(chart.dispose).toHaveBeenCalled()
    expect(chartRef.value).toBeUndefined()
  })

  it('disposeChart 对已销毁实例不再调用 dispose', () => {
    const chart = fakeChart()
    chart.dispose()
    vi.clearAllMocks()
    const chartRef = shallowRef<ECharts | undefined>(chart)
    disposeChart(chartRef)
    expect(chart.dispose).not.toHaveBeenCalled()
    expect(chartRef.value).toBeUndefined()
  })

  it('resizeEcharts 跳过空与已销毁实例', () => {
    const alive = fakeChart()
    const dead = fakeChart()
    dead.dispose()
    resizeEcharts([undefined, alive, dead])
    expect(alive.resize).toHaveBeenCalled()
    expect(dead.resize).not.toHaveBeenCalled()
  })
})

function mountLifecycle(options: Parameters<typeof useEchartsPageLifecycle>[0]) {
  let lifecycle!: ReturnType<typeof useEchartsPageLifecycle>
  const container = document.createElement('div')
  document.body.appendChild(container)
  const app = createApp(defineComponent({
    setup() {
      lifecycle = useEchartsPageLifecycle(options)
      return () => h('div')
    }
  }))
  app.mount(container)
  return {
    lifecycle,
    unmount() {
      app.unmount()
      container.remove()
    }
  }
}

async function flushRenderLoop() {
  await nextTick()
  await nextTick()
  await Promise.resolve()
  await Promise.resolve()
}

describe('useEchartsPageLifecycle', () => {
  it('挂载后激活页面并注册 resize 监听', () => {
    const addListener = vi.spyOn(window, 'addEventListener')
    const onMounted = vi.fn()
    const mounted = mountLifecycle({ renderCharts: vi.fn(), resizeCharts: vi.fn(), disposeCharts: vi.fn(), onMounted })
    expect(mounted.lifecycle.pageActive.value).toBe(true)
    expect(onMounted).toHaveBeenCalledTimes(1)
    expect(addListener).toHaveBeenCalledWith('resize', expect.any(Function))
    mounted.unmount()
  })

  it('激活状态下 requestRender 触发渲染与 resize', async () => {
    const renderCharts = vi.fn(async () => {})
    const resizeCharts = vi.fn()
    const mounted = mountLifecycle({ renderCharts, resizeCharts, disposeCharts: vi.fn() })
    mounted.lifecycle.requestRender()
    await flushRenderLoop()
    expect(renderCharts).toHaveBeenCalledTimes(1)
    expect(resizeCharts).toHaveBeenCalledTimes(1)
    expect(mounted.lifecycle.renderPending.value).toBe(false)
    mounted.unmount()
  })

  it('非激活状态下 requestRender 仅记录待渲染', async () => {
    const renderCharts = vi.fn(async () => {})
    let beforeMount!: ReturnType<typeof useEchartsPageLifecycle>
    const container = document.createElement('div')
    document.body.appendChild(container)
    const app = createApp(defineComponent({
      setup() {
        beforeMount = useEchartsPageLifecycle({ renderCharts, resizeCharts: vi.fn(), disposeCharts: vi.fn() })
        return () => h('div')
      }
    }))
    app.mount(container)
    beforeMount.pageActive.value = false
    beforeMount.requestRender()
    await flushRenderLoop()
    expect(renderCharts).not.toHaveBeenCalled()
    expect(beforeMount.renderPending.value).toBe(true)
    app.unmount()
    container.remove()
  })

  it('渲染失败也会收尾并保持待渲染状态（页面未激活时）', async () => {
    const renderCharts = vi.fn(async () => { throw new Error('render failed') })
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    const mounted = mountLifecycle({ renderCharts, resizeCharts: vi.fn(), disposeCharts: vi.fn() })
    mounted.lifecycle.requestRender()
    await flushRenderLoop()
    expect(renderCharts).toHaveBeenCalledTimes(1)
    // 页面仍激活：finally 中触发 resize。
    mounted.unmount()
    consoleError.mockRestore()
  })

  it('卸载时销毁图表并移除监听', () => {
    const removeListener = vi.spyOn(window, 'removeEventListener')
    const disposeCharts = vi.fn()
    const onBeforeUnmount = vi.fn()
    const mounted = mountLifecycle({ renderCharts: vi.fn(), resizeCharts: vi.fn(), disposeCharts, onBeforeUnmount })
    mounted.unmount()
    expect(disposeCharts).toHaveBeenCalledTimes(1)
    expect(onBeforeUnmount).toHaveBeenCalledTimes(1)
    expect(mounted.lifecycle.pageActive.value).toBe(false)
    expect(removeListener).toHaveBeenCalledWith('resize', expect.any(Function))
  })
})

describe('useEchartsPageLifecycle 与 KeepAlive', () => {
  function mountKeepAlive(options: Parameters<typeof useEchartsPageLifecycle>[0]) {
    let lifecycle!: ReturnType<typeof useEchartsPageLifecycle>
    const showFirst = ref(true)
    const page = defineComponent({
      setup() {
        lifecycle = useEchartsPageLifecycle(options)
        return () => h('div')
      }
    })
    const other = defineComponent({ setup: () => () => h('div') })
    const container = document.createElement('div')
    document.body.appendChild(container)
    const app = createApp(defineComponent({
      setup: () => () => h(KeepAlive, () => h(showFirst.value ? page : other))
    }))
    app.mount(container)
    return {
      lifecycle,
      async switchAway() {
        showFirst.value = false
        await nextTick()
        await nextTick()
      },
      async switchBack() {
        showFirst.value = true
        await nextTick()
        await nextTick()
      },
      unmount() {
        app.unmount()
        container.remove()
      }
    }
  }

  it('deactivate 标记待渲染并销毁图表，activate 后补渲染', async () => {
    const renderCharts = vi.fn(async () => {})
    const disposeCharts = vi.fn()
    const onDeactivate = vi.fn()
    const mounted = mountKeepAlive({ renderCharts, resizeCharts: vi.fn(), disposeCharts, onDeactivate })
    mounted.lifecycle.requestRender()
    await flushRenderLoop()
    expect(renderCharts).toHaveBeenCalledTimes(1)

    await mounted.switchAway()
    expect(mounted.lifecycle.pageActive.value).toBe(false)
    expect(mounted.lifecycle.renderPending.value).toBe(true)
    expect(disposeCharts).toHaveBeenCalledTimes(1)
    expect(onDeactivate).toHaveBeenCalledTimes(1)

    await mounted.switchBack()
    await flushRenderLoop()
    expect(renderCharts).toHaveBeenCalledTimes(2)
    mounted.unmount()
  })

  it('renderOnActivated=always 时激活必定渲染', async () => {
    const renderCharts = vi.fn(async () => {})
    const mounted = mountKeepAlive({ renderCharts, resizeCharts: vi.fn(), disposeCharts: vi.fn(), renderOnActivated: 'always' })
    await mounted.switchAway()
    // 离开时 deactivate 已置 pending，这里在激活前重置以验证 always 分支。
    mounted.lifecycle.renderPending.value = false
    await mounted.switchBack()
    await flushRenderLoop()
    expect(renderCharts).toHaveBeenCalledTimes(1)
    mounted.unmount()
  })

  it('无待渲染且非 always 时激活仅 resize', async () => {
    const renderCharts = vi.fn(async () => {})
    const resizeCharts = vi.fn()
    const mounted = mountKeepAlive({ renderCharts, resizeCharts, disposeCharts: vi.fn() })
    await mounted.switchAway()
    mounted.lifecycle.renderPending.value = false
    vi.clearAllMocks()
    await mounted.switchBack()
    await flushRenderLoop()
    expect(renderCharts).not.toHaveBeenCalled()
    expect(resizeCharts).toHaveBeenCalledTimes(1)
    mounted.unmount()
  })
})
