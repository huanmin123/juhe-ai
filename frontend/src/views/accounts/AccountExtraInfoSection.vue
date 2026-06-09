<template>
  <section class="form-section">
    <div class="form-section-head">
      <div>
        <h4>其他信息</h4>
        <p>{{ readonly ? '授权账户的到期时间和备注由授权方维护。' : '这些信息不影响账户立即接入，可在需要时补充。' }}</p>
      </div>
    </div>
    <a-form-item label="账户到期时间">
      <a-date-picker v-model:value="form.accountExpiresAt" show-time allow-clear :disabled="readonly" style="width: 100%" />
      <div class="form-help">可选，表示套餐/账号购买到期时间；到期后后端会自动停用账户。</div>
    </a-form-item>
    <a-form-item label="说明">
      <a-textarea v-model:value="form.notes" :rows="2" :disabled="readonly" placeholder="可填写来源、用途或额度说明" />
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

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}
</style>
