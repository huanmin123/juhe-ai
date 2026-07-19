<template>
  <main class="service-recovering-page">
    <a-card class="service-recovering-card" :bordered="false">
      <a-result status="warning" title="服务正在恢复" sub-title="发布切换期间连接短暂中断，登录状态仍然保留。请稍后重新连接。">
        <template #extra>
          <a-button type="primary" :loading="retrying" @click="retry">重新连接</a-button>
        </template>
      </a-result>
    </a-card>
  </main>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

const route = useRoute()
const router = useRouter()
const retrying = ref(false)
const redirect = computed(() => {
  const target = route.query.redirect
  return typeof target === 'string' && target.startsWith('/') && !target.startsWith('//') ? target : '/'
})

async function retry(): Promise<void> {
  if (retrying.value) return
  retrying.value = true
  try {
    await router.replace(redirect.value)
  } finally {
    retrying.value = false
  }
}
</script>

<style scoped>
.service-recovering-page { min-height: 100vh; display: grid; place-items: center; padding: 24px; background: #f5f7fa; }
.service-recovering-card { width: min(520px, 100%); box-shadow: 0 16px 48px rgb(15 23 42 / 8%); }
</style>
