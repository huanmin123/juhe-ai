<template>
  <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
</template>

<script setup lang="ts">
import type { UsageRecordListItem } from '@/types/domain'

defineProps<{
  record: UsageRecordListItem
}>()
</script>
