<template>
  <a-card class="page-card authorizations-page-card responsive-page-card">
    <AuthorizationFilterToolbar
      :filters="filters"
      :resource-type-options="resourceTypeOptions"
      :resource-options="resourceOptions"
      :teams="teams"
      :users="users"
      :active-filter-count="activeFilterCount"
      :loading="loading"
      @reset="resetFilters"
      @refresh="loadData"
      @help="helpOpen = true"
      @create="openCreateModal"
      @resource-type-change="handleResourceTypeChange"
    />

    <AuthorizationList
      :authorizations="authorizations"
      :loading="loading"
      @refresh="loadData"
      @usage-detail="openUsageDetail"
      @menu-click="handleActionMenuClick"
    />

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
          <div class="usage-section-title">团队来源今日消耗</div>
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
          <div class="usage-section-title usage-subsection-title">团队来源成员今日消耗</div>
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
import { message } from 'ant-design-vue'
import type { Dayjs } from 'dayjs'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { AccountSummary, AuthorizationUserUsageDetail, GroupSummary, ResourceAuthorizationSummary, SystemAccountSummary, SystemTeamSummary } from '@/types/domain'
import AuthorizationFilterToolbar from './AuthorizationFilterToolbar.vue'
import AuthorizationList from './AuthorizationList.vue'
import {
  buildTeamUsageSummaries,
  extractApiErrorMessage,
  formatDateTime,
  formatServerDateTimeInput,
  normalizeAuthorizationUsageResponse,
  parseDatePickerValue,
  usageSummaryText,
  type TeamUsageSummary
} from './authorizationFormatters'
import {
  type AuthorizationFilterResourceType,
  authorizationResourceTypeOptions,
  authorizationTeamUsageColumns,
  authorizationUsageDetailColumns,
  createAuthorizationResourceTypeOptions
} from './authorizationTableColumns'

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

const usageDetailColumns = authorizationUsageDetailColumns
const teamUsageColumns = authorizationTeamUsageColumns
const resourceTypeOptions = authorizationResourceTypeOptions
const createResourceTypeOptions = createAuthorizationResourceTypeOptions

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
    const usagePayload = await api.authorizations.usage(item.id)
    const detail = normalizeAuthorizationUsageResponse(usagePayload, item)
    selectedResourceAuthorizations.value = [detail]
    selectedAuthorization.value = detail
    selectedAuthorizationUsageDetails.value = detail.usageBySystemAccount ?? []
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
