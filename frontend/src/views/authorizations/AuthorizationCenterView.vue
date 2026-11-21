<template>
  <a-card class="page-card responsive-page-card authorization-center">
    <template #title>
      <div class="authorization-center__title">
        <span>{{ isManagementView ? '统一授权' : '授权中心' }}</span>
        <a-tag v-if="summary && !isManagementView" color="blue">待领取 {{ summary.pendingSources }}</a-tag>
        <a-tag v-if="summary && !isManagementView" color="green">生效中 {{ summary.activeSources ?? 0 }}</a-tag>
      </div>
    </template>
    <template #extra>
      <a-space>
        <a-button type="primary" @click="openCreate">发起授权</a-button>
        <a-button :loading="loading" @click="refresh">刷新</a-button>
      </a-space>
    </template>

    <a-tabs v-model:active-key="activeTab" @change="handleTabChange">
      <a-tab-pane v-for="tab in tabs" :key="tab.key" :tab="tab.label">
        <a-alert
          v-if="tab.key === 'inbound'"
          type="info"
          show-icon
          message="收到的授权需要你主动领取：分组授权领取后直接生效；账户授权领取时选择你的一个分组，一步进组生效。"
          style="margin-bottom: 12px"
        />

        <a-spin :spinning="loading">
          <template v-if="tab.key === 'inbound'">
            <a-table
              :columns="sourceColumns"
              :data-source="sources"
              :pagination="false"
              :row-key="(row: AuthorizationSourceSummary) => row.sourceId"
              size="middle"
              :scroll="{ x: 1270 }"
            >
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'resource'">
                  {{ record.resourceName || record.resourceId }}
                </template>
                <template v-else-if="column.key === 'resourceType'">
                  <a-tag>{{ record.resourceType === 'group' ? '分组' : '账户' }}</a-tag>
                </template>
                <template v-else-if="column.key === 'owner'">
                  {{ record.resourceOwnerName || record.resourceOwnerSystemAccountId }}
                </template>
                <template v-else-if="column.key === 'source'">
                  <a-tag :color="record.sourceType === 'team' ? 'purple' : 'default'">
                    {{ record.sourceType === 'team' ? `团队：${record.teamName || record.teamId}` : '个人' }}
                  </a-tag>
                </template>
                <template v-else-if="column.key === 'vendor'">
                  {{ record.vendorCode || '—' }}
                </template>
                <template v-else-if="column.key === 'status'">
                  <a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
                </template>
                <template v-else-if="column.key === 'expiry'">
                  {{ record.expiresAt ? formatTime(record.expiresAt) : '长期有效' }}
                </template>
                <template v-else-if="column.key === 'binding'">
                  <a-tag v-if="record.resourceType === 'account'" :color="record.bound ? 'green' : 'default'">
                    {{ record.bound ? '已绑定分组' : '未绑定分组' }}
                  </a-tag>
                  <span v-else class="authorization-center__muted">—</span>
                </template>
                <template v-else-if="column.key === 'actions'">
                  <a-space wrap>
                    <a-button v-if="record.canAccept" size="small" type="primary" @click="openAccept(record)">领取</a-button>
                    <a-popconfirm v-if="record.canReturn" title="归还后该来源立即不可用，确定归还？" @confirm="run(() => api.myAuthorizationCenter.return(record.sourceId), '已归还来源')">
                      <a-button size="small" danger>归还</a-button>
                    </a-popconfirm>
                    <a-popconfirm v-if="record.canDecline" title="拒绝后该授权作废，确定拒绝？" @confirm="run(() => api.myAuthorizationCenter.decline(record.sourceId), '已拒绝授权')">
                      <a-button size="small" danger>拒绝</a-button>
                    </a-popconfirm>
                    <a-button size="small" type="link" @click="openDetail(record)">详情</a-button>
                  </a-space>
                </template>
              </template>
            </a-table>
            <a-empty v-if="!loading && !sources.length" description="暂无收到的授权" />
          </template>

          <template v-else-if="tab.key === 'outbound' || tab.key === 'owned' || tab.key === 'all'">
            <a-table
              :columns="offerColumns"
              :data-source="offers"
              :pagination="false"
              :row-key="(row: AuthorizationOfferSummary) => row.grantId"
              size="middle"
              :scroll="{ x: 1200 }"
            >
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'resource'">
                  {{ record.resourceName || record.resourceId }}
                </template>
                <template v-else-if="column.key === 'resourceType'">
                  <a-tag>{{ record.resourceType === 'group' ? '分组' : '账户' }}</a-tag>
                </template>
                <template v-else-if="column.key === 'grantee'">
                  <a-tag :color="record.granteeType === 'team' ? 'purple' : 'default'">
                    {{ record.granteeType === 'team' ? `团队：${record.teamName || record.teamId}` : record.granteeName || record.granteeSystemAccountId }}
                  </a-tag>
                </template>
                <template v-else-if="column.key === 'createdBy'">
                  {{ record.createdByName || record.createdBy }}
                </template>
                <template v-else-if="column.key === 'status'">
                  <a-tag :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
                </template>
                <template v-else-if="column.key === 'expiry'">
                  {{ record.expiresAt ? formatTime(record.expiresAt) : '长期有效' }}
                </template>
                <template v-else-if="column.key === 'endedReason'">
                  <span v-if="record.endedReason">{{ endedReasonLabel(record.endedReason) }}</span>
                  <span v-else class="authorization-center__muted">—</span>
                </template>
                <template v-else-if="column.key === 'actions'">
                  <a-space v-if="canPause(record) || canResume(record) || canExtend(record) || canRevoke(record)" wrap>
                    <a-button v-if="canPause(record)" size="small" @click="run(() => currentApi().pause(record.grantId), '已暂停授权')">暂停</a-button>
                    <a-button v-if="canResume(record)" size="small" @click="run(() => currentApi().resume(record.grantId), '已恢复授权')">恢复</a-button>
                    <a-button v-if="canExtend(record)" size="small" @click="openExtend(record)">延长</a-button>
                    <a-popconfirm v-if="canRevoke(record)" title="回收后所有非终态来源立即撤销，确定回收？" @confirm="run(() => currentApi().revoke(record.grantId), '已回收授权')">
                      <a-button size="small" danger>回收</a-button>
                    </a-popconfirm>
                  </a-space>
                  <span v-else class="authorization-center__muted">—</span>
                </template>
              </template>
            </a-table>
            <a-empty v-if="!loading && !offers.length" description="暂无授权记录" />
          </template>

        </a-spin>
      </a-tab-pane>
    </a-tabs>

    <a-modal v-model:open="detailOpen" title="授权来源详情" :footer="null" width="760px">
      <template v-if="detail">
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="资源">{{ detail.resourceName || detail.resourceId }}</a-descriptions-item>
          <a-descriptions-item label="类型">{{ detail.resourceType === 'group' ? '授权分组' : '授权账户' }}</a-descriptions-item>
          <a-descriptions-item label="授权方">{{ detail.resourceOwnerName || detail.resourceOwnerSystemAccountId }}</a-descriptions-item>
          <a-descriptions-item label="来源">{{ detail.sourceType === 'team' ? `团队 ${detail.teamName || detail.teamId}` : '个人授权' }}</a-descriptions-item>
          <a-descriptions-item label="供应商">{{ detail.vendorCode || '—' }}</a-descriptions-item>
          <a-descriptions-item label="到期">{{ detail.expiresAt ? formatTime(detail.expiresAt) : '长期有效' }}</a-descriptions-item>
          <a-descriptions-item label="运行态">{{ detail.runtimeStatus || '—' }}</a-descriptions-item>
          <a-descriptions-item label="代理">{{ detail.proxySummary || '—' }}</a-descriptions-item>
        </a-descriptions>
        <a-divider orientation="left">可使用的上游连接</a-divider>
        <a-empty v-if="!detail.connections?.length" description="无连接信息" />
        <a-list v-else size="small" :data-source="detail.connections">
          <template #renderItem="{ item }">
            <a-list-item>
              <div>
                <div>{{ item.protocolSurfaceCode }} · {{ item.connectionPresetCode }}</div>
                <div class="authorization-center__muted">{{ item.baseUrl }}</div>
                <div class="authorization-center__muted">endpoint modes：{{ item.endpointModes?.join('、') || '默认' }}</div>
              </div>
            </a-list-item>
          </template>
        </a-list>
        <a-divider orientation="left">支持模型</a-divider>
        <a-space wrap>
          <a-tag v-for="model in detail.supportedModels || []" :key="model">{{ model }}</a-tag>
          <span v-if="!detail.supportedModels?.length" class="authorization-center__muted">未限制/未配置</span>
        </a-space>
      </template>
    </a-modal>

    <a-modal
      v-model:open="acceptOpen"
      title="领取授权"
      :confirm-loading="acting"
      ok-text="确认领取"
      @ok="confirmAccept"
    >
      <template v-if="acceptTarget">
        <a-alert
          type="info"
          show-icon
          :message="acceptTarget.resourceType === 'account'
            ? '账户授权领取时选择你的一个分组，领取即一步进组生效。'
            : '分组授权领取后直接生效。'"
          style="margin-bottom: 12px"
        />
        <a-descriptions :column="1" bordered size="small">
          <a-descriptions-item label="资源">{{ acceptTarget.resourceName || acceptTarget.resourceId }}</a-descriptions-item>
          <a-descriptions-item label="授权方">{{ acceptTarget.resourceOwnerName || acceptTarget.resourceOwnerSystemAccountId }}</a-descriptions-item>
          <a-descriptions-item label="上游地址">{{ acceptConnectionHosts.join('、') || '—' }}</a-descriptions-item>
          <a-descriptions-item label="代理摘要">{{ acceptTarget.proxySummary || '—' }}</a-descriptions-item>
          <a-descriptions-item label="支持模型数">{{ acceptModelCount }}</a-descriptions-item>
          <a-descriptions-item label="到期">{{ acceptTarget.expiresAt ? formatTime(acceptTarget.expiresAt) : '长期有效' }}</a-descriptions-item>
        </a-descriptions>
        <a-form-item v-if="acceptTarget.resourceType === 'account'" required label="领取到分组" style="margin-top: 12px">
          <a-select
            v-model:value="acceptTargetGroupId"
            :options="acceptGroupOptions"
            :loading="acceptGroupLoading"
            show-search
            option-filter-prop="label"
            placeholder="选择你的一个分组"
          />
          <div v-if="!acceptGroupLoading && !acceptGroupOptions.length" class="authorization-center__muted" style="margin-top: 8px">
            暂无可选分组，请先创建分组后再领取。
          </div>
        </a-form-item>
      </template>
    </a-modal>

    <a-modal
      v-model:open="extendOpen"
      title="延长授权有效期"
      :confirm-loading="acting"
      ok-text="确认延长"
      @ok="confirmExtend"
    >
      <a-form layout="vertical">
        <a-form-item label="新的到期时间">
          <a-date-picker v-model:value="extendDate" show-time style="width: 100%" />
        </a-form-item>
        <a-alert type="info" show-icon message="只恢复本次到期周期由授权自身到期产生且未被归还/拒绝/回收的来源。" />
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="createOpen"
      title="发起授权"
      width="640px"
      :confirm-loading="acting"
      ok-text="发送"
      @ok="confirmCreate"
    >
      <a-alert
        type="info"
        show-icon
        :message="createHint"
        style="margin-bottom: 12px"
      />
      <a-form layout="vertical">
        <a-form-item v-if="isManagementView" label="资源所有者" required>
          <a-select
            v-model:value="createForm.ownerSystemAccountId"
            show-search
            :filter-option="false"
            :options="ownerOptions"
            :loading="ownerLoading"
            placeholder="搜索并选择资源所有者"
            @search="scheduleOwnerSearch"
            @change="handleOwnerChange"
          />
        </a-form-item>
        <a-form-item label="资源类型" required>
          <a-radio-group v-model:value="createForm.resourceType" button-style="solid" @change="handleResourceTypeChange">
            <a-radio-button value="account">AI 账户</a-radio-button>
            <a-radio-button value="group">分组</a-radio-button>
          </a-radio-group>
        </a-form-item>
        <a-form-item label="授权资源" required>
          <a-select
            v-model:value="createForm.resourceId"
            show-search
            :filter-option="false"
            :options="resourceOptions"
            :loading="resourceLoading"
            :disabled="isManagementView && !createForm.ownerSystemAccountId"
            placeholder="搜索并选择要授权出去的资源"
            @search="scheduleResourceSearch"
          />
        </a-form-item>
        <a-form-item label="接收方类型" required>
          <a-radio-group v-model:value="createForm.granteeType" button-style="solid">
            <a-radio-button value="system_account">个人</a-radio-button>
            <a-radio-button value="team">团队</a-radio-button>
          </a-radio-group>
        </a-form-item>
        <a-form-item :label="createForm.granteeType === 'system_account' ? '接收方用户名' : '接收方团队名'" required>
          <a-input
            v-if="createForm.granteeType === 'system_account'"
            v-model:value="createForm.granteeUsername"
            placeholder="输入精确用户名，不接受模糊匹配"
          />
          <a-input v-else v-model:value="createForm.teamName" placeholder="输入精确团队名，团队成员将各自领取" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="createForm.remark" :rows="2" placeholder="可选，说明用途或范围" />
        </a-form-item>
        <a-form-item label="到期时间">
          <a-date-picker v-model:value="createForm.expiresAt" show-time allow-clear style="width: 100%" />
        </a-form-item>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import dayjs, { type Dayjs } from 'dayjs'
import { computed, onMounted, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import type {
  AuthorizationInboxSummary,
  AuthorizationOfferSummary,
  AuthorizationSourceDetail,
  AuthorizationSourceSummary
} from '@/api/domains/authorizationGovernance'
import { createAuthorizationSearchScheduler } from '@/views/authorizations/authorizationSearchScheduler'
import { authState } from '@/composables/useAuth'
import { useScopedMenuView } from '@/composables/useScopedMenuView'

type ResourceOption = { label: string; value: string }

interface CreateFormState {
  ownerSystemAccountId?: string
  resourceType: 'account' | 'group'
  resourceId?: string
  granteeType: 'system_account' | 'team'
  granteeUsername: string
  teamName: string
  remark: string
  expiresAt?: Dayjs
}

const { isManagementView } = useScopedMenuView()
const loading = ref(false)
const acting = ref(false)
const activeTab = ref(isManagementView.value ? 'all' : 'inbound')
const summary = ref<AuthorizationInboxSummary>()
const sources = ref<AuthorizationSourceSummary[]>([])
const offers = ref<AuthorizationOfferSummary[]>([])
const detail = ref<AuthorizationSourceDetail>()
const detailOpen = ref(false)
const acceptOpen = ref(false)
const acceptTarget = ref<AuthorizationSourceSummary>()
const acceptTargetGroupId = ref<string>()
const acceptGroupOptions = ref<ResourceOption[]>([])
const acceptGroupLoading = ref(false)
// 上游地址从详情连接派生；列表摘要不再承载该聚合字段。
const acceptConnectionHosts = computed(() => {
  const connections = (acceptTarget.value as AuthorizationSourceDetail | undefined)?.connections ?? []
  const hosts = new Set<string>()
  for (const conn of connections) {
    try {
      hosts.add(new URL(conn.baseUrl).host)
    } catch {
      hosts.add(conn.baseUrl)
    }
  }
  return [...hosts]
})
const acceptModelCount = computed(() =>
  (acceptTarget.value as AuthorizationSourceDetail | undefined)?.supportedModels?.length ?? 0
)
const extendOpen = ref(false)
const extendTarget = ref<AuthorizationOfferSummary>()
const extendDate = ref<Dayjs>(dayjs().add(30, 'day'))
const createOpen = ref(false)
const ownerLoading = ref(false)
const resourceLoading = ref(false)
const ownerOptions = ref<ResourceOption[]>([])
const resourceOptions = ref<ResourceOption[]>([])
const ownerKeyword = ref('')
const resourceKeyword = ref('')
const ownerSearch = createAuthorizationSearchScheduler({ delayMs: 250, keyword: ownerKeyword, load: loadOwnerOptions })
const resourceSearch = createAuthorizationSearchScheduler({ delayMs: 250, keyword: resourceKeyword, load: loadCreateResources })
const createForm = reactive<CreateFormState>({
  ownerSystemAccountId: undefined,
  resourceType: 'account',
  resourceId: undefined,
  granteeType: 'system_account',
  granteeUsername: '',
  teamName: '',
  remark: '',
  expiresAt: undefined
})

const createHint = computed(() => {
  if (createForm.granteeType === 'team') {
    return '发送后团队成员在「收到的授权」里各自领取：分组领取即生效，账户领取时选自己的分组一步进组。'
  }
  return '发送后对方在「收到的授权」里领取：分组领取即生效，账户领取时选自己的分组一步进组。'
})

const tabs = computed(() => {
  if (isManagementView.value) {
    return [
      { key: 'all', label: '全部业务记录' }
    ]
  }
  return [
    { key: 'inbound', label: '收到的授权' },
    { key: 'outbound', label: '我发出的授权' },
    { key: 'owned', label: '我的资源授权' }
  ]
})

// 列设计约束：一列只承载一个字段；grant ID、来源细分数、代理摘要等
// 次要信息走 rowKey 或「详情」弹窗，不挤进表格列。
const sourceColumns = [
  { title: '资源名称', key: 'resource', width: 170 },
  { title: '类型', key: 'resourceType', width: 80 },
  { title: '授权方', key: 'owner', width: 120 },
  { title: '来源', key: 'source', width: 120 },
  { title: '供应商', key: 'vendor', width: 100 },
  { title: '状态', key: 'status', width: 120 },
  { title: '到期时间', key: 'expiry', width: 140 },
  { title: '绑定', key: 'binding', width: 120 },
  { title: '操作', key: 'actions', width: 300 }
]

const offerColumns = [
  { title: '资源名称', key: 'resource', width: 180 },
  { title: '类型', key: 'resourceType', width: 80 },
  { title: '接收方', key: 'grantee', width: 150 },
  { title: '创建人', key: 'createdBy', width: 110 },
  { title: '状态', key: 'status', width: 130 },
  { title: '到期时间', key: 'expiry', width: 150 },
  { title: '结束原因', key: 'endedReason', width: 120 },
  { title: '操作', key: 'actions', width: 240 }
]

const statusLabels: Record<string, string> = {
  pending: '待领取',
  active: '生效中',
  paused: '已暂停',
  expired: '已到期',
  revoked: '已回收',
  returned: '已归还',
  declined: '已拒绝'
}

function statusLabel(status: string): string { return statusLabels[status] || status }

const endedReasonLabels: Record<string, string> = {
  authorization_expired: '授权到期',
  authorization_revoked: '授权方回收',
  authorization_returned: '接收方归还',
  source_declined: '接收方拒绝',
  owner_declined: '所有者拒绝',
  fallback_declined: '回退来源拒绝'
}

function endedReasonLabel(reason: string): string { return endedReasonLabels[reason] || reason }
function statusColor(status: string): string {
  if (status === 'active') return 'green'
  if (status === 'pending') return 'blue'
  if (status === 'paused') return 'gold'
  if (status === 'returned' || status === 'declined' || status === 'expired') return 'default'
  return 'red'
}

function formatTime(value?: string): string {
  if (!value) return '—'
  const parsed = dayjs(value)
  return parsed.isValid() ? parsed.format('YYYY-MM-DD HH:mm') : value
}

function currentApi() {
  return isManagementView.value ? api.authorizationCenter : api.myAuthorizationCenter
}

function canPause(record: AuthorizationOfferSummary): boolean {
  if (record.status !== 'active') return false
  return isManagementView.value || record.resourceOwnerSystemAccountId === authState.currentUser.value?.id
}
function canResume(record: AuthorizationOfferSummary): boolean {
  if (record.status !== 'paused') return false
  return isManagementView.value || record.resourceOwnerSystemAccountId === authState.currentUser.value?.id
}
function canExtend(record: AuthorizationOfferSummary): boolean {
  if (record.status === 'active') return Boolean(record.expiresAt)
  if (record.status !== 'expired') return false
  return record.statusReason === 'authorization_expired' || record.statusReason === 'offer_expired'
}
function canRevoke(record: AuthorizationOfferSummary): boolean {
  if (['revoked', 'returned', 'declined', 'expired'].includes(record.status)) return false
  return isManagementView.value || record.resourceOwnerSystemAccountId === authState.currentUser.value?.id
}

async function refresh() {
  loading.value = true
  try {
    if (!isManagementView.value) {
      summary.value = await api.myAuthorizationCenter.inboxSummary()
    }
    await loadActiveTab()
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    loading.value = false
  }
}

async function loadActiveTab() {
  if (activeTab.value === 'inbound') {
    sources.value = await api.myAuthorizationCenter.inbound()
  } else if (activeTab.value === 'outbound') {
    offers.value = await api.myAuthorizationCenter.outbound()
  } else if (activeTab.value === 'owned') {
    offers.value = await api.myAuthorizationCenter.owned()
  } else if (activeTab.value === 'all') {
    offers.value = await api.authorizationCenter.list()
  }
}

function handleTabChange() {
  refresh()
}

async function run(action: () => Promise<unknown>, success: string) {
  acting.value = true
  try {
    await action()
    message.success(success)
    await refresh()
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    acting.value = false
  }
}

async function openDetail(record: AuthorizationSourceSummary) {
  try {
    detail.value = await api.myAuthorizationCenter.sourceDetail(record.sourceId)
    detailOpen.value = true
  } catch (error) {
    message.error(errorMessage(error))
  }
}

async function openAccept(record: AuthorizationSourceSummary) {
  acceptTarget.value = record
  acceptTargetGroupId.value = undefined
  acceptGroupOptions.value = []
  acceptOpen.value = true
  if (record.resourceType === 'account') {
    void loadAcceptGroupOptions()
  }
  try {
    // 上游地址与模型明细不在列表摘要里，领取前按需拉取完整来源详情。
    acceptTarget.value = await api.myAuthorizationCenter.sourceDetail(record.sourceId)
  } catch {
    // 详情拉取失败时保留列表摘要，领取动作仍可继续。
  }
}

async function loadAcceptGroupOptions() {
  acceptGroupLoading.value = true
  try {
    const groups = await api.myGroups.authorizationOptions()
    acceptGroupOptions.value = groups.map((item) => ({ value: item.id, label: item.name }))
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    acceptGroupLoading.value = false
  }
}

async function confirmAccept() {
  const target = acceptTarget.value
  if (!target) return
  if (target.resourceType === 'account' && !acceptTargetGroupId.value) {
    message.error('请选择领取到的分组')
    return
  }
  acting.value = true
  try {
    await api.myAuthorizationCenter.accept(target.sourceId, {
      ...(target.resourceType === 'account' ? { targetGroupId: acceptTargetGroupId.value } : {})
    })
    message.success('已领取，授权已生效')
    acceptOpen.value = false
    await refresh()
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    acting.value = false
  }
}

function openExtend(record: AuthorizationOfferSummary) {
  extendTarget.value = record
  extendDate.value = dayjs().add(30, 'day')
  extendOpen.value = true
}

async function confirmExtend() {
  const target = extendTarget.value
  if (!target) return
  if (!extendDate.value?.isValid()) {
    message.error('请选择新的到期时间')
    return
  }
  acting.value = true
  try {
    await currentApi().extend(target.grantId, {
      expiresAt: extendDate.value.toISOString()
    })
    message.success('已延长授权')
    extendOpen.value = false
    await refresh()
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    acting.value = false
  }
}

async function openCreate() {
  ownerSearch.clear()
  resourceSearch.clear()
  ownerKeyword.value = ''
  resourceKeyword.value = ''
  createForm.ownerSystemAccountId = isManagementView.value ? undefined : authState.currentUser.value?.id
  createForm.resourceType = 'account'
  createForm.resourceId = undefined
  createForm.granteeType = 'system_account'
  createForm.granteeUsername = ''
  createForm.teamName = ''
  createForm.remark = ''
  createForm.expiresAt = undefined
  resourceOptions.value = []
  createOpen.value = true
  if (isManagementView.value) {
    await loadOwnerOptions()
  } else {
    await loadCreateResources()
  }
}

async function loadOwnerOptions(keyword = '') {
  ownerLoading.value = true
  try {
    const options = await api.systemAccounts.options({ keyword: keyword.trim() || undefined, limit: 50 })
    const next = options
      .filter((item) => item.status !== 'disabled')
      .map((item) => ({ value: item.id, label: `${item.displayName || item.username}（${item.username}）` }))
    const selected = ownerOptions.value.find((item) => item.value === createForm.ownerSystemAccountId)
    if (selected && !next.some((item) => item.value === selected.value)) {
      next.unshift(selected)
    }
    ownerOptions.value = next
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    ownerLoading.value = false
  }
}

function scheduleOwnerSearch(keyword: string) {
  ownerSearch.schedule(keyword)
}

async function handleOwnerChange() {
  resourceSearch.clear()
  resourceKeyword.value = ''
  createForm.resourceId = undefined
  await loadCreateResources()
}

async function handleResourceTypeChange() {
  resourceSearch.clear()
  resourceKeyword.value = ''
  createForm.resourceId = undefined
  await loadCreateResources()
}

async function loadCreateResources(keyword = '') {
  const ownerID = isManagementView.value ? createForm.ownerSystemAccountId : authState.currentUser.value?.id
  if (!ownerID) {
    resourceOptions.value = []
    return
  }
  resourceLoading.value = true
  const search = keyword.trim() || undefined
  try {
    let next: ResourceOption[]
    if (createForm.resourceType === 'account') {
      const accounts = isManagementView.value
        ? await api.accounts.options({ systemAccountId: ownerID, keyword: search, limit: 50 })
        : await api.myAccounts.options({ keyword: search, limit: 50 })
      next = accounts
        .filter((item) => !item.accountAuthorizationId)
        .map((item) => ({ value: item.id, label: `${item.name} · ${item.providerCode || item.type}` }))
    } else {
      const groups = isManagementView.value
        ? await api.groups.authorizationOptions({ systemAccountId: ownerID, keyword: search, limit: 50 })
        : await api.myGroups.authorizationOptions({ keyword: search, limit: 50 })
      next = groups.map((item) => ({ value: item.id, label: item.name }))
    }
    const selected = resourceOptions.value.find((item) => item.value === createForm.resourceId)
    if (selected && !next.some((item) => item.value === selected.value)) {
      next.unshift(selected)
    }
    resourceOptions.value = next
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    resourceLoading.value = false
  }
}

function scheduleResourceSearch(keyword: string) {
  resourceSearch.schedule(keyword)
}

async function confirmCreate() {
  const ownerID = createForm.ownerSystemAccountId
  if (isManagementView.value && !ownerID) {
    message.error('请选择资源所有者')
    return
  }
  if (!createForm.resourceId) {
    message.error('请选择授权资源')
    return
  }
  if (createForm.granteeType === 'system_account' && !createForm.granteeUsername.trim()) {
    message.error('请输入精确用户名')
    return
  }
  if (createForm.granteeType === 'team' && !createForm.teamName.trim()) {
    message.error('请输入精确团队名')
    return
  }
  const body = {
    resourceType: createForm.resourceType,
    resourceId: createForm.resourceId,
    granteeType: createForm.granteeType,
    ...(createForm.granteeType === 'system_account'
      ? { granteeUsername: createForm.granteeUsername.trim() }
      : { teamName: createForm.teamName.trim() }),
    ...(createForm.remark.trim() ? { remark: createForm.remark.trim() } : {}),
    ...(createForm.expiresAt?.isValid() ? { expiresAt: createForm.expiresAt.toISOString() } : {})
  }
  acting.value = true
  try {
    if (isManagementView.value && ownerID) {
      await api.authorizationCenter.create(body, ownerID)
    } else {
      await api.myAuthorizationCenter.create(body)
    }
    if (createForm.granteeType === 'team') {
      message.success('已发送，团队成员将各自领取。')
    } else {
      message.success('已发送，对方领取后生效。')
    }
    createOpen.value = false
    activeTab.value = isManagementView.value ? 'all' : 'outbound'
    await refresh()
  } catch (error) {
    message.error(errorMessage(error))
  } finally {
    acting.value = false
  }
}

function errorMessage(error: unknown): string {
  if (error && typeof error === 'object' && 'response' in error) {
    const response = (error as { response?: { data?: { message?: string } } }).response
    if (response?.data?.message) return response.data.message
  }
  return error instanceof Error ? error.message : '操作失败'
}

watch(isManagementView, (management) => {
  activeTab.value = management ? 'all' : 'inbound'
  refresh()
})

onMounted(refresh)
</script>

<style scoped>
.authorization-center__title {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
.authorization-center__muted {
  color: var(--text-color-secondary, #8a919f);
  font-size: 12px;
  line-height: 1.5;
}
.authorization-center__danger {
  color: #d4380d;
  font-size: 12px;
  line-height: 1.5;
}
</style>
