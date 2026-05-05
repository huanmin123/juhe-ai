<template>
  <StatusTag :color="statusTagColor(status)" :label="statusLabel(status)" />
</template>

<script setup lang="ts">
import StatusTag from '@/components/StatusTag.vue'
import type { AuthorizationStatus } from '@/types/domain'
import { statusLabel, statusTagColor } from './authorizationFormatters'

defineProps<{
  status: AuthorizationStatus
}>()
</script>
