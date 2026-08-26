<template>
  <section class="hero-panel">
    <div class="brand-row">
      <img class="brand-icon" :src="appIcon" :alt="`${appName} 图标`" />
      <span class="hero-badge">{{ badge }}</span>
    </div>

    <h1>{{ title }}</h1>
    <p class="hero-subtitle">{{ subtitle }}</p>

    <div class="hero-stage" aria-hidden="true">
      <div ref="showcaseRef" class="hero-showcase">
        <div class="showcase-core-panel">
          <div class="orbital-core">
            <span class="orbit orbit-a" />
            <span class="orbit orbit-b" />
            <span class="orbit orbit-c" />
            <span
              v-for="particle in orbitParticles"
              :key="particle.key"
              :class="['particle-orbit', `particle-orbit-${particle.key}`]"
            >
              <span class="particle-dot" />
            </span>
            <span class="orbit-dot dot-a" />
            <span class="orbit-dot dot-b" />
            <span class="orbit-dot dot-c" />
            <span class="core-glow" />
            <strong>AI</strong>
          </div>
        </div>

        <div class="signal-panel">
          <div class="signal-header">
            <span />
            <strong>网关运行中</strong>
          </div>
          <div v-for="item in signalRows" :key="item.label" class="signal-row">
            <span>{{ item.label }}</span>
            <i :style="{ width: item.width }" />
          </div>
        </div>
      </div>

      <div class="capability-grid">
        <div v-for="item in capabilities" :key="item.title" class="capability-card">
          <span>{{ item.index }}</span>
          <strong>{{ item.title }}</strong>
          <p>{{ item.text }}</p>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { useLoginBrandPanelOrbitAnimations } from './useLoginBrandPanelOrbitAnimations'

defineProps<{
  appIcon: string
  appName: string
  title: string
  subtitle: string
  badge: string
}>()

const signalRows = [
  { label: '账户池', width: '82%' },
  { label: '分组路由', width: '68%' },
  { label: '审计链路', width: '76%' }
]

const capabilities = [
  { index: '01', title: '统一接入', text: '供应商、账号与密钥集中维护' },
  { index: '02', title: '智能调度', text: '按分组和状态选择可用通道' },
  { index: '03', title: '全链观测', text: '请求、用量和错误持续追踪' }
]

const orbitParticles = [
  { key: 'a' },
  { key: 'b' },
  { key: 'c' }
]

const { showcaseRef } = useLoginBrandPanelOrbitAnimations()
</script>

<style scoped src="./LoginBrandPanel.css"></style>
