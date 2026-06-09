<template>
  <section class="form-section credential-section" autocomplete="off">
    <div class="form-section-head">
      <div>
        <h4>{{ title }} 配置</h4>
        <p v-if="editing">API Key 会在编辑时回显；可直接查看或替换。</p>
        <p v-else>API Key 会加密保存在本地；创建后不会在页面回显。</p>
      </div>
    </div>
    <a-form-item label="API Key" :required="!editing">
      <a-input-password
        v-model:value="form.apiKey"
        autocomplete="new-password"
        data-lpignore="true"
        data-1p-ignore="true"
        data-form-type="other"
        :placeholder="editing ? '留空保留原 API Key' : '粘贴完整 API Key'"
      />
    </a-form-item>
    <a-form-item label="Base URL" required>
      <a-input
        v-model:value="form.baseUrl"
        autocomplete="off"
        data-lpignore="true"
        data-1p-ignore="true"
        data-form-type="other"
        :placeholder="baseUrlPlaceholder"
      />
    </a-form-item>
  </section>
</template>

<script setup lang="ts">
import type { AccountFormModel } from './accountFormTypes'

defineProps<{
  baseUrlPlaceholder: string
  editing: boolean
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

.credential-section {
  border-color: #dbeafe;
  background: #f8fbff;
}
</style>
