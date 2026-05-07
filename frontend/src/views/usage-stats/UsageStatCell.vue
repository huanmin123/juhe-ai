<template>
  <UsageSummaryTags :compact="compact" :usage="usage" />
</template>

<script setup lang="ts">
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import type { AccountUsageSummary } from '@/types/domain'

defineProps<{
  compact?: boolean
  usage?: AccountUsageSummary
}>()
</script>
