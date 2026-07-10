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
        <div>
          <strong>将统一覆盖 {{ accounts.length }} 个账户</strong>
          <p>{{ accountNameSummary }}</p>
        </div>
        <a-tag color="blue">仅修改已勾选字段</a-tag>
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
                label="标签"
                description="直接覆盖全部目标账户的标签；留空表示清空标签。"
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
                    :options="proxyOptions"
                    placeholder="不使用代理"
                  />
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
                label="可用时间计划"
                description="关闭计划开关并保存表示清除现有时间计划。"
              >
                <template #default="{ disabled }">
                  <TimeScheduleSection
                    :form="scheduleForm"
                    :readonly="disabled"
                    label="可用时间计划"
                    readonly-label="可用时间计划"
                    row-key-prefix="account_batch_schedule_window"
                  />
                </template>
              </AccountBatchEditField>

              <AccountBatchEditField
                v-model:checked="form.enabled.notes"
                label="备注"
                description="留空表示清空备注。"
              >
                <template #default="{ disabled }">
                  <a-textarea
                    v-model:value="form.notes"
                    :disabled="disabled"
                    :rows="3"
                    placeholder="统一写入账户备注"
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
                v-model:checked="form.enabled.supportedEndpointModes"
                :disabled="!homogeneousModelConfiguration"
                label="接口能力限制"
                description="直接覆盖账户可承接的请求形态。"
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

              <AccountBatchEditField
                v-model:checked="form.enabled.modelMappings"
                :disabled="!homogeneousModelConfiguration"
                label="模型映射"
                description="直接覆盖全部映射；留空表示清空映射。"
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
                      />
                      <a-select
                        v-model:value="mapping.sourceModel"
                        show-search
                        option-filter-prop="label"
                        :disabled="disabled"
                        :options="modelOptions"
                        placeholder="来源模型"
                      />
                      <SwapRightOutlined class="mapping-arrow" />
                      <a-select
                        v-model:value="mapping.upstreamEndpointFamily"
                        :disabled="disabled"
                        :options="upstreamEndpointOptionsFor(mapping)"
                      />
                      <a-select
                        v-model:value="mapping.upstreamModel"
                        show-search
                        option-filter-prop="label"
                        :disabled="disabled"
                        :options="mappingUpstreamModelOptions"
                        placeholder="上游模型"
                      />
                      <a-switch v-model:checked="mapping.enabled" :disabled="disabled" />
                      <a-tooltip title="删除映射">
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
                      新增映射
                    </a-button>
                  </div>
                </template>
              </AccountBatchEditField>

              <template v-if="homogeneousAccount?.providerCode === 'gpt'">
                <div class="batch-edit-two-columns">
                  <AccountBatchEditField
                    v-model:checked="form.enabled.serviceTierOverride"
                    label="GPT 服务等级"
                    description="不覆盖客户端设置表示清除账户覆盖。"
                  >
                    <template #default="{ disabled }">
                      <a-select
                        v-model:value="form.serviceTierOverride"
                        :disabled="disabled"
                        :options="serviceTierOptions"
                      />
                    </template>
                  </AccountBatchEditField>
                  <AccountBatchEditField
                    v-model:checked="form.enabled.reasoningEffortOverride"
                    label="GPT 思考级别"
                    description="不覆盖客户端设置表示清除账户覆盖。"
                  >
                    <template #default="{ disabled }">
                      <a-select
                        v-model:value="form.reasoningEffortOverride"
                        :disabled="disabled"
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
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { isHybridProviderCode } from '@/shared/providerProtocol'
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
  type AccountBatchEditForm
} from './accountBatchEditForm'
import { accountEndpointModeOptionsForProfile } from './accountEndpointModes'
import type { AccountModelSelectOption } from './accountEditFormPayload'
import {
  accountGptRequestOverrideCapabilities,
  availableAccountGptReasoningEffortOptions,
  availableAccountGptServiceTierOptions
} from './accountGptRequestOverrides'
import {
  defaultAccountModelMappingUpstreamEndpointFamily,
  isAccountModelMappingProtocolAllowed
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
  scopeParams?: { systemAccountId: string }
  tags: AccountTagSummary[]
}>()
const emit = defineEmits<{
  (event: 'saved'): void
}>()

const activeTab = ref('general')
const loading = ref(false)
const saving = ref(false)
const modelsLoading = ref(false)
const contextError = ref('')
const accountDetails = ref<AccountSummary[]>([])
const modelOptions = ref<AccountModelSelectOption[]>([])
const form = reactive<AccountBatchEditForm>(createAccountBatchEditForm())
let loadToken = 0

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
const mappingUpstreamModelOptions = computed(() => {
  const labels = new Map(modelOptions.value.map((option) => [option.value, option.label]))
  return effectiveBatchModels.value.map((model) => ({ label: labels.get(model) ?? model, value: model }))
})
const endpointModeOptions = computed(() => {
  const profile = selectedProtocolProfile.value ?? homogeneousAccount.value
  const allowed = new Set(endpointModesForProfile(profile))
  return accountEndpointModeOptionsForProfile(profile).filter((option) => allowed.has(option.value))
})
const gptCapabilities = computed(() => accountGptRequestOverrideCapabilities({
  accountType: homogeneousAccount.value?.type ?? 'api_key',
  modelOptions: modelOptions.value,
  supportedModels: effectiveBatchModels.value
}))
const serviceTierOptions = computed(() => availableAccountGptServiceTierOptions(gptCapabilities.value))
const reasoningEffortOptions = computed(() => availableAccountGptReasoningEffortOptions(gptCapabilities.value))
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

const sourceEndpointOptions = [
  { label: 'Chat Completions', value: 'chat_completions' },
  { label: 'Responses', value: 'responses' },
  { label: 'Messages', value: 'messages' },
  { label: 'Gemini GenerateContent', value: 'generate_content' },
  { label: 'Gemini StreamGenerateContent', value: 'stream_generate_content' }
] as const
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

async function loadModelOptions(token: number): Promise<void> {
  const account = homogeneousAccount.value
  if (!account) return
  modelsLoading.value = true
  try {
    const scope = props.isManagementView
      ? accountOperationScopeParams(account, props.scopeParams)
      : undefined
    const models = isHybridProviderCode(account.providerCode)
      ? await api.providers.modelOptions(scope)
      : await api.providers.models(account.providerCode, scope)
    if (token !== loadToken || !open.value) return
    modelOptions.value = dedupeModelOptions(models.map((item) => ({
      label: item.model,
      value: item.model,
      supportedApiProtocols: item.supportedApiProtocols,
      supportedServiceTiers: item.supportedServiceTiers,
      supportedReasoningEfforts: item.supportedReasoningEfforts,
      defaultReasoningEffort: item.defaultReasoningEffort
    })))
  } finally {
    if (token === loadToken) modelsLoading.value = false
  }
}

async function save(): Promise<void> {
  if (saveDisabled.value) return
  const result = buildAccountBatchEditRequest(accountDetails.value, form)
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
  const sourceEndpointFamily = 'chat_completions' as const
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
      context: mappingContext()
    })
  }))
}

function mappingContext() {
  return {
    providerProfile: selectedProtocolProfile.value ?? homogeneousAccount.value,
    supportedEndpointModes: form.enabled.supportedEndpointModes
      ? form.supportedEndpointModes
      : homogeneousAccount.value?.credentials.supported_endpoint_modes
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

function dedupeModelOptions(options: AccountModelSelectOption[]): AccountModelSelectOption[] {
  const output: AccountModelSelectOption[] = []
  const seen = new Set<string>()
  for (const option of options) {
    const model = option.value.trim()
    if (!model || seen.has(model)) continue
    seen.add(model)
    output.push({ ...option, label: option.label || model, value: model })
  }
  return output
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
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  padding-bottom: 12px;
  border-bottom: 1px solid #eef2f7;
}

.batch-edit-summary p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
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
