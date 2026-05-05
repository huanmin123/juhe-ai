<template>
  <section class="form-section credential-section">
    <div class="form-section-head">
      <div>
        <h4>{{ title }} 配置</h4>
        <p>API Key 会完整保存在本地；列表不展示，编辑弹窗可直接查看和修改。</p>
      </div>
    </div>
    <a-form-item label="API Key" required>
      <a-input v-model:value="form.apiKey" placeholder="粘贴完整 API Key" />
    </a-form-item>
    <div class="form-grid">
      <a-form-item label="Base URL">
        <a-input v-model:value="form.baseUrl" :placeholder="baseUrlPlaceholder" />
      </a-form-item>
    </div>
  </section>
</template>

<script setup lang="ts">
import type { AccountFormModel } from './accountFormTypes'

defineProps<{
  baseUrlPlaceholder: string
  form: AccountFormModel
  title: string
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

.credential-section {
  border-color: #dbeafe;
  background: #f8fbff;
}

@media (max-width: 992px) {
  .form-grid {
    grid-template-columns: 1fr;
  }
}
</style>
