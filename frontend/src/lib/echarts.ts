import { init as echartsInit, use } from 'echarts/core'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { LabelLayout } from 'echarts/features'
import { CanvasRenderer } from 'echarts/renderers'

use([
  BarChart,
  LineChart,
  PieChart,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  LabelLayout,
  CanvasRenderer
])

const scrollBlockingEventNames = new Set(['wheel', 'mousewheel', 'touchstart', 'touchmove'])
const nativeAddEventListener = globalThis.EventTarget?.prototype.addEventListener
let passivePatchDepth = 0

export function init(...args: Parameters<typeof echartsInit>): ReturnType<typeof echartsInit> {
  return withPassiveScrollBlockingListeners(() => echartsInit(...args))
}

function withPassiveScrollBlockingListeners<T>(callback: () => T): T {
  if (!nativeAddEventListener || typeof globalThis.EventTarget === 'undefined') {
    return callback()
  }

  // zrender mounts chart input listeners during init without passive options, which makes Chrome warn on every chart.
  if (passivePatchDepth === 0) {
    globalThis.EventTarget.prototype.addEventListener = addPassiveScrollBlockingListener
  }
  passivePatchDepth += 1

  try {
    return callback()
  } finally {
    passivePatchDepth -= 1
    if (passivePatchDepth === 0) {
      globalThis.EventTarget.prototype.addEventListener = nativeAddEventListener
    }
  }
}

function addPassiveScrollBlockingListener(
  this: EventTarget,
  type: string,
  listener: EventListenerOrEventListenerObject | null,
  options?: boolean | AddEventListenerOptions
): void {
  nativeAddEventListener.call(this, type, listener, passiveScrollBlockingOptions(type, options))
}

function passiveScrollBlockingOptions(type: string, options: boolean | AddEventListenerOptions | undefined): boolean | AddEventListenerOptions | undefined {
  if (!scrollBlockingEventNames.has(type)) {
    return options
  }
  if (options === undefined) {
    return { passive: true }
  }
  if (typeof options === 'boolean') {
    return { capture: options, passive: true }
  }
  if ('passive' in options) {
    return options
  }
  return { ...options, passive: true }
}

export type { ECharts } from 'echarts/core'
