<template>
  <a-modal
    v-model:open="open"
    :title="title"
    :width="editing ? 960 : 720"
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
        <div v-if="!editing" class="wizard-steps" aria-label="创建步骤">
          <div class="wizard-step" :class="{ current: wizardStep === 1, done: wizardStep > 1 }">
            <span class="wizard-step-no">{{ wizardStep > 1 ? '✓' : '1' }}</span>
            <span class="wizard-step-text">
              <span class="wizard-step-title">类型与凭证</span>
              <span class="wizard-step-sub">密钥或授权</span>
            </span>
          </div>
          <div class="wizard-step-bar" :class="{ done: wizardStep > 1 }" />
          <div class="wizard-step" :class="{ current: wizardStep === 2, done: wizardStep > 2 }">
            <span class="wizard-step-no">{{ wizardStep > 2 ? '✓' : '2' }}</span>
            <span class="wizard-step-text">
              <span class="wizard-step-title">归属与调度</span>
              <span class="wizard-step-sub">分组、标签与状态</span>
            </span>
          </div>
          <div class="wizard-step-bar" :class="{ done: wizardStep > 2 }" />
          <div class="wizard-step" :class="{ current: wizardStep === 3 }">
            <span class="wizard-step-no">3</span>
            <span class="wizard-step-text">
              <span class="wizard-step-title">高级配置</span>
              <span class="wizard-step-sub">可选 · 可跳过</span>
            </span>
          </div>
        </div>
        <div class="account-form-layout" :class="{ workbench: editing }">
          <aside v-if="editing" class="bench-nav" aria-label="配置分区">
            <div class="bench-nav-label">配置分区</div>
            <button
              v-for="section in benchSections"
              :key="section.id"
              type="button"
              class="bench-nav-item"
              :class="{ active: activeBenchId === section.id }"
              @click="navToBench(section.id)"
            >
              <span class="bench-nav-dot" :class="section.tone" />
              <span class="bench-nav-name">{{ section.name }}</span>
              <span class="bench-nav-sum">{{ section.summary }}</span>
            </button>
            <div class="bench-nav-tip">点击直达分区；滚动表单时自动高亮当前位置。</div>
          </aside>
          <div ref="formScrollEl" class="form-scroll" @scroll="syncBenchNav">
        <AccountFormSelector
          v-show="editing || wizardStep === 1"
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

        <section
          v-if="hasAccountType"
          v-show="editing || wizardStep === 2"
          id="acct-sec-basic"
          class="form-part"
        >
          <AccountBasicInfoSection
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
          @model-options-open="$emit('model-options-open', $event)"
          @model-options-search="$emit('model-options-search', $event)"
          @group-options-dropdown="$emit('group-options-dropdown', $event)"
          @group-options-search="$emit('group-options-search', $event)"
          @tag-options-dropdown="$emit('tag-options-dropdown', $event)"
        />
        </section>

        <section
          v-show="editing || wizardStep === 1"
          id="acct-sec-credential"
          class="form-part"
        >
        <AccountApiKeySection
          v-if="isApiKeyForm && !authorizedEditing"
          :api-key-runtime-details="apiKeyRuntimeDetails"
          :api-key-runtime-loading="apiKeyRuntimeLoading"
          :api-key-test-details="apiKeyTestDetails"
          :base-url-placeholder="baseUrlPlaceholder"
          :deleting-tag-id="deletingTagId"
          :editing="editing"
          :form="form"
          :model-options="modelOptions"
          :models-loading="modelsLoading"
          :model-syncing="modelSyncing"
          :protocol-code="selectedProtocolProfile?.protocolCode"
          :protocol-version="selectedProtocolProfile?.protocolVersion"
          :tag-options="tagOptions"
          :tag-options-loading="tagOptionsLoading"
          :title="credentialTitle"
          @delete-tag="$emit('delete-tag', $event)"
          @load-api-key-runtime="(force) => $emit('load-api-key-runtime', force)"
          @model-options-open="$emit('model-options-open', $event)"
          @model-options-search="$emit('model-options-search', $event)"
          @refresh-models="$emit('refresh-models')"
          @tag-options-dropdown="$emit('tag-options-dropdown', $event)"
        />

        <AccountOAuthSection
          v-else-if="isTokenCredentialForm && !authorizedEditing"
          :auth-loading="authLoading"
          :auth-result="authResult"
          :editing="editing"
          :form="form"
          :is-anthropic-o-auth="isAnthropicOAuthForm"
          :is-open-a-i="isOpenAIOAuthForm"
          :is-google-o-auth="form.type === 'google_oauth'"
          :is-management-view="isManagementView"
          :model-options="modelOptions"
          :models-loading="modelsLoading"
          :model-syncing="modelSyncing"
          :profile-default-endpoint-modes="defaultAccountEndpointModes(form.providerCode, form.type, undefined, {
            provider: selectedProvider,
            protocolProfile: selectedProtocolProfile
          })"
          :protocol-code="selectedProtocolProfile?.protocolCode"
          :protocol-version="selectedProtocolProfile?.protocolVersion"
          :title="credentialTitle"
          @copy-auth-url="$emit('copy-auth-url', $event)"
          @generate-auth-url="$emit('generate-auth-url')"
          @open-auth-url="$emit('open-auth-url')"
          @refresh-models="$emit('refresh-models')"
          @model-options-open="$emit('model-options-open', $event)"
          @model-options-search="$emit('model-options-search', $event)"
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
              @tag-options-dropdown="$emit('tag-options-dropdown', $event)"
            />
          </div>
        </section>
        </section>

        <section
          v-if="hasAccountType"
          v-show="editing || wizardStep === 3"
          id="acct-sec-advanced"
          class="form-part"
        >
        <a-collapse
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
                :mapping-current-provider-source-model-options="mappingCurrentProviderSourceModelOptions"
                :mapping-gemini-source-model-options="mappingGeminiSourceModelOptions"
                :mapping-source-model-options="mappingSourceModelOptions"
                :mapping-upstream-model-options="mappingUpstreamModelOptions"
                :proxy-options="proxyOptions"
                :proxy-options-loading="proxyOptionsLoading"
                :selected-protocol-profile="selectedProtocolProfile"
                :authorized-editing="authorizedEditing"
                @current-provider-model-options-open="$emit('model-options-open', $event)"
                @current-provider-model-options-search="$emit('model-options-search', $event)"
                @mapping-source-model-options-open="(protocol, open) => $emit('mapping-model-options-open', protocol, open)"
                @mapping-source-model-options-search="(protocol, value) => $emit('mapping-model-options-search', protocol, value)"
                @proxy-options-dropdown="$emit('proxyOptionsDropdown', $event)"
                @proxy-options-search="$emit('proxyOptionsSearch', $event)"
              />

              <section class="form-section probe-toggle-row">
                <div class="probe-toggle-label">
                  <span>持续恢复探活</span>
                  <a-tooltip title="仅影响账户进入临时不可调用后的后台恢复探测。关闭后仍在前 10 分钟按退避有限复测；最终复测仍失败才标记异常。周期健康检查、首次激活、人工测试和限流恢复不受影响。">
                    <QuestionCircleOutlined class="probe-toggle-help" />
                  </a-tooltip>
                </div>
                <a-switch
                  v-model:checked="form.temporaryUnavailableContinuousProbeEnabled"
                  :disabled="authorizedEditing"
                  checked-children="开启"
                  un-checked-children="关闭"
                />
              </section>

              <section class="form-section">
                <div class="form-section-title">账户锁死</div>
                <div v-if="lockRuntimeStateText" class="lock-runtime-state">
                  <span>当前状态：{{ lockRuntimeStateText }}</span>
                  <small>锁死启停请在列表操作菜单执行。</small>
                </div>
                <div class="lock-config-fields">
                  <a-form-item label="死期（秒）">
                    <a-input-number v-model:value="form.lockDeathTimeoutSeconds" :min="30" :max="3600" :precision="0" />
                  </a-form-item>
                  <a-form-item label="重试间隔（秒）">
                    <a-input-number v-model:value="form.lockRetryIntervalSeconds" :min="5" :max="30" :precision="0" />
                  </a-form-item>
                </div>
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
                v-model:error-handling-rule-overrides="form.errorHandlingRuleOverrides"
                :inherited-error-policy-rules="inheritedErrorPolicyRules"
                :readonly="authorizedEditing"
              />

              <AccountResponseInspectionPolicyCard
                v-model:rules="responseInspectionRules"
                :readonly="authorizedEditing"
              />

            </div>
          </a-collapse-panel>
        </a-collapse>
        </section>
          </div>
        </div>
      </template>
    </a-form>

    <template #footer>
      <div class="account-modal-footer">
        <a-button v-if="!oauthCreateTestHidden" :disabled="testButtonDisabled" :loading="testLoading" @click="$emit('test')">测试</a-button>
        <template v-if="!editing">
          <a-button v-if="wizardStep > 1" @click="wizardStep -= 1">上一步</a-button>
          <a-button v-if="wizardStep < 3" type="primary" @click="wizardStep += 1">下一步</a-button>
        </template>
        <a-space>
          <a-button @click="$emit('cancel')">取消</a-button>
          <a-button v-if="editing || wizardStep === 3" v-bind="confirmButtonProps" :loading="confirmLoading" @click="$emit('ok')">确定</a-button>
        </a-space>
      </div>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { computed, ref, watch } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import type { AccountAdvancedDetail, AccountApiKeyRuntimeDetail, AccountEditBasicDetail, AccountTagSummary, OAuthAuthURLResult, ProviderDefinition, ProviderModelApiProtocol, ProviderProtocolProfileDefinition } from '@/types/domain'
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
import { accountValidationErrorStep } from './accountSavePayload'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountErrorPolicyInheritedRule, AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountResponseInspectionRuleForm } from './accountResponseInspectionPolicyTypes'
import type { AccountTypeChoice } from './accountEditFormDisplay'
import type { AccountModelSelectOption } from './accountEditFormPayload'

interface SelectOption<T = string> {
  label: string
  value: T
  supportedApiProtocols?: ProviderModelApiProtocol[]
}

const open = defineModel<boolean>('open', { required: true })
const errorPolicyRules = defineModel<AccountErrorPolicyRuleForm[]>('errorPolicyRules', { required: true })
const inheritedErrorPolicyRules = defineModel<AccountErrorPolicyInheritedRule[]>('inheritedErrorPolicyRules', { default: () => [] })
const responseInspectionRules = defineModel<AccountResponseInspectionRuleForm[]>('responseInspectionRules', { required: true })
const advancedActiveKeys = ref<string[]>([])

// 双形态外壳的纯展示状态：新建=三步向导、编辑=分栏工作台。
// 只控制分区可见性与导航高亮，不参与表单数据、校验与提交逻辑。
const wizardStep = ref(1)
const formScrollEl = ref<HTMLElement | null>(null)
const activeBenchId = ref('acct-sec-basic')

const props = withDefaults(defineProps<{
  accountTypeChoices: AccountTypeChoice[]
  accountDetail?: AccountEditBasicDetail
  accountAdvancedDetail?: AccountAdvancedDetail
  apiKeyRuntimeDetails?: AccountApiKeyRuntimeDetail[]
  apiKeyRuntimeLoading?: boolean
  advancedLoaded?: boolean
  advancedLoading?: boolean
  apiKeyTestDetails?: AccountApiKeyRuntimeDetail[]
  authorizedEditing: boolean
  authLoading: boolean
  authResult?: OAuthAuthURLResult
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
  isAnthropicOAuthForm: boolean
  isOAuthForm: boolean
  isTokenCredentialForm: boolean
  isOpenAIOAuthForm: boolean
  loading?: boolean
  mappingAnthropicSourceModelOptions: SelectOption[]
  mappingCurrentProviderSourceModelOptions: SelectOption[]
  mappingGeminiSourceModelOptions: SelectOption[]
  mappingSourceModelOptions: SelectOption[]
  modelOptions: AccountModelSelectOption[]
  modelsLoading: boolean
  modelSyncing?: boolean
  okButtonProps: Record<string, unknown>
  providers: ProviderDefinition[]
  proxyOptions: SelectOption[]
  proxyOptionsLoading?: boolean
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
  selectedProvider?: ProviderDefinition
  testButtonDisabled?: boolean
  testLoading?: boolean
  title: string
  validationFailure?: { message: string; seq: number }
}>(), {
  advancedLoaded: false,
  advancedLoading: false,
  apiKeyRuntimeLoading: false,
  balanceQueryCanRun: false,
  balanceQueryLoading: false,
  loading: false,
  testButtonDisabled: false,
  testLoading: false
})

// OAuth 账户新建态没有可测凭据（授权码/token 一次性，落库后才能测）：
// 隐藏底部测试按钮，编辑态（accountDetail 已加载）保留。
const oauthCreateTestHidden = computed(() => props.isOAuthForm && !props.accountDetail?.id)
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

function authorizedSourceAccountDetail(): AccountAdvancedDetail | undefined {
  const detail = props.accountAdvancedDetail
  return detail?.accessType === 'authorized' ? detail : undefined
}

// 锁死运行态只读展示：来自高级详情投影，不进入任何保存 payload。
const lockRuntimeStateText = computed(() => {
  const lockState = props.accountAdvancedDetail?.lockState
  if (lockState === 'LOCKED_IDLE') return '已锁死（等待真实流量）'
  if (lockState === 'ENGAGED') return '锁死坚持中（失败窗口内不切换账户）'
  if (lockState === 'DEAD_CONFIRMED') return '锁死待恢复（判死结算后等待探针恢复）'
  return undefined
})

const sourceAccountStatusText = computed(() => {
  const detail = authorizedSourceAccountDetail()
  const status = detail?.authorizationInstanceSourceAccountStatus
  const parts = [
    status ? statusText(status) : '-',
    detail?.authorizationInstanceSourceAccountSchedulable === false ? '已关闭调度' : ''
  ].filter(Boolean)
  return parts.join(' / ')
})

const sourceAccountExpiresAtText = computed(() => formatDateTime(authorizedSourceAccountDetail()?.accountExpiresAt))
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
    Object.keys(form.quotaRecoveryPolicy ?? {}).length > 0,
    responseInspectionRules.value.length > 0,
    Boolean(form.serviceTierOverride),
    Boolean(form.reasoningEffortOverride),
    form.temporaryUnavailableContinuousProbeEnabled === false,
    form.lockDeathTimeoutSeconds !== 300,
    form.lockRetryIntervalSeconds !== 5
  ]
  return checks.filter(Boolean).length
})
watch(open, (next) => {
  if (next) advancedActiveKeys.value = props.authorizedEditing ? ['advanced'] : []
})
watch(advancedActiveKeys, (keys) => {
  if (keys.includes('advanced')) emit('advanced-open')
})

const benchSections = computed(() => [
  { id: 'acct-sec-basic', name: '基础信息', summary: '归属与调度', tone: 'plain' },
  { id: 'acct-sec-credential', name: '凭证与模型', summary: props.isApiKeyForm ? 'API Key' : 'OAuth', tone: props.isApiKeyForm ? 'coral' : 'ok' },
  { id: 'acct-sec-advanced', name: '高级配置', summary: advancedConfiguredCount.value > 0 ? `已配置 ${advancedConfiguredCount.value} 项` : '默认', tone: 'plain' }
])

watch(open, (next) => {
  if (next) {
    wizardStep.value = 1
    activeBenchId.value = 'acct-sec-basic'
  }
})

// 向导形态下「确定」校验失败（useAccountEditSaveFlow 上报）时，把用户带回出错字段所在步骤；
// 编辑工作台形态不适用（分区间导航已有 bench-nav）。toast 由 save 流程提示，这里只做步骤回跳。
watch(() => props.validationFailure?.seq, (seq) => {
  if (!seq || props.editing || !open.value) return
  const message = props.validationFailure?.message ?? ''
  const step = accountValidationErrorStep(message)
  if (wizardStep.value !== step) wizardStep.value = step
})

function navToBench(id: string) {
  document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

function syncBenchNav() {
  const container = formScrollEl.value
  if (!container) return
  const containerTop = container.getBoundingClientRect().top
  let current = benchSections.value[0]?.id ?? ''
  for (const section of benchSections.value) {
    const el = document.getElementById(section.id)
    if (el && el.getBoundingClientRect().top - containerTop <= 120) current = section.id
  }
  activeBenchId.value = current
}

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
  (event: 'load-api-key-runtime', force?: boolean): void
  (event: 'generate-auth-url'): void
  (event: 'group-options-dropdown', open: boolean): void
  (event: 'group-options-search', value: string): void
  (event: 'model-options-open', open: boolean): void
  (event: 'model-options-search', value: string): void
  (event: 'refresh-models'): void
  (event: 'tag-options-dropdown', open: boolean): void
  (event: 'mapping-model-options-open', protocol: 'openai' | 'anthropic' | 'gemini', open: boolean): void
  (event: 'mapping-model-options-search', protocol: 'openai' | 'anthropic' | 'gemini', value: string): void
  (event: 'proxyOptionsDropdown', open: boolean): void
  (event: 'proxyOptionsSearch', value: string): void
  (event: 'ok'): void
  (event: 'open-auth-url'): void
  (event: 'select-provider', providerCode: string): void
  (event: 'select-type-choice', value: string): void
  (event: 'test'): void
}>()
</script>

<style scoped>
.wizard-steps {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 4px 2px 18px;
  border-bottom: 1px solid var(--juhe-border);
  margin-bottom: 18px;
}

.wizard-step {
  display: flex;
  align-items: center;
  gap: 9px;
  flex: 0 0 auto;
}

.wizard-step-no {
  width: 26px;
  height: 26px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  background: var(--juhe-soft);
  border: 1px solid var(--juhe-border);
  color: var(--juhe-faint);
  font-size: 12px;
  font-weight: 700;
}

.wizard-step.current .wizard-step-no {
  background: var(--juhe-primary-grad);
  border-color: transparent;
  color: #fff;
}

.wizard-step.done .wizard-step-no {
  background: var(--juhe-accent-soft);
  border-color: transparent;
  color: var(--juhe-accent);
}

.wizard-step-text {
  display: flex;
  flex-direction: column;
  gap: 1px;
}

.wizard-step-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--juhe-muted);
}

.wizard-step.current .wizard-step-title {
  color: var(--juhe-fg);
}

.wizard-step.done .wizard-step-title {
  color: var(--juhe-accent-mid);
}

.wizard-step-sub {
  font-size: 10.5px;
  color: var(--juhe-faint);
}

.wizard-step-bar {
  flex: 1 1 0;
  height: 2px;
  margin: 0 6px;
  border-radius: 1px;
  background: var(--juhe-border);
  min-width: 24px;
}

.wizard-step-bar.done {
  background: var(--juhe-accent-mid);
}

.account-form-layout {
  display: block;
  min-width: 0;
}

.account-form-layout.workbench {
  display: flex;
  gap: 16px;
  align-items: stretch;
}

.bench-nav {
  width: 184px;
  flex: 0 0 auto;
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px 8px;
  border-radius: var(--juhe-radius);
  background: var(--juhe-rail);
  border: 1px solid var(--juhe-border);
  align-self: flex-start;
  position: sticky;
  top: 0;
}

.bench-nav-label {
  font-size: 10.5px;
  letter-spacing: 0.12em;
  font-weight: 600;
  color: var(--juhe-faint);
  padding: 2px 10px 8px;
}

.bench-nav-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border: 0;
  border-radius: var(--juhe-radius-sm);
  background: transparent;
  cursor: pointer;
  text-align: left;
  position: relative;
  color: var(--juhe-fg-soft);
}

.bench-nav-item:hover {
  background: rgba(34, 40, 43, 0.04);
}

.bench-nav-item.active {
  background: var(--juhe-accent-soft);
  color: var(--juhe-accent);
  font-weight: 600;
}

.bench-nav-item.active::before {
  content: '';
  position: absolute;
  left: 0;
  top: 24%;
  bottom: 24%;
  width: 3px;
  border-radius: 2px;
  background: linear-gradient(180deg, #22282b, #53696b);
  transform: rotate(-0.6deg);
}

.bench-nav-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex: 0 0 auto;
  background: var(--juhe-faint);
}

.bench-nav-dot.ok {
  background: var(--juhe-ok);
}

.bench-nav-dot.coral {
  background: var(--juhe-coral);
}

.bench-nav-name {
  font-size: 12.5px;
  flex: 1;
}

.bench-nav-sum {
  font-size: 10px;
  color: var(--juhe-faint);
  max-width: 52px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.bench-nav-item.active .bench-nav-sum {
  color: var(--juhe-accent-mid);
}

.bench-nav-tip {
  margin-top: 6px;
  padding: 10px 10px 2px;
  border-top: 1px dashed var(--juhe-border);
  font-size: 10.5px;
  color: var(--juhe-faint);
  line-height: 1.6;
}

.form-scroll {
  flex: 1 1 auto;
  min-width: 0;
  min-height: 0;
  max-height: calc(100vh - 340px);
  overflow-y: auto;
  padding-right: 4px;
}

.form-part {
  scroll-margin-top: 8px;
}

.form-section {
  min-width: 0;
  padding: 0;
  border-bottom: 0;
  background: transparent;
}

.probe-toggle-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  min-height: 32px;
}

.probe-toggle-label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  color: var(--juhe-fg-soft);
  font-weight: 500;
}

.probe-toggle-help {
  color: var(--juhe-muted);
  cursor: help;
}

.readonly-config-section {
  padding: 12px;
  border: 1px solid var(--juhe-border);
  border-radius: var(--juhe-radius-sm);
  background: var(--juhe-soft);
}

.readonly-model-mappings {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.account-advanced-collapse {
  max-width: 100%;
  min-width: 0;
  border: 1px solid var(--juhe-border);
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

.lock-runtime-state {
  display: flex;
  flex-direction: column;
  gap: 2px;
  margin-bottom: 12px;
  padding: 8px 10px;
  border: 1px solid var(--juhe-border);
  border-radius: 6px;
  background: var(--juhe-soft);
  color: var(--juhe-fg-soft);
  font-size: 12px;
}

.lock-runtime-state small {
  color: #64748b;
  font-size: 12px;
}

.lock-config-fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

@media (max-width: 640px) {
  .lock-config-fields {
    grid-template-columns: 1fr;
  }
}

.advanced-section-stack :deep(.form-section:last-child) {
  padding-bottom: 0;
  border-bottom: 0;
}

.advanced-section-stack :deep(.error-policy-collapse) {
  overflow: hidden;
  padding: 0;
  border: 1px solid var(--juhe-border);
  border-radius: 8px;
  background: #fff;
}

.advanced-section-stack :deep(.response-policy-collapse) {
  overflow: hidden;
  padding: 0;
  border: 1px solid var(--juhe-border);
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

  /* 新建向导：只保留「序号 + 主标题」，三步均分一行，连接条与副标题隐藏 */
  .wizard-steps {
    gap: 6px;
    padding-bottom: 12px;
    margin-bottom: 14px;
  }

  .wizard-step {
    flex: 1 1 0;
    min-width: 0;
    gap: 6px;
  }

  .wizard-step-text {
    min-width: 0;
  }

  .wizard-step-title {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .wizard-step-sub,
  .wizard-step-bar {
    display: none;
  }

  /* 编辑工作台：折叠为纵向，分区导航变横向条，右侧表单回到全宽 */
  .account-form-layout.workbench {
    flex-direction: column;
    gap: 12px;
  }

  .bench-nav {
    width: 100%;
    flex-direction: row;
    align-items: center;
    gap: 6px;
    padding: 8px;
    position: static;
  }

  .bench-nav-label,
  .bench-nav-sum,
  .bench-nav-tip {
    display: none;
  }

  .bench-nav-item {
    flex: 1 1 0;
    min-width: 0;
    justify-content: center;
    padding: 7px 8px;
  }

  .bench-nav-name {
    flex: 0 1 auto;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  .bench-nav-item.active::before {
    left: 8%;
    right: 8%;
    top: auto;
    bottom: 0;
    width: auto;
    height: 2px;
    transform: none;
  }

  /* 窄屏撤销内层滚动窗口，由 modal-body 统一滚动，避免双层滚动 */
  .form-scroll {
    max-height: none;
    overflow: visible;
    padding-right: 0;
  }
}
</style>
