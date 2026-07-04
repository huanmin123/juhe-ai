<template>
  <RuntimeAvailabilityAlert
    :visible="runtimeLogsAlertVisible"
    message="日志运行态暂时不可观测"
    :description="runtimeLogsAlertDescription"
  />
</template>

<script setup lang="ts">
import RuntimeAvailabilityAlert from '@/components/RuntimeAvailabilityAlert.vue'

withDefaults(defineProps<{
  runtimeLogsAlertDescription?: string
  runtimeLogsAlertVisible?: boolean
}>(), {
  runtimeLogsAlertDescription: undefined,
  runtimeLogsAlertVisible: false
})
</script>
