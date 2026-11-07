<template>
  <a-card class="page-card responsive-page-card">
    <!-- 代理 / 代理池 双标签页（代理池设计 §9.1）：不新增菜单，复用 /proxies 路由。 -->
    <a-tabs v-model:active-key="activeTab" class="proxies-tabs">
      <a-tab-pane key="profiles" tab="代理">
        <ProxyProfilesPanel />
      </a-tab-pane>
      <a-tab-pane key="pools" tab="代理池">
        <ProxyPoolsPanel />
      </a-tab-pane>
    </a-tabs>
  </a-card>
</template>

<script setup lang="ts">
import { ref } from 'vue'

import ProxyPoolsPanel from './ProxyPoolsPanel.vue'
import ProxyProfilesPanel from './ProxyProfilesPanel.vue'

// 页签状态仅会话内有效；/proxies 页面状态缓存（关键词/分页）由各面板自持。
const activeTab = ref('profiles')
</script>
