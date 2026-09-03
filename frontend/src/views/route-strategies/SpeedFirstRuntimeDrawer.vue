<template>
  <a-drawer
    :open="open"
    class="speed-first-runtime-drawer"
    :title="`速度优先状态：${strategyName}`"
    :width="1040"
    destroy-on-close
    @close="emit('update:open', false)"
  >
    <a-alert
      class="speed-first-runtime-drawer-intro"
      type="info"
      show-icon
      message="这是当前策略作用域的速度运行态"
      description="状态仅影响当前策略绑定分组下的候选排序；账号仍可能作为故障回退或其他兜底候选被调度，不等同于账号全局不可用。"
    />

    <div class="speed-first-runtime-drawer-toolbar">
      <span v-if="runtime?.generatedAt" class="muted-cell">数据生成于 {{ formatDateTime(runtime.generatedAt) }}</span>
      <a-button :loading="loading" @click="emit('refresh')">
        <template #icon><ReloadOutlined /></template>
        刷新
      </a-button>
    </div>

    <a-spin :spinning="loading">
      <a-alert v-if="error" type="error" show-icon :message="error" />
      <a-alert
        v-else-if="runtime && !runtime.enabled"
        type="info"
        show-icon
        message="速度优先未启用"
        description="当前策略不是启用中的速度优先普通路由，因此没有可展示的速度降级运行态。"
      />
      <a-alert
        v-else-if="runtime && !runtime.runtimeAvailable"
        type="warning"
        show-icon
        message="速度状态暂不可用"
        description="当前运行实例没有提供这套路由策略的速度运行态，请稍后重试。"
      />
      <template v-else-if="runtime">
        <a-alert
          v-if="runtime.degradedCount === 0 && runtime.items.length === 0"
          class="speed-first-runtime-empty-alert"
          type="success"
          show-icon
          message="速度正常"
          description="当前没有需要展示的速度降级账号。"
        />
        <a-alert
          v-else
          class="speed-first-runtime-summary-alert"
          :type="runtime.degradedCount > 0 ? 'warning' : 'info'"
          show-icon
          :message="runtime.degradedCount > 0 ? `当前有 ${runtime.degradedCount} 个账号处于速度降级` : '当前没有账号处于速度降级'"
          description="可在列表中核对慢样本触发情况和恢复进度。"
        />

        <a-table
          v-if="runtime.items.length"
          class="speed-first-runtime-table"
          :columns="runtimeColumns"
          :data-source="runtime.items"
          :pagination="false"
          :scroll="{ x: 1080 }"
          :row-key="speedFirstRuntimeRowKey"
          size="small"
        >
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'account'">
              <div class="speed-first-runtime-account">
                <span class="name-cell">{{ record.accountName || record.accountId }}</span>
                <span v-if="record.accountName" class="muted-cell">{{ record.accountId }}</span>
              </div>
            </template>
            <template v-else-if="column.key === 'scope'">
              <span class="mono-cell">{{ record.scope.groupId }}</span>
            </template>
            <template v-else-if="column.key === 'slowCount'">
              <span>{{ record.slowCount }}/{{ record.slowTriggerCount }}</span>
              <span class="muted-cell">（{{ record.slowWindowSeconds }} 秒窗口）</span>
            </template>
            <template v-else-if="column.key === 'degradedUntil'">
              <a-tag v-if="record.degradedUntil" color="orange">速度降级</a-tag>
              <span v-if="record.degradedUntil">{{ formatDateTime(record.degradedUntil) }}</span>
              <span v-else class="muted-cell">-</span>
            </template>
            <template v-else-if="column.key === 'nextProbeAt'">
              <span>{{ formatDateTime(record.nextProbeAt || undefined) }}</span>
            </template>
            <template v-else-if="column.key === 'requestRecovery'">
              <span>{{ record.recoverySuccessCount }}/{{ record.requiredRecoverySuccessCount }}</span>
            </template>
            <template v-else-if="column.key === 'probeRecovery'">
              <span>{{ record.recoveryProbeRoundSuccessCount }}/{{ record.recoveryProbeRoundAttemptCount }}</span>
            </template>
            <template v-else-if="column.key === 'reason'">
              <span>{{ record.reason || '-' }}</span>
            </template>
          </template>
        </a-table>
        <a-empty v-else class="speed-first-runtime-empty" description="当前没有速度运行态记录。" />
      </template>
      <a-empty v-else class="speed-first-runtime-empty" description="点击刷新加载速度状态。" />
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import { ReloadOutlined } from '@ant-design/icons-vue'

import { formatDateTime } from '@/shared/formatters'
import type { RouteStrategySpeedFirstLatencyRuntime, RouteStrategySpeedFirstLatencyRuntimeItem } from '@/types/domain'

defineProps<{
  open: boolean
  strategyName: string
  loading: boolean
  runtime?: RouteStrategySpeedFirstLatencyRuntime
  error?: string
}>()

const emit = defineEmits<{
  (event: 'update:open', value: boolean): void
  (event: 'refresh'): void
}>()

function speedFirstRuntimeRowKey(record: RouteStrategySpeedFirstLatencyRuntimeItem): string {
  return `${record.accountId}|${record.scope.groupId}|${record.degradedUntil ?? ''}`
}

const runtimeColumns = [
  { title: 'AI 账户', key: 'account', width: 190, fixed: 'left' },
  { title: '策略分组', key: 'scope', width: 140 },
  { title: '慢样本', key: 'slowCount', width: 190 },
  { title: '降级保留至', key: 'degradedUntil', width: 190 },
  { title: '下一次恢复探针', key: 'nextProbeAt', width: 190 },
  { title: '真实请求恢复', key: 'requestRecovery', width: 130 },
  { title: '后台探针窗口', key: 'probeRecovery', width: 130 },
  { title: '原因', key: 'reason', width: 220 }
]
</script>

<style scoped>
.speed-first-runtime-drawer-intro,
.speed-first-runtime-summary-alert,
.speed-first-runtime-empty-alert {
  margin-bottom: 16px;
}

.speed-first-runtime-drawer-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}

.speed-first-runtime-account {
  display: grid;
  gap: 2px;
  min-width: 0;
}

.speed-first-runtime-empty {
  margin: 40px 0;
}

:deep(.speed-first-runtime-table .ant-table-cell) {
  vertical-align: top;
}
</style>
