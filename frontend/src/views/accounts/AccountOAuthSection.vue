<template>
  <section class="form-section" autocomplete="off">
    <template v-if="isGoogleOAuth && editing">
      <a-alert class="form-alert" type="info" show-icon message="Gemini Google OAuth 使用用户授权 Refresh Token 自动换取 Access Token；可留空 Refresh Token 以使用尚未过期的 Access Token。" />
      <a-form-item label="OAuth 类型"><a-segmented v-model:value="form.oauthType" :options="geminiOAuthTypeOptions" block disabled /></a-form-item>
      <a-form-item v-if="geminiSupportsTierId" label="额度层级"><a-select v-model:value="form.tierId" :options="geminiTierOptions" /></a-form-item>
      <a-form-item v-if="geminiSupportsProjectId" label="GCP Project ID"><a-input v-model:value="form.projectId" placeholder="已保存的 OAuth 运行项目" /></a-form-item>
      <a-form-item label="Access Token"><a-textarea v-model:value="form.accessToken" :rows="3" autocomplete="off" placeholder="粘贴 Google Access Token" /></a-form-item>
      <a-form-item label="Refresh Token"><a-textarea v-model:value="form.refreshToken" :rows="3" autocomplete="off" placeholder="粘贴 Google OAuth Refresh Token" /></a-form-item>
      <template v-if="geminiRequiresClientCredentials">
        <a-form-item label="Client ID"><a-input v-model:value="form.googleClientId" autocomplete="off" /></a-form-item>
        <a-form-item label="Client Secret"><a-input-password v-model:value="form.googleClientSecret" autocomplete="off" /></a-form-item>
      </template>
      <a-form-item label="Quota Project ID"><a-input v-model:value="form.googleQuotaProjectId" placeholder="可选，用于 x-goog-user-project" /></a-form-item>
    </template>

    <template v-else-if="editing">
      <a-form-item label="Access Token">
        <a-textarea
          v-model:value="form.accessToken"
          :rows="3"
          autocomplete="off"
          data-lpignore="true"
          data-1p-ignore="true"
          data-form-type="other"
          placeholder="粘贴完整 Access Token"
        />
      </a-form-item>
      <a-form-item label="Refresh Token">
        <a-textarea
          v-model:value="form.refreshToken"
          :rows="3"
          autocomplete="off"
          data-lpignore="true"
          data-1p-ignore="true"
          data-form-type="other"
          placeholder="粘贴完整 Refresh Token"
        />
      </a-form-item>
    </template>

    <template v-else-if="isGoogleOAuth">
      <AccountOAuthAuthorizePanel
        :auth-loading="authLoading"
        :auth-result="authResult"
        :form="form"
        :oauth-mode-options="oauthModeOptions"
        :manual-alert-message="geminiRequiresClientCredentials
          ? '使用自己的 Google OAuth 客户端授权；完成后粘贴完整回调 URL，或包含 code/state 的授权结果。'
          : '使用内置 Gemini CLI OAuth 客户端授权；完成后粘贴完整回调 URL，或包含 code/state 的授权结果。'"
        manual-authorize-step-text="登录 Google 并允许访问"
        :refresh-token-alert-message="geminiRequiresClientCredentials
          ? '已有 Google Refresh Token 时可直接换取 Access Token；Client ID 与 Client Secret 必须和签发该令牌的客户端一致。'
          : '已有 Gemini CLI Refresh Token 时可直接换取 Access Token。'"
        access-token-alert-message="也可以直接录入当前可用的 Google Access Token。"
        refresh-token-placeholder="粘贴 Google OAuth Refresh Token"
        access-token-placeholder="粘贴 Google Access Token"
        @copy-auth-url="$emit('copy-auth-url', $event)"
        @generate-auth-url="$emit('generate-auth-url')"
        @open-auth-url="$emit('open-auth-url')"
      >
        <template #credentials>
          <a-form-item label="OAuth 类型">
            <a-segmented v-model:value="form.oauthType" :options="geminiOAuthTypeOptions" block />
          </a-form-item>
          <a-form-item v-if="geminiSupportsTierId" label="额度层级">
            <a-select v-model:value="form.tierId" :options="geminiTierOptions" />
          </a-form-item>
          <a-form-item v-if="geminiSupportsProjectId" label="GCP Project ID" :required="geminiProjectIdRequired">
            <a-input v-model:value="form.projectId" :placeholder="geminiProjectIdPlaceholder" />
          </a-form-item>
          <template v-if="geminiRequiresClientCredentials && form.oauthMode !== 'access_token'">
            <a-form-item label="Client ID" required>
              <a-input v-model:value="form.googleClientId" autocomplete="off" placeholder="Google OAuth Client ID" />
            </a-form-item>
            <a-form-item label="Client Secret" required>
              <a-input-password v-model:value="form.googleClientSecret" autocomplete="off" placeholder="Google OAuth Client Secret" />
            </a-form-item>
          </template>
          <a-form-item label="Quota Project ID">
            <a-input v-model:value="form.googleQuotaProjectId" placeholder="可选，用于 x-goog-user-project" />
          </a-form-item>
        </template>
      </AccountOAuthAuthorizePanel>
    </template>

    <template v-else-if="isOpenAI">
      <AccountOAuthAuthorizePanel
        :auth-loading="authLoading"
        :auth-result="authResult"
        :form="form"
        :oauth-mode-options="oauthModeOptions"
        @copy-auth-url="$emit('copy-auth-url', $event)"
        @generate-auth-url="$emit('generate-auth-url')"
        @open-auth-url="$emit('open-auth-url')"
      >
        <template #credentials>
          <a-form-item v-if="form.oauthMode === 'refresh_token'" label="Client ID">
            <a-input v-model:value="form.googleClientId" autocomplete="off" placeholder="可选；留空使用内置 Codex Client ID" />
          </a-form-item>
        </template>
      </AccountOAuthAuthorizePanel>
    </template>

    <template v-else-if="isAnthropicOAuth">
      <AccountOAuthAuthorizePanel
        :auth-loading="authLoading"
        :auth-result="authResult"
        :form="form"
        :oauth-mode-options="oauthModeOptions"
        manual-alert-message="浏览器授权完成后粘贴完整回调 URL、code#state，或仅粘贴授权码。"
        manual-authorize-step-text="登录 Claude 并允许跳转"
        refresh-token-alert-message="已有 Anthropic Refresh Token 时可直接粘贴，后端会重新换取 Access Token。"
        access-token-alert-message="也可以直接录入已有的 Anthropic OAuth / Claude Code Access Token。"
        access-token-placeholder="粘贴 CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_AUTH_TOKEN"
        optional-refresh-token-placeholder="可选；如已有 Refresh Token 一并保存"
        @copy-auth-url="$emit('copy-auth-url', $event)"
        @generate-auth-url="$emit('generate-auth-url')"
        @open-auth-url="$emit('open-auth-url')"
      />
    </template>

    <template v-else-if="isGrokOAuth">
      <AccountOAuthAuthorizePanel
        :auth-loading="authLoading"
        :auth-result="authResult"
        :form="form"
        :oauth-mode-options="oauthModeOptions"
        manual-alert-message="浏览器授权完成后粘贴完整回调 URL，或包含 code/state 的授权结果；Grok OAuth 会使用本次 PKCE 会话交换令牌。"
        manual-authorize-step-text="登录 xAI 并允许访问"
        refresh-token-alert-message="已有 Grok Refresh Token 时可直接换取新的 Access Token。"
        access-token-alert-message="也可以直接录入当前可用的 Grok OAuth Access Token。"
        refresh-token-placeholder="粘贴 Grok OAuth Refresh Token"
        access-token-placeholder="粘贴 Grok OAuth Access Token"
        @copy-auth-url="$emit('copy-auth-url', $event)"
        @generate-auth-url="$emit('generate-auth-url')"
        @open-auth-url="$emit('open-auth-url')"
      />
    </template>

    <a-alert v-else class="form-alert" type="warning" show-icon message="该供应商的 OAuth 创建流程尚未开放。" />

    <a-form-item required>
      <template #label>
        <div class="supported-models-label">
          <span>支持模型</span>
          <a-tooltip title="声明这个 OAuth 账户实际支持的上游模型；账户必须至少选择一个模型，模型映射右侧只能从这里选择。">
            <QuestionCircleOutlined class="supported-models-help" />
          </a-tooltip>
          <span class="supported-models-label-spacer"></span>
          <a-tooltip title="从上游同步可新增模型">
            <a-button class="supported-models-refresh-button" size="small" type="text" :loading="modelSyncing" @click="$emit('refresh-models')">
              <template #icon><SyncOutlined /></template>
            </a-button>
          </a-tooltip>
        </div>
      </template>
      <a-select
        v-model:value="form.supportedModels"
        allow-clear
        mode="multiple"
        :loading="modelsLoading"
        option-filter-prop="label"
        placeholder="选择这个账户支持的模型"
        :options="modelOptions"
        show-search
        @dropdown-visible-change="$emit('model-options-open', $event)"
        @search="$emit('model-options-search', $event)"
      />
    </a-form-item>
    <AccountHealthCheckModelField
      :form="form"
      :model-options="modelOptions"
      :models-loading="modelsLoading"
      :protocol-code="protocolCode"
      :protocol-version="protocolVersion"
    />
  </section>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined, SyncOutlined } from '@ant-design/icons-vue'
import { computed, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { GeminiOAuthCapabilities } from '@/api/domains/geminiOAuth'
import { isXaiProviderCode } from '@/shared/providerProtocol'
import type { AccountSupportedEndpointMode, OAuthAuthURLResult } from '@/types/domain'
import AccountHealthCheckModelField from './AccountHealthCheckModelField.vue'
import AccountOAuthAuthorizePanel from './AccountOAuthAuthorizePanel.vue'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountModelSelectOption } from './accountEditFormPayload'

const props = defineProps<{
  authLoading: boolean
  authResult?: OAuthAuthURLResult
  editing: boolean
  form: AccountFormModel
  isAnthropicOAuth: boolean
  isOpenAI: boolean
  isGoogleOAuth: boolean
  isManagementView: boolean
  modelOptions: AccountModelSelectOption[]
  modelSyncing?: boolean
  modelsLoading: boolean
  profileDefaultEndpointModes: AccountSupportedEndpointMode[]
  protocolCode?: string
  protocolVersion?: string
  title: string
}>()

const oauthModeOptions = computed(() => {
  if (props.isAnthropicOAuth || props.isGoogleOAuth || isGrokOAuth.value) {
    return [
      { label: '官方 OAuth', value: 'manual' as const },
      { label: '粘贴 Refresh Token', value: 'refresh_token' as const },
      { label: '直接 Token', value: 'access_token' as const },
      ...(isGrokOAuth.value ? [{ label: 'SSO 导入', value: 'sso_cookie' as const }] : [])
    ]
  }
  return [
    { label: '手动授权', value: 'manual' as const },
    { label: '粘贴 Refresh Token', value: 'refresh_token' as const }
  ]
})

const isGrokOAuth = computed(() => isXaiProviderCode(props.form.providerCode) && props.form.type === 'oauth')
const fallbackGeminiOAuthTypes = [
  { label: 'Code Assist', oauthType: 'code_assist' as const, requiresClientCredentials: false, supportsProjectId: true, supportsTierId: true, supportedEndpointModes: ['generate_content_json', 'generate_content_sse'] as AccountSupportedEndpointMode[] },
  { label: 'Google One', oauthType: 'google_one' as const, requiresClientCredentials: false, supportsProjectId: true, supportsTierId: true, supportedEndpointModes: ['generate_content_json', 'generate_content_sse'] as AccountSupportedEndpointMode[] },
  { label: 'AI Studio', oauthType: 'ai_studio' as const, requiresClientCredentials: true, supportsProjectId: true, supportsTierId: true, supportedEndpointModes: [] as AccountSupportedEndpointMode[] }
]
const geminiCapabilities = ref<GeminiOAuthCapabilities>()
let geminiCapabilitiesRequestId = 0
const availableGeminiOAuthTypes = computed(() => geminiCapabilities.value?.oauthTypes.length
  ? geminiCapabilities.value.oauthTypes
  : fallbackGeminiOAuthTypes)
const selectedGeminiCapability = computed(() => availableGeminiOAuthTypes.value.find((item) => item.oauthType === props.form.oauthType))
const geminiRequiresClientCredentials = computed(() => selectedGeminiCapability.value?.requiresClientCredentials ?? false)
const geminiSupportsProjectId = computed(() => selectedGeminiCapability.value?.supportsProjectId ?? true)
const geminiSupportsTierId = computed(() => selectedGeminiCapability.value?.supportsTierId ?? true)
const geminiProjectIdRequired = computed(() => props.form.oauthMode === 'access_token' && props.form.oauthType !== 'ai_studio')
const geminiProjectIdPlaceholder = computed(() => geminiProjectIdRequired.value
  ? '直接 Token 必填，用于 Code Assist / Google One 请求'
  : '可选，留空由后端自动探测')
const geminiOAuthTypeOptions = computed(() => availableGeminiOAuthTypes.value.map((item) => ({
  label: item.label,
  value: item.oauthType
})))
const geminiProfileDefaultEndpointModes = computed(() => [...props.profileDefaultEndpointModes])
const geminiTierOptionsByOAuthType = {
  ai_studio: [
    { label: 'AI Studio Free', value: 'aistudio_free' },
    { label: 'AI Studio Paid', value: 'aistudio_paid' }
  ],
  code_assist: [
    { label: 'GCP Standard', value: 'gcp_standard' },
    { label: 'GCP Enterprise', value: 'gcp_enterprise' }
  ],
  google_one: [
    { label: 'Google One Free', value: 'google_one_free' },
    { label: 'Google AI Pro', value: 'google_ai_pro' },
    { label: 'Google AI Ultra', value: 'google_ai_ultra' }
  ]
}
const geminiTierOptions = computed(() => geminiTierOptionsByOAuthType[props.form.oauthType])

function applyGeminiEndpointModes() {
  if (props.editing) return
  const capabilityModes = selectedGeminiCapability.value?.supportedEndpointModes ?? []
  const endpointModes = capabilityModes.length
    ? capabilityModes
    : props.form.oauthType === 'ai_studio'
      ? geminiProfileDefaultEndpointModes.value
      : []
  if (endpointModes.length) props.form.supportedEndpointModes = [...endpointModes]
}

watch(() => props.form.oauthType, () => {
  if (!geminiTierOptions.value.some((option) => option.value === props.form.tierId)) {
    props.form.tierId = geminiTierOptions.value[0]?.value ?? ''
  }
  applyGeminiEndpointModes()
})

async function loadGeminiCapabilities() {
  const requestId = ++geminiCapabilitiesRequestId
  if (!props.isGoogleOAuth) {
    geminiCapabilities.value = undefined
    return
  }
  const providerCode = props.form.providerCode
  const providerProtocolProfileId = props.form.providerProtocolProfileId
  const managementView = props.isManagementView
  const initialOAuthType = props.form.oauthType
  try {
    const capabilities = managementView
      ? await api.geminiOAuth.capabilities()
      : await api.myGeminiOAuth.capabilities()
    if (requestId !== geminiCapabilitiesRequestId
      || !props.isGoogleOAuth
      || props.form.providerCode !== providerCode
      || props.form.providerProtocolProfileId !== providerProtocolProfileId
      || props.isManagementView !== managementView) return
    geminiCapabilities.value = capabilities
    if (!capabilities.oauthTypes.some((item) => item.oauthType === props.form.oauthType)
      || (!props.editing && props.form.oauthType === initialOAuthType)) {
      props.form.oauthType = capabilities.defaultOAuthType
    }
    applyGeminiEndpointModes()
  } catch (error) {
    if (requestId !== geminiCapabilitiesRequestId
      || props.form.providerCode !== providerCode
      || props.form.providerProtocolProfileId !== providerProtocolProfileId
      || props.isManagementView !== managementView) return
    console.error(error)
    geminiCapabilities.value = undefined
    applyGeminiEndpointModes()
  }
}

watch(
  [
    () => props.isGoogleOAuth,
    () => props.form.providerCode,
    () => props.form.providerProtocolProfileId,
    () => props.isManagementView
  ],
  () => { void loadGeminiCapabilities() },
  { immediate: true }
)

defineEmits<{
  (event: 'copy-auth-url', value: string): void
  (event: 'generate-auth-url'): void
  (event: 'open-auth-url'): void
  (event: 'model-options-open', open: boolean): void
  (event: 'model-options-search', value: string): void
  (event: 'refresh-models'): void
}>()
</script>

<style scoped>
.form-section {
  padding: 0;
  border: 0;
  background: transparent;
}

.supported-models-label {
  display: flex;
  flex: 1;
  align-items: center;
  min-width: 0;
}

.supported-models-label-spacer {
  flex: 1;
}

.supported-models-label :deep(.ant-btn) {
  flex: none;
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

.supported-models-help {
  color: rgba(0, 0, 0, 0.45);
  cursor: help;
}

:deep(.ant-form-item-label > label:has(.supported-models-label)) {
  width: 100%;
}

.oauth-actions {
  display: flex;
  width: 100%;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 12px;
  margin-bottom: 16px;
}

.oauth-mode-item {
  margin-bottom: 12px;
}

.oauth-flow-panel,
.oauth-token-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.oauth-step-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}

.oauth-step-card {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding: 12px;
  border: 1px solid #dbeafe;
  border-radius: 14px;
  background: #fff;
}

.oauth-step-card span {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  color: #1d4ed8;
  font-weight: 700;
  border-radius: 999px;
  background: #dbeafe;
}

.oauth-step-card div {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.oauth-step-card strong {
  color: #0f172a;
  font-size: 13px;
}

.oauth-step-card small {
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.oauth-url-field,
.oauth-callback-field,
.oauth-token-field {
  margin-bottom: 0;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}

.form-alert {
  border-radius: 12px;
}

@media (max-width: 992px) {
  .oauth-step-grid {
    grid-template-columns: 1fr;
  }
}
</style>
