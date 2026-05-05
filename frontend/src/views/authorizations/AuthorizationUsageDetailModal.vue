<template>
  <a-modal v-model:open="open" :title="title" width="960px" :footer="null">
    <template v-if="authorization">
      <a-alert
        class="usage-alert"
        type="info"
        show-icon
        :message="`今日授权总计（不含归属人自己消耗）：${usageSummaryText(authorization.usage)}`"
      />
      <div v-if="teamUsageSummaries.length" class="usage-team-section">
        <div class="usage-section-title">团队来源今日消耗</div>
        <div class="usage-team-cards">
          <article v-for="summary in teamUsageSummaries" :key="summary.teamId" class="usage-team-card">
            <div class="usage-team-card-head">
              <span class="usage-team-card-title">{{ summary.teamName }}</span>
              <a-tag color="gold">团队来源</a-tag>
            </div>
            <strong class="usage-team-card-summary">{{ usageSummaryText(summary.usage) }}</strong>
            <span class="usage-team-card-meta">成员 {{ summary.memberCount }} 人</span>
          </article>
        </div>
        <div class="usage-section-title usage-subsection-title">团队来源成员今日消耗</div>
        <a-table size="small" :columns="teamUsageColumns" :data-source="teamUsageRows" row-key="key" :pagination="false">
          <template #emptyText>
            <a-empty description="暂无团队成员用量" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'teamName'">
              {{ record.teamName }}
            </template>
            <template v-else-if="column.key === 'memberName'">
              {{ record.systemAccountName || '未命名成员' }}
            </template>
            <template v-else-if="column.key === 'usage'">
              {{ usageSummaryText(record.usage) }}
            </template>
          </template>
        </a-table>
      </div>
      <div class="usage-section-title">每系统账户今日消耗</div>
      <a-table size="small" :columns="usageDetailColumns" :data-source="usageDetails" row-key="systemAccountId" :pagination="false">
        <template #emptyText>
          <a-empty description="暂无用量明细" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">
            {{ record.systemAccountName || '未知账户' }}
          </template>
          <template v-else-if="column.key === 'usage'">
            {{ usageSummaryText(record) }}
          </template>
          <template v-else-if="column.key === 'lastUsedAt'">
            {{ formatDateTime(record.lastUsedAt) }}
          </template>
        </template>
      </a-table>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { AuthorizationUserUsageDetail, ResourceAuthorizationSummary } from '@/types/domain'
import { formatDateTime, usageSummaryText, type TeamUsageSummary } from './authorizationFormatters'

type TeamUsageRow = TeamUsageSummary['members'][number]

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  authorization?: ResourceAuthorizationSummary
  teamUsageColumns: Array<Record<string, unknown>>
  teamUsageRows: TeamUsageRow[]
  teamUsageSummaries: TeamUsageSummary[]
  usageDetailColumns: Array<Record<string, unknown>>
  usageDetails: AuthorizationUserUsageDetail[]
}>()

const title = computed(() => props.authorization
  ? `今日用量明细：${props.authorization.resourceName || props.authorization.resourceId}`
  : '今日用量明细')
</script>

<style scoped>
.usage-alert {
  margin-bottom: 12px;
}

.usage-team-section {
  display: grid;
  gap: 12px;
  margin-bottom: 16px;
}

.usage-section-title {
  color: #0f172a;
  font-size: 14px;
  font-weight: 700;
}

.usage-subsection-title {
  margin-top: -2px;
}

.usage-team-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 12px;
}

.usage-team-card {
  display: grid;
  gap: 8px;
  padding: 14px;
  border: 1px solid #e8edf5;
  border-radius: 14px;
  background: linear-gradient(180deg, #fffdf5 0%, #ffffff 100%);
}

.usage-team-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.usage-team-card-title {
  color: #0f172a;
  font-weight: 700;
}

.usage-team-card-summary {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 14px;
}

.usage-team-card-meta {
  color: #64748b;
  font-size: 12px;
}
</style>
