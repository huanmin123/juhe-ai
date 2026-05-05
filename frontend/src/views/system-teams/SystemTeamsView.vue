<template>
  <a-card class="page-card system-teams-page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadData">
      <template #actions>
        <a-button v-if="isAdmin" type="primary" @click="openCreateTeam">新建团队</a-button>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table system-teams-table" :columns="columns" :data-source="teams" row-key="id" :loading="loading" :scroll-x="930" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadData">
      <template #emptyText>
        <a-empty class="page-empty-card" description="还没有团队，先创建一个团队并添加成员。" />
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
          <a-space v-if="isAdmin" :size="8">
            <a-button type="link" size="small" @click="openEditTeam(record)">编辑</a-button>
            <a-button type="link" size="small" @click="openMemberModal(record)">成员管理</a-button>
          </a-space>
          <a-button v-else type="link" size="small" @click="openMemberModal(record)">成员查看</a-button>
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
          <div class="mobile-list-card-actions two-actions">
            <template v-if="isAdmin">
              <a-button type="primary" @click="openEditTeam(record)">编辑</a-button>
              <a-button @click="openMemberModal(record)">成员管理</a-button>
            </template>
            <a-button v-else type="primary" @click="openMemberModal(record)">成员查看</a-button>
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="teamModalOpen" :title="editingTeamId ? '编辑团队' : '新建团队'" width="620px" @ok="saveTeam">
      <a-form layout="vertical">
        <a-form-item label="团队名称" required>
          <a-input v-model:value="teamForm.name" placeholder="例如：产品运营团队" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="teamForm.description" :rows="3" placeholder="可选，描述团队职责与授权范围" />
        </a-form-item>
        <a-form-item label="状态">
          <a-switch v-model:checked="teamForm.statusActive" checked-children="启用" un-checked-children="停用" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="memberModalOpen" :title="selectedTeam ? `团队成员：${selectedTeam.name}` : '团队成员'" width="720px" :footer="null">
      <div class="team-members-modal">
        <div v-if="isAdmin" class="team-members-create-row">
          <a-select
            v-model:value="memberForm.systemAccountIds"
            mode="multiple"
            show-search
            option-filter-prop="label"
            class="team-member-selector"
            :options="memberOptions"
            :disabled="selectedTeam?.status !== 'active'"
            placeholder="选择一个或多个系统账户"
          />
          <a-button type="primary" :loading="memberSaving" :disabled="selectedTeam?.status !== 'active'" @click="addMembers">添加成员</a-button>
        </div>
        <a-alert
          v-if="isAdmin && selectedTeam?.status !== 'active'"
          type="warning"
          show-icon
          message="团队已停用，暂时不能添加新成员；如需继续维护，请先把团队状态改为启用。"
        />
        <a-table size="small" :columns="memberColumns" :data-source="activeTeamMembers" row-key="id" :pagination="false">
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
              <a-popconfirm title="确认移除该成员？" @confirm="removeMember(record.id)">
                <a-button type="link" size="small" danger>移除</a-button>
              </a-popconfirm>
            </template>
          </template>
        </a-table>
      </div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import axios from 'axios'
import { message } from 'ant-design-vue'
import { computed, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { authState } from '@/composables/useAuth'
import type { SystemAccountSummary, SystemTeamMemberSummary, SystemTeamSummary } from '@/types/domain'

const loading = ref(false)
const memberSaving = ref(false)

const teams = ref<SystemTeamSummary[]>([])
const systemAccounts = ref<SystemAccountSummary[]>([])
const isAdmin = authState.isAdmin

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
  { title: '团队名称', key: 'name', width: 220 },
  { title: '状态', key: 'status', width: 90 },
  { title: '成员数', key: 'memberCount', width: 90 },
  { title: '说明', key: 'description', width: 200 },
  { title: '创建时间', key: 'createdAt', width: 170 },
  { title: '操作', key: 'actions', width: 160, fixed: 'right' }
]

const memberColumns = computed(() => {
  const baseColumns = [
    { title: '成员', key: 'memberName', width: 220 },
    { title: '加入时间', key: 'joinedAt', width: 180 }
  ]
  if (isAdmin.value) {
    baseColumns.push({ title: '操作', key: 'actions', width: 100 })
  }
  return baseColumns
})

const selectedTeam = computed(() => teams.value.find((team) => team.id === selectedTeamId.value))
const activeTeamMembers = computed(() => selectedTeam.value ? activeMembers(selectedTeam.value) : [])
const usedMemberIdSet = computed(() => new Set(activeTeamMembers.value.map((item) => item.systemAccountId)))
const memberOptions = computed(() => systemAccounts.value
  .filter((account) => account.status === 'active' && !usedMemberIdSet.value.has(account.id))
  .map((account) => ({
    label: `${account.displayName || account.username}（${account.username}）`,
    value: account.id
  })))

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

async function loadData() {
  loading.value = true
  try {
    const [teamList, accounts] = await Promise.all([
      api.systemTeams.list(),
      isAdmin.value ? api.systemAccounts.list() : Promise.resolve([] as SystemAccountSummary[])
    ])
    teams.value = teamList
    systemAccounts.value = accounts
  } catch (error) {
    console.error(error)
    message.error('加载团队数据失败')
  } finally {
    loading.value = false
  }
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

async function saveTeam() {
  const teamName = teamForm.name.trim()
  if (!teamName) {
    message.warning('请填写团队名称')
    return
  }
  if (hasDuplicateTeamName(teamName, editingTeamId.value)) {
    message.warning('团队名称已存在')
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
      message.success('团队已更新')
    } else {
      await api.systemTeams.create(payload)
      message.success('团队已创建')
    }
    teamModalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存团队失败'))
  }
}

function openMemberModal(team: SystemTeamSummary) {
  selectedTeamId.value = team.id
  memberForm.systemAccountIds = []
  memberModalOpen.value = true
}

async function addMembers() {
  if (!selectedTeam.value) return
  if (!memberForm.systemAccountIds.length) {
    message.warning('请先选择成员')
    return
  }
  memberSaving.value = true
  try {
    await api.systemTeams.addMembers(selectedTeam.value.id, {
      systemAccountIds: memberForm.systemAccountIds
    })
    memberForm.systemAccountIds = []
    message.success('成员已添加')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('添加成员失败')
  } finally {
    memberSaving.value = false
  }
}

async function removeMember(memberId: string) {
  if (!selectedTeam.value) return
  try {
    await api.systemTeams.removeMember(selectedTeam.value.id, memberId)
    message.success('成员已移除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('移除成员失败')
  }
}

function hasDuplicateTeamName(name: string, excludeId?: string): boolean {
  const normalized = name.toLocaleLowerCase()
  return teams.value.some((team) => team.id !== excludeId && team.name.toLocaleLowerCase() === normalized)
}

function extractApiErrorMessage(error: unknown, fallback: string): string {
  if (axios.isAxiosError<{ message?: string }>(error)) {
    return error.response?.data?.message ?? fallback
  }
  return fallback
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
  font-weight: 700;
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

.system-teams-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

@media (max-width: 768px) {
  .team-members-create-row {
    grid-template-columns: 1fr;
  }
}
</style>
