<template>
  <RuntimeAvailabilityAlert
    :visible="runtimeLogsAlertVisible"
    message="日志运行态暂时不可观测"
    :description="runtimeLogsAlertDescription"
  />
  <RuntimeAvailabilityAlert
    :visible="queueHealthAlertVisible"
    message="后台队列存在积压或丢弃"
    :description="queueHealthAlertDescription"
  />
</template>

<script setup lang="ts">
import RuntimeAvailabilityAlert from '@/components/RuntimeAvailabilityAlert.vue'

withDefaults(defineProps<{
  queueHealthAlertDescription?: string
  queueHealthAlertVisible?: boolean
  runtimeLogsAlertDescription?: string
  runtimeLogsAlertVisible?: boolean
}>(), {
  queueHealthAlertDescription: undefined,
  queueHealthAlertVisible: false,
  runtimeLogsAlertDescription: undefined,
  runtimeLogsAlertVisible: false
})
</script>
