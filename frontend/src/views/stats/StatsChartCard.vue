<template>
  <a-card :title="title" class="page-card chart-card" :loading="loading">
    <a-empty v-if="!hasData" :description="emptyDescription" />
    <slot v-else />
  </a-card>
</template>

<script setup lang="ts">
defineProps<{
  title: string
  loading: boolean
  hasData: boolean
  emptyDescription: string
}>()
</script>

<style scoped>
.chart-card :deep(.ant-card-body) {
  min-height: 328px;
}
</style>
