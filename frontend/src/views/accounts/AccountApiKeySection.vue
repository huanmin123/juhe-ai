<template>
  <section class="form-section credential-section" autocomplete="off">
    <div class="form-section-head">
      <div>
        <h4>{{ title }} 配置</h4>
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
    <a-form-item label="Base URL" required extra="填写服务根地址或 /v1 版本根地址，例如 https://api.openai.com/v1；不要填写 /responses 等具体接口路径。">
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
  padding: 0;
  border: 0;
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

.credential-section {
  padding-top: 2px;
}
</style>
