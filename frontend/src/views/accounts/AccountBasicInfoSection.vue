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
          :maxlength="maxAccountNameLength"
          :placeholder="['oauth', 'google_oauth'].includes(form.type) ? '凭据账户可使用授权信息作为名称' : '例如 openai-main'"
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
      <a-form-item label="并发上限" tooltip="这个账号同一时间最多承接多少个请求。达到上限后，调度会等待或尝试其他可用账号。">
        <a-input-number v-model:value="form.concurrencyLimit" :disabled="authorizedEditing" :min="1" style="width: 100%" />
      </a-form-item>
      <a-form-item label="优先级" tooltip="分组内账号排序使用小值优先；0 会排在 1 前面。授权账号这里表示当前使用方本地分组内的调度优先级。">
        <a-input-number v-model:value="form.priority" :min="0" style="width: 100%" />
      </a-form-item>
      <AccountMetaFields
        v-if="showMetaFields"
        :deleting-tag-id="deletingTagId"
        :form="form"
        :readonly="authorizedEditing"
        :tag-options="tagOptions"
        :tag-options-loading="tagOptionsLoading"
        @delete-tag="$emit('delete-tag', $event)"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import GroupSelect from '@/components/GroupSelect.vue'
import type { AccountTagSummary } from '@/types/domain'
import type { AccountFormModel } from './accountFormTypes'
import AccountMetaFields from './AccountMetaFields.vue'

const maxAccountNameLength = 128

const props = defineProps<{
  authorizedEditing: boolean
  editing: boolean
  form: AccountFormModel
  groupOptionsLoading: boolean
  groupOptions: Array<{ label: string; value: string }>
  showMetaFields?: boolean
  tagOptionsLoading: boolean
  tagOptions: AccountTagSummary[]
  deletingTagId?: string
}>()

const emit = defineEmits<{
  (event: 'delete-tag', tagId: string): void
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
