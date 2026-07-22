import { nextTick, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, type Ref, type ShallowRef } from 'vue'
import type { ECharts } from '@/lib/echarts'

export type EChartsInstanceRef = ShallowRef<ECharts | undefined>

type EchartsPageLifecycleOptions = {
  renderCharts: () => void | Promise<void>
  resizeCharts: () => void
  disposeCharts: () => void
  onMounted?: () => void | Promise<void>
  onDeactivate?: () => void
  onBeforeUnmount?: () => void
  renderOnActivated?: 'pending' | 'always'
}

let echartsRuntime: Promise<typeof import('@/lib/echarts')> | undefined

export function ensureChart(elementRef: Ref<HTMLDivElement | undefined>, chartRef: EChartsInstanceRef, shouldCreate?: () => boolean): Promise<ECharts | undefined> {
  return ensureChartFromElement(elementRef.value, chartRef, shouldCreate)
}

export async function ensureChartFromElement(element: HTMLDivElement | undefined, chartRef: EChartsInstanceRef, shouldCreate?: () => boolean): Promise<ECharts | undefined> {
  if (!element) return undefined
  if (shouldCreate && !shouldCreate()) return undefined
  const { init } = await loadEchartsRuntime()
  if (shouldCreate && !shouldCreate()) return undefined
  if (!element.isConnected) return undefined
  if (chartRef.value && !chartRef.value.isDisposed() && chartRef.value.getDom() !== element) {
    chartRef.value.dispose()
    chartRef.value = undefined
  }
  if (!chartRef.value || chartRef.value.isDisposed()) {
    chartRef.value = init(element)
  }
  return chartRef.value
}

function loadEchartsRuntime(): Promise<typeof import('@/lib/echarts')> {
  echartsRuntime ??= import('@/lib/echarts')
  return echartsRuntime
}

export function disposeChart(chartRef: EChartsInstanceRef): void {
  if (chartRef.value && !chartRef.value.isDisposed()) {
    chartRef.value.dispose()
  }
  chartRef.value = undefined
}

export function resizeEcharts(charts: Iterable<ECharts | undefined>): void {
  for (const chart of charts) {
    if (chart && !chart.isDisposed()) {
      chart.resize()
    }
  }
}

export function useEchartsPageLifecycle(options: EchartsPageLifecycleOptions) {
  const pageActive = ref(false)
  const renderPending = ref(false)
  let renderScheduled = false
  let resizeListenerAttached = false

  function requestRender() {
    if (!pageActive.value) {
      renderPending.value = true
      return
    }
    if (renderScheduled) return
    renderScheduled = true
    void nextTick(() => {
      renderScheduled = false
      if (!pageActive.value) {
        renderPending.value = true
        return
      }
      renderPending.value = false
      void Promise.resolve(options.renderCharts())
        .catch((error) => {
          console.error(error)
        })
        .finally(() => {
          if (!pageActive.value) {
            renderPending.value = true
            return
          }
          resizeWhenActive()
        })
    })
  }

  function resizeWhenActive() {
    if (!pageActive.value) return
    options.resizeCharts()
  }

  function addResizeListener() {
    if (resizeListenerAttached || typeof window === 'undefined') return
    resizeListenerAttached = true
    window.addEventListener('resize', resizeWhenActive)
  }

  function removeResizeListener() {
    if (!resizeListenerAttached || typeof window === 'undefined') return
    resizeListenerAttached = false
    window.removeEventListener('resize', resizeWhenActive)
  }

  function disposePageCharts() {
    renderScheduled = false
    options.disposeCharts()
  }

  onMounted(() => {
    pageActive.value = true
    addResizeListener()
    void options.onMounted?.()
  })

  onActivated(() => {
    pageActive.value = true
    addResizeListener()
    if (renderPending.value || options.renderOnActivated === 'always') {
      requestRender()
      return
    }
    void nextTick(resizeWhenActive)
  })

  onDeactivated(() => {
    pageActive.value = false
    renderPending.value = true
    removeResizeListener()
    options.onDeactivate?.()
    disposePageCharts()
  })

  onBeforeUnmount(() => {
    pageActive.value = false
    removeResizeListener()
    options.onBeforeUnmount?.()
    disposePageCharts()
  })

  return {
    pageActive,
    renderPending,
    requestRender,
    resizeWhenActive
  }
}

export type { ECharts }
