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
            <strong>Gateway Active</strong>
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
import { animate, type JSAnimation } from 'animejs'
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'

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

const showcaseRef = ref<HTMLElement | null>(null)
const orbitAnimations: JSAnimation[] = []

function stopOrbitAnimations(): void {
  while (orbitAnimations.length > 0) {
    orbitAnimations.pop()?.cancel()
  }
}

function startOrbitAnimations(): void {
  stopOrbitAnimations()

  const showcase = showcaseRef.value
  if (!showcase) return

  showcase.querySelectorAll<HTMLElement>('.particle-dot').forEach((element, index) => {
    orbitAnimations.push(animate(element, {
      scale: [0.82, 1.18, 0.82],
      opacity: [0.56, 1, 0.56],
      duration: 1800 + index * 240,
      ease: 'inOutSine',
      loop: true
    }))
  })
}

onMounted(async () => {
  await nextTick()
  startOrbitAnimations()
})

onBeforeUnmount(() => {
  stopOrbitAnimations()
})
</script>

<style scoped>
.hero-panel {
  position: relative;
  z-index: 1;
  width: 100%;
  max-width: 720px;
}

.brand-row {
  display: flex;
  align-items: center;
  gap: 14px;
}

.brand-icon {
  width: 64px;
  height: 64px;
  padding: 10px;
  background: linear-gradient(180deg, #ffffff, #dbeafe);
  border: 1px solid rgba(191, 219, 254, 0.78);
  border-radius: 8px;
  box-shadow: 0 18px 48px rgba(2, 6, 23, 0.32), 0 0 36px rgba(59, 130, 246, 0.32);
  animation: iconFloat 5s ease-in-out infinite;
  will-change: transform;
}

.hero-badge {
  display: inline-flex;
  padding: 8px 13px;
  color: #dbeafe;
  background: rgba(2, 6, 23, 0.62);
  border: 1px solid rgba(96, 165, 250, 0.28);
  border-radius: 999px;
  font-size: 12px;
  font-weight: 800;
  box-shadow: 0 0 26px rgba(37, 99, 235, 0.18);
}

.hero-panel h1 {
  max-width: 720px;
  margin: 28px 0 0;
  color: #f8fbff;
  font-size: 64px;
  font-weight: 900;
  line-height: 1.04;
  text-shadow: 0 24px 70px rgba(2, 6, 23, 0.68), 0 0 28px rgba(96, 165, 250, 0.14);
}

.hero-subtitle {
  max-width: 560px;
  margin: 22px 0 0;
  color: #d5e3f8;
  font-size: 17px;
  font-weight: 700;
  line-height: 1.8;
}

.hero-stage {
  position: relative;
  max-width: 680px;
  margin-top: 36px;
  padding: 8px 0 0;
}

.hero-stage::before {
  content: none;
}

.hero-stage::after {
  content: '';
  position: absolute;
  top: 148px;
  left: 34px;
  width: 620px;
  height: 1px;
  background: linear-gradient(90deg, rgba(96, 165, 250, 0), rgba(96, 165, 250, 0.22), rgba(96, 165, 250, 0));
  pointer-events: none;
}

.hero-showcase {
  position: relative;
  display: grid;
  grid-template-columns: minmax(0, 1fr) 224px;
  gap: 24px;
  align-items: center;
  min-height: 228px;
  padding: 0 0 14px;
  z-index: 1;
}

.hero-showcase::before {
  content: '';
  position: absolute;
  top: 18px;
  left: 50%;
  width: 1px;
  height: 176px;
  background: linear-gradient(180deg, rgba(96, 165, 250, 0), rgba(96, 165, 250, 0.18), rgba(96, 165, 250, 0));
  transform: translateX(14px);
}

.hero-showcase::after {
  content: '';
  position: absolute;
  right: 18px;
  bottom: 22px;
  left: 20px;
  height: 1px;
  background: linear-gradient(90deg, transparent, rgba(96, 165, 250, 0.18), transparent);
}

.showcase-core-panel {
  position: relative;
  display: grid;
  min-height: 228px;
  overflow: visible;
  background: transparent;
  place-items: center;
}

.showcase-core-panel::before {
  content: '';
  position: absolute;
  top: 50%;
  left: 16px;
  width: 300px;
  height: 300px;
  background: radial-gradient(circle, rgba(37, 99, 235, 0.12), transparent 56%);
  transform: translateY(-50%);
  filter: blur(8px);
}

.showcase-core-panel::after {
  content: none;
}

.orbital-core {
  position: relative;
  z-index: 1;
  width: clamp(244px, 24vw, 300px);
  height: clamp(192px, 18vw, 220px);
  display: grid;
  place-items: center;
  color: #f8fbff;
  font-size: 36px;
  font-weight: 900;
}

.orbit,
.core-glow,
.orbit-dot,
.particle-orbit,
.particle-dot {
  position: absolute;
  border-radius: 50%;
}

.orbit {
  inset: 0;
  margin: auto;
  border: 1px solid rgba(147, 197, 253, 0.3);
}

.orbit-a {
  width: 220px;
  height: 220px;
  animation: orbitSpin 18s linear infinite;
}

.orbit-b {
  width: 176px;
  height: 176px;
  border-color: rgba(56, 189, 248, 0.36);
  animation: orbitSpinReverse 14s linear infinite;
}

.orbit-c {
  width: 132px;
  height: 132px;
  border-color: rgba(129, 140, 248, 0.36);
  animation: orbitPulse 3.8s ease-in-out infinite;
}

.core-glow {
  inset: 0;
  margin: auto;
  width: 86px;
  height: 86px;
  background: radial-gradient(circle, #7dd3fc, #2563eb 56%, #1e1b4b);
  box-shadow: 0 0 48px rgba(37, 99, 235, 0.68), inset 0 0 22px rgba(255, 255, 255, 0.18);
  animation: coreBreath 3.4s ease-in-out infinite;
}

.orbital-core::before {
  content: '';
  position: absolute;
  inset: 0;
  margin: auto;
  width: 242px;
  height: 242px;
  background: conic-gradient(from 0deg, transparent, rgba(125, 211, 252, 0.42), transparent 40%, transparent 100%);
  border-radius: 50%;
  opacity: 0.56;
  animation: orbitSweep 9s linear infinite;
  will-change: transform;
}

.particle-orbit {
  inset: 0;
  margin: auto;
  will-change: transform;
}

.particle-orbit-a {
  width: 252px;
  height: 252px;
  animation: particleRotateA 10s linear infinite;
}

.particle-orbit-b {
  width: 204px;
  height: 204px;
  animation: particleRotateB 7.6s linear infinite;
}

.particle-orbit-c {
  width: 156px;
  height: 156px;
  animation: particleRotateC 5.8s linear infinite;
}

.particle-dot {
  top: 0;
  left: 50%;
  width: 7px;
  height: 7px;
  margin-left: -3.5px;
  margin-top: -3.5px;
  background: #f8fbff;
  box-shadow: 0 0 14px rgba(191, 219, 254, 0.9);
}

.particle-orbit-a .particle-dot {
  background: #93c5fd;
}

.particle-orbit-b .particle-dot {
  background: #38bdf8;
}

.particle-orbit-c .particle-dot {
  width: 6px;
  height: 6px;
  margin-left: -3px;
  background: #818cf8;
}

.orbit-dot {
  width: 9px;
  height: 9px;
  background: #bfdbfe;
  box-shadow: 0 0 18px rgba(147, 197, 253, 0.95);
}

.dot-a {
  top: 22px;
  left: 146px;
  animation: dotFloatA 5s ease-in-out infinite;
}

.dot-b {
  right: 56px;
  bottom: 44px;
  background: #818cf8;
  animation: dotFloatB 6s ease-in-out infinite;
}

.dot-c {
  left: 52px;
  bottom: 72px;
  background: #38bdf8;
  animation: dotFloatC 4.8s ease-in-out infinite;
}

.orbital-core strong {
  position: relative;
  z-index: 1;
}

.signal-panel {
  position: relative;
  z-index: 1;
  min-height: 164px;
  padding: 8px 0 8px 24px;
  background: transparent;
  border: 0;
  border-left: 1px solid rgba(96, 165, 250, 0.12);
  box-shadow: none;
  overflow: visible;
}

.signal-panel::before {
  content: '';
  position: absolute;
  inset: 10px 0 10px -1px;
  background: radial-gradient(circle at 0 50%, rgba(37, 99, 235, 0.12), transparent 42%);
  opacity: 0.78;
}

.signal-header {
  display: flex;
  align-items: center;
  gap: 9px;
  margin-bottom: 24px;
  color: #e0efff;
  font-size: 14px;
  font-weight: 900;
}

.signal-header span {
  width: 8px;
  height: 8px;
  background: #60a5fa;
  border-radius: 50%;
  box-shadow: 0 0 12px rgba(96, 165, 250, 0.62);
}

.signal-row {
  display: grid;
  gap: 10px;
  margin-top: 16px;
  padding-top: 12px;
  border-top: 1px solid rgba(96, 165, 250, 0.1);
  color: #9fb6d9;
  font-size: 12px;
  font-weight: 700;
}

.signal-row i {
  display: block;
  height: 6px;
  background: linear-gradient(90deg, #2563eb, #38bdf8);
  border-radius: 999px;
  box-shadow: 0 0 10px rgba(56, 189, 248, 0.22);
}

.capability-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0;
  margin-top: 18px;
  padding-top: 18px;
  border-top: 1px solid rgba(96, 165, 250, 0.1);
}

.capability-card {
  position: relative;
  min-height: auto;
  padding: 0 18px 0 0;
  background: transparent;
  border: 0;
  box-shadow: none;
  overflow: visible;
}

.capability-card + .capability-card {
  padding: 0 18px;
  border-left: 1px solid rgba(96, 165, 250, 0.08);
}

.capability-card:last-child {
  padding-right: 0;
}

.capability-card::before {
  content: '';
  position: absolute;
  top: -18px;
  left: 0;
  width: 40px;
  height: 1px;
  background: linear-gradient(90deg, rgba(37, 99, 235, 0.78), rgba(56, 189, 248, 0.24), transparent);
}

.capability-card span {
  color: #93c5fd;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 11px;
  font-weight: 800;
}

.capability-card strong {
  display: block;
  margin-top: 8px;
  color: #f8fbff;
  font-size: 18px;
  font-weight: 900;
}

.capability-card p {
  margin: 8px 0 0;
  color: #a9b9d2;
  font-size: 12px;
  line-height: 1.55;
}

@media (min-width: 1600px) {
  .hero-panel {
    max-width: 800px;
    padding-top: 6px;
  }

  .brand-row {
    gap: 16px;
  }

  .brand-icon {
    width: 70px;
    height: 70px;
  }

  .hero-panel h1 {
    max-width: 800px;
    font-size: 72px;
  }

  .hero-subtitle {
    max-width: 620px;
    font-size: 18px;
  }

  .hero-stage {
    max-width: 760px;
    margin-top: 52px;
    margin-left: 8px;
  }

  .hero-stage::after {
    top: 176px;
    left: 42px;
    width: 700px;
  }

  .hero-showcase {
    grid-template-columns: minmax(0, 1fr) 248px;
    gap: 38px;
    min-height: 270px;
    align-items: start;
  }

  .hero-showcase::before {
    top: 34px;
    height: 196px;
    transform: translateX(20px);
  }

  .showcase-core-panel {
    min-height: 264px;
  }

  .showcase-core-panel::before {
    width: 336px;
    height: 336px;
    left: 22px;
  }

  .orbital-core {
    width: 336px;
    height: 252px;
    font-size: 42px;
    transform: translate3d(0, 10px, 0);
  }

  .orbit-a {
    width: 244px;
    height: 244px;
  }

  .orbit-b {
    width: 196px;
    height: 196px;
  }

  .orbit-c {
    width: 148px;
    height: 148px;
  }

  .core-glow {
    width: 96px;
    height: 96px;
  }

  .orbital-core::before {
    width: 268px;
    height: 268px;
  }

  .particle-orbit-a {
    width: 280px;
    height: 280px;
  }

  .particle-orbit-b {
    width: 228px;
    height: 228px;
  }

  .particle-orbit-c {
    width: 176px;
    height: 176px;
  }

  .dot-a {
    top: 20px;
    left: 164px;
  }

  .dot-b {
    right: 46px;
    bottom: 42px;
  }

  .dot-c {
    left: 54px;
    bottom: 72px;
  }

  .signal-panel {
    min-height: 182px;
    margin-top: 34px;
    padding-top: 20px;
    padding-left: 28px;
  }

  .signal-header {
    margin-bottom: 26px;
    font-size: 15px;
  }

  .signal-row {
    font-size: 13px;
  }

  .capability-grid {
    margin-top: 30px;
    padding-top: 26px;
  }

  .capability-card strong {
    font-size: 20px;
  }

  .capability-card p {
    font-size: 13px;
  }

  .capability-card:nth-child(2) {
    margin-top: 12px;
  }

  .capability-card:nth-child(3) {
    margin-top: 24px;
  }
}

@media (min-width: 1920px) {
  .hero-panel {
    max-width: 860px;
  }

  .hero-panel h1 {
    max-width: 860px;
    font-size: 78px;
  }

  .hero-stage {
    max-width: 820px;
    margin-top: 58px;
  }

  .hero-showcase {
    grid-template-columns: minmax(0, 1fr) 264px;
    gap: 44px;
  }

  .hero-stage::after {
    top: 184px;
    width: 760px;
  }

  .signal-panel {
    margin-top: 40px;
  }

  .capability-grid {
    margin-top: 36px;
    padding-top: 28px;
  }

  .capability-card:nth-child(2) {
    margin-top: 14px;
  }

  .capability-card:nth-child(3) {
    margin-top: 28px;
  }
}

@media (max-width: 1160px) {
  .hero-stage {
    max-width: 640px;
    padding-top: 4px;
  }

  .hero-showcase {
    grid-template-columns: minmax(0, 1fr) 210px;
    gap: 14px;
  }

  .showcase-core-panel {
    min-height: 232px;
  }

  .signal-panel {
    padding-left: 18px;
  }
}

@media (max-width: 980px) {
  .hero-panel h1 {
    font-size: 48px;
  }

  .hero-stage {
    max-width: 680px;
  }

  .hero-showcase {
    grid-template-columns: 1fr;
  }

  .showcase-core-panel {
    min-height: 236px;
  }

  .signal-panel {
    width: min(100%, 300px);
    justify-self: end;
  }
}

@media (max-width: 1080px) {
  .hero-panel {
    display: none;
  }
}

@keyframes iconFloat {
  0%, 100% {
    transform: translate3d(0, 0, 0);
  }
  50% {
    transform: translate3d(0, -4px, 0);
  }
}

@keyframes orbitSpin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

@keyframes orbitSpinReverse {
  from {
    transform: rotate(360deg);
  }
  to {
    transform: rotate(0deg);
  }
}

@keyframes orbitPulse {
  0%, 100% {
    opacity: 0.58;
    transform: scale(1);
  }
  50% {
    opacity: 1;
    transform: scale(1.08);
  }
}

@keyframes coreBreath {
  0%, 100% {
    transform: scale(0.96);
  }
  50% {
    transform: scale(1.08);
  }
}

@keyframes dotFloatA {
  0%, 100% {
    transform: translate3d(0, 0, 0);
  }
  50% {
    transform: translate3d(22px, 16px, 0);
  }
}

@keyframes dotFloatB {
  0%, 100% {
    transform: translate3d(0, 0, 0);
  }
  50% {
    transform: translate3d(-18px, -18px, 0);
  }
}

@keyframes dotFloatC {
  0%, 100% {
    transform: translate3d(0, 0, 0);
  }
  50% {
    transform: translate3d(18px, -12px, 0);
  }
}

@keyframes particleRotateA {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

@keyframes particleRotateB {
  from {
    transform: rotate(360deg);
  }
  to {
    transform: rotate(0deg);
  }
}

@keyframes particleRotateC {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

@keyframes orbitSweep {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

</style>
