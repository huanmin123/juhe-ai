<template>
  <section class="hero-panel">
    <div class="hero-title-row">
      <img class="brand-icon" :src="appIcon" :alt="`${appName} 图标`" />
      <h1>{{ title }}</h1>
    </div>
    <p>{{ subtitle }}</p>
    <div class="hero-divider">
      <span />
      <strong>AI GATEWAY PLATFORM</strong>
    </div>
    <div class="hero-topline">
      <div class="hero-badge">{{ badge }}</div>
      <div class="hero-status">
        <span />
        Control Plane Online
      </div>
    </div>
    <div class="hero-command-panel">
      <div class="command-grid">
        <span v-for="(node, index) in commandNodes" :key="node" :style="{ '--delay': `${index * 0.18}s` }" />
      </div>
      <div class="command-copy">
        <span>统一纳管</span>
        <strong>供应商 · 账号 · 分组 · 密钥 · 记录</strong>
      </div>
      <div class="command-glow" />
    </div>
    <div class="signal-grid">
      <div v-for="item in signals" :key="item.title" class="signal-card">
        <span class="signal-index">{{ item.index }}</span>
        <span class="signal-value">{{ item.value }}</span>
        <span class="signal-title">{{ item.title }}</span>
      </div>
    </div>
    <div class="hero-footnote">为多模型、多供应商和多账号体系预留统一控制面。</div>
  </section>
</template>

<script setup lang="ts">
defineProps<{
  appIcon: string
  appName: string
  title: string
  subtitle: string
  badge: string
}>()

const signals = [
  { index: '01', value: '多厂商接入', title: '统一纳管模型供应商与上游账号' },
  { index: '02', value: '智能调度', title: '围绕分组、密钥和策略完成路由' },
  { index: '03', value: '安全隔离', title: '系统账户、配置和记录独立成域' }
]
const commandNodes = ['providers', 'accounts', 'groups', 'keys', 'usage', 'settings']
</script>

<style scoped>
.hero-panel {
  position: relative;
  z-index: 1;
  max-width: 980px;
}

.hero-topline {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: center;
  margin-top: 30px;
}

.hero-badge {
  display: inline-flex;
  padding: 8px 12px;
  color: #93c5fd;
  background: rgba(15, 23, 42, 0.62);
  border: 1px solid rgba(96, 165, 250, 0.26);
  border-radius: 999px;
  font-size: 12px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  box-shadow: 0 0 24px rgba(59, 130, 246, 0.12);
  transition: transform 0.22s ease, border-color 0.22s ease, box-shadow 0.22s ease;
}

.hero-badge:hover {
  transform: translateY(-1px);
  border-color: rgba(125, 211, 252, 0.48);
  box-shadow: 0 0 30px rgba(59, 130, 246, 0.22);
}

.hero-status {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: #8aa6c9;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 11px;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.hero-status span {
  width: 7px;
  height: 7px;
  background: #22c55e;
  border-radius: 999px;
  box-shadow: 0 0 14px rgba(34, 197, 94, 0.82);
  animation: statusPulse 1.8s ease-in-out infinite;
}

.hero-command-panel {
  position: relative;
  display: grid;
  grid-template-columns: 210px minmax(0, 1fr);
  gap: 22px;
  align-items: center;
  max-width: 620px;
  min-height: 112px;
  margin-top: 14px;
  padding: 18px 20px;
  overflow: hidden;
  background:
    linear-gradient(90deg, rgba(15, 23, 42, 0.74), rgba(15, 23, 42, 0.38)),
    radial-gradient(circle at 22% 50%, rgba(37, 99, 235, 0.26), transparent 42%);
  border: 1px solid rgba(96, 165, 250, 0.18);
  border-radius: 24px;
  box-shadow: 0 20px 60px rgba(2, 8, 23, 0.22), inset 0 1px 0 rgba(255, 255, 255, 0.04);
  backdrop-filter: blur(16px);
  transition: transform 0.28s ease, border-color 0.28s ease, box-shadow 0.28s ease;
}

.hero-command-panel::before {
  content: '';
  position: absolute;
  inset: -1px;
  background: linear-gradient(120deg, transparent 10%, rgba(125, 211, 252, 0.28) 34%, transparent 58%);
  opacity: 0;
  transform: translateX(-40%);
  transition: opacity 0.28s ease;
}

.hero-command-panel:hover {
  transform: translateY(-3px);
  border-color: rgba(125, 211, 252, 0.34);
  box-shadow: 0 24px 80px rgba(8, 47, 73, 0.38), inset 0 1px 0 rgba(255, 255, 255, 0.06);
}

.hero-command-panel:hover::before {
  opacity: 1;
  animation: panelSweep 1.5s ease forwards;
}

.command-grid {
  position: relative;
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
}

.command-grid::before,
.command-grid::after {
  content: '';
  position: absolute;
  inset: 50% 14px auto;
  height: 1px;
  background: linear-gradient(90deg, rgba(96, 165, 250, 0), rgba(96, 165, 250, 0.6), rgba(96, 165, 250, 0));
}

.command-grid::after {
  inset: 14px auto 14px 50%;
  width: 1px;
  height: auto;
  background: linear-gradient(180deg, rgba(96, 165, 250, 0), rgba(96, 165, 250, 0.54), rgba(96, 165, 250, 0));
}

.command-grid span {
  position: relative;
  z-index: 1;
  width: 42px;
  height: 42px;
  background: linear-gradient(180deg, rgba(30, 64, 175, 0.65), rgba(15, 23, 42, 0.7));
  border: 1px solid rgba(147, 197, 253, 0.28);
  border-radius: 14px;
  box-shadow: 0 0 26px rgba(37, 99, 235, 0.18);
  animation: nodeBreath 2.8s ease-in-out infinite;
  animation-delay: var(--delay);
}

.command-grid span::after {
  content: '';
  position: absolute;
  inset: 13px;
  background: #7dd3fc;
  border-radius: 999px;
  box-shadow: 0 0 14px rgba(125, 211, 252, 0.9);
  animation: nodeCore 1.9s ease-in-out infinite;
  animation-delay: var(--delay);
}

.command-copy {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.command-copy span {
  color: #7dd3fc;
  font-size: 12px;
  font-weight: 800;
  letter-spacing: 0.18em;
}

.command-copy strong {
  max-width: 320px;
  color: #eaf4ff;
  font-size: 20px;
  font-weight: 900;
  line-height: 1.4;
}

.command-glow {
  position: absolute;
  right: -40px;
  bottom: -70px;
  width: 160px;
  height: 160px;
  background: radial-gradient(circle, rgba(34, 211, 238, 0.28), transparent 66%);
  animation: glowPulse 4s ease-in-out infinite;
}

.hero-title-row {
  display: flex;
  align-items: flex-start;
  gap: 18px;
  margin-top: 0;
}

.brand-icon {
  flex: 0 0 auto;
  width: 58px;
  height: 58px;
  margin-top: 3px;
  padding: 10px;
  background: rgba(255, 255, 255, 0.94);
  border-radius: 18px;
  box-shadow: 0 0 34px rgba(59, 130, 246, 0.46);
  animation: iconFloat 3.2s ease-in-out infinite;
}

.hero-panel h1 {
  max-width: 680px;
  margin: 0 0 14px;
  color: #fff;
  font-size: clamp(40px, 5vw, 68px);
  font-weight: 900;
  line-height: 1.02;
  letter-spacing: -0.04em;
  text-shadow: 0 0 32px rgba(96, 165, 250, 0.18);
}

.hero-panel p {
  max-width: 560px;
  margin: 0;
  color: #b6c6dc;
  font-size: 16px;
  line-height: 1.8;
}

.hero-divider {
  display: flex;
  align-items: center;
  gap: 12px;
  max-width: 600px;
  margin-top: 30px;
  color: #8fbaff;
  font-size: 11px;
  font-weight: 800;
  letter-spacing: 0.18em;
}

.hero-divider span {
  width: 48px;
  height: 1px;
  background: linear-gradient(90deg, #60a5fa, rgba(96, 165, 250, 0));
}

.hero-divider strong {
  font-weight: 800;
}

.signal-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 12px;
  max-width: 600px;
  margin-top: 24px;
}

.signal-card {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 124px;
  padding: 18px 16px 17px;
  background: linear-gradient(180deg, rgba(15, 23, 42, 0.66), rgba(15, 23, 42, 0.5));
  border: 1px solid rgba(148, 163, 184, 0.18);
  border-radius: 18px;
  backdrop-filter: blur(14px);
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.04);
  transition: transform 0.22s ease, border-color 0.22s ease, background 0.22s ease, box-shadow 0.22s ease;
}

.signal-card:hover {
  transform: translateY(-5px);
  background: linear-gradient(180deg, rgba(15, 23, 42, 0.76), rgba(15, 23, 42, 0.56));
  border-color: rgba(125, 211, 252, 0.36);
  box-shadow: 0 18px 50px rgba(8, 47, 73, 0.28), inset 0 1px 0 rgba(255, 255, 255, 0.07);
}

.signal-card::before {
  content: '';
  position: absolute;
  top: 0;
  right: 18px;
  left: 18px;
  height: 1px;
  background: linear-gradient(90deg, rgba(96, 165, 250, 0), rgba(96, 165, 250, 0.85), rgba(34, 211, 238, 0));
}

.signal-index {
  color: #60a5fa;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.08em;
}

.signal-value {
  color: #f8fbff;
  font-size: 17px;
  font-weight: 800;
}

.signal-title {
  color: #9fb0c7;
  font-size: 12px;
  line-height: 1.55;
}

.hero-footnote {
  max-width: 600px;
  margin-top: 16px;
  color: #7890ad;
  font-size: 13px;
  line-height: 1.7;
}

@media (max-width: 980px) {
  .hero-title-row {
    align-items: center;
  }

  .brand-icon {
    width: 50px;
    height: 50px;
    margin-top: 0;
    border-radius: 16px;
  }

  .signal-grid {
    grid-template-columns: 1fr;
  }

  .hero-command-panel {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 820px) {
  .hero-panel {
    display: none;
  }
}

@keyframes statusPulse {
  0%, 100% {
    opacity: 0.72;
    transform: scale(1);
  }
  50% {
    opacity: 1;
    transform: scale(1.35);
  }
}

@keyframes panelSweep {
  from {
    transform: translateX(-55%);
  }
  to {
    transform: translateX(55%);
  }
}

@keyframes nodeBreath {
  0%, 100% {
    border-color: rgba(147, 197, 253, 0.24);
    box-shadow: 0 0 20px rgba(37, 99, 235, 0.14);
  }
  50% {
    border-color: rgba(125, 211, 252, 0.5);
    box-shadow: 0 0 30px rgba(56, 189, 248, 0.28);
  }
}

@keyframes nodeCore {
  0%, 100% {
    opacity: 0.72;
    transform: scale(0.9);
  }
  50% {
    opacity: 1;
    transform: scale(1.15);
  }
}

@keyframes glowPulse {
  0%, 100% {
    opacity: 0.6;
    transform: scale(1);
  }
  50% {
    opacity: 1;
    transform: scale(1.18);
  }
}

@keyframes iconFloat {
  0%, 100% {
    transform: translateY(0);
  }
  50% {
    transform: translateY(-3px);
  }
}

@media (prefers-reduced-motion: reduce) {
  .hero-status span,
  .command-grid span,
  .command-grid span::after,
  .command-glow,
  .brand-icon {
    animation: none;
  }
}
</style>
