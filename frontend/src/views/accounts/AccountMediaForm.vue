<template>
  <div class="media-form">
    <!-- 段1 凭据（D7 清单表驱动）：供应商/类型按域矩阵过滤，凭据字段按 档案×域 动态。 -->
    <section class="media-form-section">
      <div v-show="!providerPickerExpanded" class="media-section-label">
        <span class="media-section-name">凭据</span>
        <span class="media-section-hint">必填 · 按供应商与能力域动态</span>
        <span class="media-section-line" />
      </div>
      <AccountFormSelector
        :account-counts="accountCounts"
        :account-type="form.type"
        :account-type-choices="domainAccountTypeChoices"
        :cloning="cloning"
        :editing="editing"
        :open="open"
        :provider-code="form.providerCode"
        :providers="domainProviders"
        :selected-protocol-profile="selectedProtocolProfile"
        :selected-provider="selectedProvider"
        @picker-expanded-change="handlePickerExpandedChange"
        @select-provider="$emit('select-provider', $event)"
        @select-type-choice="$emit('select-type-choice', $event)"
      />

      <!-- 阶段二（已选供应商配置）：卡片墙展开期间整块隐藏，先选供应商再进配置。 -->
      <div v-show="!providerPickerExpanded">
      <AccountApiKeySection
        v-if="form.type === 'api_key' && !authorizedEditing"
        :base-url-placeholder="baseUrlPlaceholder"
        :editing="editing"
        :form="form"
        :model-options="[]"
        :models-loading="false"
        :title="credentialTitle"
        variant="media"
      />

      <AccountOAuthSection
        v-else-if="isTokenCredentialForm && !authorizedEditing"
        :auth-loading="authLoading"
        :auth-result="authResult"
        :capability-domain="capabilityDomain"
        :editing="editing"
        :form="form"
        :is-anthropic-o-auth="false"
        :is-open-a-i="false"
        :is-google-o-auth="form.type === 'google_oauth'"
        :is-management-view="isManagementView"
        :model-options="domainModelOptions"
        :models-loading="modelsLoading"
        :models-truncated="modelsTruncated"
        :model-syncing="modelSyncing"
        :profile-default-endpoint-modes="[]"
        :protocol-code="selectedProtocolProfile?.protocolCode"
        :protocol-version="selectedProtocolProfile?.protocolVersion"
        :title="credentialTitle"
        @copy-auth-url="$emit('copy-auth-url', $event)"
        @generate-auth-url="$emit('generate-auth-url')"
        @model-options-open="$emit('model-options-open', $event)"
        @model-options-search="$emit('model-options-search', $event)"
        @open-auth-url="$emit('open-auth-url')"
        @refresh-models="$emit('refresh-models')"
      />

      <section v-else-if="authorizedEditing" class="form-section readonly-config-section">
        <a-descriptions bordered size="small" :column="2">
          <a-descriptions-item label="Base URL" :span="2">{{ form.baseUrl || '-' }}</a-descriptions-item>
          <a-descriptions-item label="支持模型" :span="2">
            <a-space v-if="form.supportedModels.length" wrap>
              <a-tag v-for="model in form.supportedModels" :key="model" class="mono-cell">{{ model }}</a-tag>
            </a-space>
            <span v-else>-</span>
          </a-descriptions-item>
        </a-descriptions>
      </section>

      <template v-if="isVolcengineAudio && !authorizedEditing">
        <a-form-item required class="media-speech-field">
          <template #label>
            <div class="media-speech-label">
              <span>语音应用 App ID</span>
              <a-tooltip title="豆包 TTS（openspeech.bytedance.com）的语音应用双值凭据；出站请求不经账户 Base URL，缺一即显式失败。">
                <QuestionCircleOutlined class="media-speech-help" />
              </a-tooltip>
            </div>
          </template>
          <a-input v-model:value="form.speechAppId" autocomplete="off" placeholder="火山语音应用 App ID" />
        </a-form-item>
        <a-form-item required label="语音应用 Access Token">
          <a-input-password v-model:value="form.speechToken" autocomplete="new-password" placeholder="火山语音应用 Access Token" />
        </a-form-item>
      </template>
      </div>
    </section>

    <!-- 段2 模型与映射：支持模型一律必选（D7）；hybrid 视频/音频域保留域内映射编辑。 -->
    <section v-show="!providerPickerExpanded" class="media-form-section">
      <div class="media-section-label">
        <span class="media-section-name">模型{{ isHybridMappingDomain ? '与映射' : '' }}</span>
        <span class="media-section-hint">{{ domainModelSectionHint }}</span>
        <span class="media-section-line" />
      </div>
      <a-form-item v-if="form.type !== 'google_oauth'" required>
        <template #label>
          <div class="media-speech-label">
            <span>支持模型</span>
            <a-tooltip title="声明这个账户实际支持的上游模型；媒体域账户必须至少选择一个模型。">
              <QuestionCircleOutlined class="media-speech-help" />
            </a-tooltip>
          </div>
        </template>
        <div class="supported-models-control">
          <a-select
            v-model:value="form.supportedModels"
            mode="multiple"
            :disabled="authorizedEditing"
            :loading="modelsLoading"
            option-filter-prop="label"
            placeholder="选择这个账户支持的模型"
            :options="domainModelOptions"
            show-search
            @dropdown-visible-change="$emit('model-options-open', $event)"
            @search="$emit('model-options-search', $event)"
          >
            <template v-if="modelsTruncated" #dropdownRender="{ menuNode }">
              <ModelOptionsTruncatedHint :menu-node="menuNode" />
            </template>
          </a-select>
          <a-tooltip title="从上游同步可新增模型">
            <a-button
              class="supported-models-refresh-button"
              size="small"
              type="text"
              aria-label="从上游同步可新增模型"
              :disabled="authorizedEditing"
              :loading="modelSyncing"
              @click.stop="$emit('refresh-models')"
            >
              <template #icon><SyncOutlined /></template>
            </a-button>
          </a-tooltip>
        </div>
        <div v-if="domainModelAvailabilityHint" class="media-domain-model-empty-hint">{{ domainModelAvailabilityHint }}</div>
      </a-form-item>

      <!-- v8-B A组①：上游接口能力域内勾选（与 chat AccountStrategySection 同一底层
           逻辑：allowedEndpointModesFor 域允许集渲染，声明集必须 ⊆ 域允许集）。
           OAuth（gemini native google_oauth）按来源能力只读，与 chat OAuth 表单同规。 -->
      <a-form-item v-if="form.type" class="media-capability-field">
        <template #label>
          <div class="media-speech-label">
            <span>上游接口能力</span>
            <a-tooltip title="声明这个账号真实上游支持的接口形态；未勾选的上游请求不会进入该账号候选。默认为能力域默认集，可在域允许集内收窄。">
              <QuestionCircleOutlined class="media-speech-help" />
            </a-tooltip>
          </div>
        </template>
        <div class="media-capability-grid">
          <a-checkbox
            v-for="option in mediaEndpointModeOptions"
            :key="option.value"
            :checked="form.supportedEndpointModes.includes(option.value)"
            :disabled="capabilityModesDisabled"
            @change="setMediaModeEnabled(option.value, $event.target.checked)"
          >
            {{ option.label }}
          </a-checkbox>
        </div>
      </a-form-item>

      <a-form-item v-if="isHybridMappingDomain" class="media-mapping-field">
        <template #label>
          <div class="media-speech-label">
            <span>域内模型映射</span>
            <a-tooltip :title="mediaMappingTooltip">
              <QuestionCircleOutlined class="media-speech-help" />
            </a-tooltip>
          </div>
        </template>
        <div v-if="form.modelMappings.length" class="media-mapping-list">
          <div v-for="(mapping, index) in form.modelMappings" :key="index" class="media-mapping-row">
            <a-select
              v-model:value="mapping.sourceModel"
              allow-clear
              :disabled="authorizedEditing"
              option-filter-prop="label"
              :options="mediaMappingSourceModelOptions"
              placeholder="客户端模型"
              show-search
              @dropdown-visible-change="emitMappingSourceOptionsOpen($event)"
              @search="emitMappingSourceOptionsSearch($event)"
            />
            <SwapRightOutlined class="media-mapping-arrow" />
            <a-select
              v-model:value="mapping.upstreamModel"
              allow-clear
              :disabled="authorizedEditing"
              option-filter-prop="label"
              :options="mediaMappingUpstreamModelOptions"
              placeholder="上游模型"
              show-search
            />
            <div class="media-mapping-actions">
              <a-switch v-model:checked="mapping.enabled" :disabled="authorizedEditing" />
              <a-tooltip title="删除映射">
                <a-button type="text" danger :disabled="authorizedEditing" @click="removeMediaMapping(index)">
                  <template #icon><DeleteOutlined /></template>
                </a-button>
              </a-tooltip>
            </div>
          </div>
        </div>
        <a-button v-if="!authorizedEditing" block type="dashed" @click="addMediaMapping">
          <template #icon><PlusOutlined /></template>
          新增映射
        </a-button>
      </a-form-item>
    </section>

    <!-- 段3 调度与运维：并发 + 状态（新建默认 active）+ 代理 + 标签/说明 + 加入分组。 -->
    <section v-show="!providerPickerExpanded" class="media-form-section">
      <div class="media-section-label">
        <span class="media-section-name">调度与运维</span>
        <span class="media-section-hint">并发 · 优先级 · 代理 · 归属 · 高级配置</span>
        <span class="media-section-line" />
      </div>
      <div class="media-dispatch-grid">
        <a-form-item label="并发上限" tooltip="这个账号同一时间最多承接多少个请求。达到上限后，调度会等待或尝试其他可用账号。">
          <a-input-number v-model:value="form.concurrencyLimit" :disabled="authorizedEditing" :min="1" style="width: 100%" />
        </a-form-item>
        <!-- v8-B A组②：派发优先级分层与特权（超级优先/降级备用）与 chat 调度字段同源。 -->
        <a-form-item label="优先级" tooltip="分组内账号排序使用小值优先；0 会排在 1 前面。">
          <a-input-number v-model:value="form.priority" :disabled="authorizedEditing" :min="0" style="width: 100%" />
        </a-form-item>
        <a-form-item label="特权" tooltip="同族改名映射与直连同层参与调度排序；跨协议转换映射排在直连之后。超级优先只在其模型命中层级内提权。">
          <a-select v-model:value="form.privilege" :disabled="authorizedEditing" style="width: 100%">
            <a-select-option value="normal">无</a-select-option>
            <a-select-option value="super_priority">超级优先</a-select-option>
            <a-select-option value="fallback">降级备用</a-select-option>
          </a-select>
        </a-form-item>
        <a-form-item v-if="!editing" class="media-status-field" label="状态">
          <a-radio-group v-model:value="form.status" :disabled="authorizedEditing" @change="form.statusSelectionExplicit = true">
            <a-radio value="active">可调度</a-radio>
            <a-radio value="pending_test">待检查</a-radio>
            <a-radio value="disabled">停用</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item
          class="media-proxy-field"
          :required="oauthProxyRequired"
          :label="oauthProxyRequired ? '代理（必选）' : '代理'"
          :tooltip="oauthProxyRequired
            ? '该供应商的 OAuth 账户上游必须经代理访问：不绑定代理时后端会拒绝创建、刷新和重新授权。'
            : '仅影响这个账号访问上游供应商时使用的代理；不使用代理时直接按 Base URL 访问上游。'"
        >
          <ProxySelect
            v-model:value="form.proxyProfileId"
            allow-clear
            :disabled="authorizedEditing"
            :loading="proxyOptionsLoading"
            :placeholder="oauthProxyRequired ? '必须选择出海代理' : '不使用代理'"
            :options="proxyOptions"
            @dropdown-visible-change="$emit('proxyOptionsDropdown', $event)"
            @search="$emit('proxyOptionsSearch', $event)"
          />
        </a-form-item>
      </div>
      <div class="media-attribution-grid">
        <!-- 加入分组为创建/编辑均必选（validateAccountSaveForm 无编辑态豁免），星号恒显 -->
        <a-form-item label="加入分组" required>
          <GroupSelect
            v-model:value="form.groupId"
            v-model:selected-group="form.group"
            :filter-option="false"
            :loading="groupOptionsLoading"
            :options="groupOptions"
            placeholder="输入分组名称"
            @dropdown-visible-change="$emit('group-options-dropdown', $event)"
            @search="$emit('group-options-search', $event)"
          />
        </a-form-item>
        <AccountMetaFields
          v-if="!authorizedEditing"
          :deleting-tag-id="deletingTagId"
          :form="form"
          :tag-options="tagOptions"
          :tag-options-loading="tagOptionsLoading"
          @delete-tag="$emit('delete-tag', $event)"
          @tag-options-dropdown="$emit('tag-options-dropdown', $event)"
        />
      </div>
      <!-- v8-B A组④-⑦：低频项收进「高级配置」折叠卡（复用 chat AccountAdvancedCard 壳）：
           到期时间 / 余额查询 / 时间计划 / 错误处理策略。C 组（请求覆盖、响应检查策略、
           周期探活与特供恢复、账户锁死）按契约「对话专属语义媒体不渲染」明确不出现。 -->
      <AccountAdvancedCard
        class="media-advanced-card"
        :open="advancedCardOpen"
        :count="advancedCardCount"
        :icon="SettingOutlined"
        name="高级配置"
        :summary="advancedCardSummary"
        @toggle="toggleMediaAdvancedCard"
      >
        <div v-if="editing && advancedLoading" class="media-advanced-loading">
          <a-spin tip="正在加载高级配置" />
        </div>
        <div v-else-if="advancedPending" class="media-advanced-loading">
          <a-button type="primary" @click="$emit('advanced-open')">加载高级配置</a-button>
        </div>
        <div v-else class="media-advanced-stack">
          <AccountExtraInfoSection :form="form" :readonly="authorizedEditing" />
          <AccountBalanceQuerySection
            :can-query="balanceQueryCanRun"
            :form="form"
            :query-loading="balanceQueryLoading"
            :readonly="authorizedEditing"
            @query="$emit('balance-query')"
          />
          <AccountAvailabilityScheduleSection :form="form" :readonly="authorizedEditing" />
          <AccountErrorPolicyCard
            v-model:error-handling-rule-overrides="form.errorHandlingRuleOverrides"
            v-model:rules="errorPolicyRules"
            :inherited-error-policy-rules="inheritedErrorPolicyRules"
            :readonly="authorizedEditing"
          />
        </div>
      </AccountAdvancedCard>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { DeleteOutlined, PlusOutlined, QuestionCircleOutlined, SettingOutlined, SwapRightOutlined, SyncOutlined } from '@ant-design/icons-vue'
import GroupSelect from '@/components/GroupSelect.vue'
import ProxySelect from '@/components/ProxySelect.vue'
import type { SelectOption } from '@/shared/selectLabelCache'
import {
  OPENAI_TTS_FAMILY,
  OPENAI_VIDEO_GENERATION_FAMILY,
  isHybridProviderCode,
  isVolcengineProviderCode
} from '@/shared/providerProtocol'
import type { AccountTagSummary, OAuthAuthURLResult, ProviderDefinition, ProviderProtocolProfileDefinition } from '@/types/domain'
import AccountAdvancedCard from './AccountAdvancedCard.vue'
import AccountAvailabilityScheduleSection from './AccountAvailabilityScheduleSection.vue'
import AccountApiKeySection from './AccountApiKeySection.vue'
import AccountBalanceQuerySection from './AccountBalanceQuerySection.vue'
import AccountErrorPolicyCard from './AccountErrorPolicyCard.vue'
import AccountExtraInfoSection from './AccountExtraInfoSection.vue'
import AccountFormSelector from './AccountFormSelector.vue'
import AccountMetaFields from './AccountMetaFields.vue'
import AccountOAuthSection from './AccountOAuthSection.vue'
import {
  capabilityDomainAccountTypes,
  mediaDomainModelProtocolTokens
} from './accountCapabilityDomains'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import {
  mediaAdvancedCardConfiguredCount,
  mediaAdvancedCardSummary,
  mediaCapabilityModeOptions,
  mediaDomainModelAvailabilityHint
} from './accountMediaFormBlocks'
import { applyMediaCreateDefaults } from './accountMediaCreateDefaults'
import type { AccountErrorPolicyInheritedRule, AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountModelSelectOption } from './accountEditFormPayload'
import { ModelOptionsTruncatedHint } from '@/components/ModelOptionsTruncatedHint'
import type { AccountTypeChoice } from './accountEditFormDisplay'
import {
  accountModelMappingSourceModelOptions,
  filterAccountModelMappingOptionsByEndpointFamily,
  type AccountModelMappingModelOption
} from './accountModelMappingModelOptions'
import type { AccountFormModel } from './accountFormTypes'

type MappingSourceModelOption = AccountModelMappingModelOption

interface SimpleSelectOption {
  label: string
  value: string
}

const props = withDefaults(defineProps<{
  /** 卡片墙账户计数（providerCode → 当前范围账户数），透传给第 1 段内嵌的 AccountFormSelector。 */
  accountCounts?: Record<string, number>
  accountTypeChoices: AccountTypeChoice[]
  authLoading?: boolean
  authResult?: OAuthAuthURLResult
  authorizedEditing?: boolean
  /** v8-B A组：高级详情加载状态（编辑态高级配置卡体加载/待加载分支，chat 同规）。 */
  advancedLoaded?: boolean
  advancedLoading?: boolean
  balanceQueryCanRun?: boolean
  balanceQueryLoading?: boolean
  baseUrlPlaceholder: string
  capabilityDomain: AccountCapabilityDomain
  /** 克隆态透传：内嵌 AccountFormSelector 打开即阶段二（已选摘要 + 配置），不强制展开卡片墙。 */
  cloning?: boolean
  credentialTitle: string
  deletingTagId?: string
  editing: boolean
  form: AccountFormModel
  groupOptions: SimpleSelectOption[]
  groupOptionsLoading: boolean
  isManagementView: boolean
  mappingAnthropicSourceModelOptions?: MappingSourceModelOption[]
  mappingCurrentProviderSourceModelOptions?: MappingSourceModelOption[]
  mappingGeminiSourceModelOptions?: MappingSourceModelOption[]
  mappingSourceModelOptions?: MappingSourceModelOption[]
  mappingUpstreamModelOptions?: AccountModelSelectOption[]
  modelOptions: AccountModelSelectOption[]
  modelsLoading: boolean
  /** 当前目录 scope 下候选是否完成过至少一次加载（v8-D 空态提示判据）。 */
  modelsSettled?: boolean
  /** 最近一次浏览取数命中 50 条窗口时在下拉面板底部提示可搜索。 */
  modelsTruncated?: boolean
  modelSyncing?: boolean
  open: boolean
  providers: ProviderDefinition[]
  proxyOptions: SelectOption[]
  proxyOptionsLoading?: boolean
  selectedProtocolProfile?: ProviderProtocolProfileDefinition
  selectedProvider?: ProviderDefinition
  tagOptions: AccountTagSummary[]
  tagOptionsLoading: boolean
}>(), {
  accountCounts: () => ({}),
  advancedLoaded: false,
  advancedLoading: false,
  authorizedEditing: false,
  authLoading: false,
  balanceQueryCanRun: false,
  balanceQueryLoading: false,
  cloning: false,
  mappingAnthropicSourceModelOptions: () => [],
  mappingCurrentProviderSourceModelOptions: () => [],
  mappingGeminiSourceModelOptions: () => [],
  mappingSourceModelOptions: () => [],
  mappingUpstreamModelOptions: () => [],
  modelSyncing: false,
  proxyOptionsLoading: false
})

// v8-B A组⑦：错误处理策略规则（编辑态由 useAccountEditForm 装载，卡片内编辑，
// 保存时经 buildAccountCredentials 写入凭据 error_handling_rules）。
const errorPolicyRules = defineModel<AccountErrorPolicyRuleForm[]>('errorPolicyRules', { required: true })
const inheritedErrorPolicyRules = defineModel<AccountErrorPolicyInheritedRule[]>('inheritedErrorPolicyRules', { default: () => [] })

// 供应商卡片墙展开状态（阶段一=独立供应商选择）：展开时隐藏段1凭据字段与段2/段3 配置区，
// 并上抛弹窗驱动标题与底部按钮区。
const providerPickerExpanded = ref(true)

function handlePickerExpandedChange(expanded: boolean): void {
  providerPickerExpanded.value = expanded
  emit('picker-expanded-change', expanded)
}

const emit = defineEmits<{
  (event: 'advanced-open'): void
  (event: 'balance-query'): void
  (event: 'copy-auth-url', value: string): void
  (event: 'delete-tag', tagId: string): void
  (event: 'generate-auth-url'): void
  (event: 'group-options-dropdown', open: boolean): void
  (event: 'group-options-search', value: string): void
  (event: 'mapping-source-model-options-open', protocol: 'openai' | 'anthropic' | 'gemini', open: boolean): void
  (event: 'mapping-source-model-options-search', protocol: 'openai' | 'anthropic' | 'gemini', value: string): void
  (event: 'model-options-open', open: boolean): void
  (event: 'model-options-search', value: string): void
  (event: 'open-auth-url'): void
  (event: 'picker-expanded-change', expanded: boolean): void
  (event: 'proxyOptionsDropdown', open: boolean): void
  (event: 'proxyOptionsSearch', value: string): void
  (event: 'refresh-models'): void
  (event: 'select-provider', providerCode: string): void
  (event: 'select-type-choice', value: string): void
  (event: 'tag-options-dropdown', open: boolean): void
}>()

const isTokenCredentialForm = computed(() => ['oauth', 'google_oauth'].includes(props.form.type))
const isVolcengineAudio = computed(() =>
  props.capabilityDomain === 'audio' && isVolcengineProviderCode(props.form.providerCode))

// ---- v8-B A组①：上游接口能力域内勾选（chat AccountStrategySection 同一底层逻辑） ----

const mediaEndpointModeOptions = computed(() => mediaCapabilityModeOptions({
  capabilityDomain: props.capabilityDomain,
  providerCode: props.form.providerCode,
  profile: props.selectedProtocolProfile,
  accountType: props.form.type
}))

// 与 chat 同规：授权实例（凭据面 403）与 OAuth 表单（gemini native google_oauth，
// modes 按来源能力只读）不可勾选。
const capabilityModesDisabled = computed(() => props.authorizedEditing || props.form.type === 'google_oauth')

function setMediaModeEnabled(
  mode: AccountFormModel['supportedEndpointModes'][number],
  enabled: boolean
): void {
  props.form.supportedEndpointModes = enabled
    ? [...new Set([...props.form.supportedEndpointModes, mode])]
    : props.form.supportedEndpointModes.filter((item) => item !== mode)
}

// ---- v8-B A组④-⑦：高级配置折叠卡（到期/余额/时间计划/错误策略） ----

const advancedCardOpen = ref(false)
// 编辑态高级详情未就绪时卡体显示加载/待加载分支（chat 编辑态摘要卡同规）。
const advancedPending = computed(() => props.editing && !props.authorizedEditing && !props.advancedLoaded)
const advancedCardState = computed(() => ({
  accountExpiresAt: props.form.accountExpiresAt,
  balanceQueryEnabled: props.form.balanceQueryEnabled,
  scheduleEnabled: props.form.availabilitySchedule.enabled,
  scheduleWindowCount: props.form.availabilitySchedule.windows.length,
  errorPolicyRuleCount: errorPolicyRules.value.length
}))
const advancedCardSummary = computed(() => mediaAdvancedCardSummary(advancedCardState.value))
const advancedCardCount = computed(() => mediaAdvancedCardConfiguredCount(advancedCardState.value))

function toggleMediaAdvancedCard(): void {
  advancedCardOpen.value = !advancedCardOpen.value
  // 双保险：编辑态首次展开触发高级详情加载（chat advancedActiveKeys watch 同规；
  // 已加载时 loadAdvancedAccountDetail 幂等早退）。
  if (advancedCardOpen.value) emit('advanced-open')
}

// 授权实例打开即展开（只读通览）；普通打开默认收起，摘要可见当前值。
watch(
  () => [props.open, props.authorizedEditing] as const,
  ([open, authorizedEditing]) => {
    if (!open) {
      advancedCardOpen.value = false
      return
    }
    advancedCardOpen.value = authorizedEditing
  },
  { immediate: true }
)

// ---- 段1：供应商 × 类型按域矩阵过滤 ----

// 供应商选择按域矩阵过滤：只列该域允许集非空的供应商（minimax 无 chat 域、
// deepseek/anthropic 无媒体域，据此从对应菜单的选择器中隐去）。
const domainProviders = computed(() => props.providers.filter((provider) => (
  provider.protocolProfiles.some((profile) => (
    capabilityDomainAccountTypes({
      capabilityDomain: props.capabilityDomain,
      providerCode: provider.code,
      profile
    }).length > 0
  ))
)))

const domainAccountTypeChoices = computed(() => {
  const profileId = props.selectedProtocolProfile?.id ?? ''
  const allowedTypes = capabilityDomainAccountTypes({
    capabilityDomain: props.capabilityDomain,
    providerCode: props.form.providerCode,
    profile: props.selectedProtocolProfile
  })
  return props.accountTypeChoices.filter((choice) => (
    allowedTypes.includes(choice.type)
    && (!profileId || choice.providerProtocolProfileId === profileId)
  ))
})

const oauthProxyRequired = computed(() =>
  props.form.type === 'google_oauth'
  && ['gpt', 'anthropic', 'gemini', 'xai'].includes(props.form.providerCode))

// ---- 段2：支持模型按域 modes 过滤 + hybrid 域内映射 ----

// 目录协议 token 过滤（mediaDomainModelProtocolTokens 与 model_catalog 种子同源）。
// v8-C：服务端已按账户域目录 mode 过滤（模型候选加载传 DOMAIN_MODEL_MODES[域]），
// 此处仅作域内协议 token 二次保险；不再保留「过滤结果为空回退全量」的旁路——
// 服务端 mode 过滤后空结果即该供应商在域内确无目录模型，属预期而非异常。
// custom 手加模型行未声明 mode 时会被服务端过滤排除，同属预期。
const domainModelOptions = computed(() => {
  const tokens = new Set(mediaDomainModelProtocolTokens(props.capabilityDomain))
  if (!tokens.size) return props.modelOptions
  return props.modelOptions.filter((option) =>
    option.supportedApiProtocols?.some((protocol) => tokens.has(protocol)))
})

const isHybridAccount = computed(() => isHybridProviderCode(props.form.providerCode))
const isHybridMappingDomain = computed(() =>
  isHybridAccount.value
  && !props.authorizedEditing
  && (props.capabilityDomain === 'video' || props.capabilityDomain === 'audio'))

// v8-D：选中供应商在当前域目录候选「加载后确认为空」时的引导提示。候选是
// 懒加载（弹窗打开加载一次、切供应商即清空、下拉展开/搜索再加载），未加载
// 的空不是真没有货，settled 前不提示（hybrid 映射域上游模型来自映射面，恒不提示）。
const domainModelAvailabilityHint = computed(() => mediaDomainModelAvailabilityHint({
  capabilityDomain: props.capabilityDomain,
  optionCount: domainModelOptions.value.length,
  loading: props.modelsLoading,
  settled: props.modelsSettled === true,
  hybridMappingDomain: isHybridMappingDomain.value
}))
const mediaMappingFamily = computed(() =>
  props.capabilityDomain === 'video' ? OPENAI_VIDEO_GENERATION_FAMILY : OPENAI_TTS_FAMILY)
const mediaMappingTooltip = computed(() => (
  props.capabilityDomain === 'video'
    ? '混合供应商视频域映射：客户端视频模型名 → 真实上游视频模型名（同族改写）；上游模型只能从支持模型中选择。'
    : '混合供应商音频域映射：客户端语音合成模型名 → 真实上游语音模型名（同族改写）；上游模型只能从支持模型中选择。'
))

const mediaMappingSourceModelOptions = computed(() => accountModelMappingSourceModelOptions({
  providerCode: props.form.providerCode,
  sourceEndpointFamily: mediaMappingFamily.value,
  currentProviderOptions: props.mappingCurrentProviderSourceModelOptions,
  openAIProtocolOptions: props.mappingSourceModelOptions,
  anthropicProtocolOptions: props.mappingAnthropicSourceModelOptions,
  geminiProtocolOptions: props.mappingGeminiSourceModelOptions
}))

const mediaMappingUpstreamModelOptions = computed(() =>
  filterAccountModelMappingOptionsByEndpointFamily(props.mappingUpstreamModelOptions, mediaMappingFamily.value))

function emitMappingSourceOptionsOpen(open: boolean): void {
  // 媒体映射来源恒为媒体族（openai 协议池），固定走 openai 协议选项加载。
  emit('mapping-source-model-options-open', 'openai', open)
}

function emitMappingSourceOptionsSearch(value: string): void {
  emit('mapping-source-model-options-search', 'openai', value)
}

function addMediaMapping(): void {
  props.form.modelMappings.push({
    sourceModel: '',
    sourceEndpointFamily: mediaMappingFamily.value,
    upstreamModel: '',
    upstreamEndpointFamily: mediaMappingFamily.value,
    enabled: true
  })
}

function removeMediaMapping(index: number): void {
  props.form.modelMappings.splice(index, 1)
}

const domainModelSectionHint = computed(() => {
  if (isHybridMappingDomain.value) return '支持模型必选 · 域内映射'
  if (props.capabilityDomain === 'video') return '支持模型必选 · 视频模型'
  if (props.capabilityDomain === 'audio') return '支持模型必选 · 语音模型'
  return '支持模型必选 · 图像模型'
})

// ---- 新建态域默认值（生命周期定案 v7 域默认集）：打开弹窗或切换供应商/类型时重放 ----
//
// 编辑态不重放：装载契约保证完整声明集不被裁剪，域边界以服务端为准。
watch(
  () => [props.open, props.editing, props.form.providerCode, props.form.providerProtocolProfileId, props.form.type] as const,
  ([open, editing]) => {
    if (!open || editing) return
    const profile = props.selectedProtocolProfile
    const allowedTypes = capabilityDomainAccountTypes({
      capabilityDomain: props.capabilityDomain,
      providerCode: props.form.providerCode,
      profile
    })
    if (props.form.type && !allowedTypes.includes(props.form.type)) {
      // 切换到该域不允许的类型（如 gpt 默认 oauth）：回退到域内首个类型选项，
      // 触发上层 selectAccountTypeChoice 重置表单后再收敛。
      const fallback = props.accountTypeChoices.find((choice) => (
        allowedTypes.includes(choice.type)
        && choice.providerProtocolProfileId === (profile?.id ?? choice.providerProtocolProfileId)
      ))
      if (fallback && fallback.value !== `${props.form.providerProtocolProfileId}:${props.form.type}`) {
        emit('select-type-choice', fallback.value)
        return
      }
    }
    applyMediaCreateDefaults(props.form, {
      capabilityDomain: props.capabilityDomain,
      providerCode: props.form.providerCode,
      profile,
      type: props.form.type
    })
  },
  { immediate: true }
)
</script>

<style scoped>
.media-form {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.media-form-section {
  min-width: 0;
  padding-bottom: 12px;
}

.media-section-label {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 8px 2px 12px;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.06em;
  color: var(--juhe-muted);
}

.media-section-hint {
  font-size: 11px;
  font-weight: 400;
  letter-spacing: 0;
  color: var(--juhe-faint);
}

.media-section-line {
  flex: 1 1 0;
  height: 1px;
  background: var(--juhe-border);
}

.media-speech-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.media-speech-help {
  color: rgba(0, 0, 0, 0.45);
  cursor: help;
}

.media-speech-field {
  margin-bottom: 12px;
}

.supported-models-control {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.supported-models-control :deep(.ant-select) {
  flex: 1;
  min-width: 0;
}

.supported-models-refresh-button {
  width: 24px;
  height: 24px;
  padding: 0;
  color: rgba(0, 0, 0, 0.55);
  border-radius: 6px;
}

.supported-models-refresh-button :deep(.anticon) {
  font-size: 13px;
}

.media-mapping-list {
  display: grid;
  gap: 8px;
  min-width: 0;
  margin-bottom: 8px;
}

.media-mapping-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 18px minmax(0, 1fr) auto;
  gap: 8px;
  align-items: center;
  min-width: 0;
}

.media-mapping-arrow {
  color: var(--juhe-muted);
  font-size: 16px;
}

.media-mapping-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 4px;
  min-width: max-content;
}

.media-dispatch-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(96px, 132px)) minmax(200px, 1fr);
  gap: 0 18px;
}

.media-dispatch-grid > * {
  min-width: 0;
}

/* v8-B A组①：上游接口能力域内勾选（chat endpoint-mode-grid 同款排版） */
.media-capability-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 8px 16px;
  align-items: center;
}

.media-capability-grid :deep(.ant-checkbox-wrapper) {
  min-width: 0;
  white-space: normal;
}

.media-capability-grid :deep(.ant-checkbox + span) {
  min-width: 0;
  line-height: 1.5;
}

/* v8-D：选中供应商域内无在售目录模型的引导提示 */
.media-domain-model-empty-hint {
  margin-top: 8px;
  color: var(--juhe-muted);
  font-size: 12px;
  line-height: 1.5;
}

/* v8-B A组④-⑦：高级配置折叠卡（chat bench-cards 同款壳的媒体段内间距） */
.media-advanced-card {
  margin-top: 8px;
}

.media-advanced-loading {
  display: grid;
  place-items: center;
  padding: 24px 0;
}

.media-advanced-stack {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.media-status-field :deep(.ant-radio-group) {
  display: inline-flex;
  flex-wrap: nowrap;
  gap: 12px;
}

.media-status-field :deep(.ant-radio-wrapper) {
  margin-inline-end: 0;
  white-space: nowrap;
}

.media-attribution-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 18px;
}

.media-attribution-grid :deep(.tag-form-item),
.media-attribution-grid :deep(.notes-form-item) {
  grid-column: 1 / -1;
}

.readonly-config-section {
  margin-top: 4px;
  padding: 12px;
  border: 1px solid var(--juhe-border);
  border-radius: var(--juhe-radius-sm);
  background: var(--juhe-soft);
}

.media-form :deep(.ant-form-item),
.media-form :deep(.ant-form-item-control),
.media-form :deep(.ant-form-item-control-input),
.media-form :deep(.ant-form-item-control-input-content) {
  min-width: 0;
  max-width: 100%;
}

.media-form :deep(.ant-select),
.media-form :deep(.ant-input),
.media-form :deep(.ant-input-number) {
  width: 100%;
  min-width: 0;
  max-width: 100%;
}

@media (max-width: 720px) {
  .media-mapping-row {
    grid-template-columns: minmax(0, 1fr);
  }

  .media-mapping-arrow {
    display: none;
  }

  .media-mapping-actions {
    justify-content: flex-end;
  }
}

@media (max-width: 640px) {
  .media-dispatch-grid {
    grid-template-columns: 1fr;
  }

  .media-attribution-grid {
    grid-template-columns: 1fr;
  }

  .media-status-field :deep(.ant-radio-group) {
    flex-wrap: wrap;
  }
}
</style>
