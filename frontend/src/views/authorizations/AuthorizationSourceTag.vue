<template>
  <a-tag v-if="label" :color="color">{{ label }}</a-tag>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { ResourceAuthorizationSummary } from '@/types/domain'
import { granteeSourceLabel, granteeSourceTagColor } from './authorizationFormatters'

const props = defineProps<{
  authorization: ResourceAuthorizationSummary
}>()

const label = computed(() => granteeSourceLabel(props.authorization))
const color = computed(() => granteeSourceTagColor(props.authorization))
</script>
