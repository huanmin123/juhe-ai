<template>
  <section class="form-section credential-section">
    <div class="form-section-head">
      <div>
        <h4>{{ title }} 配置</h4>
        <p>API Key 会完整保存在本地；列表不展示，编辑弹窗可通过显示按钮查看和修改。</p>
      </div>
    </div>
    <a-form-item label="API Key" required>
      <a-input-password v-model:value="form.apiKey" autocomplete="off" placeholder="粘贴完整 API Key" />
    </a-form-item>
    <a-form-item label="Base URL" required>
      <a-input v-model:value="form.baseUrl" :placeholder="baseUrlPlaceholder" />
    </a-form-item>
    <a-form-item label="OpenAI 组织">
      <a-input v-model:value="form.openaiOrganization" placeholder="可选，例如 org_xxx" />
      <div class="form-help">只在上游账号需要指定组织时填写；客户端传入的同名头不会透传。</div>
    </a-form-item>
    <a-form-item label="OpenAI 项目">
      <a-input v-model:value="form.openaiProject" placeholder="可选，例如 proj_xxx" />
    </a-form-item>
    <a-form-item label="OpenAI Beta">
      <a-input v-model:value="form.openaiBeta" placeholder="可选，例如 assistants=v2" />
    </a-form-item>
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

.form-help {
  margin-top: 6px;
  color: #64748b;
  font-size: 12px;
  line-height: 1.6;
}

.credential-section {
  border-color: #dbeafe;
  background: #f8fbff;
}
</style>
