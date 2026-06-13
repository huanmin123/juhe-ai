<template>
  <section class="form-section">
    <div class="form-section-head">
      <div>
        <h4>有效期</h4>
      </div>
    </div>
    <a-form-item label="账户到期时间">
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

.form-section-head {
  margin-bottom: 8px;
}

.form-section-head h4 {
  margin: 0;
  color: #0f172a;
  font-size: 14px;
  font-weight: 600;
}

.form-section-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}
</style>
