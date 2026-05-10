<template>
  <a-modal :open="open" :title="modalTitle" width="900px" :footer="null" @cancel="close" @update:open="handleOpenUpdate">
    <div class="authorization-usage-modal">
      <a-tabs class="authorization-usage-tabs">
        <a-tab-pane key="users" tab="授权用户">
          <ResponsiveDataList :columns="userColumns" :data-source="overview?.users ?? []" :row-key="rowKey" :loading="loading" :pagination="false" :scroll-x="userTableScrollX" :lock-body-scroll="false">
            <template #bodyCell="{ column, record }">
              <div v-if="column.key === 'grantee'" class="usage-principal-cell">
                <strong>{{ formatPrincipalName(record.granteeSystemAccountName, record.granteeUsername) }}</strong>
              </div>
              <UsageStatCell v-else-if="isWindowColumn(column.key)" :usage="record.usageByWindow[column.key]" compact />
            </template>
            <template #card="{ record }">
              <article class="usage-mobile-card">
                <strong>{{ formatPrincipalName(record.granteeSystemAccountName, record.granteeUsername) }}</strong>
                <span v-for="window in currentWindows(overview?.windows)" :key="window.key">{{ window.label }} {{ formatUsageBrief(record.usageByWindow[window.key]) }}</span>
              </article>
            </template>
          </ResponsiveDataList>
        </a-tab-pane>
        <a-tab-pane key="teams" tab="授权团队">
          <ResponsiveDataList
            :columns="teamColumns"
            :data-source="teamDataSource"
            :row-key="rowKey"
            :loading="loading"
            :pagination="false"
            :scroll-x="teamTableScrollX"
            :lock-body-scroll="false"
            :expandable="{ expandIcon: () => null, rowExpandable: (record: TeamUsageRow) => Boolean(record.children) }"
          >
            <template #bodyCell="{ column, record }">
              <template v-if="record.isMembersRow">
                <span v-if="column.key !== 'team'"></span>
                <div v-else class="team-member-usage-panel">
                  <div v-for="member in record.memberUsage" :key="member.authorizationId" class="team-member-usage-row">
                    <span>{{ formatPrincipalName(member.systemAccountName, member.username) }}</span>
                    <span v-for="window in currentWindows(overview?.windows)" :key="window.key">{{ window.label }} {{ formatUsageBrief(member.usageByWindow[window.key]) }}</span>
                  </div>
                  <span v-if="!record.memberUsage.length" class="muted-cell">暂无成员消耗</span>
                </div>
              </template>
              <div v-else-if="column.key === 'team'" class="usage-principal-cell">
                <strong>{{ record.teamName || record.teamId }}</strong>
                <span>{{ record.memberUsage.length }} 个成员</span>
              </div>
              <UsageStatCell v-else-if="isWindowColumn(column.key)" :usage="record.usageByWindow[column.key]" compact />
              <RowActions v-else-if="column.key === 'actions'" :actions="teamActions(record)" @action-click="toggleTeam(record.teamId)" />
            </template>
            <template #card="{ record }">
              <article v-if="record.isMembersRow" class="usage-mobile-card">
                <strong>成员明细</strong>
                <span v-for="member in record.memberUsage" :key="member.authorizationId">{{ formatPrincipalName(member.systemAccountName, member.username) }}</span>
                <span v-if="!record.memberUsage.length" class="muted-cell">暂无成员消耗</span>
              </article>
              <article v-else class="usage-mobile-card">
                <strong>{{ record.teamName || record.teamId }}</strong>
                <span>{{ record.memberUsage.length }} 个成员</span>
                <span v-for="window in currentWindows(overview?.windows)" :key="window.key">{{ window.label }} {{ formatUsageBrief(record.usageByWindow[window.key]) }}</span>
                <RowActions :actions="teamActions(record)" variant="button" @action-click="toggleTeam(record.teamId)" />
              </article>
            </template>
          </ResponsiveDataList>
        </a-tab-pane>
      </a-tabs>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import type {
  AccountAuthorizationUsageOverview,
  AuthorizationTeamUsageDetail,
  UsageStatsWindowKey
} from '@/types/domain'
import UsageStatCell from './UsageStatCell.vue'
import { currentUsageWindows, detailWindowKeys, formatPrincipalName, formatUsageBrief, isUsageWindowColumn } from './usageStatsFormatters'
const usageColumnWidth = 86
const userTableScrollX = 180 + usageColumnWidth * detailWindowKeys.length
const teamTableScrollX = 180 + usageColumnWidth * detailWindowKeys.length + 110

type TeamUsageRow = AuthorizationTeamUsageDetail & {
  children?: TeamUsageRow[]
  isMembersRow?: boolean
}

const props = defineProps<{
  loading: boolean
  open: boolean
  overview?: AccountAuthorizationUsageOverview
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'update:open', value: boolean): void
}>()

const expandedTeamKeys = ref<string[]>([])

const modalTitle = computed(() => props.overview ? `${props.overview.resourceName} 授权用量` : '授权用量')
const userColumns = computed(() => [
  { title: '授权用户', key: 'grantee', width: 180 },
  ...currentWindows(props.overview?.windows).map((window) => ({ title: window.label, key: window.key, width: usageColumnWidth }))
])
const teamColumns = computed(() => [
  { title: '授权团队', key: 'team', width: 180 },
  ...currentWindows(props.overview?.windows).map((window) => ({ title: window.label, key: window.key, width: usageColumnWidth })),
  { title: '操作', key: 'actions', width: 110 }
])
const teamDataSource = computed<TeamUsageRow[]>(() => (props.overview?.teams ?? []).map((team) => ({
  ...team,
  children: isExpanded(team.teamId)
    ? [{ teamId: `${team.teamId}:members`, teamName: team.teamName, memberUsage: team.memberUsage, usageByWindow: team.usageByWindow, isMembersRow: true }]
    : undefined
})))

function close() {
  emit('close')
}

function handleOpenUpdate(value: boolean) {
  emit('update:open', value)
}

function rowKey(row: { id?: string; teamId?: string }) {
  return row.id ?? row.teamId ?? ''
}

function toggleTeam(teamId: string) {
  expandedTeamKeys.value = expandedTeamKeys.value.includes(teamId)
    ? expandedTeamKeys.value.filter((id) => id !== teamId)
    : [...expandedTeamKeys.value, teamId]
}

function teamActions(row: TeamUsageRow): RowActionItem[] {
  return [
    {
      key: 'members',
      label: isExpanded(row.teamId) ? '收起明细' : '成员明细',
      icon: isExpanded(row.teamId) ? 'disable' : 'members',
      tone: 'info'
    }
  ]
}

function isExpanded(teamId: string) {
  return expandedTeamKeys.value.includes(teamId)
}

function isWindowColumn(value: unknown): value is UsageStatsWindowKey {
  return isUsageWindowColumn(value)
}

function currentWindows(source = props.overview?.windows) {
  return currentUsageWindows(source)
}
</script>

<style scoped>
.authorization-usage-modal {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.usage-principal-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.usage-principal-cell strong {
  color: #0f172a;
}

.usage-principal-cell span {
  color: #64748b;
  font-size: 12px;
}

.team-member-usage-panel {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 920px;
  padding: 10px 12px;
  border-radius: 10px;
  background: #f8fafc;
}

.team-member-usage-row {
  display: grid;
  grid-template-columns: minmax(120px, 1fr) repeat(6, minmax(130px, 1fr));
  gap: 8px;
  color: #475569;
  font-size: 12px;
}

.muted-cell {
  color: #94a3b8;
}

.usage-mobile-card {
  display: grid;
  gap: 6px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
  color: #64748b;
  font-size: 12px;
}

.usage-mobile-card strong {
  color: #0f172a;
  font-size: 13px;
}
</style>
