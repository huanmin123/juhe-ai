<template>
  <a-alert
    v-if="visible"
    class="runtime-availability-alert"
    type="warning"
    show-icon
    :message="message"
    :description="description"
  />
</template>

<script setup lang="ts">
withDefaults(defineProps<{
  visible?: boolean
  message: string
  description?: string
}>(), {
  visible: true,
  description: undefined
})
</script>

<style scoped>
.runtime-availability-alert {
  margin-bottom: 14px;
}
</style>
