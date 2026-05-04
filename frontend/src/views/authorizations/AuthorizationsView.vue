<template>
  <a-card class="page-card authorizations-page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" filter-title="筛选授权" :active-filter-count="activeFilterCount" :refresh-loading="loading" @reset="resetFilters" @refresh="loadData">
      <template #inline-filters>
        <a-select v-model:value="filters.resourceType" class="filter-select responsive-list-inline-filter" :options="resourceTypeOptions" @change="handleResourceTypeChange" />
        <a-select
          v-model:value="filters.resourceId"
          show-search
          allow-clear
          option-filter-prop="label"
          class="filter-select filter-resource responsive-list-inline-filter"
          :options="resourceOptions"
          :disabled="filters.resourceType === 'all'"
          :placeholder="filters.resourceType === 'all' ? '先选择资源类型' : '筛选资源'"
          @change="loadData"
        />
        <a-select v-model:value="filters.teamId" show-search allow-clear option-filter-prop="label" class="filter-select responsive-list-inline-filter" :options="teamOptions" placeholder="筛选团队来源" @change="loadData" />
        <a-select v-model:value="filters.granteeSystemAccountId" show-search allow-clear option-filter-prop="label" class="filter-select filter-user responsive-list-inline-filter" :options="userOptions" placeholder="筛选被授权用户" @change="loadData" />
      </template>
      <template #actions>
        <a-button @click="helpOpen = true">
          <template #icon><question-circle-outlined /></template>
          授权帮助
        </a-button>
        <a-button type="primary" @click="openCreateModal">新增授权</a-button>
      </template>
      <template #filters>
        <label class="mobile-filter-field">
          <span>资源类型</span>
          <a-select v-model:value="filters.resourceType" :options="resourceTypeOptions" @change="handleResourceTypeChange" />
        </label>
        <label class="mobile-filter-field">
          <span>资源</span>
          <a-select
            v-model:value="filters.resourceId"
            show-search
            allow-clear
            option-filter-prop="label"
            :options="resourceOptions"
            :disabled="filters.resourceType === 'all'"
            :placeholder="filters.resourceType === 'all' ? '先选择资源类型' : '筛选资源'"
            @change="loadData"
          />
        </label>
        <label class="mobile-filter-field">
          <span>团队来源</span>
          <a-select v-model:value="filters.teamId" show-search allow-clear option-filter-prop="label" :options="teamOptions" placeholder="筛选团队来源" @change="loadData" />
        </label>
        <label class="mobile-filter-field">
          <span>被授权用户</span>
          <a-select v-model:value="filters.granteeSystemAccountId" show-search allow-clear option-filter-prop="label" :options="userOptions" placeholder="筛选被授权用户" @change="loadData" />
        </label>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table authorizations-table" :columns="columns" :data-source="authorizations" row-key="id" :loading="loading" :scroll-x="1620" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadData">
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无授权记录，请先新增授权。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'resource'">
          <div class="resource-cell">
            <a-tag :color="record.resourceType === 'account' ? 'blue' : 'purple'">{{ record.resourceType === 'account' ? 'AI账户' : '分组' }}</a-tag>
            <span class="resource-name">{{ record.resourceName || record.resourceId }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'owner'">
          {{ record.resourceOwnerSystemAccountName || record.resourceOwnerSystemAccountId }}
        </template>
        <template v-else-if="column.key === 'grantee'">
          {{ record.granteeSystemAccountName || record.granteeSystemAccountId }}
        </template>
        <template v-else-if="column.key === 'sources'">
          <div class="sources-cell">
            <a-tag v-for="source in record.authorizationSources" :key="source.id" :color="sourceTagColor(source)">
              {{ sourceLabel(source) }}
            </a-tag>
            <span v-if="!record.authorizationSources?.length" class="muted-cell">-</span>
          </div>
        </template>
        <template v-else-if="column.key === 'usageTotal'">
          <div class="usage-total-cell">
            <span>{{ usageSummaryText(record.usage) }}</span>
            <a-button type="link" size="small" @click="openUsageDetail(record)">查看明细</a-button>
          </div>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '生效中' : '已收回' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'createdAt'">
          {{ formatDateTime(record.createdAt) }}
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space :size="8">
            <a-popconfirm v-if="record.status === 'active' && hasManualSource(record)" title="确认收回个人授权来源？" @confirm="revokeManualSource(record)">
              <a-button type="link" size="small" danger>收回个人</a-button>
            </a-popconfirm>
            <a-dropdown v-if="activeTeamSources(record).length">
              <a-button type="link" size="small">收回团队来源</a-button>
              <template #overlay>
                <a-menu @click="revokeTeamSourceByMenu($event, record)">
                  <a-menu-item v-for="teamSource in activeTeamSources(record)" :key="teamSource.sourceTeamId">
                    {{ teamSource.sourceTeamName || teamSource.sourceTeamId }}
                  </a-menu-item>
                </a-menu>
              </template>
            </a-dropdown>
          </a-space>
        </template>
      </template>

      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.resourceName || record.resourceId }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="record.resourceType === 'account' ? 'blue' : 'purple'">{{ record.resourceType === 'account' ? 'AI账户' : '分组' }}</a-tag>
              <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '生效中' : '已收回' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>归属人</span>
              <strong>{{ record.resourceOwnerSystemAccountName || record.resourceOwnerSystemAccountId }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>被授权用户</span>
              <strong>{{ record.granteeSystemAccountName || record.granteeSystemAccountId }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>授权来源</span>
              <strong>{{ sourceText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>授权后用量</span>
              <strong>{{ usageSummaryText(record.usage) }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions two-actions">
            <a-button type="primary" @click="openUsageDetail(record)">查看明细</a-button>
            <a-popconfirm v-if="record.status === 'active' && hasManualSource(record)" title="确认收回个人授权来源？" @confirm="revokeManualSource(record)">
              <a-button danger>收回个人</a-button>
            </a-popconfirm>
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="helpOpen" title="统一授权规则" width="640px" :footer="null">
      <div class="authorization-help-content">
        <div class="authorization-help-section">
          <span class="authorization-help-title">授权范围</span>
          <p>资源所有者可以把自有 AI 账户或分组授权给系统账户或系统团队；授权只提供使用权，不开放编辑、删除、查看敏感凭据或继续转授权。</p>
        </div>
        <div class="authorization-help-section">
          <span class="authorization-help-title">团队生效</span>
          <p>团队授权会自动同步到团队内启用成员；新增成员、移除成员、团队停用或系统账户停用后，会影响对应用户是否还能继续使用。</p>
        </div>
        <div class="authorization-help-section">
          <span class="authorization-help-title">来源合并</span>
          <p>同一用户通过个人和团队拿到同一资源时，列表只保留一条有效授权，并在“授权来源”里展示个人来源和团队来源。</p>
        </div>
        <div class="authorization-help-section">
          <span class="authorization-help-title">用量口径</span>
          <p>授权用量不包含资源归属人自己的消耗；团队视图只是团队成员用户消耗的汇总，真实资源总量仍归资源所有者。</p>
        </div>
      </div>
    </a-modal>

    <a-modal v-model:open="createModalOpen" title="新增授权" width="680px" @ok="createAuthorization">
      <a-form layout="vertical">
        <a-form-item label="资源类型" required>
          <a-select v-model:value="createForm.resourceType" :options="createResourceTypeOptions" />
        </a-form-item>
        <a-form-item label="资源" required>
          <a-select
            v-model:value="createForm.resourceId"
            show-search
            option-filter-prop="label"
            :options="createResourceOptions"
            :disabled="!createResourceOptions.length"
            :placeholder="createForm.resourceType === 'account' ? '请选择 AI 账户' : '请选择分组'"
          />
        </a-form-item>
        <a-form-item label="授权对象类型" required>
          <a-radio-group v-model:value="createForm.granteeType">
            <a-radio-button value="system_account">个人</a-radio-button>
            <a-radio-button value="team">团队</a-radio-button>
          </a-radio-group>
        </a-form-item>
        <a-form-item :label="createForm.granteeType === 'system_account' ? '被授权用户' : '团队'" required>
          <a-select
            v-model:value="createForm.granteeId"
            show-search
            option-filter-prop="label"
            :options="createForm.granteeType === 'system_account' ? userOptions : activeTeamOptions"
            :disabled="!(createForm.granteeType === 'system_account' ? userOptions.length : activeTeamOptions.length)"
            :placeholder="createForm.granteeType === 'system_account' ? '选择一个用户' : '选择一个团队'"
          />
        </a-form-item>
        <a-form-item label="备注">
          <a-textarea v-model:value="createForm.remark" :rows="3" placeholder="可选" />
        </a-form-item>
        <a-alert
          v-if="createForm.granteeType === 'team'"
          type="info"
          show-icon
          message="团队授权会自动同步到团队内所有启用成员；成员移除后，对应团队来源授权也会自动回收。"
        />
      </a-form>
    </a-modal>

    <a-modal v-model:open="usageDetailOpen" :title="selectedAuthorization ? `用量明细：${selectedAuthorization.resourceName || selectedAuthorization.resourceId}` : '用量明细'" width="960px" :footer="null">
      <template v-if="selectedAuthorization">
        <a-alert
          class="usage-alert"
          type="info"
          show-icon
          :message="`授权总计（不含归属人自己消耗）：${usageSummaryText(selectedAuthorization.usage)}`"
        />
        <div v-if="selectedTeamUsageSummaries.length" class="usage-team-section">
          <div class="usage-section-title">团队总消耗</div>
          <div class="usage-team-cards">
            <article v-for="summary in selectedTeamUsageSummaries" :key="summary.teamId" class="usage-team-card">
              <div class="usage-team-card-head">
                <span class="usage-team-card-title">{{ summary.teamName }}</span>
                <a-tag color="gold">团队来源</a-tag>
              </div>
              <strong class="usage-team-card-summary">{{ usageSummaryText(summary.usage) }}</strong>
              <span class="usage-team-card-meta">成员 {{ summary.memberCount }} 人</span>
            </article>
          </div>
          <div class="usage-section-title usage-subsection-title">团队成员分别消耗</div>
          <a-table size="small" :columns="teamUsageColumns" :data-source="selectedTeamUsageRows" row-key="key" :pagination="false">
            <template #emptyText>
              <a-empty description="暂无团队成员用量" />
            </template>
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'teamName'">
                {{ record.teamName }}
              </template>
              <template v-else-if="column.key === 'memberName'">
                {{ record.systemAccountName || record.systemAccountId }}
              </template>
              <template v-else-if="column.key === 'usage'">
                {{ usageSummaryText(record.usage) }}
              </template>
            </template>
          </a-table>
        </div>
        <div class="usage-section-title">每系统账户消耗</div>
        <a-table size="small" :columns="usageDetailColumns" :data-source="selectedAuthorizationUsageDetails" row-key="systemAccountId" :pagination="false">
          <template #emptyText>
            <a-empty description="暂无用量明细" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'name'">
              {{ record.systemAccountName || record.systemAccountId }}
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
  </a-card>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import type { AccountSummary, AccountUsageSummary, AuthorizationSourceSummary, AuthorizationUserUsageDetail, GroupSummary, ResourceAuthorizationSummary, SystemAccountSummary, SystemTeamSummary } from '@/types/domain'

type AuthorizationFilterResourceType = 'all' | 'account' | 'group'

interface AuthorizationUsageResponseDetail {
  systemAccountId?: string
  systemAccountName?: string
  username?: string
  usage?: Partial<AccountUsageSummary>
  requestCount?: number
  clientCount?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  totalTokens?: number
  totalCost?: number
  lastUsedAt?: string
}

interface AuthorizationUsageResponseShape {
  authorization?: ResourceAuthorizationSummary
  usage?: Partial<AccountUsageSummary>
  details?: AuthorizationUsageResponseDetail[]
}

interface TeamUsageSummary {
  teamId: string
  teamName: string
  usage: AccountUsageSummary
  memberCount: number
  members: Array<{
    key: string
    teamId: string
    teamName: string
    systemAccountId: string
    systemAccountName: string
    usage: AccountUsageSummary
  }>
}

const loading = ref(false)
const createModalOpen = ref(false)
const usageDetailOpen = ref(false)
const helpOpen = ref(false)
const route = useRoute()

const authorizations = ref<ResourceAuthorizationSummary[]>([])
const accounts = ref<AccountSummary[]>([])
const groups = ref<GroupSummary[]>([])
const teams = ref<SystemTeamSummary[]>([])
const users = ref<SystemAccountSummary[]>([])

const selectedAuthorization = ref<ResourceAuthorizationSummary>()
const selectedAuthorizationUsageDetails = ref<AuthorizationUserUsageDetail[]>([])
const selectedResourceAuthorizations = ref<ResourceAuthorizationSummary[]>([])

const filters = reactive({
  resourceType: 'all' as AuthorizationFilterResourceType,
  resourceId: undefined as string | undefined,
  teamId: undefined as string | undefined,
  granteeSystemAccountId: undefined as string | undefined
})

const createForm = reactive({
  resourceType: 'account' as 'account' | 'group',
  resourceId: '' as string,
  granteeType: 'system_account' as 'system_account' | 'team',
  granteeId: '' as string,
  remark: ''
})

const columns = [
  { title: '资源', key: 'resource', width: 260 },
  { title: '归属人', key: 'owner', width: 180 },
  { title: '被授权用户', key: 'grantee', width: 180 },
  { title: '授权来源', key: 'sources', width: 260 },
  { title: '授权后用量', key: 'usageTotal', width: 260 },
  { title: '状态', key: 'status', width: 90 },
  { title: '授权时间', key: 'createdAt', width: 170 },
  { title: '操作', key: 'actions', width: 190, fixed: 'right' }
]

const usageDetailColumns = [
  { title: '系统账户', key: 'name', width: 220 },
  { title: '账号 ID', dataIndex: 'systemAccountId', key: 'systemAccountId', width: 260 },
  { title: '用量汇总', key: 'usage', width: 280 },
  { title: '最后使用', key: 'lastUsedAt', width: 180 }
]

const teamUsageColumns = [
  { title: '团队', key: 'teamName', width: 180 },
  { title: '成员', key: 'memberName', width: 180 },
  { title: '系统账户 ID', dataIndex: 'systemAccountId', key: 'systemAccountId', width: 240 },
  { title: '用量汇总', key: 'usage', width: 260 }
]

const resourceTypeOptions = [
  { label: '全部资源', value: 'all' },
  { label: 'AI账户', value: 'account' },
  { label: '分组', value: 'group' }
]
const createResourceTypeOptions = resourceTypeOptions.filter((item) => item.value !== 'all')

const resourceOptions = computed(() => {
  if (filters.resourceType === 'all') {
    return []
  }
  if (filters.resourceType === 'account') {
    return accounts.value.map((account) => ({ label: account.name, value: account.id }))
  }
  return groups.value.map((group) => ({ label: group.name, value: group.id }))
})

const createResourceOptions = computed(() => {
  if (createForm.resourceType === 'account') {
    return accounts.value.map((account) => ({ label: account.name, value: account.id }))
  }
  return groups.value.map((group) => ({ label: group.name, value: group.id }))
})

const teamOptions = computed(() => teams.value.map((team) => ({ label: team.name, value: team.id })))
const activeTeamOptions = computed(() => teams.value
  .filter((team) => team.status === 'active')
  .map((team) => ({ label: team.name, value: team.id })))
const userOptions = computed(() => users.value.map((user) => ({ label: `${user.displayName || user.username}（${user.username}）`, value: user.id })))
const activeFilterCount = computed(() => {
  let count = 0
  if (filters.resourceType !== 'all') count += 1
  if (filters.resourceId) count += 1
  if (filters.teamId) count += 1
  if (filters.granteeSystemAccountId) count += 1
  return count
})
const selectedTeamUsageSummaries = computed<TeamUsageSummary[]>(() => {
  const authorization = selectedAuthorization.value
  if (!authorization) {
    return []
  }
  return relatedTeamSources(authorization).map((teamSource) => {
    const members = selectedResourceAuthorizations.value
      .filter((item) => item.resourceType === authorization.resourceType && item.resourceId === authorization.resourceId && hasTeamSource(item, teamSource.teamId))
      .map((item) => ({
        key: `${teamSource.teamId}:${item.granteeSystemAccountId}`,
        teamId: teamSource.teamId,
        teamName: teamSource.teamName,
        systemAccountId: item.granteeSystemAccountId,
        systemAccountName: item.granteeSystemAccountName || item.granteeUsername || item.granteeSystemAccountId,
        usage: normalizeUsageSummary(item.usage)
      }))
      .sort((left, right) => left.systemAccountName.localeCompare(right.systemAccountName, 'zh-CN'))
    return {
      teamId: teamSource.teamId,
      teamName: teamSource.teamName,
      usage: sumUsageSummaries(members.map((member) => member.usage)),
      memberCount: members.length,
      members
    }
  }).filter((summary) => summary.memberCount > 0)
})
const selectedTeamUsageRows = computed(() => selectedTeamUsageSummaries.value.flatMap((summary) => summary.members))

function sourceLabel(source: AuthorizationSourceSummary): string {
  const baseLabel = source.sourceType === 'manual'
    ? '个人授权'
    : `团队授权：${source.sourceTeamName || source.sourceTeamId || '-'}`
  if (source.status === 'active') return baseLabel
  if (source.status === 'superseded') return `${baseLabel}（已被团队覆盖）`
  return `${baseLabel}（已收回）`
}

function sourceTagColor(source: AuthorizationSourceSummary): string {
  if (source.status !== 'active') return 'default'
  return source.sourceType === 'manual' ? 'cyan' : 'gold'
}

function sourceText(item: ResourceAuthorizationSummary): string {
  if (!item.authorizationSources?.length) return '-'
  return item.authorizationSources.map((source) => sourceLabel(source)).join('；')
}

function hasManualSource(item: ResourceAuthorizationSummary): boolean {
  return item.authorizationSources?.some((source) => source.sourceType === 'manual' && source.status === 'active') ?? false
}

function activeTeamSources(item: ResourceAuthorizationSummary): AuthorizationSourceSummary[] {
  return item.authorizationSources?.filter((source) => source.sourceType === 'team' && source.status === 'active' && source.sourceTeamId) ?? []
}

function hasTeamSource(item: ResourceAuthorizationSummary, teamId: string): boolean {
  return item.authorizationSources?.some((source) => source.sourceType === 'team' && source.sourceTeamId === teamId && source.status === 'active') ?? false
}

function relatedTeamSources(item: ResourceAuthorizationSummary): Array<{ teamId: string; teamName: string }> {
  const sourceMap = new Map<string, string>()
  for (const source of item.authorizationSources ?? []) {
    if (source.sourceType !== 'team' || !source.sourceTeamId) {
      continue
    }
    sourceMap.set(source.sourceTeamId, source.sourceTeamName || teams.value.find((team) => team.id === source.sourceTeamId)?.name || source.sourceTeamId)
  }
  if (filters.teamId && !sourceMap.has(filters.teamId)) {
    sourceMap.set(filters.teamId, teams.value.find((team) => team.id === filters.teamId)?.name || filters.teamId)
  }
  return [...sourceMap.entries()].map(([teamId, teamName]) => ({ teamId, teamName }))
}

function usageSummaryText(usage?: {
  requestCount?: number
  totalTokens?: number
  totalCost?: number
}): string {
  return `${formatNumber(usage?.requestCount)}req / ${formatUsageAmount(usage?.totalTokens)} / ${formatCost(usage?.totalCost)}`
}

function formatDateTime(value?: string): string {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

function formatNumber(value?: number): string {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0)
}

function formatUsageAmount(value?: number): string {
  const amount = value ?? 0
  const absoluteValue = Math.abs(amount)
  if (absoluteValue >= 1_000_000_000) return `${(amount / 1_000_000_000).toFixed(1)}B`
  if (absoluteValue >= 1_000_000) return `${(amount / 1_000_000).toFixed(1)}M`
  if (absoluteValue >= 1_000) return `${(amount / 1_000).toFixed(1)}K`
  return formatNumber(amount)
}

function formatCost(value?: number): string {
  return `$${(value ?? 0).toFixed(2)}`
}

function emptyUsageSummary(): AccountUsageSummary {
  return {
    requestCount: 0,
    clientCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function normalizeUsageSummary(usage?: Partial<AccountUsageSummary>): AccountUsageSummary {
  return {
    requestCount: usage?.requestCount ?? 0,
    clientCount: usage?.clientCount ?? 0,
    inputTokens: usage?.inputTokens ?? 0,
    outputTokens: usage?.outputTokens ?? 0,
    cacheReadTokens: usage?.cacheReadTokens ?? 0,
    totalTokens: usage?.totalTokens ?? 0,
    totalCost: usage?.totalCost ?? 0,
    lastUsedAt: usage?.lastUsedAt
  }
}

function sumUsageSummaries(items: Array<Partial<AccountUsageSummary> | undefined>): AccountUsageSummary {
  return items.reduce<AccountUsageSummary>((summary, usage) => {
    const current = normalizeUsageSummary(usage)
    const lastUsedAt = [summary.lastUsedAt, current.lastUsedAt]
      .filter((value): value is string => Boolean(value))
      .sort((left, right) => Date.parse(right) - Date.parse(left))[0]
    return {
      requestCount: summary.requestCount + current.requestCount,
      clientCount: summary.clientCount + current.clientCount,
      inputTokens: summary.inputTokens + current.inputTokens,
      outputTokens: summary.outputTokens + current.outputTokens,
      cacheReadTokens: summary.cacheReadTokens + current.cacheReadTokens,
      totalTokens: summary.totalTokens + current.totalTokens,
      totalCost: summary.totalCost + current.totalCost,
      lastUsedAt
    }
  }, emptyUsageSummary())
}

function normalizeUsageDetail(detail: AuthorizationUsageResponseDetail): AuthorizationUserUsageDetail | undefined {
  if (!detail.systemAccountId) {
    return undefined
  }
  const usage = normalizeUsageSummary(detail.usage ?? detail)
  return {
    systemAccountId: detail.systemAccountId,
    systemAccountName: detail.systemAccountName || detail.username,
    requestCount: usage.requestCount,
    clientCount: usage.clientCount,
    inputTokens: usage.inputTokens,
    outputTokens: usage.outputTokens,
    cacheReadTokens: usage.cacheReadTokens,
    totalTokens: usage.totalTokens,
    totalCost: usage.totalCost,
    lastUsedAt: usage.lastUsedAt
  }
}

function normalizeAuthorizationUsageResponse(payload: unknown, fallback: ResourceAuthorizationSummary): ResourceAuthorizationSummary {
  if (payload && typeof payload === 'object' && 'authorization' in payload) {
    const response = payload as AuthorizationUsageResponseShape
    const authorization = response.authorization ?? fallback
    const usageBySystemAccount = Array.isArray(response.details)
      ? response.details.map(normalizeUsageDetail).filter((detail): detail is AuthorizationUserUsageDetail => Boolean(detail))
      : authorization.usageBySystemAccount ?? []
    return {
      ...fallback,
      ...authorization,
      usage: normalizeUsageSummary(response.usage ?? authorization.usage ?? fallback.usage),
      usageBySystemAccount
    }
  }
  const authorization = (payload as Partial<ResourceAuthorizationSummary>) ?? {}
  return {
    ...fallback,
    ...authorization,
    usage: normalizeUsageSummary(authorization.usage ?? fallback.usage),
    usageBySystemAccount: Array.isArray(authorization.usageBySystemAccount) ? authorization.usageBySystemAccount : fallback.usageBySystemAccount ?? []
  }
}

function aggregateUsageBySystemAccount(items: ResourceAuthorizationSummary[]): AuthorizationUserUsageDetail[] {
  const summaryMap = new Map<string, AuthorizationUserUsageDetail>()
  for (const item of items) {
    const current = summaryMap.get(item.granteeSystemAccountId)
    const mergedUsage = sumUsageSummaries([current, item.usage])
    summaryMap.set(item.granteeSystemAccountId, {
      systemAccountId: item.granteeSystemAccountId,
      systemAccountName: item.granteeSystemAccountName || item.granteeUsername || item.granteeSystemAccountId,
      requestCount: mergedUsage.requestCount,
      clientCount: mergedUsage.clientCount,
      inputTokens: mergedUsage.inputTokens,
      outputTokens: mergedUsage.outputTokens,
      cacheReadTokens: mergedUsage.cacheReadTokens,
      totalTokens: mergedUsage.totalTokens,
      totalCost: mergedUsage.totalCost,
      lastUsedAt: mergedUsage.lastUsedAt
    })
  }
  return [...summaryMap.values()].sort((left, right) => {
    const leftName = left.systemAccountName || left.systemAccountId
    const rightName = right.systemAccountName || right.systemAccountId
    return leftName.localeCompare(rightName, 'zh-CN')
  })
}

async function loadMetaData() {
  const [accountResult, groupResult, teamResult, userResult] = await Promise.allSettled([
    api.accounts.list(),
    api.groups.list(),
    api.systemTeams.list(),
    api.systemAccounts.list()
  ])
  if (accountResult.status === 'fulfilled') {
    accounts.value = accountResult.value
  } else {
    console.error(accountResult.reason)
    message.error('加载可授权账户失败')
  }
  if (groupResult.status === 'fulfilled') {
    groups.value = groupResult.value
  } else {
    console.error(groupResult.reason)
    message.error('加载可授权分组失败')
  }
  if (teamResult.status === 'fulfilled') {
    teams.value = teamResult.value
  } else {
    console.error(teamResult.reason)
    message.error('加载团队列表失败')
  }
  if (userResult.status === 'fulfilled') {
    users.value = userResult.value
  } else {
    console.error(userResult.reason)
    message.error('加载系统账户列表失败')
  }
}

async function loadData() {
  loading.value = true
  try {
    const params = {
      resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
      resourceId: filters.resourceType === 'all' ? undefined : filters.resourceId,
      teamId: filters.teamId,
      granteeSystemAccountId: filters.granteeSystemAccountId
    }
    authorizations.value = await api.authorizations.list(params)
  } catch (error) {
    console.error(error)
    message.error('加载授权列表失败')
  } finally {
    loading.value = false
  }
}

function openCreateModal() {
  createForm.resourceType = filters.resourceType === 'group' ? 'group' : 'account'
  createForm.resourceId = ''
  createForm.granteeType = 'system_account'
  createForm.granteeId = ''
  createForm.remark = ''
  createModalOpen.value = true
}

function handleResourceTypeChange() {
  filters.resourceId = undefined
  void loadData()
}

function resetFilters() {
  filters.resourceType = 'all'
  filters.resourceId = undefined
  filters.teamId = undefined
  filters.granteeSystemAccountId = undefined
  void loadData()
}

async function createAuthorization() {
  if (!createForm.resourceId) {
    message.warning('请选择资源')
    return
  }
  if (!createForm.granteeId) {
    message.warning(createForm.granteeType === 'system_account' ? '请选择被授权用户' : '请选择团队')
    return
  }
  if (createForm.granteeType === 'team' && !teams.value.some((team) => team.id === createForm.granteeId && team.status === 'active')) {
    message.warning('请选择启用中的团队')
    return
  }
  try {
    await api.authorizations.create({
      resourceType: createForm.resourceType,
      resourceId: createForm.resourceId,
      granteeType: createForm.granteeType,
      granteeId: createForm.granteeId,
      remark: createForm.remark.trim() || undefined
    })
    createModalOpen.value = false
    message.success(createForm.granteeType === 'team' ? '团队授权已创建，成员会自动展开为用户授权' : '授权已创建')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('创建授权失败')
  }
}

async function revokeManualSource(item: ResourceAuthorizationSummary) {
  try {
    await api.authorizations.revoke(item.id, { sourceType: 'manual' })
    message.success('个人授权来源已收回')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('收回个人授权失败')
  }
}

async function revokeTeamSource(item: ResourceAuthorizationSummary, sourceTeamId: string) {
  try {
    await api.authorizations.revoke(item.id, { sourceType: 'team', sourceTeamId })
    message.success('团队授权来源已收回')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('收回团队授权失败')
  }
}

function revokeTeamSourceByMenu(event: { key: string | number }, item: ResourceAuthorizationSummary) {
  const sourceTeamId = String(event.key)
  if (!sourceTeamId) return
  void revokeTeamSource(item, sourceTeamId)
}

async function openUsageDetail(item: ResourceAuthorizationSummary) {
  try {
    const [usagePayload, resourceAuthorizations] = await Promise.all([
      api.authorizations.usage(item.id),
      api.authorizations.list({
        resourceType: item.resourceType,
        resourceId: item.resourceId,
        teamId: filters.teamId
      })
    ])
    const detail = normalizeAuthorizationUsageResponse(usagePayload, item)
    selectedResourceAuthorizations.value = resourceAuthorizations
    const usageBySystemAccount = aggregateUsageBySystemAccount(resourceAuthorizations)
    selectedAuthorization.value = {
      ...detail,
      usage: sumUsageSummaries(resourceAuthorizations.map((authorization) => authorization.usage)),
      usageBySystemAccount
    }
    selectedAuthorizationUsageDetails.value = usageBySystemAccount
    usageDetailOpen.value = true
  } catch (error) {
    console.error(error)
    message.error('加载用量明细失败')
  }
}

onMounted(async () => {
  applyRouteFilters()
  await loadMetaData()
  await loadData()
})

function applyRouteFilters() {
  filters.resourceType = 'all'
  filters.resourceId = undefined
  filters.teamId = undefined
  filters.granteeSystemAccountId = undefined
  const resourceType = route.query.resourceType === 'group' ? 'group' : route.query.resourceType === 'account' ? 'account' : undefined
  const resourceId = typeof route.query.resourceId === 'string' ? route.query.resourceId : undefined
  const teamId = typeof route.query.teamId === 'string' ? route.query.teamId : undefined
  const granteeSystemAccountId = typeof route.query.granteeSystemAccountId === 'string' ? route.query.granteeSystemAccountId : undefined
  if (resourceType) {
    filters.resourceType = resourceType
    createForm.resourceType = resourceType
  }
  if (resourceId) {
    filters.resourceId = resourceId
  }
  if (teamId) {
    filters.teamId = teamId
  }
  if (granteeSystemAccountId) {
    filters.granteeSystemAccountId = granteeSystemAccountId
  }
}
</script>

<style scoped>
.authorizations-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.filter-select {
  min-width: 140px;
}

.filter-resource {
  min-width: 220px;
}

.filter-user {
  min-width: 220px;
}

.authorization-help-content {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.authorization-help-section {
  padding: 14px;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  background: #fbfdff;
}

.authorization-help-title {
  display: block;
  margin-bottom: 6px;
  color: #0f172a;
  font-size: 15px;
  font-weight: 700;
}

.authorization-help-section p {
  margin: 0;
  color: #475569;
  font-size: 13px;
  line-height: 1.7;
}

.resource-cell {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.resource-name {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.sources-cell {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.usage-total-cell {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

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

.authorizations-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}
</style>
