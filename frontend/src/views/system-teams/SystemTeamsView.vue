<template>
  <a-card class="page-card system-teams-page-card responsive-page-card">
    <ResponsiveListToolbar v-model:keyword="keyword" search-placeholder="搜索团队名称" :show-reset="Boolean(keyword.trim())" :refresh-loading="loading" @search="searchTeams" @reset="resetSearch" @refresh="refreshTeams">
      <template #actions>
        <a-button v-if="isManagementView" type="primary" @click="openCreateTeam">新建授权团队</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table system-teams-table" :columns="columns" :data-source="teams" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="900" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobileTeams" @mobile-refresh="refreshMobileTeams">
      <template #emptyText>
        <a-empty class="page-empty-card" :description="emptyTeamDescription" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="team-name-cell">
            <span class="team-name">{{ record.name }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'memberCount'">
          {{ record.activeMemberCount ?? record.memberCount ?? 0 }}
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'createdAt'">
          {{ formatDateTime(record.createdAt) }}
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="teamActions" @action-click="handleTeamAction($event, record)" />
        </template>
      </template>

      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>成员数</span>
              <strong>{{ record.activeMemberCount ?? record.memberCount ?? 0 }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>创建时间</span>
              <strong>{{ formatDateTime(record.createdAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="teamActions" @action-click="handleTeamAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="teamModalOpen" :title="editingTeamId ? '编辑授权团队' : '新建授权团队'" width="620px" :confirm-loading="teamSaving" :ok-button-props="{ disabled: teamSaving }" @ok="saveTeam">
      <a-form layout="vertical">
        <a-form-item label="授权团队名称" required>
          <a-input v-model:value="teamForm.name" placeholder="例如：产品运营团队" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="teamForm.description" :rows="3" placeholder="可选，描述授权团队职责与授权范围" />
        </a-form-item>
        <a-form-item label="状态">
          <a-switch v-model:checked="teamForm.statusActive" checked-children="启用" un-checked-children="停用" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="memberModalOpen" :title="selectedTeam ? `授权团队成员：${selectedTeam.name}` : '授权团队成员'" width="720px" :footer="null">
      <div class="team-members-modal">
        <div v-if="isManagementView" class="team-members-create-row">
          <SystemPrincipalSelect
            v-model:value="memberForm.systemAccountIds"
            v-model:selected-principals="memberForm.systemAccounts"
            :accounts="systemAccounts"
            :excluded-ids="usedMemberIds"
            :filter-option="false"
            :loading="memberOptionsLoading"
            mode="multiple"
            class="team-member-selector"
            :disabled="selectedTeam?.status !== 'active'"
            placeholder="输入用户名称搜索"
            @dropdown-visible-change="handleMemberOptionsDropdown"
            @search="handleMemberOptionsSearch"
          />
          <a-button type="primary" :loading="memberSaving" :disabled="selectedTeam?.status !== 'active' || memberSaving" v-submit-lock="{ key: 'system_teams.add_members', pending: memberSaving }" @click="addMembers">添加成员</a-button>
        </div>
        <a-alert
          v-if="isManagementView && selectedTeam?.status !== 'active'"
          type="warning"
          show-icon
          message="授权团队已停用，暂时不能添加新成员；如需继续维护，请先把授权团队状态改为启用。"
        />
        <ResponsiveDataList
          size="small"
          table-class="team-members-table"
          :columns="memberColumns"
          :data-source="activeTeamMembers"
          row-key="id"
          :loading="memberDetailLoading"
          :pagination="false"
          :table-scroll-enabled="false"
          :lock-body-scroll="false"
        >
          <template #emptyText>
            <a-empty description="还没有成员" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'memberName'">
              {{ memberDisplayName(record) }}
            </template>
            <template v-else-if="column.key === 'joinedAt'">
              {{ formatDateTime(record.joinedAt) }}
            </template>
            <template v-else-if="column.key === 'actions'">
              <RowActions v-if="isManagementView" :actions="memberActions" @action-click="handleMemberAction($event, record)" />
            </template>
          </template>
          <template #card="{ record }">
            <article class="team-member-card">
              <div>
                <strong>{{ memberDisplayName(record) }}</strong>
                <span>{{ formatDateTime(record.joinedAt) }}</span>
              </div>
              <RowActions v-if="isManagementView" :actions="memberActions" variant="button" @action-click="handleMemberAction($event, record)" />
            </article>
          </template>
        </ResponsiveDataList>
      </div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { RowActionItem } from '@/components/rowActions'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedSystemTeamsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatNumber } from '@/shared/formatters'
import { sanitizePaginationState, stringOrFallback, type PagePaginationState } from '@/shared/pageStateSanitizers'
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type { SystemTeamMemberSummary, SystemTeamSummary } from '@/types/domain'

interface SystemTeamsPageState {
  keyword: string
  pagination: PagePaginationState
}

const pageSize = 20
const pageStateCache = usePageStateCache<SystemTeamsPageState>(undefined, defaultSystemTeamsPageState, {
  sanitize: sanitizeSystemTeamsPageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const { submitAction, submittingRef } = useSubmitAction('system-teams')
const teamSaving = submittingRef('system_teams.save')
const memberSaving = submittingRef('system_teams.add_members')

const keyword = ref(initialPageState.keyword)
const { isManagementView } = useScopedMenuView()
const systemTeamsApi = useScopedSystemTeamsApi(isManagementView)
const {
  handleDropdown: handleMemberOptionsDropdown,
  handleSearch: handleMemberOptionsSearch,
  load: loadMemberOptions,
  loading: memberOptionsLoading,
  resetSearch: resetMemberOptionSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  errorMessage: '加载系统账户候选失败',
  selectedIds: () => memberForm.systemAccountIds
})

const teamModalOpen = ref(false)
const memberModalOpen = ref(false)
const memberDetailLoading = ref(false)
const editingTeamId = ref<string>()
const selectedTeamId = ref<string>()
const selectedTeamDetail = ref<SystemTeamSummary>()

const teamForm = reactive({
  name: '',
  description: '',
  statusActive: true
})

const memberForm = reactive({
  systemAccountIds: [] as string[],
  systemAccounts: [] as PrincipalSelection[]
})

const {
  items: teams,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileTeams,
  refreshMobile: refreshMobileTeams,
  resetPagination
} = useResponsivePagedList<SystemTeamSummary>({
  pageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${formatNumber(range?.[1] ?? total - 1)} 个授权团队，还有更多`
    : `共 ${formatNumber(total)} 个授权团队`,
  fetchPage: async (_options, pageState) => {
    const params = {
      keyword: keyword.value.trim() || undefined,
      page: pageState.current,
      pageSize: pageState.pageSize
    }
    return systemTeamsApi.list(params)
  },
  onError: (error) => {
    console.error(error)
    message.error('加载授权团队数据失败')
  }
})

const columns = [
  { title: '授权团队名称', key: 'name', width: 180 },
  { title: '状态', key: 'status', width: 90 },
  { title: '成员数', key: 'memberCount', width: 90 },
  { title: '创建时间', key: 'createdAt', width: 170 },
  { title: '说明', key: 'description', width: 200 },
  { title: '操作', key: 'actions', fixed: 'right' }
]

const memberColumns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '成员', key: 'memberName', width: 220 },
    { title: '加入时间', key: 'joinedAt', width: 180 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '操作', key: 'actions' })
  }
  return baseColumns
})

const selectedTeam = computed(() => selectedTeamDetail.value ?? teams.value.find((team) => team.id === selectedTeamId.value))
const activeTeamMembers = computed(() => selectedTeam.value ? activeMembers(selectedTeam.value) : [])
const usedMemberIds = computed(() => activeTeamMembers.value.map((item) => item.systemAccountId))
const emptyTeamDescription = computed(() => isManagementView.value ? '还没有授权团队，先创建一个授权团队并添加成员。' : '你还没有加入任何授权团队。')
const teamActions = computed<RowActionItem[]>(() => isManagementView.value
  ? [
      { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
      { key: 'members', label: '成员管理', icon: 'members', tone: 'purple' }
    ]
  : [
      { key: 'members', label: '成员查看', icon: 'members', tone: 'info' }
    ])
const memberActions: RowActionItem[] = [
  {
    key: 'remove',
    label: '移除',
    icon: 'delete',
    tone: 'danger',
    confirmTitle: '确认移除该成员？',
    confirmOkText: '移除'
  }
]

function activeMembers(team: SystemTeamSummary): SystemTeamMemberSummary[] {
  return (team.members ?? []).filter((member) => member.status === 'active')
}

function memberDisplayName(member: SystemTeamMemberSummary): string {
  return member.systemAccountName || '未命名成员'
}

function refreshTeams() {
  void loadData()
}

function searchTeams() {
  resetPagination()
  void loadData()
}

function resetSearch() {
  keyword.value = ''
  pageStateCache.clear()
  searchTeams()
}

function openCreateTeam() {
  if (!ensureManagementAction()) return
  editingTeamId.value = undefined
  Object.assign(teamForm, {
    name: '',
    description: '',
    statusActive: true
  })
  teamModalOpen.value = true
}

function openEditTeam(team: SystemTeamSummary) {
  if (!ensureManagementAction()) return
  editingTeamId.value = team.id
  Object.assign(teamForm, {
    name: team.name,
    description: team.description ?? '',
    statusActive: team.status === 'active'
  })
  teamModalOpen.value = true
}

const saveTeam = submitAction('system_teams.save', async () => {
  if (!ensureManagementAction()) return
  const teamName = teamForm.name.trim()
  if (!teamName) {
    message.warning('请填写授权团队名称')
    return
  }
  if (hasDuplicateTeamName(teamName, editingTeamId.value)) {
    message.warning('授权团队名称已存在')
    return
  }
  const payload = {
    name: teamName,
    description: teamForm.description.trim() || undefined,
    status: (teamForm.statusActive ? 'active' : 'disabled') as 'active' | 'disabled'
  }
  try {
    if (editingTeamId.value) {
      await api.systemTeams.update(editingTeamId.value, payload)
      message.success('授权团队已更新')
    } else {
      await api.systemTeams.create(payload)
      message.success('授权团队已创建')
    }
    teamModalOpen.value = false
    resetPagination()
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存授权团队失败'))
  }
})

async function openMemberModal(team: SystemTeamSummary) {
  selectedTeamId.value = team.id
  selectedTeamDetail.value = undefined
  memberForm.systemAccountIds = []
  memberForm.systemAccounts = []
  resetMemberOptionSearch()
  try {
    await loadSelectedTeamDetail(team.id)
    memberModalOpen.value = true
    void loadMemberOptions()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载团队成员失败'))
  }
}

function handleTeamAction(key: string, team: SystemTeamSummary) {
  if (key === 'edit') {
    if (!ensureManagementAction()) return
    openEditTeam(team)
    return
  }
  if (key === 'members') {
    void openMemberModal(team)
  }
}

function handleMemberAction(key: string, member: SystemTeamMemberSummary) {
  if (!ensureManagementAction()) return
  if (key === 'remove') {
    void removeMember(member.id)
  }
}

const addMembers = submitAction('system_teams.add_members', async () => {
  if (!ensureManagementAction()) return
  if (!selectedTeam.value) return
  if (!memberForm.systemAccountIds.length) {
    message.warning('请先选择成员')
    return
  }
  const teamId = selectedTeam.value.id
  try {
    await api.systemTeams.addMembers(teamId, {
      systemAccountIds: memberForm.systemAccountIds
    })
    memberForm.systemAccountIds = []
    memberForm.systemAccounts = []
    message.success('成员已添加')
    await Promise.all([
      loadData(),
      loadSelectedTeamDetail(teamId),
      loadMemberOptions()
    ])
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '添加成员失败'))
  }
})

async function removeMember(memberId: string) {
  if (!ensureManagementAction()) return
  if (!selectedTeam.value) return
  const teamId = selectedTeam.value.id
  try {
    await api.systemTeams.removeMember(teamId, memberId)
    message.success('成员已移除')
    await Promise.all([
      loadData(),
      loadSelectedTeamDetail(teamId),
      loadMemberOptions()
    ])
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '移除成员失败'))
  }
}

async function loadSelectedTeamDetail(teamId: string): Promise<void> {
  memberDetailLoading.value = true
  try {
    selectedTeamDetail.value = await systemTeamsApi.detail(teamId)
  } finally {
    memberDetailLoading.value = false
  }
}

function hasDuplicateTeamName(name: string, excludeId?: string): boolean {
  const normalized = name.toLocaleLowerCase()
  return teams.value.some((team) => team.id !== excludeId && team.name.toLocaleLowerCase() === normalized)
}

function ensureManagementAction(): boolean {
  if (isManagementView.value) return true
  message.warning('当前是只读视图，不能维护授权团队')
  return false
}

function defaultSystemTeamsPageState(): SystemTeamsPageState {
  return {
    keyword: '',
    pagination: { current: 1, pageSize }
  }
}

function sanitizeSystemTeamsPageState(value: unknown, fallback: SystemTeamsPageState): SystemTeamsPageState {
  const source = value && typeof value === 'object' ? value as Partial<SystemTeamsPageState> : {}
  return {
    keyword: stringOrFallback(source.keyword, fallback.keyword),
    pagination: sanitizePaginationState(source.pagination, fallback.pagination)
  }
}

function snapshotPageState(): SystemTeamsPageState {
  return {
    keyword: keyword.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize }
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

onMounted(loadData)
</script>

<style scoped>
.system-teams-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.team-name-cell {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.team-name {
  color: #0f172a;
  font-weight: 400;
}

.team-members-modal {
  display: grid;
  gap: 12px;
}

.team-members-create-row {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 10px;
}

.team-member-selector {
  min-width: 0;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}

.system-teams-table :deep(.ant-table-thead > tr > th),
.system-teams-table :deep(.ant-table-cell),
.team-members-table :deep(.ant-table-thead > tr > th),
.team-members-table :deep(.ant-table-cell) {
  font-weight: 400;
  white-space: nowrap;
}

.system-teams-page-card :deep(.mobile-list-card-title),
.system-teams-page-card :deep(.mobile-list-meta-item strong) {
  font-weight: 400;
}

.team-member-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.team-member-card div {
  display: grid;
  gap: 4px;
}

.team-member-card strong {
  color: #0f172a;
  font-weight: 400;
}

.team-member-card span {
  color: #64748b;
  font-size: 12px;
}

@media (max-width: 768px) {
  .team-members-create-row {
    grid-template-columns: 1fr;
  }
}
</style>
