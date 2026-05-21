<template>
  <a-modal v-model:open="open" :title="title" width="920px" :confirm-loading="confirmLoading" :ok-button-props="okButtonProps" @ok="$emit('ok')" @cancel="$emit('cancel')">
    <a-form layout="vertical" class="account-form">
      <a-alert v-if="editing" class="form-alert" type="info" show-icon message="编辑账户时不修改供应商和账户类型；Access/API Key 与 Refresh Token 只在这里展示和修改。" />
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
        @group-options-dropdown="$emit('group-options-dropdown', $event)"
        @group-options-search="$emit('group-options-search', $event)"
      />

      <AccountApiKeySection
        v-if="isApiKeyForm"
        :base-url-placeholder="baseUrlPlaceholder"
        :form="form"
        :title="credentialTitle"
      />

      <AccountOAuthSection
        v-else-if="isOAuthForm"
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

      <AccountStrategySection
        v-if="hasAccountType"
        :form="form"
        :is-management-view="isManagementView"
        :model-options="modelOptions"
        :models-loading="modelsLoading"
        :proxy-options="proxyOptions"
      />

      <AccountErrorPolicyCard v-if="hasAccountType" v-model:rules="errorPolicyRules" />
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import type { AccountType, OpenAIAuthURLResult, ProviderDefinition } from '@/types/domain'
import AccountApiKeySection from './AccountApiKeySection.vue'
import AccountBasicInfoSection from './AccountBasicInfoSection.vue'
import AccountErrorPolicyCard from './AccountErrorPolicyCard.vue'
import AccountFormSelector from './AccountFormSelector.vue'
import AccountOAuthSection from './AccountOAuthSection.vue'
import AccountStrategySection from './AccountStrategySection.vue'
import type { AccountErrorPolicyRuleForm } from './accountErrorPolicy'
import type { AccountFormModel } from './accountFormTypes'

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

defineProps<{
  accountTypeChoices: AccountTypeChoice[]
  authLoading: boolean
  authResult?: OpenAIAuthURLResult
  baseUrlPlaceholder: string
  confirmLoading: boolean
  credentialTitle: string
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
