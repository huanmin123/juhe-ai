<template>
  <section class="form-section">
    <a-form-item label="账户到期时间" tooltip="不填表示不按到期时间限制；填写后，超过这个时间账号将不再参与调度。">
      <a-date-picker v-model:value="form.accountExpiresAt" show-time allow-clear :disabled="readonly" style="width: 100%" />
    </a-form-item>
  </section>
</template>

<script setup lang="ts">
import type { AccountFormModel } from './accountFormTypes'

defineProps<{
  form: AccountFormModel
  readonly: boolean
}>()
</script>

<style scoped>
.form-section {
  padding: 0 0 16px;
  border-bottom: 1px solid #eef2f7;
  background: transparent;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}
</style>
