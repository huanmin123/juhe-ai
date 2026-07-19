<template>
  <a-modal
    v-model:open="open"
    :title="title"
    width="820px"
    :confirm-loading="confirmLoading"
    :focus-trigger-after-close="false"
    force-render
    transition-name=""
    mask-transition-name=""
    :ok-button-props="confirmButtonProps"
    @ok="$emit('ok')"
    @cancel="$emit('cancel')"
  >
    <a-form layout="vertical" class="account-form">
      <div v-if="loading" class="account-form-loading">
        <a-spin tip="正在加载账户配置" />
      </div>
      <template v-else>
        <AccountFormSelector
          :account-type="form.type"
          :account-type-choices="accountTypeChoices"
          :editing="editing"
          :provider-code="form.providerCode"
          :providers="providers"
          :selected-protocol-profile="selectedProtocolProfile"
          :selected-provider="selectedProvider"
          @select-provider="$emit('select-provider', $event)"
          @select-type-choice="$emit('select-type-choice', $event)"
        />

        <AccountBasicInfoSection
          v-if="hasAccountType"
          :editing="editing"
          :form="form"
          :group-options="groupOptions"
          :group-options-loading="groupOptionsLoading"
          :show-meta-fields="!isApiKeyForm"
          :tag-options="tagOptions"
          :tag-options-loading="tagOptionsLoading"
          :deleting-tag-id="deletingTagId"
          :authorized-editing="authorizedEditing"
          @delete-tag="$emit('delete-tag', $event)"
          @group-options-dropdown="$emit('group-options-dropdown', $event)"
          @group-options-search="$emit('group-options-search', $event)"
        />

        <AccountApiKeySection
          v-if="isApiKeyForm && !authorizedEditing"
          :api-key-runtime-details="apiKeyRuntimeDetails"
          :api-key-test-details="apiKeyTestDetails"
          :base-url-placeholder="baseUrlPlaceholder"
          :deleting-tag-id="deletingTagId"
          :editing="editing"
          :form="form"
          :model-options="modelOptions"
          :models-loading="modelsLoading"
          :tag-options="tagOptions"
          :tag-options-loading="tagOptionsLoading"
          :title="credentialTitle"
          @delete-tag="$emit('delete-tag', $event)"
        />

        <AccountOAuthSection
          v-else-if="isTokenCredentialForm && !authorizedEditing"
          :auth-loading="authLoading"
          :auth-result="authResult"
          :editing="editing"
          :form="form"
          :is-open-a-i="isOpenAIOAuthForm"
          :is-google-o-auth="form.type === 'google_oauth'"
          :model-options="modelOptions"
          :models-loading="modelsLoading"
          :title="credentialTitle"
          @copy-auth-url="$emit('copy-auth-url', $event)"
          @generate-auth-url="$emit('generate-auth-url')"
          @open-auth-url="$emit('open-auth-url')"
        />

        <section v-if="authorizedEditing" class="form-section readonly-config-section">
          <a-descriptions bordered size="small" :column="2">
            <a-descriptions-item label="Base URL" :span="2">{{ form.baseUrl || '-' }}</a-descriptions-item>
            <a-descriptions-item label="支持模型" :span="2">
              <a-space v-if="form.supportedModels.length" wrap>
                <a-tag v-for="model in form.supportedModels" :key="model" class="mono-cell">{{ model }}</a-tag>
              </a-space>
              <span v-else>-</span>
            </a-descriptions-item>
            <a-descriptions-item label="来源账户状态">{{ sourceAccountStatusText }}</a-descriptions-item>
            <a-descriptions-item label="来源套餐到期">{{ sourceAccountExpiresAtText }}</a-descriptions-item>
            <a-descriptions-item v-for="item in publicCredentialItems" :key="item.key" :label="item.label">
              {{ item.value }}
            </a-descriptions-item>
            <a-descriptions-item label="模型映射" :span="2">
              <div v-if="readonlyModelMappings.length" class="readonly-model-mappings">
                <a-tag v-for="item in readonlyModelMappings" :key="`${item.sourceModel}:${item.sourceEndpointFamily}:${item.upstreamModel}:${item.upstreamEndpointFamily}`">
                  {{ item.sourceModel }} / {{ endpointFamilyText(item.sourceEndpointFamily) }} -> {{ item.upstreamModel }} / {{ endpointFamilyText(item.upstreamEndpointFamily) }}{{ item.enabled === false ? '（停用）' : '' }}
                </a-tag>
              </div>
              <span v-else>-</span>
            </a-descriptions-item>
          </a-descriptions>
          <div v-if="isApiKeyForm" class="readonly-meta-fields">
            <AccountMetaFields
              :deleting-tag-id="deletingTagId"
              :form="form"
              readonly
              :tag-options="tagOptions"
              :tag-options-loading="tagOptionsLoading"
              @delete-tag="$emit('delete-tag', $event)"
            />
          </div>
        </section>

        <a-collapse
          v-if="hasAccountType"
          v-model:activeKey="advancedActiveKeys"
          class="account-advanced-collapse"
          expand-icon-position="end"
        >
          <a-collapse-panel key="advanced">
            <template #header>
              <div class="advanced-header">
                <span>高级配置</span>
                <small v-if="advancedConfiguredCount > 0">已配置 {{ advancedConfiguredCount }} 项</small>
              </div>
            </template>
            <div v-if="advancedLoading" class="advanced-loading">
              <a-spin tip="正在加载高级配置" />
            </div>
            <div v-else-if="advancedPending" class="advanced-loading">
              <a-button type="primary" @click="emit('advanced-open')">加载高级配置</a-button>
            </div>
            <div v-else-if="shouldRenderAdvancedSections" class="advanced-section-stack">
              <AccountGptRequestOverridesSection
                :form="form"
                :model-options="modelOptions"
                :models-loading="modelsLoading"
                :readonly="authorizedEditing"
              />

              <AccountStrategySection
                :form="form"
                :is-management-view="isManagementView"
                :is-o-auth-form="isOAuthForm"
                :mapping-anthropic-source-model-options="mappingAnthropicSourceModelOptions"
                :mapping-gemini-source-model-options="mappingGeminiSourceModelOptions"
                :mapping-source-model-options="mappingSourceModelOptions"
                :mapping-upstream-model-options="mappingUpstreamModelOptions"
                :proxy-options="proxyOptions"
                :selected-protocol-profile="selectedProtocolProfile"
                :authorized-editing="authorizedEditing"
              />

              <section class="form-section">
                <div class="form-section-title">
                  <span>恢复探活</span>
                  <a-tooltip title="仅影响账户进入临时不可调用后的后台恢复探测。关闭后仍在前 10 分钟按退避有限复测；最终复测仍失败才标记异常。周期健康检查、首次激活、人工测试和限流恢复不受影响。">
                    <span class="form-section-tip">?</span>
                  </a-tooltip>
                </div>
                <a-form-item label="持续恢复探活">
                  <a-switch
                    v-model:checked="form.temporaryUnavailableContinuousProbeEnabled"
                    :disabled="authorizedEditing"
                    checked-children="开启"
                    un-checked-children="关闭"
                  />
                </a-form-item>
              </section>

              <AccountExtraInfoSection
                :form="form"
                :readonly="authorizedEditing"
              />

              <AccountBalanceQuerySection
                :can-query="balanceQueryCanRun"
                :form="form"
                :query-loading="balanceQueryLoading"
                :readonly="authorizedEditing"
                @query="emit('balance-query')"
              />

              <AccountAvailabilityScheduleSection
                :form="form"
                :readonly="authorizedEditing"
              />

              <AccountErrorPolicyCard
                v-model:rules="errorPolicyRules"
                :readonly="authorizedEditing"
              />

              <AccountResponseInspectionPolicyCard
                v-model:rules="responseInspectionRules"
                :readonly="authorizedEditing"
              />
            </div>
          </a-collapse-panel>
        </a-collapse>
      </template>
    </a-form>

    <template #footer>
      <div class="account-modal-footer">
        <a-button :disabled="testButtonDisabled" :loading="testLoading" @click="$emit('test')">测试</a-button>
        <a-space>
          <a-button @click="$emit('cancel')">取消</a-button>
          <a-button v-bind="confirmButtonProps" :loading="confirmLoading" @click="$emit('ok')">确定</a-button>
        </a-space>
      </div>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import type { AccountApiKeyRuntimeDetail, AccountSummary, AccountTagSummary, OpenAIAuthURLResult, ProviderDefinition, ProviderModelApiProtocol, ProviderProtocolProfileDefinition } from '@/types/domain'
import AccountAvailabilityScheduleSection from './AccountAvailabilityScheduleSection.vue'
import AccountApiKeySection from './AccountApiKeySection.vue'
import AccountBasicInfoSection from './AccountBasicInfoSection.vue'
import AccountErrorPolicyCard from './AccountErrorPolicyCard.vue'
import AccountExtraInfoSection from './AccountExtraInfoSection.vue'
import AccountBalanceQuerySection from './AccountBalanceQuerySection.vue'
import AccountFormSelector from './AccountFormSelector.vue'
import AccountGptRequestOverridesSection from './AccountGptRequestOverridesSection.vue'
import AccountMetaFields from './AccountMetaFields.vue'
import AccountOAuthSection from './AccountOAuthSection.vue'
import AccountResponseInspectionPolicyCard from './AccountResponseInspectionPolicyCard.vue'
import AccountStrategySection from './AccountStrategySection.vue'
import { statusText } from './accountFormatters'
import {
  accountEndpointModeText,
  defaultAccountEndpointModes,
  endpointModesEqual
} from './accountEndpointModes'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import type { AccountTypeChoice } from './accountEditFormDisplay'

interface SelectOption<T = string> {
  label: string
  value: T
  supportedApiProtocols?: ProviderModelApiProtocol[]
}

const open = defineModel<boolean>('open', { required: true })
const errorPolicyRules = defineModel<AccountErrorPolicyRuleForm[]>('errorPolicyRules', { required: true })
const responseInspectionRules = defineModel<AccountResponseInspectionRuleForm[]>('responseInspectionRules', { required: true })
const advancedActiveKeys = ref<string[]>([])

const props = withDefaults(defineProps<{
  accountTypeChoices: AccountTypeChoice[]
  accountDetail?: AccountSummary
  apiKeyRuntimeDetails?: AccountApiKeyRuntimeDetail[]
  advancedLoaded?: boolean
  advancedLoading?: boolean
  apiKeyTestDetails?: AccountApiKeyRuntimeDetail[]
  authorizedEditing: boolean
  authLoading: boolean
  authResult?: OpenAIAuthURLResult
  baseUrlPlaceholder: string
  balanceQueryCanRun?: boolean
  balanceQueryLoading?: boolean
  confirmLoading: boolean
  credentialTitle: string
  editing: boolean
  form: AccountFormModel
  groupOptions: SelectOption[]
  groupOptionsLoading: boolean
  tagOptions: AccountTagSummary[]
  tagOptionsLoading: boolean
  deletingTagId?: string
  hasAccountType: boolean
  isApiKeyForm: boolean
  isManagementView: boolean
  isOAuthForm: boolean
  isTokenCredentialForm: boolean
  isOpenAIOAuthForm: boolean
  loading?: boolean
  mappingAnthropicSourceModelOptions: SelectOption[]
  mappingGeminiSourceModelOptions: SelectOption[]
  mappingSourceModelOptions: SelectOption[]
  modelOptions: SelectOption[]
  modelsLoading: boolean
  okButtonProps: Record<string, unknown>
  providers: ProviderDefinition[]
  proxyOptions: SelectOption[]
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
  selectedProvider?: ProviderDefinition
  testButtonDisabled?: boolean
  testLoading?: boolean
  title: string
}>(), {
  advancedLoaded: false,
  advancedLoading: false,
  balanceQueryCanRun: false,
  balanceQueryLoading: false,
  loading: false,
  testButtonDisabled: false,
  testLoading: false
})

const publicCredentialItems = computed(() => {
  const credentials = props.accountDetail?.credentials ?? {}
  const items = [
    credentialItem('expires_at', 'Token 到期时间', formatCredentialDate(credentials.expires_at)),
    credentialItem('client_id', 'Client ID', credentials.client_id),
    credentialItem('email', '邮箱', credentials.email),
    credentialItem('account_id', 'OpenAI 账户 ID', credentials.account_id),
    credentialItem('chatgpt_user_id', 'ChatGPT 用户 ID', credentials.chatgpt_user_id),
    credentialItem('plan_type', '套餐类型', credentials.plan_type),
    credentialItem('supported_endpoint_modes', '上游接口能力', accountEndpointModeText(credentials.supported_endpoint_modes, props.accountDetail ?? props.form))
  ]
  return items.filter((item): item is { key: string; label: string; value: string } => Boolean(item))
})

const sourceAccountStatusText = computed(() => {
  const detail = props.accountDetail
  const status = detail?.authorizationInstanceSourceAccountStatus
  const parts = [
    status ? statusText(status) : '-',
    detail?.authorizationInstanceSourceAccountSchedulable === false ? '已关闭调度' : ''
  ].filter(Boolean)
  return parts.join(' / ')
})

const sourceAccountExpiresAtText = computed(() => formatDateTime(props.accountDetail?.authorizationInstanceSourceAccountExpiresAt))
const readonlyModelMappings = computed(() => props.form.modelMappings ?? [])
const mappingUpstreamModelOptions = computed<SelectOption[]>(() => {
  const output: SelectOption[] = []
  const seen = new Set<string>()
  const optionProtocolsByModel = new Map(props.modelOptions.map((option) => [option.value, option.supportedApiProtocols ?? []]))
  for (const item of props.form.supportedModels) {
    const model = item.trim()
    if (!model || seen.has(model)) continue
    seen.add(model)
    output.push({ label: model, value: model, supportedApiProtocols: optionProtocolsByModel.get(model) ?? [] })
  }
  return output
})

function endpointFamilyText(value: AccountFormModel['modelMappings'][number]['sourceEndpointFamily'] | AccountFormModel['modelMappings'][number]['upstreamEndpointFamily']): string {
  if (value === 'responses') return 'Responses'
  if (value === 'messages') return 'Messages'
  if (value === 'generate_content') return 'Gemini GenerateContent'
  if (value === 'stream_generate_content') return 'Gemini StreamGenerateContent'
  return 'Chat Completions'
}
const confirmButtonProps = computed(() => ({
  ...props.okButtonProps,
  disabled: Boolean(props.okButtonProps.disabled) || props.loading || props.testLoading
}))
const advancedPending = computed(() => props.editing && !props.authorizedEditing && !props.advancedLoaded)
const shouldRenderAdvancedSections = computed(() => (props.authorizedEditing || !advancedPending.value) && advancedActiveKeys.value.includes('advanced'))
const advancedConfiguredCount = computed(() => {
  const form = props.form
  const checks = [
    form.modelMappings.length > 0,
    !endpointModesEqual(form.supportedEndpointModes, defaultAccountEndpointModes(form.providerCode, form.type, undefined, {
      provider: props.selectedProvider,
      protocolProfile: props.selectedProtocolProfile
    })),
    Boolean(form.proxyProfileId),
    Boolean(form.accountExpiresAt),
    form.availabilitySchedule.enabled,
    errorPolicyRules.value.length > 0,
    responseInspectionRules.value.length > 0,
    Boolean(form.serviceTierOverride),
    Boolean(form.reasoningEffortOverride),
    form.temporaryUnavailableContinuousProbeEnabled === false
  ]
  return checks.filter(Boolean).length
})
watch(open, (next) => {
  if (next) advancedActiveKeys.value = props.authorizedEditing ? ['advanced'] : []
})
watch(advancedActiveKeys, (keys) => {
  if (keys.includes('advanced')) emit('advanced-open')
})

function credentialItem(key: string, label: string, value: unknown): { key: string; label: string; value: string } | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text ? { key, label, value: text } : undefined
}

function formatCredentialDate(value: unknown): string | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined
  return formatDateTime(value)
}

const emit = defineEmits<{
  (event: 'advanced-open'): void
  (event: 'balance-query'): void
  (event: 'cancel'): void
  (event: 'copy-auth-url', value: string): void
  (event: 'delete-tag', tagId: string): void
  (event: 'generate-auth-url'): void
  (event: 'group-options-dropdown', open: boolean): void
  (event: 'group-options-search', value: string): void
  (event: 'ok'): void
  (event: 'open-auth-url'): void
  (event: 'select-provider', providerCode: string): void
  (event: 'select-type-choice', value: string): void
  (event: 'test'): void
}>()
</script>

<style scoped>
.form-section {
  min-width: 0;
  padding: 0;
  border-bottom: 0;
  background: transparent;
}

.readonly-config-section {
  padding: 12px;
  border: 1px solid #dbeafe;
  border-radius: 8px;
  background: #f8fbff;
}

.readonly-model-mappings {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.account-advanced-collapse {
  max-width: 100%;
  min-width: 0;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.account-form,
.account-form :deep(.ant-form-item),
.account-form :deep(.ant-form-item-control),
.account-form :deep(.ant-form-item-control-input),
.account-form :deep(.ant-form-item-control-input-content) {
  min-width: 0;
  max-width: 100%;
}

.account-form-loading {
  display: grid;
  min-height: 260px;
  place-items: center;
}

.advanced-loading {
  display: grid;
  min-height: 180px;
  place-items: center;
}

.account-advanced-collapse :deep(.ant-collapse-item) {
  min-width: 0;
  border-bottom: 0;
}

.account-advanced-collapse :deep(.ant-collapse-header) {
  align-items: center;
  padding: 14px 16px !important;
}

.account-advanced-collapse :deep(.ant-collapse-content) {
  min-width: 0;
  border-top: 1px solid #eef2f7;
}

.account-advanced-collapse :deep(.ant-collapse-content-box) {
  min-width: 0;
  padding: 16px !important;
  background: #f8fafc;
}

.advanced-header {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.advanced-header span {
  color: #0f172a;
  font-size: 15px;
  font-weight: 600;
}

.advanced-header small {
  color: #64748b;
  font-size: 12px;
}

.advanced-section-stack {
  display: grid;
  gap: 12px;
  min-width: 0;
}

.advanced-section-stack :deep(.form-section:last-child) {
  padding-bottom: 0;
  border-bottom: 0;
}

.advanced-section-stack :deep(.error-policy-collapse) {
  overflow: hidden;
  padding: 0;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.advanced-section-stack :deep(.response-policy-collapse) {
  overflow: hidden;
  padding: 0;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.advanced-section-stack :deep(.error-policy-collapse .ant-collapse-header) {
  padding: 12px 14px !important;
}

.advanced-section-stack :deep(.response-policy-collapse .ant-collapse-header) {
  padding: 12px 14px !important;
}

.advanced-section-stack :deep(.error-policy-collapse .ant-collapse-content) {
  border-top-color: #eef2f7;
}

.advanced-section-stack :deep(.response-policy-collapse .ant-collapse-content) {
  border-top-color: #eef2f7;
}

.advanced-section-stack :deep(.error-policy-collapse .ant-collapse-content-box) {
  padding: 12px 14px 14px !important;
}

.advanced-section-stack :deep(.response-policy-collapse .ant-collapse-content-box) {
  padding: 12px 14px 14px !important;
}

.advanced-section-stack :deep(.policy-title-row h4) {
  font-size: 14px;
  font-weight: 600;
}

.account-modal-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

@media (max-width: 640px) {
  .account-modal-footer {
    align-items: stretch;
    flex-direction: column;
  }

  .account-modal-footer :deep(.ant-space) {
    justify-content: flex-end;
  }
}
</style>
