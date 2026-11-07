<template>
  <div class="proxy-pool-panel">
    <ResponsiveListToolbar v-model:keyword="keyword" search-placeholder="搜索代理池名称" :show-reset="Boolean(keyword.trim())" :refresh-loading="loading" @search="searchPools" @reset="resetSearch" @refresh="loadData">
      <template #actions>
        <a-button type="primary" @click="openCreate">新建代理池</a-button>
      </template>
    </ResponsiveListToolbar>
    <ResponsiveDataList table-class="page-table proxy-pool-table" :columns="poolColumns" :data-source="pools" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="1120" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobilePools" @mobile-refresh="refreshMobilePools">
      <template #emptyText>
        <a-empty class="page-empty-card" description="先创建代理，再创建代理池或直接在账户中选择代理" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'strategy'">
          <a-tag :color="proxyPoolStrategyColor(record.strategy)">{{ proxyPoolStrategyText(record.strategy) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'enabled'">
          <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'assignableMemberCount'">
          <span :class="record.assignableMemberCount > 0 ? '' : 'pool-count-zero'">{{ record.assignableMemberCount }}</span>
        </template>
        <template v-else-if="column.key === 'updatedAt'">
          <span>{{ formatDateTime(record.updatedAt) }}</span>
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="poolActionsFor(record)" @action-click="handlePoolAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="proxyPoolStrategyColor(record.strategy)">{{ proxyPoolStrategyText(record.strategy) }}</a-tag>
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>成员数</span>
              <strong>{{ record.memberCount }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>可分配</span>
              <strong :class="record.assignableMemberCount > 0 ? '' : 'pool-count-zero'">{{ record.assignableMemberCount }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>租约总数</span>
              <strong>{{ record.leaseCount }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>更新时间</span>
              <strong>{{ formatDateTime(record.updatedAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="poolActionsFor(record)" @action-click="handlePoolAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑代理池' : '新建代理池'" width="960px" :confirm-loading="poolSaving" :ok-button-props="{ type: 'primary', disabled: poolSaving }" @ok="savePool">
      <a-form layout="vertical" class="modal-form" autocomplete="off">
        <a-row :gutter="16">
          <a-col :span="12">
            <a-form-item label="名称" required>
              <a-input v-model:value="form.name" placeholder="例如出海账号池" />
            </a-form-item>
          </a-col>
          <a-col :span="6">
            <a-form-item label="策略">
              <a-select v-model:value="form.strategy" :options="proxyPoolStrategyOptions" />
            </a-form-item>
          </a-col>
          <a-col :span="6">
            <a-form-item label="状态">
              <a-switch v-model:checked="form.enabled" checked-children="启用" un-checked-children="停用" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="2" placeholder="可选，填写用途或绑定场景" />
        </a-form-item>

        <div class="pool-members-section">
          <div class="pool-members-head">
            <span class="pool-members-title">成员（{{ form.members.length }}/50）</span>
            <a-select
              class="pool-member-add-select"
              :value="memberAddValue"
              show-search
              allow-clear
              :filter-option="false"
              :loading="memberOptionsLoading"
              :options="memberOptionSelects"
              placeholder="添加成员代理（按名称搜索）"
              @dropdown-visible-change="handleMemberOptionsDropdown"
              @search="handleMemberOptionsSearch"
              @update:value="handleMemberAddValue"
            />
          </div>
          <a-table
            v-if="form.members.length"
            class="pool-member-table"
            size="small"
            :columns="memberColumns"
            :data-source="form.members"
            :pagination="false"
            row-key="proxyProfileId"
            :scroll="{ x: 1240 }"
          >
            <template #bodyCell="{ column, record, index }">
              <template v-if="column.key === 'name'">
                <span class="pool-member-name">{{ record.name }}</span>
                <a-tag class="pool-member-type-tag" :color="proxyTypeColor(record.type)">{{ record.type.toUpperCase() }}</a-tag>
              </template>
              <template v-else-if="column.key === 'enabled'">
                <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
              </template>
              <template v-else-if="column.key === 'memberEnabled'">
                <a-switch v-model:checked="record.memberEnabled" checked-children="启用" un-checked-children="停用" size="small" />
              </template>
              <template v-else-if="column.key === 'weight'">
                <a-input-number v-model:value="record.weight" :min="1" :max="100" :precision="0" size="small" style="width: 84px" />
              </template>
              <template v-else-if="column.key === 'order'">
                <a-space :size="4">
                  <a-button size="small" :disabled="index === 0" @click="moveMember(index, -1)">上移</a-button>
                  <a-button size="small" :disabled="index === form.members.length - 1" @click="moveMember(index, 1)">下移</a-button>
                </a-space>
              </template>
              <template v-else-if="column.key === 'poolHealth'">
                <a-tooltip :title="proxyPoolHealthTooltip(record)">
                  <a-space :size="6">
                    <a-tag :color="proxyPoolHealthColor(record.poolHealthStatus)">{{ proxyPoolHealthText(record.poolHealthStatus) }}</a-tag>
                    <span :class="record.poolHealthFailureStreak > 0 ? 'pool-streak-warn' : 'pool-muted'">{{ record.poolHealthFailureStreak }}/3</span>
                  </a-space>
                </a-tooltip>
              </template>
              <template v-else-if="column.key === 'poolHealthCheckedAt'">
                <span class="pool-muted">{{ formatDateTime(record.poolHealthCheckedAt) }}</span>
              </template>
              <template v-else-if="column.key === 'testStatus'">
                <a-space :size="6">
                  <a-tag :color="testStatusColor(record.testStatus)">{{ testStatusText(record.testStatus) }}</a-tag>
                  <span class="pool-muted">{{ formatDateTime(record.lastTestedAt) }}</span>
                </a-space>
              </template>
              <template v-else-if="column.key === 'latency'">
                <span :class="record.latencyMs === undefined ? 'pool-muted' : 'latency-value'">{{ formatLatency(record.latencyMs) }}</span>
              </template>
              <template v-else-if="column.key === 'leaseCount'">
                <span>{{ record.leaseCount }}</span>
              </template>
              <template v-else-if="column.key === 'assignable'">
                <a-tag :color="poolMemberAssignableColor(record)">{{ poolMemberAssignableText(record) }}</a-tag>
              </template>
              <template v-else-if="column.key === 'remove'">
                <a-button danger size="small" type="text" @click="removeMember(index)">移除</a-button>
              </template>
            </template>
          </a-table>
          <div v-else class="pool-members-empty">尚未添加成员。先在「代理」标签页创建代理，再在此处加入代理池。</div>
          <div v-if="form.strategy === 'weighted'" class="pool-weight-hint">权重策略按成员权重分配新绑定；调整权重不影响已有租约。</div>
        </div>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, reactive, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatNumber } from '@/shared/formatters'
import type { ProxyPoolDetail, ProxyPoolMemberOptionSummary, ProxyPoolSummary } from '@/types/domain'
import {
  formatLatency,
  poolMemberAssignableColor,
  poolMemberAssignableText,
  proxyPoolHealthColor,
  proxyPoolHealthText,
  proxyPoolHealthTooltip,
  proxyPoolStrategyColor,
  proxyPoolStrategyOptions,
  proxyPoolStrategyText,
  proxyTypeColor,
  testStatusColor,
  testStatusText
} from './proxyPoolDisplay'
import { useProxyPoolMemberOptions } from './proxyPoolMemberOptions'
import {
  buildProxyPoolCreatePayload,
  buildProxyPoolPatchPayload,
  clampMemberWeight,
  defaultProxyPoolForm,
  movePoolMember,
  proxyPoolFormFromDetail,
  validateProxyPoolForm,
  type ProxyPoolFormState
} from './proxyPoolMutation'

const pageSize = 20
const keyword = ref('')
const modalOpen = ref(false)
const editingId = ref<string>()
const editingUpdatedAt = ref<string>()
const editingBaseline = ref<ProxyPoolFormState>()
const memberAddValue = ref<string>()
const form = reactive<ProxyPoolFormState>(defaultProxyPoolForm())
const { submitAction, submittingRef } = useSubmitAction('proxyPools')
const poolSaving = submittingRef('proxyPools.save')

const poolColumns = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 200, fixed: 'left' },
  { title: '策略', key: 'strategy', width: 90 },
  { title: '启用', key: 'enabled', width: 80 },
  { title: '成员数', dataIndex: 'memberCount', key: 'memberCount', width: 90 },
  { title: '可分配成员', key: 'assignableMemberCount', width: 110 },
  { title: '租约总数', dataIndex: 'leaseCount', key: 'leaseCount', width: 100 },
  { title: '更新时间', key: 'updatedAt', width: 170 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
  { title: '操作', key: 'actions', fixed: 'right', customRender: () => '' }
]

const memberColumns = [
  { title: '成员代理', key: 'name', width: 200, fixed: 'left' },
  { title: '代理启用', key: 'enabled', width: 90 },
  { title: '成员启停', key: 'memberEnabled', width: 96 },
  { title: '权重', key: 'weight', width: 100 },
  { title: '顺序', key: 'order', width: 130 },
  { title: '池健康', key: 'poolHealth', width: 140 },
  { title: '池检测时间', key: 'poolHealthCheckedAt', width: 160 },
  { title: '检测状态', key: 'testStatus', width: 200 },
  { title: '延迟', key: 'latency', width: 90 },
  { title: '租约数', dataIndex: 'leaseCount', key: 'leaseCount', width: 80 },
  { title: '可分配', key: 'assignable', width: 110 },
  { title: '', key: 'remove', width: 70, fixed: 'right' }
]

// 删除前展示影响面（§9.1）：列表行携带租约总数；绑定账户数由删除被拒时的 409 文案给出。
function poolActionsFor(pool: ProxyPoolSummary): RowActionItem[] {
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
    {
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      confirmTitle: `确认删除代理池「${pool.name}」？当前租约总数 ${pool.leaseCount}；仍被账户绑定或存在租约时删除会被拒绝，需先解除绑定。`,
      confirmOkText: '删除'
    }
  ]
}

const {
  items: pools,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobilePools,
  removeItems: removePoolItems,
  refreshMobile: refreshMobilePools,
  resetPagination,
  updateItems: updatePoolItems
} = useResponsivePagedList<ProxyPoolSummary>({
  pageSize,
  showTotal: (total) => `共 ${formatNumber(total)} 个代理池`,
  fetchPage: (_options, pageState) => api.proxyPools.list({
    keyword: keyword.value.trim() || undefined,
    page: pageState.current,
    pageSize: pageState.pageSize
  }),
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载代理池失败'))
  }
})

const {
  loading: memberOptionsLoading,
  options: memberOptions,
  handleDropdown: handleMemberOptionsDropdown,
  handleSearch: handleMemberOptionsSearch
} = useProxyPoolMemberOptions()

// 成员选择器只展示 id/name/type/enabled/testStatus（§8.1），不展示 host/port/密码。
const memberOptionSelects = computed(() => memberOptions.value.map((option) => ({
  label: `${option.name}（${option.type}${option.enabled ? '' : '，已停用'}）`,
  value: option.id
})))

function searchPools() {
  resetPagination()
  void loadData()
}

function resetSearch() {
  keyword.value = ''
  searchPools()
}

function handlePoolAction(key: string, pool: ProxyPoolSummary) {
  if (key === 'edit') {
    void openEdit(pool)
    return
  }
  if (key === 'delete') {
    void removePool(pool)
  }
}

function openCreate() {
  editingId.value = undefined
  editingUpdatedAt.value = undefined
  editingBaseline.value = undefined
  memberAddValue.value = undefined
  Object.assign(form, defaultProxyPoolForm())
  modalOpen.value = true
}

async function openEdit(pool: ProxyPoolSummary) {
  try {
    const detail = await api.proxyPools.detail(pool.id)
    editingId.value = pool.id
    editingUpdatedAt.value = detail.updatedAt
    const nextForm = proxyPoolFormFromDetail(detail)
    editingBaseline.value = { ...nextForm, members: nextForm.members.map((member) => ({ ...member })) }
    Object.assign(form, nextForm)
    memberAddValue.value = undefined
    modalOpen.value = true
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载代理池详情失败'))
  }
}

const savePool = submitAction('proxyPools.save', async () => {
  const validation = validateProxyPoolForm(form)
  if (validation) {
    message.warning(validation)
    return
  }
  try {
    const targetId = editingId.value
    if (targetId) {
      const baseline = editingBaseline.value
      const expectedUpdatedAt = editingUpdatedAt.value
      if (!baseline || !expectedUpdatedAt) {
        message.error('代理池编辑版本缺失，请重新打开编辑窗口')
        return
      }
      const patch = buildProxyPoolPatchPayload(baseline, form, expectedUpdatedAt)
      // 无变化不发 PATCH（成员增删/权重/启停与池字段一起按基线差量）。
      if (Object.keys(patch).length === 1) {
        modalOpen.value = false
        return
      }
      const detail = await api.proxyPools.update(targetId, patch)
      applyPoolDetail(detail)
      message.success('代理池已更新')
    } else {
      const detail = await api.proxyPools.create(buildProxyPoolCreatePayload(form))
      upsertPoolRow(detail)
      message.success('代理池已创建')
    }
    modalOpen.value = false
  } catch (error) {
    console.error(error)
    // 409 文案直接展示（§9.1）：proxy_pool_bound_accounts / proxy_pool_revision_conflict 等。
    message.error(extractApiErrorMessage(error, '保存代理池失败'))
  }
})

async function removePool(pool: ProxyPoolSummary) {
  try {
    await api.proxyPools.delete(pool.id)
    removePoolItems((item) => item.id === pool.id)
    message.success('代理池已删除')
  } catch (error) {
    console.error(error)
    // 删除被拒绝时展示 409 文案（绑定账户影响面由后端给出）。
    message.error(extractApiErrorMessage(error, '删除代理池失败'))
  }
}

function handleMemberAddValue(value: string | number | undefined) {
  memberAddValue.value = undefined
  const id = String(value ?? '').trim()
  if (!id) return
  addMember(id)
}

function addMember(profileId: string) {
  if (form.members.some((member) => member.proxyProfileId === profileId)) {
    message.warning('该代理已在池中')
    return
  }
  if (form.members.length >= 50) {
    message.warning('代理池成员数量超出上限（最多 50 个）')
    return
  }
  const option: ProxyPoolMemberOptionSummary | undefined = memberOptions.value.find((item) => item.id === profileId)
  form.members.push({
    proxyProfileId: profileId,
    name: option?.name ?? profileId,
    type: option?.type ?? '',
    enabled: option?.enabled ?? true,
    testStatus: option?.testStatus ?? '',
    weight: clampMemberWeight(1),
    memberEnabled: true,
    poolHealthStatus: 'unknown',
    poolHealthFailureStreak: 0,
    leaseCount: 0,
    assignable: (option?.enabled ?? true)
  })
}

function removeMember(index: number) {
  if (index < 0 || index >= form.members.length) return
  form.members.splice(index, 1)
}

function moveMember(index: number, offset: -1 | 1) {
  form.members = movePoolMember(form.members, index, offset)
}

function applyPoolDetail(detail: ProxyPoolDetail): void {
  updatePoolItems((item) => item.id === detail.id, () => poolRowFromDetail(detail))
}

function upsertPoolRow(detail: ProxyPoolDetail): void {
  if (pools.value.some((item) => item.id === detail.id)) {
    applyPoolDetail(detail)
    return
  }
  pools.value = [poolRowFromDetail(detail), ...pools.value]
}

function poolRowFromDetail(detail: ProxyPoolDetail): ProxyPoolSummary {
  return {
    id: detail.id,
    name: detail.name,
    description: detail.description,
    strategy: detail.strategy,
    enabled: detail.enabled,
    memberCount: detail.memberCount,
    assignableMemberCount: detail.assignableMemberCount,
    leaseCount: detail.leaseCount,
    updatedAt: detail.updatedAt
  }
}
</script>

<style scoped>
.proxy-pool-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.pool-count-zero {
  color: var(--juhe-danger);
}

.pool-members-section {
  display: grid;
  gap: 10px;
}

.pool-members-head {
  display: flex;
  gap: 12px;
  align-items: center;
  justify-content: space-between;
}

.pool-members-title {
  color: var(--juhe-muted);
  font-weight: 600;
}

.pool-member-add-select {
  min-width: 280px;
}

.pool-member-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.pool-member-name {
  margin-right: 6px;
}

.pool-muted {
  color: var(--juhe-muted);
}

.pool-streak-warn {
  color: var(--juhe-warn);
  font-variant-numeric: tabular-nums;
}

.pool-members-empty {
  padding: 16px;
  color: var(--juhe-muted);
  border: 1px dashed var(--juhe-border);
  border-radius: 8px;
  text-align: center;
}

.pool-weight-hint {
  color: var(--juhe-muted);
  font-size: 12px;
}

.latency-value {
  color: var(--juhe-fg);
  font-variant-numeric: tabular-nums;
}
</style>
