import type { Ref, ShallowRef } from 'vue'
import { init, type ECharts } from '@/lib/echarts'

export type EChartsInstanceRef = ShallowRef<ECharts | undefined>

export function ensureChart(elementRef: Ref<HTMLDivElement | undefined>, chartRef: EChartsInstanceRef): ECharts | undefined {
  return ensureChartFromElement(elementRef.value, chartRef)
}

export function ensureChartFromElement(element: HTMLDivElement | undefined, chartRef: EChartsInstanceRef): ECharts | undefined {
  if (!element) return undefined
  if (!chartRef.value || chartRef.value.isDisposed()) {
    chartRef.value = init(element)
  }
  return chartRef.value
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

export type { ECharts }
