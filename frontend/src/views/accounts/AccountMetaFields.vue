<template>
  <a-form-item class="tag-form-item" label="账户标签">
    <AccountTagSelect
      v-model:value="form.tags"
      :deleting-tag-id="deletingTagId"
      :loading="tagOptionsLoading"
      :options="tagOptions"
      @delete="$emit('delete-tag', $event)"
    />
  </a-form-item>
  <a-form-item class="notes-form-item" label="说明">
    <a-textarea v-model:value="form.notes" :rows="2" :disabled="readonly" placeholder="可填写来源、用途或额度说明" />
  </a-form-item>
</template>

<script setup lang="ts">
import type { AccountTagSummary } from '@/types/domain'
import AccountTagSelect from './AccountTagSelect.vue'
import type { AccountFormModel } from './accountFormTypes'

defineProps<{
  deletingTagId?: string
  form: AccountFormModel
  readonly?: boolean
  tagOptions: AccountTagSummary[]
  tagOptionsLoading: boolean
}>()

defineEmits<{
  (event: 'delete-tag', tagId: string): void
}>()
</script>

<style scoped>
.tag-form-item,
.notes-form-item {
  grid-column: 1 / -1;
}
</style>
