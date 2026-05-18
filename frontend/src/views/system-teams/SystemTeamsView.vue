<template>
  <a-card class="page-card system-teams-page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="refreshTeams">
      <template #actions>
        <a-button v-if="isManagementView" type="primary" @click="openCreateTeam">新建授权团队</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table system-teams-table" :columns="columns" :data-source="teams" row-key="id" :loading="loading" :scroll-x="900" pull-refresh-enabled :refreshing="loading" @mobile-refresh="refreshTeams">
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
          {{ record.members?.length ?? record.memberCount ?? 0 }}
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
              <strong>{{ record.members?.length ?? record.memberCount ?? 0 }}</strong>
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
            :accounts="systemAccounts"
            :excluded-ids="usedMemberIds"
            mode="multiple"
            class="team-member-selector"
            :disabled="selectedTeam?.status !== 'active'"
            placeholder="选择一个或多个系统账户"
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
              {{ formatDateTime(record.joinedAt || record.createdAt) }}
            </template>
            <template v-else-if="column.key === 'actions'">
              <RowActions :actions="memberActions" @action-click="handleMemberAction($event, record)" />
            </template>
          </template>
          <template #card="{ record }">
            <article class="team-member-card">
              <div>
                <strong>{{ memberDisplayName(record) }}</strong>
                <span>{{ formatDateTime(record.joinedAt || record.createdAt) }}</span>
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
import { computed, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { RowActionItem } from '@/components/rowActions'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { SystemAccountSummary, SystemTeamMemberSummary, SystemTeamSummary } from '@/types/domain'

const loading = ref(false)
const { submitAction, submittingRef } = useSubmitAction('system-teams')
const teamSaving = submittingRef('system_teams.save')
const memberSaving = submittingRef('system_teams.add_members')

const teams = ref<SystemTeamSummary[]>([])
const systemAccounts = ref<SystemAccountSummary[]>([])
const memberOptionsLoaded = ref(false)
const memberOptionsScopeKey = ref('')
const { isManagementView } = useScopedMenuView()

const teamModalOpen = ref(false)
const memberModalOpen = ref(false)
const editingTeamId = ref<string>()
const selectedTeamId = ref<string>()

const teamForm = reactive({
  name: '',
  description: '',
  statusActive: true
})

const memberForm = reactive({
  systemAccountIds: [] as string[]
})

const columns = [
  { title: '授权团队名称', key: 'name', width: 180 },
  { title: '状态', key: 'status', width: 90 },
  { title: '成员数', key: 'memberCount', width: 90 },
  { title: '创建时间', key: 'createdAt', width: 170 },
  { title: '说明', key: 'description', width: 200 },
  { title: '操作', key: 'actions', width: 100, fixed: 'right' }
]

const memberColumns = computed(() => {
  const baseColumns = [
    { title: '成员', key: 'memberName', width: 220 },
    { title: '加入时间', key: 'joinedAt', width: 180 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '操作', key: 'actions', width: 100 })
  }
  return baseColumns
})

const selectedTeam = computed(() => teams.value.find((team) => team.id === selectedTeamId.value))
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
  return member.systemAccountName || member.username || member.systemAccountUsername || '未命名成员'
}

function formatDateTime(value?: string): string {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString('zh-CN', { hour12: false })
}

async function loadData(options: { forceOptions?: boolean } = {}) {
  loading.value = true
  try {
    const [teamList] = await Promise.all([
      isManagementView.value ? api.systemTeams.list() : api.myTeams.list(),
      loadMemberOptions(options.forceOptions === true)
    ])
    teams.value = teamList
  } catch (error) {
    console.error(error)
    message.error('加载授权团队数据失败')
  } finally {
    loading.value = false
  }
}

async function loadMemberOptions(force = false): Promise<void> {
  const scopeKey = isManagementView.value ? 'management' : 'self'
  if (!force && memberOptionsLoaded.value && memberOptionsScopeKey.value === scopeKey) {
    return
  }
  systemAccounts.value = isManagementView.value
    ? await api.systemAccounts.list()
    : []
  memberOptionsLoaded.value = true
  memberOptionsScopeKey.value = scopeKey
}

function refreshTeams() {
  void loadData({ forceOptions: true })
}

function openCreateTeam() {
  editingTeamId.value = undefined
  Object.assign(teamForm, {
    name: '',
    description: '',
    statusActive: true
  })
  teamModalOpen.value = true
}

function openEditTeam(team: SystemTeamSummary) {
  editingTeamId.value = team.id
  Object.assign(teamForm, {
    name: team.name,
    description: team.description ?? '',
    statusActive: team.status === 'active'
  })
  teamModalOpen.value = true
}

const saveTeam = submitAction('system_teams.save', async () => {
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
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存授权团队失败'))
  }
})

function openMemberModal(team: SystemTeamSummary) {
  selectedTeamId.value = team.id
  memberForm.systemAccountIds = []
  memberModalOpen.value = true
}

function handleTeamAction(key: string, team: SystemTeamSummary) {
  if (key === 'edit') {
    openEditTeam(team)
    return
  }
  if (key === 'members') {
    openMemberModal(team)
  }
}

function handleMemberAction(key: string, member: SystemTeamMemberSummary) {
  if (key === 'remove') {
    void removeMember(member.id)
  }
}

const addMembers = submitAction('system_teams.add_members', async () => {
  if (!selectedTeam.value) return
  if (!memberForm.systemAccountIds.length) {
    message.warning('请先选择成员')
    return
  }
  try {
    await api.systemTeams.addMembers(selectedTeam.value.id, {
      systemAccountIds: memberForm.systemAccountIds
    })
    memberForm.systemAccountIds = []
    message.success('成员已添加')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '添加成员失败'))
  } finally {
  }
})

async function removeMember(memberId: string) {
  if (!selectedTeam.value) return
  try {
    await api.systemTeams.removeMember(selectedTeam.value.id, memberId)
    message.success('成员已移除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '移除成员失败'))
  }
}

function hasDuplicateTeamName(name: string, excludeId?: string): boolean {
  const normalized = name.toLocaleLowerCase()
  return teams.value.some((team) => team.id !== excludeId && team.name.toLocaleLowerCase() === normalized)
}

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
