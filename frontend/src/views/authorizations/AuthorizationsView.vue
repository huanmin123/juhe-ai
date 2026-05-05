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
        <SystemPrincipalSelect
          v-model:value="filters.teamId"
          :teams="teams"
          :active-only="false"
          allow-clear
          class="filter-select responsive-list-inline-filter"
          placeholder="筛选授权来源"
          scope="team"
          @change="loadData"
        />
        <SystemPrincipalSelect
          v-model:value="filters.granteeSystemAccountId"
          :accounts="users"
          :active-only="false"
          allow-clear
          class="filter-select filter-user responsive-list-inline-filter"
          placeholder="筛选被授权用户"
          @change="loadData"
        />
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
          <span>授权来源</span>
          <SystemPrincipalSelect v-model:value="filters.teamId" :teams="teams" :active-only="false" allow-clear scope="team" placeholder="筛选授权来源" @change="loadData" />
        </label>
        <label class="mobile-filter-field">
          <span>被授权用户</span>
          <SystemPrincipalSelect v-model:value="filters.granteeSystemAccountId" :accounts="users" :active-only="false" allow-clear placeholder="筛选被授权用户" @change="loadData" />
        </label>
      </template>
    </ResponsiveListToolbar>

      <ResponsiveDataList table-class="page-table authorizations-table" :columns="columns" :data-source="authorizations" row-key="id" :loading="loading" :scroll-x="1320" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadData">
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无授权记录，请先新增授权。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'resource'">
          <div class="resource-cell">
            <span class="resource-name">{{ record.resourceName || record.resourceId }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'owner'">
          {{ record.resourceOwnerSystemAccountName || record.resourceOwnerSystemAccountId }}
        </template>
        <template v-else-if="column.key === 'grantee'">
          <div class="grantee-cell">
            <span>{{ record.granteeSystemAccountName || record.granteeSystemAccountId }}</span>
            <a-tag v-if="granteeSourceLabel(record)" :color="granteeSourceTagColor(record)">{{ granteeSourceLabel(record) }}</a-tag>
          </div>
        </template>
        <template v-else-if="column.key === 'usageTotal'">
          <div class="usage-total-cell">
            <span>{{ usageSummaryText(record.usage) }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'status'">
          <StatusTag :color="statusTagColor(record.status)" :label="statusLabel(record.status)" />
        </template>
        <template v-else-if="column.key === 'createdAt'">
          {{ formatDateTime(record.createdAt) }}
        </template>
        <template v-else-if="column.key === 'remark'">
          <span>{{ record.remark || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <div class="authorization-actions">
            <a-button size="small" @click="openUsageDetail(record)">明细</a-button>
            <a-dropdown>
              <a-button size="small">
                更多
              </a-button>
              <template #overlay>
                <a-menu @click="handleActionMenuClick($event, record)">
                  <a-menu-item key="edit-expire">修改到期时间</a-menu-item>
                  <a-menu-item v-if="record.status === 'active'" key="pause">暂停授权</a-menu-item>
                  <a-menu-item v-if="record.status === 'paused'" key="resume">恢复授权</a-menu-item>
                  <a-menu-item v-if="record.status === 'active' && hasManualSource(record)" key="revoke-manual">回收</a-menu-item>
                  <a-sub-menu v-if="activeTeamSources(record).length" key="revoke-team" title="回收">
                    <a-menu-item v-for="teamSource in activeTeamSources(record)" :key="`team:${teamSource.sourceTeamId}`">
                      {{ teamSource.sourceTeamName || teamSource.sourceTeamId }}
                    </a-menu-item>
                  </a-sub-menu>
                </a-menu>
              </template>
            </a-dropdown>
          </div>
        </template>
      </template>

      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.resourceName || record.resourceId }}</div>
            <div class="mobile-list-card-tags">
              <StatusTag :color="statusTagColor(record.status)" :label="statusLabel(record.status)" />
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>归属人</span>
              <strong>{{ record.resourceOwnerSystemAccountName || record.resourceOwnerSystemAccountId }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>被授权用户</span>
              <strong class="mobile-user-tag-line">
                <span>{{ record.granteeSystemAccountName || record.granteeSystemAccountId }}</span>
                <a-tag v-if="granteeSourceLabel(record)" :color="granteeSourceTagColor(record)">{{ granteeSourceLabel(record) }}</a-tag>
              </strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>用量(日)</span>
              <strong>{{ usageSummaryText(record.usage) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.remark || '-' }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions two-actions">
            <a-button @click="openUsageDetail(record)">明细</a-button>
            <a-dropdown>
              <a-button>
                更多
              </a-button>
              <template #overlay>
                <a-menu @click="handleActionMenuClick($event, record)">
                  <a-menu-item key="edit-expire">修改到期时间</a-menu-item>
                  <a-menu-item v-if="record.status === 'active'" key="pause">暂停授权</a-menu-item>
                  <a-menu-item v-if="record.status === 'paused'" key="resume">恢复授权</a-menu-item>
                  <a-menu-item v-if="record.status === 'active' && hasManualSource(record)" key="revoke-manual">回收</a-menu-item>
                  <a-sub-menu v-if="activeTeamSources(record).length" key="revoke-team" title="回收">
                    <a-menu-item v-for="teamSource in activeTeamSources(record)" :key="`team:${teamSource.sourceTeamId}`">
                      {{ teamSource.sourceTeamName || teamSource.sourceTeamId }}
                    </a-menu-item>
                  </a-sub-menu>
                </a-menu>
              </template>
            </a-dropdown>
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
          <p>团队授权会自动展开到团队内启用成员；新增成员、移除成员、团队停用或系统账户停用后，会影响对应用户是否还能继续使用。</p>
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
          <SystemPrincipalSelect
            v-model:value="createForm.granteeId"
            :accounts="users"
            :teams="teams"
            :scope="createForm.granteeType === 'system_account' ? 'system_account' : 'team'"
            :disabled="!hasCreateGranteeOptions"
            :placeholder="createForm.granteeType === 'system_account' ? '选择一个用户' : '选择一个团队'"
          />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="createForm.remark" :rows="3" placeholder="可选，填写授权用途或范围说明" />
        </a-form-item>
        <a-form-item label="到期时间">
          <a-date-picker v-model:value="createForm.expiresAt" show-time allow-clear style="width: 100%" />
          <div class="form-help">可选，支持选择明天 0 点或中午 12 点，到期后授权自动变为“授权到期”。</div>
        </a-form-item>
        <a-alert
          v-if="createForm.granteeType === 'team'"
          type="info"
          show-icon
          message="团队授权会自动展开到团队内所有启用成员；成员移除后，对应团队来源授权也会自动回收。"
        />
      </a-form>
    </a-modal>

    <a-modal v-model:open="expireModalOpen" title="修改到期时间" width="520px" @ok="confirmExpireChange">
      <a-form layout="vertical">
        <a-form-item label="到期时间">
          <a-date-picker v-model:value="expireForm.expiresAt" show-time allow-clear style="width: 100%" />
          <div class="form-help">清空后表示不设置自动回收时间。</div>
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="usageDetailOpen" :title="selectedAuthorization ? `今日用量明细：${selectedAuthorization.resourceName || selectedAuthorization.resourceId}` : '今日用量明细'" width="960px" :footer="null">
      <template v-if="selectedAuthorization">
        <a-alert
          class="usage-alert"
          type="info"
          show-icon
          :message="`今日授权总计（不含归属人自己消耗）：${usageSummaryText(selectedAuthorization.usage)}`"
        />
        <div v-if="selectedTeamUsageSummaries.length" class="usage-team-section">
          <div class="usage-section-title">团队今日总消耗</div>
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
          <div class="usage-section-title usage-subsection-title">团队成员今日分别消耗</div>
          <a-table size="small" :columns="teamUsageColumns" :data-source="selectedTeamUsageRows" row-key="key" :pagination="false">
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
        <a-table size="small" :columns="usageDetailColumns" :data-source="selectedAuthorizationUsageDetails" row-key="systemAccountId" :pagination="false">
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
  </a-card>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import type { Dayjs } from 'dayjs'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import StatusTag from '@/components/StatusTag.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { AccountSummary, AuthorizationUserUsageDetail, GroupSummary, ResourceAuthorizationSummary, SystemAccountSummary, SystemTeamSummary } from '@/types/domain'
import {
  activeTeamSources,
  aggregateUsageBySystemAccount,
  buildTeamUsageSummaries,
  extractApiErrorMessage,
  formatDateTime,
  formatServerDateTimeInput,
  granteeSourceLabel,
  granteeSourceTagColor,
  hasManualSource,
  normalizeAuthorizationUsageResponse,
  parseDatePickerValue,
  statusLabel,
  statusTagColor,
  sumUsageSummaries,
  usageSummaryText,
  type TeamUsageSummary
} from './authorizationFormatters'

type AuthorizationFilterResourceType = 'all' | 'account' | 'group'

const loading = ref(false)
const createModalOpen = ref(false)
const usageDetailOpen = ref(false)
const helpOpen = ref(false)
const expireModalOpen = ref(false)
const route = useRoute()

const authorizations = ref<ResourceAuthorizationSummary[]>([])
const accounts = ref<AccountSummary[]>([])
const groups = ref<GroupSummary[]>([])
const teams = ref<SystemTeamSummary[]>([])
const users = ref<SystemAccountSummary[]>([])

const selectedAuthorization = ref<ResourceAuthorizationSummary>()
const expireAuthorization = ref<ResourceAuthorizationSummary>()
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
  remark: '',
  expiresAt: undefined as Dayjs | undefined
})

const expireForm = reactive({
  expiresAt: undefined as Dayjs | undefined
})

const columns = [
  { title: 'AI账户名称', key: 'resource', width: 260 },
  { title: '归属人', key: 'owner', width: 180 },
  { title: '被授权用户', key: 'grantee', width: 180 },
  { title: '用量(日)', key: 'usageTotal', width: 260 },
  { title: '状态', key: 'status', width: 90 },
  { title: '授权时间', key: 'createdAt', width: 170 },
  { title: '说明', key: 'remark', width: 200 },
  { title: '操作', key: 'actions', width: 140, fixed: 'right' }
]

const usageDetailColumns = [
  { title: '系统账户', key: 'name', width: 220 },
  { title: '今日用量', key: 'usage', width: 280 },
  { title: '最后使用', key: 'lastUsedAt', width: 180 }
]

const teamUsageColumns = [
  { title: '团队', key: 'teamName', width: 180 },
  { title: '成员', key: 'memberName', width: 180 },
  { title: '今日用量', key: 'usage', width: 260 }
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

const hasCreateGranteeOptions = computed(() => createForm.granteeType === 'system_account'
  ? users.value.some((user) => user.status === 'active')
  : teams.value.some((team) => team.status === 'active'))
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
  return buildTeamUsageSummaries(authorization, selectedResourceAuthorizations.value, teams.value, filters.teamId)
})
const selectedTeamUsageRows = computed(() => selectedTeamUsageSummaries.value.flatMap((summary) => summary.members))

watch(() => createForm.granteeType, () => {
  createForm.granteeId = ''
})

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
      granteeSystemAccountId: filters.granteeSystemAccountId,
      status: 'all' as const
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
  createForm.expiresAt = undefined
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
  if (createForm.granteeType === 'system_account' && !users.value.some((user) => user.id === createForm.granteeId && user.status === 'active')) {
    message.warning('请选择启用中的系统账户')
    return
  }
  if (createForm.granteeType === 'team' && !teams.value.some((team) => team.id === createForm.granteeId && team.status === 'active')) {
    message.warning('请选择启用中的团队')
    return
  }
  try {
    const expiresAt = formatServerDateTimeInput(createForm.expiresAt) ?? undefined
    await api.authorizations.create({
      resourceType: createForm.resourceType,
      resourceId: createForm.resourceId,
      granteeType: createForm.granteeType,
      granteeId: createForm.granteeId,
      remark: createForm.remark.trim() || undefined,
      expiresAt
    })
    createModalOpen.value = false
    message.success(createForm.granteeType === 'team' ? '团队授权已创建，成员会自动展开为用户授权' : '授权已创建')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '创建授权失败'))
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

function handleActionMenuClick(event: { key: string | number }, item: ResourceAuthorizationSummary) {
  const key = String(event.key)
  if (key === 'edit-expire') {
    openExpireModal(item)
    return
  }
  if (key === 'pause') {
    void updateAuthorizationStatus(item, 'paused')
    return
  }
  if (key === 'resume') {
    void updateAuthorizationStatus(item, 'active')
    return
  }
  if (key === 'revoke-manual') {
    void revokeManualSource(item)
    return
  }
  if (key.startsWith('team:')) {
    const sourceTeamId = key.slice('team:'.length)
    if (sourceTeamId) {
      void revokeTeamSource(item, sourceTeamId)
    }
  }
}

async function updateAuthorizationStatus(item: ResourceAuthorizationSummary, status: 'active' | 'paused') {
  try {
    await api.authorizations.update(item.id, { status })
    message.success(status === 'active' ? '授权已恢复' : '授权已暂停')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, status === 'active' ? '恢复授权失败' : '暂停授权失败'))
  }
}

function openExpireModal(item: ResourceAuthorizationSummary) {
  expireAuthorization.value = item
  expireForm.expiresAt = parseDatePickerValue(item.expiresAt)
  expireModalOpen.value = true
}

async function confirmExpireChange() {
  const authorization = expireAuthorization.value
  if (!authorization) {
    expireModalOpen.value = false
    return
  }
  try {
    await api.authorizations.updateExpire(authorization.id, {
      expiresAt: formatServerDateTimeInput(expireForm.expiresAt)
    })
    expireModalOpen.value = false
    expireAuthorization.value = undefined
    message.success('到期时间已更新')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '修改到期时间失败'))
  }
}

async function openUsageDetail(item: ResourceAuthorizationSummary) {
  try {
    const [usagePayload, resourceAuthorizations] = await Promise.all([
      api.authorizations.usage(item.id),
      api.authorizations.list({
        resourceType: item.resourceType,
        resourceId: item.resourceId,
        teamId: filters.teamId,
        status: 'all'
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

.filter-resource,
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

.resource-cell,
.grantee-cell,
.usage-total-cell {
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

.authorization-actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}
</style>
