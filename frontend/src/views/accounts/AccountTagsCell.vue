<template>
  <div v-if="tags.length" class="account-tags-cell">
    <a-tag v-for="tag in visibleTags" :key="tag.id || tag.name" color="blue">{{ tag.name }}</a-tag>
    <a-tooltip v-if="hiddenTags.length" :title="hiddenTagText">
      <a-tag>+{{ hiddenTags.length }}</a-tag>
    </a-tooltip>
  </div>
  <span v-else class="muted-cell">-</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { AccountSummary } from '@/types/domain'

const props = defineProps<{
  account: AccountSummary
}>()

const tags = computed(() => props.account.tags ?? [])
const visibleTags = computed(() => tags.value.slice(0, 3))
const hiddenTags = computed(() => tags.value.slice(3))
const hiddenTagText = computed(() => hiddenTags.value.map((tag) => tag.name).join('、'))
</script>

<style scoped>
.account-tags-cell {
  display: flex;
  max-width: 100%;
  flex-wrap: wrap;
  gap: 4px;
}

.account-tags-cell :deep(.ant-tag) {
  max-width: 100%;
  margin-inline-end: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.muted-cell {
  color: #94a3b8;
}
</style>
