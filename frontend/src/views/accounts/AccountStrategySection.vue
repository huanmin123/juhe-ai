<template>
  <section class="form-section">
    <div class="form-section-head">
      <div>
        <h4>请求策略</h4>
        <p>并发、优先级和代理会影响后续请求转发与账户选择。</p>
      </div>
    </div>
    <a-form-item label="支持模型">
      <a-select
        v-model:value="form.supportedModels"
        allow-clear
        mode="multiple"
        :loading="modelsLoading"
        option-filter-prop="label"
        placeholder="不限制模型"
        :options="modelOptions"
        show-search
      />
      <div class="form-help">不选择表示该账户不做模型限制。</div>
    </a-form-item>
    <div class="strategy-grid">
      <a-form-item label="并发上限">
        <a-input-number v-model:value="form.concurrencyLimit" :min="1" style="width: 100%" />
      </a-form-item>
      <a-form-item label="优先级">
        <a-input-number v-model:value="form.priority" :min="0" style="width: 100%" />
      </a-form-item>
    </div>
    <div class="form-help strategy-help">优先级数字越小越优先。</div>
    <a-form-item class="strategy-proxy-field" label="代理">
      <ProxySelect
        v-model:value="form.proxyProfileId"
        allow-clear
        placeholder="不使用代理"
        :options="proxyOptions"
      />
      <div v-if="!isManagementView" class="form-help">代理配置由管理员统一维护；这里可以选择已启用的全局代理。</div>
    </a-form-item>
  </section>
</template>

<script setup lang="ts">
import ProxySelect from '@/components/ProxySelect.vue'
import type { SelectOption } from '@/shared/selectLabelCache'
import type { AccountFormModel } from './accountFormTypes'

defineProps<{
  form: AccountFormModel
  isManagementView: boolean
  modelOptions: Array<{ label: string; value: string }>
  modelsLoading: boolean
  proxyOptions: SelectOption[]
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

.strategy-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(120px, 1fr));
  gap: 0 16px;
}

.form-help {
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}

.strategy-help {
  margin-top: -8px;
  margin-bottom: 16px;
}

.strategy-proxy-field {
  margin-bottom: 16px;
}

@media (max-width: 992px) {
  .strategy-grid {
    grid-template-columns: 1fr;
  }
}
</style>
