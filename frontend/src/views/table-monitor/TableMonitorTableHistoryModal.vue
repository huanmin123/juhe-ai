<template>
  <a-modal
    v-model:open="open"
    :footer="null"
    :title="modalTitle"
    width="760px"
    @after-open-change="handleAfterOpenChange"
  >
    <div class="table-history-modal-body">
      <a-spin v-if="loading" />
      <div v-else-if="rows.length" ref="chartElement" class="table-history-chart" />
      <a-empty v-else description="当前日期范围内没有表历史数据" />
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, shallowRef, watch } from 'vue'

import { disposeChart, ensureChartFromElement, type ECharts } from '@/composables/useEcharts'
import type { TableStorageHistoryPoint, TableStorageOverviewSummary } from '@/types/domain'

import { buildTableStorageHistoryChartOption, databaseRoleLabel } from './tableMonitorDisplay'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  loading: boolean
  rows: TableStorageHistoryPoint[]
  table?: TableStorageOverviewSummary
}>()

const chartElement = ref<HTMLDivElement>()
const chart = shallowRef<ECharts>()
let renderGeneration = 0

const modalTitle = computed(() => props.table
  ? `${databaseRoleLabel(props.table.databaseRole)} / ${props.table.tableName}`
  : '表历史趋势')

async function renderChart() {
  const generation = ++renderGeneration
  if (!open.value || props.loading || props.rows.length === 0) {
    disposeChart(chart)
    return
  }
  await nextTick()
  if (generation !== renderGeneration || !open.value) return
  const instance = await ensureChartFromElement(chartElement.value, chart, () => generation === renderGeneration && open.value)
  if (!instance || generation !== renderGeneration || !open.value) return
  instance.setOption(buildTableStorageHistoryChartOption(props.rows), { notMerge: true })
  instance.resize()
}

function handleAfterOpenChange(visible: boolean) {
  if (visible) {
    void renderChart()
    return
  }
  renderGeneration += 1
  disposeChart(chart)
}

watch(() => [open.value, props.loading, props.rows] as const, () => {
  void renderChart()
})

onBeforeUnmount(() => {
  renderGeneration += 1
  disposeChart(chart)
})
</script>

<style scoped>
.table-history-modal-body {
  display: grid;
  min-height: 360px;
  place-items: center;
}

.table-history-chart {
  width: 100%;
  height: 360px;
}
</style>
