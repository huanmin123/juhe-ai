<template>
  <section class="form-section">
    <div class="form-section-head">
      <div>
        <h4>基础信息</h4>
        <p>账户主动选择归属分组；API Key 再绑定分组来统一调度该组账户。</p>
      </div>
    </div>
    <div class="form-grid">
      <a-form-item label="账户名称" :required="form.type === 'api_key' || editing">
        <a-input v-model:value="form.name" :placeholder="form.type === 'oauth' ? 'OAuth 可留空，默认使用授权信息' : '例如 openai-main'" />
      </a-form-item>
      <a-form-item label="归属分组" required>
        <a-select v-model:value="form.groupId" :options="groupOptions" placeholder="请选择同供应商分组" />
        <div class="form-help">添加账户时会根据供应商默认选择默认分组。</div>
      </a-form-item>
      <a-form-item label="账户到期时间">
        <a-date-picker v-model:value="form.accountExpiresAt" show-time allow-clear style="width: 100%" />
        <div class="form-help">可选，表示套餐/账号购买到期时间；到期后后端会自动停用账户。</div>
      </a-form-item>
    </div>
    <a-form-item label="说明">
      <a-textarea v-model:value="form.notes" :rows="2" placeholder="可填写来源、用途或额度说明" />
    </a-form-item>
  </section>
</template>

<script setup lang="ts">
import type { AccountFormModel } from './accountFormTypes'

defineProps<{
  editing: boolean
  form: AccountFormModel
  groupOptions: Array<{ label: string; value: string }>
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

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}

@media (max-width: 992px) {
  .form-grid {
    grid-template-columns: 1fr;
  }
}
</style>
