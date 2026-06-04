<template>
  <a-modal v-model:open="open" :title="title" width="920px" :confirm-loading="confirmLoading" :ok-button-props="okButtonProps" @ok="$emit('ok')" @cancel="$emit('cancel')">
    <a-form layout="vertical" class="account-form">
      <a-alert v-if="cloning" class="form-alert" type="info" show-icon :message="cloneAlertMessage" />
      <a-alert v-else-if="authorizedEditing" class="form-alert" type="info" show-icon message="授权账户的上游配置由授权方维护；你只能调整加入分组和分组内优先级。" />
      <a-alert v-else-if="editing" class="form-alert" type="info" show-icon message="编辑账户时不修改供应商和账户类型；有凭据权限的用户可查看和修改完整凭据。" />
      <a-alert v-else-if="targetSystemAccountLabel" class="form-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />

      <AccountFormSelector
        :account-type="form.type"
        :account-type-choices="accountTypeChoices"
        :editing="editing"
        :provider-code="form.providerCode"
        :providers="providers"
        :selected-provider="selectedProvider"
        @select-provider="$emit('select-provider', $event)"
        @select-type="$emit('select-type', $event)"
      />

      <AccountBasicInfoSection
        v-if="hasAccountType"
        :editing="editing"
        :form="form"
        :group-options="groupOptions"
        :group-options-loading="groupOptionsLoading"
        :authorized-editing="authorizedEditing"
        @group-options-dropdown="$emit('group-options-dropdown', $event)"
        @group-options-search="$emit('group-options-search', $event)"
      />

      <AccountApiKeySection
        v-if="isApiKeyForm && !authorizedEditing"
        :base-url-placeholder="baseUrlPlaceholder"
        :editing="editing"
        :form="form"
        :title="credentialTitle"
      />

      <AccountOAuthSection
        v-else-if="isOAuthForm && !authorizedEditing"
        :auth-loading="authLoading"
        :auth-result="authResult"
        :editing="editing"
        :form="form"
        :is-open-a-i="isOpenAIOAuthForm"
        :title="credentialTitle"
        @copy-auth-url="$emit('copy-auth-url', $event)"
        @generate-auth-url="$emit('generate-auth-url')"
        @open-auth-url="$emit('open-auth-url')"
      />

      <section v-if="authorizedEditing" class="form-section readonly-config-section">
        <div class="form-section-head">
          <div>
            <h4>上游公开配置</h4>
            <p>来源账户的敏感凭据不会展示。</p>
          </div>
        </div>
        <a-descriptions bordered size="small" :column="2">
          <a-descriptions-item label="Base URL" :span="2">{{ form.baseUrl || '-' }}</a-descriptions-item>
          <a-descriptions-item label="来源账户状态">{{ sourceAccountStatusText }}</a-descriptions-item>
          <a-descriptions-item label="来源套餐到期">{{ sourceAccountExpiresAtText }}</a-descriptions-item>
          <a-descriptions-item v-for="item in publicCredentialItems" :key="item.key" :label="item.label">
            {{ item.value }}
          </a-descriptions-item>
        </a-descriptions>
      </section>

      <AccountStrategySection
        v-if="hasAccountType"
        :form="form"
        :is-management-view="isManagementView"
        :model-options="modelOptions"
        :models-loading="modelsLoading"
        :proxy-options="proxyOptions"
        :authorized-editing="authorizedEditing"
      />

      <AccountAvailabilityScheduleSection
        v-if="hasAccountType"
        :form="form"
        :readonly="authorizedEditing"
      />

      <AccountErrorPolicyCard
        v-if="hasAccountType"
        v-model:rules="errorPolicyRules"
        :account-type="form.type"
        :base-url="form.baseUrl"
        :provider-code="form.providerCode"
        :readonly="authorizedEditing"
      />

      <AccountStreamInterceptPolicyCard
        v-if="hasAccountType"
        v-model:rules="streamInterceptRules"
        :readonly="authorizedEditing"
      />
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import { formatDateTime } from '@/shared/formatters'
import type { AccountSummary, AccountType, OpenAIAuthURLResult, ProviderDefinition } from '@/types/domain'
import AccountAvailabilityScheduleSection from './AccountAvailabilityScheduleSection.vue'
import AccountApiKeySection from './AccountApiKeySection.vue'
import AccountBasicInfoSection from './AccountBasicInfoSection.vue'
import AccountErrorPolicyCard from './AccountErrorPolicyCard.vue'
import AccountFormSelector from './AccountFormSelector.vue'
import AccountOAuthSection from './AccountOAuthSection.vue'
import AccountStrategySection from './AccountStrategySection.vue'
import AccountStreamInterceptPolicyCard from './AccountStreamInterceptPolicyCard.vue'
import { statusText } from './accountFormatters'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'
import type { AccountFormModel } from './accountFormTypes'
import type { AccountStreamInterceptRuleForm } from './accountStreamInterceptPolicyTypes'

interface AccountTypeChoice {
  value: AccountType
  label: string
  description: string
  tag: string
}

interface SelectOption<T = string> {
  label: string
  value: T
}

const open = defineModel<boolean>('open', { required: true })
const errorPolicyRules = defineModel<AccountErrorPolicyRuleForm[]>('errorPolicyRules', { required: true })
const streamInterceptRules = defineModel<AccountStreamInterceptRuleForm[]>('streamInterceptRules', { required: true })

const props = defineProps<{
  accountTypeChoices: AccountTypeChoice[]
  accountDetail?: AccountSummary
  authorizedEditing: boolean
  authLoading: boolean
  authResult?: OpenAIAuthURLResult
  baseUrlPlaceholder: string
  confirmLoading: boolean
  credentialTitle: string
  cloning: boolean
  editing: boolean
  form: AccountFormModel
  groupOptions: SelectOption[]
  groupOptionsLoading: boolean
  hasAccountType: boolean
  isApiKeyForm: boolean
  isManagementView: boolean
  isOAuthForm: boolean
  isOpenAIOAuthForm: boolean
  modelOptions: SelectOption[]
  modelsLoading: boolean
  okButtonProps: Record<string, unknown>
  providers: ProviderDefinition[]
  proxyOptions: SelectOption[]
  selectedProvider?: ProviderDefinition
  title: string
  targetSystemAccountLabel?: string
}>()

const cloneAlertMessage = computed(() => {
  const targetText = props.targetSystemAccountLabel ? `，创建目标：${props.targetSystemAccountLabel}` : ''
  return `已按源账户预填配置${targetText}；API Key、Access Token 与 Refresh Token 不会复制，请重新填写凭据。`
})

const publicCredentialItems = computed(() => {
  const credentials = props.accountDetail?.credentials ?? {}
  const items = [
    credentialItem('expires_at', 'Token 到期时间', formatCredentialDate(credentials.expires_at)),
    credentialItem('client_id', 'Client ID', credentials.client_id),
    credentialItem('email', '邮箱', credentials.email),
    credentialItem('account_id', 'OpenAI 账户 ID', credentials.account_id),
    credentialItem('chatgpt_user_id', 'ChatGPT 用户 ID', credentials.chatgpt_user_id),
    credentialItem('plan_type', '套餐类型', credentials.plan_type)
  ]
  return items.filter((item): item is { key: string; label: string; value: string } => Boolean(item))
})

const sourceAccountStatusText = computed(() => {
  const detail = props.accountDetail
  const status = detail?.authorizationInstanceSourceAccountStatus
  const parts = [
    status ? statusText(status) : '-',
    detail?.authorizationInstanceSourceAccountSchedulable === false ? '已关闭调度' : '',
    detail?.authorizationInstanceSourceAccountScheduleActive === false ? '当前计划停用' : ''
  ].filter(Boolean)
  return parts.join(' / ')
})

const sourceAccountExpiresAtText = computed(() => formatDateTime(props.accountDetail?.authorizationInstanceSourceAccountExpiresAt))

function credentialItem(key: string, label: string, value: unknown): { key: string; label: string; value: string } | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text ? { key, label, value: text } : undefined
}

function formatCredentialDate(value: unknown): string | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined
  return formatDateTime(value)
}

defineEmits<{
  (event: 'cancel'): void
  (event: 'copy-auth-url', value: string): void
  (event: 'generate-auth-url'): void
  (event: 'group-options-dropdown', open: boolean): void
  (event: 'group-options-search', value: string): void
  (event: 'ok'): void
  (event: 'open-auth-url'): void
  (event: 'select-provider', providerCode: string): void
  (event: 'select-type', type: AccountType): void
}>()
</script>

<style scoped>
.form-section {
  padding: 16px;
  border: 1px solid #e8edf5;
  border-radius: 16px;
  background: #fff;
}

.form-section-head {
  margin-bottom: 12px;
}

.form-section-head h4 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
}

.form-section-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
}

.readonly-config-section {
  border-color: #dbeafe;
  background: #f8fbff;
}
</style>
