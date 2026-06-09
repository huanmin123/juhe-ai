<template>
  <section class="form-section">
    <div class="form-grid">
      <a-form-item label="账户名称" :required="form.type === 'api_key' || editing">
        <a-input
          v-model:value="form.name"
          autocomplete="off"
          data-lpignore="true"
          data-1p-ignore="true"
          data-form-type="other"
          :disabled="authorizedEditing"
          :placeholder="form.type === 'oauth' ? 'OAuth 可留空，默认使用授权信息' : '例如 openai-main'"
        />
      </a-form-item>
      <a-form-item label="加入分组" required>
        <div @pointerdown.capture="markGroupDropdownRequested" @keydown.capture="markGroupDropdownRequested">
          <GroupSelect
            v-model:value="form.groupId"
            v-model:selected-group="form.group"
            :filter-option="false"
            :open="groupDropdownOpen"
            :loading="groupOptionsLoading"
            :options="groupOptions"
            placeholder="输入分组名称"
            @dropdown-visible-change="handleGroupDropdownVisibleChange"
            @search="$emit('group-options-search', $event)"
          />
        </div>
      </a-form-item>
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import GroupSelect from '@/components/GroupSelect.vue'
import type { AccountFormModel } from './accountFormTypes'

const props = defineProps<{
  authorizedEditing: boolean
  editing: boolean
  form: AccountFormModel
  groupOptionsLoading: boolean
  groupOptions: Array<{ label: string; value: string }>
}>()

const emit = defineEmits<{
  (event: 'group-options-dropdown', open: boolean): void
  (event: 'group-options-search', value: string): void
}>()

const groupDropdownOpen = ref(false)
const groupDropdownRequested = ref(false)

watch(
  () => props.form.providerCode,
  () => {
    groupDropdownOpen.value = false
    groupDropdownRequested.value = false
  }
)

function markGroupDropdownRequested(): void {
  groupDropdownRequested.value = true
}

function handleGroupDropdownVisibleChange(open: boolean): void {
  if (open && !groupDropdownRequested.value) {
    groupDropdownOpen.value = false
    return
  }
  groupDropdownOpen.value = open
  if (!open) groupDropdownRequested.value = false
  emit('group-options-dropdown', open)
}
</script>

<style scoped>
.form-section {
  padding: 2px 0 4px;
  border-bottom: 1px solid #eef2f7;
  background: transparent;
}

.form-section-head {
  margin-bottom: 10px;
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

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 18px;
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
