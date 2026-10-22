<template>
  <a-modal
    :open="open"
    :title="`超级分组成员${groupName ? `：${groupName}` : ''}`"
    width="760px"
    :confirm-loading="saving"
    :ok-button-props="{ type: 'primary', disabled: saving || !loaded }"
    ok-text="保存成员"
    @ok="save"
    @update:open="emit('update:open', $event)"
  >
    <a-alert
      class="members-alert"
      type="info"
      show-icon
      message="成员必须与超级分组属于同一所有者、使用相同调度策略；最多 20 行。成员可随时添加、停用或调整顺序；未配置启用成员时该目标暂不可用。"
    />
    <div class="members-toolbar">
      <a-select
        v-model:value="addGroupId"
        :options="candidateOptions"
        show-search
        allow-clear
        :filter-option="filterOption"
        placeholder="选择要加入的普通分组"
        class="members-select"
      />
      <a-button type="primary" :disabled="!addGroupId || members.length >= 20" @click="addMember">添加成员</a-button>
    </div>
    <a-spin :spinning="loading">
      <a-table :data-source="members" :columns="columns" row-key="memberGroupId" size="small" :pagination="false">
        <template #bodyCell="{ column, record, index }">
          <template v-if="column.key === 'enabled'">
            <a-switch v-model:checked="record.enabled" size="small" />
          </template>
          <template v-else-if="column.key === 'actions'">
            <a-space>
              <a-button size="small" :disabled="index === 0" @click="move(index, -1)">上移</a-button>
              <a-button size="small" :disabled="index === members.length - 1" @click="move(index, 1)">下移</a-button>
              <a-button size="small" danger @click="remove(index)">移除</a-button>
            </a-space>
          </template>
        </template>
      </a-table>
    </a-spin>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { message } from 'ant-design-vue'

import type { SuperGroupMemberInput } from '@/types/domain'
import { extractApiErrorMessage } from '@/shared/apiError'
import { groupsApi, myGroupsApi } from '@/api/domains/groups'

const props = defineProps<{
  open: boolean
  groupId: string
  groupName: string
  management: boolean
  systemAccountId?: string
}>()

const emit = defineEmits<{
  (event: 'saved'): void
  (event: 'update:open', value: boolean): void
}>()

type EditableMember = {
  memberGroupId: string
  memberGroupName: string
  enabled: boolean
  groupEnabled: boolean
}

const columns = [
  { title: '成员分组', dataIndex: 'memberGroupName', key: 'memberGroupName' },
  { title: '启用', key: 'enabled', width: 90 },
  { title: '顺序 / 移除', key: 'actions', width: 240 }
]

const loading = ref(false)
const saving = ref(false)
const loaded = ref(false)
const revision = ref('')
const members = ref<EditableMember[]>([])
const candidates = ref<Array<{ label: string; value: string }>>([])
const addGroupId = ref<string | undefined>()

const candidateOptions = computed(() => candidates.value.filter((candidate) => !members.value.some((member) => member.memberGroupId === candidate.value)))

function scopeParams() {
  return props.management && props.systemAccountId ? { systemAccountId: props.systemAccountId } : undefined
}

async function load() {
  if (!props.open || !props.groupId) return
  loading.value = true
  loaded.value = false
  try {
    const summary = props.management
      ? await groupsApi.superMembers(props.groupId, scopeParams())
      : await myGroupsApi.superMembers(props.groupId)
    revision.value = summary.revision
    members.value = summary.members.map((member) => ({
      memberGroupId: member.memberGroupId,
      memberGroupName: member.memberGroupName,
      enabled: member.enabled,
      groupEnabled: member.groupEnabled
    }))
    const options = props.management
      ? await groupsApi.routeStrategyOptions({ groupKind: 'standard', limit: 50, ...(scopeParams() ?? {}) })
      : await myGroupsApi.routeStrategyOptions({ groupKind: 'standard', limit: 50 })
    candidates.value = options.map((option) => ({ label: option.name, value: option.id }))
    loaded.value = true
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载超级分组成员失败'))
  } finally {
    loading.value = false
  }
}

watch(() => props.open, (open) => { if (open) void load() })

function filterOption(input: string, option?: { label?: string }) {
  return (option?.label ?? '').toLowerCase().includes(input.toLowerCase())
}

function addMember() {
  const selected = candidates.value.find((candidate) => candidate.value === addGroupId.value)
  if (!selected || members.value.some((member) => member.memberGroupId === selected.value)) return
  members.value.push({ memberGroupId: selected.value, memberGroupName: selected.label, enabled: true, groupEnabled: true })
  addGroupId.value = undefined
}

function remove(index: number) {
  members.value.splice(index, 1)
}

function move(index: number, delta: number) {
  const target = index + delta
  if (target < 0 || target >= members.value.length) return
  const [item] = members.value.splice(index, 1)
  if (item) members.value.splice(target, 0, item)
}

async function save() {
  if (!props.groupId) return
  saving.value = true
  try {
    const payload: SuperGroupMemberInput[] = members.value.map((member, index) => ({
      memberGroupId: member.memberGroupId,
      priority: index,
      enabled: member.enabled
    }))
    const summary = props.management
      ? await groupsApi.updateSuperMembers(props.groupId, { expectedRevision: revision.value, members: payload }, scopeParams())
      : await myGroupsApi.updateSuperMembers(props.groupId, { expectedRevision: revision.value, members: payload })
    revision.value = summary.revision
    members.value = summary.members.map((member) => ({
      memberGroupId: member.memberGroupId,
      memberGroupName: member.memberGroupName,
      enabled: member.enabled,
      groupEnabled: member.groupEnabled
    }))
    message.success('超级分组成员已保存')
    emit('saved')
  } catch (error) {
    message.error(extractApiErrorMessage(error, '保存超级分组成员失败'))
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.members-alert {
  margin-bottom: 16px;
}

.members-toolbar {
  display: flex;
  gap: 12px;
  margin-bottom: 16px;
}

.members-select {
  width: 320px;
}
</style>
