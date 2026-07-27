<template>
  <a-modal
    v-model:open="open"
    title="批量编辑 AI 账户"
    width="1040px"
    :footer="null"
    :focus-trigger-after-close="false"
    @cancel="close"
  >
    <div class="batch-edit-modal">
      <div class="batch-edit-summary">
        <div class="batch-edit-summary-main">
          <strong>已选择 {{ accounts.length }} 个账户</strong>
          <span>{{ accountNameSummary }}</span>
        </div>
        <span class="batch-edit-summary-hint">仅修改勾选项</span>
      </div>

      <a-alert
        v-if="contextError"
        type="error"
        show-icon
        :message="contextError"
      />

      <div v-if="loading" class="batch-edit-loading">
        <a-spin tip="正在获取所选账户的最新配置" />
      </div>

      <a-form v-else layout="vertical">
        <a-tabs v-model:activeKey="activeTab">
          <a-tab-pane key="general" tab="通用配置">
            <div class="batch-edit-section">
              <AccountBatchEditField
                v-model:checked="form.enabled.tags"
                label="账户标签"
                description="直接覆盖全部目标账户的账户标签；留空表示清空账户标签。"
              >
                <template #default="{ disabled }">
                  <a-select
                    v-model:value="form.tags"
                    mode="tags"
                    allow-clear
                    :disabled="disabled"
                    :options="tagOptions"
                    placeholder="输入或选择标签"
                  />
                </template>
              </AccountBatchEditField>

              <AccountBatchEditField
                v-model:checked="form.enabled.proxyProfileId"
                label="代理"
                description="选择统一代理；留空表示全部改为不使用代理。"
              >
                <template #default="{ disabled }">
                  <ProxySelect
                    v-model:value="form.proxyProfileId"
                    allow-clear
                    :disabled="disabled"
                    :loading="proxyOptionsLoading"
                    :options="proxyOptions"
                    placeholder="不使用代理"
                    @dropdown-visible-change="$emit('proxyOptionsDropdown', $event)"
                    @search="$emit('proxyOptionsSearch', $event)"
                  />
                </template>
              </AccountBatchEditField>

              <AccountBatchEditField
                v-model:checked="form.enabled.supportedEndpointModes"
                :disabled="!homogeneousModelConfiguration"
                label="上游接口能力"
                description="直接覆盖账户真实上游支持的接口形态。"
              >
                <template #default="{ disabled }">
                  <a-checkbox-group
                    v-model:value="form.supportedEndpointModes"
                    :disabled="disabled"
                  >
                    <div class="endpoint-mode-grid">
                      <a-checkbox
                        v-for="option in endpointModeOptions"
                        :key="option.value"
                        :value="option.value"
                      >
                        {{ option.label }}
                      </a-checkbox>
                    </div>
                  </a-checkbox-group>
                </template>
              </AccountBatchEditField>

              <div class="batch-edit-two-columns">
                <AccountBatchEditField
                  v-model:checked="form.enabled.concurrencyLimit"
                  label="并发上限"
                >
                  <template #default="{ disabled }">
                    <a-input-number
                      v-model:value="form.concurrencyLimit"
                      :disabled="disabled"
                      :min="1"
                      :precision="0"
                    />
                  </template>
                </AccountBatchEditField>
                <AccountBatchEditField
                  v-model:checked="form.enabled.priority"
                  label="优先级"
                >
                  <template #default="{ disabled }">
                    <a-input-number
                      v-model:value="form.priority"
                      :disabled="disabled"
                      :min="0"
                      :precision="0"
                    />
                  </template>
                </AccountBatchEditField>
              </div>

              <div class="batch-edit-two-columns">
                <AccountBatchEditField
                  v-model:checked="form.enabled.superPriorityEnabled"
                  label="超级优先"
                  description="开启后优先进入调度候选。"
                >
                  <template #default="{ disabled }">
                    <a-switch v-model:checked="form.superPriorityEnabled" :disabled="disabled" />
                  </template>
                </AccountBatchEditField>
                <AccountBatchEditField
                  v-model:checked="form.enabled.fallbackEnabled"
                  label="降级备用"
                  description="开启后仅作为降级候选。"
                >
                  <template #default="{ disabled }">
                    <a-switch v-model:checked="form.fallbackEnabled" :disabled="disabled" />
                  </template>
                </AccountBatchEditField>
              </div>

              <AccountBatchEditField
                v-model:checked="form.enabled.accountExpiresAt"
                label="账户到期时间"
                description="留空表示清除套餐到期限制。"
              >
                <template #default="{ disabled }">
                  <a-date-picker
                    v-model:value="form.accountExpiresAt"
                    allow-clear
                    show-time
                    :disabled="disabled"
                  />
                </template>
              </AccountBatchEditField>

              <AccountBatchEditField
                v-model:checked="form.enabled.availabilitySchedule"
                label="时间计划"
                description="关闭计划开关并保存表示清除现有时间计划。"
              >
                <template #default="{ disabled }">
                  <TimeScheduleSection
                    :form="scheduleForm"
                    :readonly="disabled"
                    label="时间计划"
                    readonly-label="时间计划"
                    row-key-prefix="account_batch_schedule_window"
                  />
                </template>
              </AccountBatchEditField>

              <AccountBatchEditField
                v-model:checked="form.enabled.notes"
                label="说明"
                description="留空表示清空说明。"
              >
                <template #default="{ disabled }">
                  <a-textarea
                    v-model:value="form.notes"
                    :disabled="disabled"
                    :rows="3"
                    placeholder="统一写入账户说明"
                  />
                </template>
              </AccountBatchEditField>
            </div>
          </a-tab-pane>

          <a-tab-pane key="rules" tab="策略规则">
            <div class="batch-edit-section">
              <AccountBatchEditField
                v-model:checked="form.enabled.errorHandlingRules"
                label="错误处理策略"
                description="直接覆盖账户专属错误规则；不添加到原规则。"
              >
                <template #default="{ disabled }">
                  <AccountErrorPolicyCard
                    v-model:rules="form.errorHandlingRules"
                    :account-type="homogeneousAccount?.type"
                    :provider-code="homogeneousAccount?.providerCode"
                    :readonly="disabled"
                  />
                </template>
              </AccountBatchEditField>

              <AccountBatchEditField
                v-model:checked="form.enabled.responseInspectionRules"
                label="响应检查策略"
                description="直接覆盖账户专属响应检查规则；留空表示清空。"
              >
                <template #default="{ disabled }">
                  <AccountResponseInspectionPolicyCard
                    v-model:rules="form.responseInspectionRules"
                    :readonly="disabled"
                  />
                </template>
              </AccountBatchEditField>
            </div>
          </a-tab-pane>

          <a-tab-pane key="models" tab="模型与协议">
            <a-alert
              v-if="!homogeneousModelConfiguration"
              type="warning"
              show-icon
              message="所选账户的供应商、协议档案或账户类型不一致，不能统一覆盖模型与协议配置。"
              class="model-config-alert"
            />

            <div class="batch-edit-section">
              <AccountBatchEditField
                v-model:checked="form.enabled.supportedModels"
                :disabled="!homogeneousModelConfiguration"
                label="支持模型"
                description="直接覆盖全部目标账户的支持模型。"
              >
                <template #default="{ disabled }">
                  <a-select
                    v-model:value="form.supportedModels"
                    mode="multiple"
                    allow-clear
                    show-search
                    option-filter-prop="label"
                    :disabled="disabled"
                    :loading="modelsLoading"
                    :options="modelOptions"
                    placeholder="选择账户实际支持的模型"
                  />
                </template>
              </AccountBatchEditField>

              <div class="batch-edit-two-columns">
                <AccountBatchEditField
                  v-model:checked="form.enabled.healthCheckModel"
                  :disabled="!homogeneousModelConfiguration"
                  label="检查模型"
                  description="后台激活、周期检查和恢复探测统一使用该模型。"
                >
                  <template #default="{ disabled }">
                    <a-select
                      v-model:value="form.healthCheckModel"
                      show-search
                      option-filter-prop="label"
                      :disabled="disabled || !healthCheckModelOptions.length"
                      :options="healthCheckModelOptions"
                      placeholder="选择检查模型"
                    />
                  </template>
                </AccountBatchEditField>

                <AccountBatchEditField
                  v-model:checked="form.enabled.healthCheckEndpointMode"
                  :disabled="!homogeneousModelConfiguration"
                  label="检查请求形态"
                  description="后台检查直接使用所选请求形态；GPT 建议使用 Responses API（Streaming）。"
                >
                  <template #default="{ disabled }">
                    <a-select
                      v-model:value="form.healthCheckEndpointMode"
                      :disabled="disabled || !healthCheckEndpointModeOptions.length"
                      :options="healthCheckEndpointModeOptions"
                      placeholder="选择检查请求形态"
                    />
                  </template>
                </AccountBatchEditField>
              </div>

              <AccountBatchEditField
                v-model:checked="form.enabled.modelMappings"
                :disabled="!homogeneousModelConfiguration"
                label="账号模型别名"
                description="直接覆盖全部账号模型别名；留空表示清空。"
              >
                <template #default="{ disabled }">
                  <div class="mapping-list">
                    <div
                      v-for="(mapping, index) in form.modelMappings"
                      :key="index"
                      class="mapping-row"
                    >
                      <a-select
                        v-model:value="mapping.sourceEndpointFamily"
                        :disabled="disabled"
                        :options="sourceEndpointOptions"
                        placeholder="客户端协议"
                      />
                      <a-select
                        v-model:value="mapping.sourceModel"
                        show-search
                        option-filter-prop="label"
                        :disabled="disabled"
                        :options="mappingSourceModelOptionsFor(mapping)"
                        placeholder="客户端模型"
                        @dropdown-visible-change="handleMappingModelOptionsOpen"
                        @search="handleMappingModelOptionsSearch"
                      />
                      <SwapRightOutlined class="mapping-arrow" />
                      <a-select
                        v-model:value="mapping.upstreamEndpointFamily"
                        :disabled="disabled"
                        :options="upstreamEndpointOptionsFor(mapping)"
                        placeholder="上游协议"
                      />
                      <a-select
                        v-model:value="mapping.upstreamModel"
                        show-search
                        option-filter-prop="label"
                        :disabled="disabled"
                        :options="mappingUpstreamModelOptionsFor(mapping)"
                        placeholder="上游模型"
                        @dropdown-visible-change="handleMappingModelOptionsOpen"
                        @search="handleMappingModelOptionsSearch"
                      />
                      <a-switch v-model:checked="mapping.enabled" :disabled="disabled" />
                      <a-tooltip title="删除别名">
                        <a-button
                          danger
                          type="text"
                          :disabled="disabled"
                          @click="removeMapping(index)"
                        >
                          <template #icon><DeleteOutlined /></template>
                        </a-button>
                      </a-tooltip>
                    </div>
                    <a-button
                      block
                      type="dashed"
                      :disabled="disabled"
                      @click="addMapping"
                    >
                      <template #icon><PlusOutlined /></template>
                      新增别名
                    </a-button>
                  </div>
                </template>
              </AccountBatchEditField>

              <template v-if="requestOverridesSupported">
                <div class="batch-edit-two-columns">
                  <AccountBatchEditField
                    v-model:checked="form.enabled.serviceTierOverride"
                    label="服务等级"
                    :description="serviceTierDescription"
                  >
                    <template #default="{ disabled }">
                      <a-select
                        v-model:value="form.serviceTierOverride"
                        :disabled="disabled || modelsLoading || !gptCapabilities.serviceTiers.length"
                        :options="serviceTierOptions"
                      />
                    </template>
                  </AccountBatchEditField>
                  <AccountBatchEditField
                    v-model:checked="form.enabled.reasoningEffortOverride"
                    label="思考级别"
                    :description="reasoningEffortDescription"
                  >
                    <template #default="{ disabled }">
                      <a-select
                        v-model:value="form.reasoningEffortOverride"
                        :disabled="disabled || modelsLoading || !gptCapabilities.reasoningEfforts.length"
                        :options="reasoningEffortOptions"
                      />
                    </template>
                  </AccountBatchEditField>
                </div>
              </template>
            </div>
          </a-tab-pane>
        </a-tabs>
      </a-form>

      <div class="batch-edit-footer">
        <span>{{ footerSummary }}</span>
        <a-space>
          <a-button @click="close">取消</a-button>
          <a-popconfirm
            :title="confirmTitle"
            ok-text="确认覆盖"
            cancel-text="返回检查"
            :disabled="saveDisabled"
            @confirm="save"
          >
            <a-button type="primary" :disabled="saveDisabled" :loading="saving">
              保存批量配置
            </a-button>
          </a-popconfirm>
        </a-space>
      </div>
    </div>
  </a-modal>
</template>

<script setup lang="ts">
import { DeleteOutlined, PlusOutlined, SwapRightOutlined } from '@ant-design/icons-vue'
import { computed, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import ProxySelect from '@/components/ProxySelect.vue'
import { loadAccountProviderModelOptionsResource } from '@/views/accounts/useAccountProviderModelOptions'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import type {
  AccountModelMapping,
  AccountSummary,
  AccountTagSummary,
  ProviderDefinition
} from '@/types/domain'
import TimeScheduleSection from '@/views/shared/TimeScheduleSection.vue'
import AccountBatchEditField from './AccountBatchEditField.vue'
import AccountErrorPolicyCard from './AccountErrorPolicyCard.vue'
import AccountResponseInspectionPolicyCard from './AccountResponseInspectionPolicyCard.vue'
import {
  buildAccountBatchEditRequest,
  createAccountBatchEditForm,
  enabledAccountBatchEditFieldLabels,
  intersectAccountSupportedEndpointModes,
  type AccountBatchEditForm
} from './accountBatchEditForm'
import { accountEndpointModeOptionsForProfile } from './accountEndpointModes'
import { accountHealthCheckEndpointModeOptions } from './accountHealthCheckEndpointMode'
import { providerModelsForProtocolProfile, type AccountModelSelectOption } from './accountEditFormPayload'
import {
  accountModelMappingSourceModelOptions,
  accountModelMappingUpstreamModelOptions
} from './accountModelMappingModelOptions'
import {
  accountGptRequestOverrideCapabilities,
  availableAccountGptReasoningEffortOptions,
  availableAccountGptServiceTierOptions,
  isAccountRequestOverrideProviderSupported
} from './accountGptRequestOverrides'
import {
  defaultAccountModelMappingSourceEndpointFamily,
  defaultAccountModelMappingUpstreamEndpointFamily,
  isAccountModelMappingProtocolAllowed,
  isAccountModelMappingSourceEndpointFamilyAllowed
} from './accountModelMappingProtocolMatrix'
import { accountOperationScopeParams } from './accountOperationScope'
import { endpointModesForProfile } from './accountProviderCapabilities'
import { canBatchEditAccount } from './accountRules'

interface SelectOption {
  label: string
  value: string
  disabled?: boolean
}

const open = defineModel<boolean>('open', { required: true })
const props = defineProps<{
  accounts: AccountSummary[]
  isManagementView: boolean
  providers: ProviderDefinition[]
  proxyOptions: SelectOption[]
  proxyOptionsLoading?: boolean
  scopeParams?: { systemAccountId: string }
  tags: AccountTagSummary[]
}>()
const emit = defineEmits<{
  (event: 'proxyOptionsDropdown', open: boolean): void
  (event: 'proxyOptionsSearch', value: string): void
  (event: 'saved'): void
}>()

const activeTab = ref('general')
const loading = ref(false)
const saving = ref(false)
const modelsLoading = ref(false)
const contextError = ref('')
const accountDetails = ref<AccountSummary[]>([])
const currentProviderModelOptions = ref<AccountModelSelectOption[]>([])
const modelOptions = ref<AccountModelSelectOption[]>([])
const form = reactive<AccountBatchEditForm>(createAccountBatchEditForm())
let loadToken = 0
let modelOptionsRequestId = 0

const tagOptions = computed(() => props.tags.map((tag) => ({ label: tag.name, value: tag.name })))
const accountNameSummary = computed(() => {
  const names = props.accounts.slice(0, 4).map((account) => account.name)
  return props.accounts.length > 4 ? `${names.join('、')} 等 ${props.accounts.length} 个账户` : names.join('、')
})
const homogeneousModelConfiguration = computed(() => {
  if (!accountDetails.value.length) return false
  const signatures = new Set(accountDetails.value.map((account) => [
    account.providerCode,
    account.providerProtocolProfileId ?? '',
    account.type
  ].join('\u0000')))
  return signatures.size === 1
})
const homogeneousAccount = computed(() => homogeneousModelConfiguration.value ? accountDetails.value[0] : undefined)
const selectedProvider = computed(() => props.providers.find((provider) => provider.code === homogeneousAccount.value?.providerCode))
const selectedProtocolProfile = computed(() => {
  const account = homogeneousAccount.value
  const provider = selectedProvider.value
  return provider?.protocolProfiles.find((profile) => profile.id === account?.providerProtocolProfileId)
    ?? provider?.protocolProfiles.find((profile) => profile.enabled)
    ?? provider?.protocolProfiles[0]
})
const sharedSupportedModels = computed(() => intersectAccountModels(accountDetails.value))
const effectiveBatchModels = computed(() => (
  form.enabled.supportedModels ? normalizedTextList(form.supportedModels) : sharedSupportedModels.value
))
const healthCheckModelOptions = computed(() => effectiveBatchModels.value.map((model) => ({ label: model, value: model })))
const effectiveBatchEndpointModes = computed(() => (
  form.enabled.supportedEndpointModes
    ? form.supportedEndpointModes
    : intersectAccountSupportedEndpointModes(accountDetails.value)
))
const healthCheckEndpointModeOptions = computed(() => accountHealthCheckEndpointModeOptions(effectiveBatchEndpointModes.value))
const mappingUpstreamModelOptions = computed(() => {
  const options = new Map(currentProviderModelOptions.value.map((option) => [option.value, option]))
  return effectiveBatchModels.value.map((model) => {
    const option = options.get(model)
    return {
      label: option?.label ?? model,
      value: model,
      supportedApiProtocols: option?.supportedApiProtocols
    }
  })
})
const endpointModeOptions = computed(() => {
  const profile = selectedProtocolProfile.value ?? homogeneousAccount.value
  const allowed = new Set(endpointModesForProfile(profile))
  return accountEndpointModeOptionsForProfile(profile).filter((option) => allowed.has(option.value))
})
const gptCapabilities = computed(() => accountGptRequestOverrideCapabilities({
  providerCode: homogeneousAccount.value?.providerCode,
  accountType: homogeneousAccount.value?.type ?? 'api_key',
  modelOptions: modelOptions.value,
  supportedModels: effectiveBatchModels.value,
  supportedEndpointModes: effectiveBatchEndpointModes.value
}))
const requestOverridesSupported = computed(() => Boolean(
  homogeneousAccount.value
  && isAccountRequestOverrideProviderSupported(homogeneousAccount.value.providerCode, effectiveBatchEndpointModes.value)
))
const serviceTierOptions = computed(() => availableAccountGptServiceTierOptions(gptCapabilities.value))
const reasoningEffortOptions = computed(() => availableAccountGptReasoningEffortOptions(gptCapabilities.value))
const serviceTierDescription = computed(() => gptCapabilities.value.serviceTiers.length
  ? '不覆盖客户端设置表示清除账户覆盖。'
  : '当前已选模型未声明可用服务等级；仍可批量清除已有覆盖。')
const reasoningEffortDescription = computed(() => gptCapabilities.value.reasoningEfforts.length
  ? '不覆盖客户端设置表示清除账户覆盖。'
  : '当前已选模型未声明可用思考级别；仍可批量清除已有覆盖。')
const scheduleForm = computed(() => ({ availabilitySchedule: form.availabilitySchedule }))
const enabledLabels = computed(() => enabledAccountBatchEditFieldLabels(form))
const saveDisabled = computed(() => (
  loading.value
  || saving.value
  || Boolean(contextError.value)
  || props.accounts.length < 2
  || enabledLabels.value.length === 0
))
const footerSummary = computed(() => (
  enabledLabels.value.length
    ? `将覆盖 ${props.accounts.length} 个账户的 ${enabledLabels.value.length} 项配置`
    : '勾选需要覆盖的配置后才能保存'
))
const confirmTitle = computed(() => (
  `确认用当前值覆盖 ${props.accounts.length} 个账户的 ${enabledLabels.value.join('、')}？`
))

const sourceEndpointBaseOptions = [
  { label: 'Chat Completions', value: 'chat_completions' },
  { label: 'Responses', value: 'responses' },
  { label: 'Messages', value: 'messages' },
  { label: 'Gemini GenerateContent', value: 'generate_content' },
  { label: 'Gemini StreamGenerateContent', value: 'stream_generate_content' }
] as const
const sourceEndpointOptions = computed(() => sourceEndpointBaseOptions.map((option) => ({
  ...option,
  disabled: !isAccountModelMappingSourceEndpointFamilyAllowed(option.value, mappingContext())
})))
const upstreamEndpointBaseOptions = [
  { label: 'Chat Completions', value: 'chat_completions' },
  { label: 'Responses', value: 'responses' },
  { label: 'Messages', value: 'messages' },
  { label: 'Gemini GenerateContent', value: 'generate_content' }
] as const

watch(open, (next) => {
  if (next) void loadContext()
}, { immediate: true })

async function loadContext(): Promise<void> {
  const token = ++loadToken
  activeTab.value = 'general'
  Object.assign(form, createAccountBatchEditForm())
  accountDetails.value = []
  currentProviderModelOptions.value = []
  modelOptions.value = []
  contextError.value = ''
  if (props.accounts.length < 2 || props.accounts.length > 100) {
    contextError.value = '批量编辑一次只能选择 2 到 100 个账户'
    return
  }
  if (props.accounts.some((account) => !canBatchEditAccount(account))) {
    contextError.value = '所选账户包含授权实例或无编辑权限账户，请重新选择'
    return
  }
  loading.value = true
  try {
    const accountIds = props.accounts.map((account) => account.id)
    const details = props.isManagementView
      ? await api.accounts.batchEditContext(accountIds, props.scopeParams)
      : await api.myAccounts.batchEditContext(accountIds)
    if (token !== loadToken || !open.value) return
    accountDetails.value = details
    if (accountDetails.value.length !== props.accounts.length) {
      throw new Error('部分账户详情未能加载')
    }
    if (homogeneousModelConfiguration.value) {
      await loadModelOptions(token)
    }
  } catch (error) {
    console.error(error)
    contextError.value = extractApiErrorMessage(error, '获取批量编辑配置失败，请刷新列表后重试')
  } finally {
    if (token === loadToken) loading.value = false
  }
}

async function loadModelOptions(token: number, keyword = ''): Promise<void> {
  const account = homogeneousAccount.value
  if (!account) return
  const requestId = ++modelOptionsRequestId
  modelsLoading.value = true
  try {
    const scope = props.isManagementView
      ? accountOperationScopeParams(account, props.scopeParams)
      : undefined
    const models = await loadAccountProviderModelOptionsResource({
      isManagementView: props.isManagementView,
      providerCode: account.providerCode,
      scopeParams: scope,
      selectedIds: [
        ...effectiveBatchModels.value,
        ...form.modelMappings.flatMap((mapping) => [mapping.sourceModel, mapping.upstreamModel])
      ],
      keyword
    })
    if (token !== loadToken || requestId !== modelOptionsRequestId || !open.value) return
    currentProviderModelOptions.value = models.data
    modelOptions.value = providerModelsForProtocolProfile(models.data, selectedProtocolProfile.value, account.type)
  } finally {
    if (token === loadToken && requestId === modelOptionsRequestId) modelsLoading.value = false
  }
}

function handleMappingModelOptionsOpen(nextOpen: boolean): void {
  if (nextOpen) void loadModelOptions(loadToken)
}

function handleMappingModelOptionsSearch(value: string): void {
  void loadModelOptions(loadToken, value.trim())
}

async function save(): Promise<void> {
  if (saveDisabled.value) return
  const result = buildAccountBatchEditRequest(accountDetails.value, form, {
    mappingAnthropicSourceModelOptions: currentProviderModelOptions.value,
    mappingCurrentProviderSourceModelOptions: currentProviderModelOptions.value,
    mappingGeminiSourceModelOptions: currentProviderModelOptions.value,
    mappingSourceModelOptions: currentProviderModelOptions.value,
    mappingUpstreamModelOptions: mappingUpstreamModelOptions.value,
    providerCode: homogeneousAccount.value?.providerCode,
    providerProfile: selectedProtocolProfile.value ?? selectedProvider.value
  })
  if (!result.payload) {
    message.warning(result.message ?? '批量编辑配置无效')
    return
  }
  saving.value = true
  try {
    if (props.isManagementView) {
      await api.accounts.batchUpdate(result.payload, props.scopeParams)
    } else {
      await api.myAccounts.batchUpdate(result.payload)
    }
    message.success(`已批量更新 ${accountDetails.value.length} 个账户`)
    open.value = false
    emit('saved')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '批量编辑账户失败'))
  } finally {
    saving.value = false
  }
}

function close(): void {
  loadToken += 1
  open.value = false
}

function addMapping(): void {
  const sourceEndpointFamily = defaultAccountModelMappingSourceEndpointFamily(mappingContext())
  const upstreamEndpointFamily = defaultAccountModelMappingUpstreamEndpointFamily(
    sourceEndpointFamily,
    mappingContext()
  )
  form.modelMappings.push({
    sourceModel: '',
    sourceEndpointFamily,
    upstreamModel: '',
    upstreamEndpointFamily,
    enabled: true
  })
}

function removeMapping(index: number): void {
  form.modelMappings.splice(index, 1)
}

function upstreamEndpointOptionsFor(mapping: AccountModelMapping) {
  return upstreamEndpointBaseOptions.map((option) => ({
    ...option,
    disabled: !isAccountModelMappingProtocolAllowed({
      sourceEndpointFamily: mapping.sourceEndpointFamily,
      upstreamEndpointFamily: option.value,
      enabled: mapping.enabled,
      context: mappingContext()
    })
  }))
}

function mappingSourceModelOptionsFor(mapping: AccountModelMapping): AccountModelSelectOption[] {
  return accountModelMappingSourceModelOptions({
    providerCode: homogeneousAccount.value?.providerCode,
    sourceEndpointFamily: mapping.sourceEndpointFamily,
    currentProviderOptions: currentProviderModelOptions.value,
    openAIProtocolOptions: currentProviderModelOptions.value,
    anthropicProtocolOptions: currentProviderModelOptions.value,
    geminiProtocolOptions: currentProviderModelOptions.value
  }) as AccountModelSelectOption[]
}

function mappingUpstreamModelOptionsFor(mapping: AccountModelMapping): AccountModelSelectOption[] {
  return accountModelMappingUpstreamModelOptions(
    mappingUpstreamModelOptions.value,
    mapping.upstreamEndpointFamily
  ) as AccountModelSelectOption[]
}

function mappingContext() {
  return {
    providerProfile: selectedProtocolProfile.value ?? homogeneousAccount.value,
    supportedEndpointModes: form.enabled.supportedEndpointModes
      ? form.supportedEndpointModes
      : intersectAccountSupportedEndpointModes(accountDetails.value)
  }
}

function intersectAccountModels(accounts: AccountSummary[]): string[] {
  if (!accounts.length) return []
  const [first, ...rest] = accounts.map((account) => normalizedTextList(account.supportedModels ?? []))
  return first.filter((model) => rest.every((models) => models.includes(model)))
}

function normalizedTextList(values: string[]): string[] {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
}

</script>

<style scoped>
.batch-edit-modal {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 16px;
}

.batch-edit-summary {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  padding: 2px 0 12px;
  border-bottom: 1px solid #eef2f7;
}

.batch-edit-summary-main {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}

.batch-edit-summary-main span,
.batch-edit-summary-hint {
  color: #64748b;
  font-size: 12px;
}

.batch-edit-summary-main span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.batch-edit-summary-hint {
  flex-shrink: 0;
}

.batch-edit-loading {
  display: grid;
  min-height: 320px;
  place-items: center;
}

.batch-edit-section {
  max-height: min(62vh, 660px);
  overflow-x: hidden;
  overflow-y: auto;
  padding-right: 8px;
}

.batch-edit-two-columns {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 24px;
}

.batch-edit-two-columns :deep(.batch-edit-field) {
  grid-template-columns: minmax(138px, 170px) minmax(0, 1fr);
}

.batch-edit-section :deep(.ant-select),
.batch-edit-section :deep(.ant-picker),
.batch-edit-section :deep(.ant-input-number) {
  width: 100%;
}

.model-config-alert {
  margin-bottom: 12px;
}

.endpoint-mode-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 16px;
}

.mapping-list {
  display: grid;
  gap: 8px;
}

.mapping-row {
  display: grid;
  grid-template-columns: 150px minmax(130px, 1fr) 18px 150px minmax(130px, 1fr) 36px 32px;
  gap: 8px;
  align-items: center;
  min-width: 0;
}

.mapping-arrow {
  color: #64748b;
}

.batch-edit-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding-top: 14px;
  border-top: 1px solid #eef2f7;
  color: #64748b;
  font-size: 13px;
}

@media (max-width: 900px) {
  .batch-edit-two-columns {
    grid-template-columns: 1fr;
  }

  .mapping-row {
    grid-template-columns: minmax(120px, 1fr) minmax(120px, 1fr) 24px;
  }

  .mapping-arrow {
    display: none;
  }
}

@media (max-width: 640px) {
  .batch-edit-summary,
  .batch-edit-footer {
    flex-direction: column;
    align-items: stretch;
  }

  .endpoint-mode-grid {
    grid-template-columns: 1fr;
  }
}
</style>
