<template>
  <a-form-item class="account-name-field" :required="required">
    <template #label>
      <span class="account-name-label">
        <span>账户名称</span>
        <span v-if="hint" class="account-name-hint">{{ hint }}</span>
      </span>
    </template>
    <a-input
      v-model:value="form.name"
      autocomplete="off"
      data-lpignore="true"
      data-1p-ignore="true"
      data-form-type="other"
      :maxlength="128"
      :placeholder="['oauth', 'google_oauth'].includes(form.type) ? '凭据账户可使用授权信息作为名称' : '例如 openai-main'"
    />
  </a-form-item>
</template>

<script setup lang="ts">
import type { AccountFormModel } from './accountFormTypes'

withDefaults(defineProps<{
  form: AccountFormModel
  hint?: string
  required?: boolean
}>(), {
  hint: '',
  required: false
})
</script>

<style scoped>
.account-name-label {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}

.account-name-hint {
  color: var(--juhe-faint);
  font-size: 11px;
  font-weight: 400;
}
</style>
