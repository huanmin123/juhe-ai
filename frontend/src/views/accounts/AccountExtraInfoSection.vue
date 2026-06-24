<template>
  <section class="form-section">
    <div class="form-section-head">
      <div>
        <h4 class="section-title">
          <span>有效期</span>
          <a-tooltip title="用于表达账号套餐或授权到期时间；到期后调度会跳过该账号。">
            <QuestionCircleOutlined class="help-icon" />
          </a-tooltip>
        </h4>
      </div>
    </div>
    <a-form-item label="账户到期时间" tooltip="不填表示不按到期时间限制；填写后，超过这个时间账号将不再参与调度。">
      <a-date-picker v-model:value="form.accountExpiresAt" show-time allow-clear :disabled="readonly" style="width: 100%" />
    </a-form-item>
  </section>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'

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

.section-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.help-icon {
  color: #94a3b8;
  cursor: help;
  font-size: 14px;
}

.help-icon:hover {
  color: #1677ff;
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
