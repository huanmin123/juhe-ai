<template>
  <a-modal
    v-model:open="open"
    :title="`重新授权 ${providerName} OAuth`"
    width="760px"
    :confirm-loading="saving"
    ok-text="更新授权"
    cancel-text="取消"
    @ok="$emit('save')"
    @cancel="$emit('cancel')"
  >
    <a-form layout="vertical" class="reauthorize-form">
      <a-alert
        v-if="account"
        class="form-alert"
        type="info"
        show-icon
        :message="`当前账户：${account.name}`"
        description="重新授权只会覆盖该账户的 OAuth Token，不会修改名称、分组、代理、并发或错误策略。"
      />

      <AccountOAuthAuthorizePanel
        :auth-loading="authLoading"
        :auth-result="authResult"
        :form="form"
        :oauth-mode-options="oauthModeOptions"
        :manual-alert-message="manualAlertMessage"
        :manual-authorize-step-text="manualAuthorizeStepText"
        :refresh-token-alert-message="refreshTokenAlertMessage"
        @copy-auth-url="$emit('copy-auth-url', $event)"
        @generate-auth-url="$emit('generate-auth-url')"
        @open-auth-url="$emit('open-auth-url')"
      >
        <template #credentials>
          <template v-if="providerKind === 'gemini'">
            <a-form-item label="OAuth 类型">
              <a-segmented v-model:value="form.oauthType" :options="geminiOAuthTypeOptions" block />
            </a-form-item>
            <a-form-item v-if="geminiSupportsTierId" label="额度层级">
              <a-select v-model:value="form.tierId" :options="geminiTierOptions" />
            </a-form-item>
            <a-form-item v-if="geminiSupportsProjectId" label="GCP Project ID">
              <a-input v-model:value="form.projectId" placeholder="可选，留空由后端自动探测" />
            </a-form-item>
            <template v-if="geminiRequiresClientCredentials && form.oauthMode === 'manual'">
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
        </template>
      </AccountOAuthAuthorizePanel>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { GeminiOAuthCapabilities } from '@/api/domains/geminiOAuth'
import type { AccountSummary, OAuthAuthURLResult } from '@/types/domain'
import AccountOAuthAuthorizePanel from './AccountOAuthAuthorizePanel.vue'
import type { AccountOAuthAuthorizeForm } from './accountFormTypes'
import { managedOAuthProviderKind } from './accountProviderCapabilities'

const open = defineModel<boolean>('open', { required: true })

const props = defineProps<{
  account?: AccountSummary
  authLoading: boolean
  authResult?: OAuthAuthURLResult
  form: AccountOAuthAuthorizeForm
  isManagementView: boolean
  saving: boolean
}>()

const providerKind = computed(() => managedOAuthProviderKind({ profile: props.account }))
const providerName = computed(() => {
  if (providerKind.value === 'anthropic') return 'Anthropic'
  if (providerKind.value === 'gemini') return 'Gemini'
  if (providerKind.value === 'grok') return 'Grok'
  return 'OpenAI'
})
const manualAuthorizeStepText = computed(() => {
  if (providerKind.value === 'anthropic') return '登录 Claude 并允许跳转'
  if (providerKind.value === 'gemini') return '登录 Google 并允许访问'
  if (providerKind.value === 'grok') return '登录 xAI 并允许访问'
  return '登录 OpenAI 并允许跳转'
})
const manualAlertMessage = computed(() => `授权完成后复制浏览器地址栏完整回调 URL，提交后会覆盖当前账户的 ${providerName.value} OAuth Token。`)
const refreshTokenAlertMessage = computed(() => `已有新的 ${providerName.value} Refresh Token 时可直接粘贴，后端会换取 Access Token 并覆盖当前账户的 OAuth Token。`)

const oauthModeOptions = computed(() => {
  return [
    { label: '官方 OAuth', value: 'manual' as const },
    { label: '粘贴 Refresh Token', value: 'refresh_token' as const }
  ]
})

const fallbackGeminiOAuthTypeOptions = [
  { label: 'Code Assist', value: 'code_assist' as const },
  { label: 'Google One', value: 'google_one' as const },
  { label: 'AI Studio', value: 'ai_studio' as const }
]
const geminiCapabilities = ref<GeminiOAuthCapabilities>()
const selectedGeminiCapability = computed(() => geminiCapabilities.value?.oauthTypes.find((item) => item.oauthType === props.form.oauthType))
const geminiOAuthTypeOptions = computed(() => geminiCapabilities.value?.oauthTypes.length
  ? geminiCapabilities.value.oauthTypes.map((item) => ({ label: item.label, value: item.oauthType }))
  : fallbackGeminiOAuthTypeOptions)
const geminiRequiresClientCredentials = computed(() => selectedGeminiCapability.value?.requiresClientCredentials ?? props.form.oauthType === 'ai_studio')
const geminiSupportsProjectId = computed(() => selectedGeminiCapability.value?.supportsProjectId ?? props.form.oauthType === 'code_assist')
const geminiSupportsTierId = computed(() => selectedGeminiCapability.value?.supportsTierId ?? true)
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

watch(() => props.form.oauthType, () => {
  if (!geminiTierOptions.value.some((option) => option.value === props.form.tierId)) {
    props.form.tierId = geminiTierOptions.value[0]?.value ?? ''
  }
})

async function loadGeminiCapabilities() {
  if (providerKind.value !== 'gemini' || geminiCapabilities.value) return
  try {
    geminiCapabilities.value = props.isManagementView
      ? await api.geminiOAuth.capabilities()
      : await api.myGeminiOAuth.capabilities()
    if (!geminiCapabilities.value.oauthTypes.some((item) => item.oauthType === props.form.oauthType)) {
      props.form.oauthType = geminiCapabilities.value.defaultOAuthType
    }
  } catch (error) {
    console.error(error)
    geminiCapabilities.value = undefined
  }
}

onMounted(() => { void loadGeminiCapabilities() })

defineEmits<{
  (event: 'cancel'): void
  (event: 'copy-auth-url', value: string): void
  (event: 'generate-auth-url'): void
  (event: 'open-auth-url'): void
  (event: 'save'): void
}>()
</script>

<style scoped>
.reauthorize-form {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.form-alert {
  border-radius: 12px;
}
</style>
