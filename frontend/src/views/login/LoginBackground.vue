<template>
  <div class="login-background" aria-hidden="true">
    <div class="login-grid" />
    <div class="light-beam light-beam-a" />
    <div class="light-beam light-beam-b" />
    <div class="ambient-layer ambient-layer-a" />
    <div class="ambient-layer ambient-layer-b" />
    <div class="ambient-layer ambient-layer-c" />
    <div class="login-cursor-light" />
    <div class="particle-field">
      <span v-for="particle in particles" :key="`particle-${particle}`" />
    </div>
    <div class="route-field">
      <span v-for="line in routeLines" :key="`line-${line}`" :class="`route-line route-line-${line}`" />
      <span v-for="pulse in routePulses" :key="`pulse-${pulse}`" :class="`route-pulse route-pulse-${pulse}`" />
    </div>
  </div>
</template>

<script setup lang="ts">
const routeLines = Array.from({ length: 7 }, (_, index) => index + 1)
const routePulses = Array.from({ length: 6 }, (_, index) => index + 1)
const particles = Array.from({ length: 10 }, (_, index) => index + 1)
</script>

<style scoped>
.login-background {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
}

.login-grid {
  position: absolute;
  inset: 0;
  background:
    linear-gradient(rgba(148, 163, 184, 0.04) 1px, transparent 1px),
    linear-gradient(90deg, rgba(148, 163, 184, 0.04) 1px, transparent 1px),
    radial-gradient(circle at 18% 10%, rgba(37, 99, 235, 0.16), transparent 28%),
    linear-gradient(135deg, #020617, #07111f 48%, #080b1a);
  background-size: 42px 42px, 42px 42px, auto, auto;
}

.login-grid::before {
  content: '';
  position: absolute;
  inset: 0;
  background:
    linear-gradient(115deg, transparent 0%, rgba(56, 189, 248, 0.045) 28%, transparent 48%),
    radial-gradient(circle at 74% 78%, rgba(79, 70, 229, 0.12), transparent 30%);
}

.login-grid::after {
  content: '';
  position: absolute;
  inset: auto auto 10% 54%;
  width: 420px;
  height: 420px;
  background: radial-gradient(circle, rgba(37, 99, 235, 0.14), rgba(15, 23, 42, 0.02) 58%, transparent 72%);
  transform: translate3d(-50%, 0, 0);
  opacity: 0.72;
  animation: gridBreath 10s ease-in-out infinite;
}

.light-beam {
  position: absolute;
  width: 42vw;
  height: 1px;
  background: linear-gradient(90deg, transparent, rgba(125, 211, 252, 0.32), transparent);
  opacity: 0.42;
  will-change: transform, opacity;
}

.light-beam-a {
  top: 24%;
  left: -8%;
  transform: rotate(11deg);
  animation: beamDriftA 15s ease-in-out infinite;
}

.light-beam-b {
  right: -10%;
  bottom: 18%;
  transform: rotate(-13deg);
  animation: beamDriftB 17s ease-in-out infinite;
}

.ambient-layer {
  position: absolute;
  border-radius: 50%;
  opacity: 0.58;
  will-change: transform, opacity;
}

.ambient-layer-a {
  top: 14%;
  left: 4%;
  width: 520px;
  height: 520px;
  background: radial-gradient(circle, rgba(37, 99, 235, 0.16), rgba(14, 165, 233, 0.05) 46%, transparent 70%);
  animation: ambientFloatA 14s ease-in-out infinite;
}

.ambient-layer-b {
  right: 5%;
  bottom: 0;
  width: 460px;
  height: 460px;
  background: radial-gradient(circle, rgba(99, 102, 241, 0.16), rgba(30, 64, 175, 0.05) 48%, transparent 72%);
  animation: ambientFloatB 16s ease-in-out infinite;
}

.ambient-layer-c {
  top: 34%;
  right: 28%;
  width: 300px;
  height: 300px;
  background: radial-gradient(circle, rgba(125, 211, 252, 0.09), transparent 70%);
  animation: ambientPulse 8s ease-in-out infinite;
}

.login-cursor-light {
  position: absolute;
  top: 0;
  left: 0;
  width: 320px;
  height: 320px;
  background: radial-gradient(circle, rgba(56, 189, 248, 0.18), rgba(37, 99, 235, 0.08) 42%, transparent 70%);
  opacity: 0;
  transform: translate3d(calc(50vw - 160px), calc(50vh - 160px), 0);
  transition: opacity 0.18s ease-out;
  will-change: transform, opacity;
}

:global(.login-page-pointer-active) .login-cursor-light {
  opacity: 1;
}

.particle-field {
  position: absolute;
  inset: 0;
}

.particle-field span {
  position: absolute;
  width: 4px;
  height: 4px;
  background: rgba(191, 219, 254, 0.82);
  border-radius: 50%;
  box-shadow: 0 0 14px rgba(125, 211, 252, 0.66);
  animation: particleFloat 9s ease-in-out infinite;
  will-change: transform, opacity;
}

.particle-field span:nth-child(1) { top: 18%; left: 22%; animation-delay: -0.8s; }
.particle-field span:nth-child(2) { top: 28%; left: 58%; animation-delay: -2.4s; }
.particle-field span:nth-child(3) { top: 42%; left: 14%; animation-delay: -1.2s; }
.particle-field span:nth-child(4) { top: 52%; left: 66%; animation-delay: -3.6s; }
.particle-field span:nth-child(5) { top: 66%; left: 32%; animation-delay: -5s; }
.particle-field span:nth-child(6) { top: 74%; left: 78%; animation-delay: -2s; }
.particle-field span:nth-child(7) { top: 20%; left: 74%; animation-delay: -4.8s; }
.particle-field span:nth-child(8) { top: 36%; left: 40%; animation-delay: -6.2s; }
.particle-field span:nth-child(9) { top: 60%; left: 50%; animation-delay: -3s; }
.particle-field span:nth-child(10) { top: 82%; left: 18%; animation-delay: -7s; }

.route-field {
  position: absolute;
  inset: 0;
}

.route-line,
.route-pulse {
  position: absolute;
  display: block;
}

.route-line {
  background: rgba(125, 211, 252, 0.11);
}

.route-line::after {
  content: '';
  position: absolute;
  background: linear-gradient(90deg, transparent, rgba(96, 165, 250, 0.92), transparent);
  opacity: 0.58;
  transform: translate3d(-100%, 0, 0);
  animation: routeFlowX 6.8s linear infinite;
  will-change: transform;
}

.route-line-1,
.route-line-2,
.route-line-3,
.route-line-5 {
  height: 1px;
}

.route-line-1::after,
.route-line-2::after,
.route-line-3::after,
.route-line-5::after {
  top: 0;
  left: 0;
  width: 42%;
  height: 1px;
}

.route-line-4,
.route-line-6,
.route-line-7 {
  width: 1px;
}

.route-line-4::after,
.route-line-6::after,
.route-line-7::after {
  top: 0;
  left: 0;
  width: 1px;
  height: 38%;
  background: linear-gradient(180deg, transparent, rgba(96, 165, 250, 0.9), transparent);
  animation-name: routeFlowY;
}

.route-line-1 { top: 18%; left: 6%; width: 44%; }
.route-line-2 { top: 34%; left: 18%; width: 58%; }
.route-line-3 { top: 56%; left: 9%; width: 48%; }
.route-line-4 { top: 18%; left: 50%; height: 38%; }
.route-line-5 { top: 74%; left: 30%; width: 42%; }
.route-line-6 { top: 34%; left: 76%; height: 40%; }
.route-line-7 { top: 42%; left: 18%; height: 32%; }

.route-line-2::after { animation-delay: -2.1s; }
.route-line-3::after { animation-delay: -4.3s; }
.route-line-4::after { animation-delay: -1.4s; }
.route-line-5::after { animation-delay: -3.2s; }
.route-line-6::after { animation-delay: -5.1s; }
.route-line-7::after { animation-delay: -2.8s; }

.route-pulse {
  width: 8px;
  height: 8px;
  background: #93c5fd;
  border: 1px solid rgba(191, 219, 254, 0.82);
  border-radius: 50%;
  box-shadow: 0 0 18px rgba(96, 165, 250, 0.72);
  animation: nodePulse 2.8s ease-in-out infinite;
  will-change: transform, opacity;
}

.route-pulse-1 { top: 17.5%; left: 49.6%; }
.route-pulse-2 { top: 33.5%; left: 75.6%; animation-delay: -0.8s; }
.route-pulse-3 { top: 55.5%; left: 17.6%; animation-delay: -1.6s; }
.route-pulse-4 { top: 73.5%; left: 58%; animation-delay: -2.2s; }
.route-pulse-5 { top: 41.5%; left: 17.6%; animation-delay: -1.1s; }
.route-pulse-6 { top: 33.5%; left: 49.6%; animation-delay: -2.6s; }

@keyframes routeFlowX {
  from {
    transform: translate3d(-100%, 0, 0);
  }
  to {
    transform: translate3d(240%, 0, 0);
  }
}

@keyframes routeFlowY {
  from {
    transform: translate3d(0, -100%, 0);
  }
  to {
    transform: translate3d(0, 240%, 0);
  }
}

@keyframes nodePulse {
  0%, 100% {
    opacity: 0.42;
    transform: scale(0.86);
  }
  50% {
    opacity: 1;
    transform: scale(1.18);
  }
}

@keyframes ambientFloatA {
  0%, 100% {
    transform: translate3d(0, 0, 0) scale(1);
    opacity: 0.58;
  }
  50% {
    transform: translate3d(34px, -22px, 0) scale(1.08);
    opacity: 0.58;
  }
}

@keyframes ambientFloatB {
  0%, 100% {
    transform: translate3d(0, 0, 0) scale(1);
    opacity: 0.38;
  }
  50% {
    transform: translate3d(-26px, 18px, 0) scale(1.1);
    opacity: 0.54;
  }
}

@keyframes ambientPulse {
  0%, 100% {
    transform: scale(0.9);
    opacity: 0.26;
  }
  50% {
    transform: scale(1.18);
    opacity: 0.48;
  }
}

@keyframes beamDriftA {
  0%, 100% {
    transform: translate3d(0, 0, 0) rotate(11deg);
    opacity: 0.26;
  }
  50% {
    transform: translate3d(52px, -10px, 0) rotate(11deg);
    opacity: 0.46;
  }
}

@keyframes beamDriftB {
  0%, 100% {
    transform: translate3d(0, 0, 0) rotate(-13deg);
    opacity: 0.2;
  }
  50% {
    transform: translate3d(-56px, 12px, 0) rotate(-13deg);
    opacity: 0.38;
  }
}

@keyframes particleFloat {
  0%, 100% {
    opacity: 0.32;
    transform: translate3d(0, 0, 0) scale(0.9);
  }
  50% {
    opacity: 0.9;
    transform: translate3d(0, -16px, 0) scale(1.18);
  }
}

@keyframes gridBreath {
  0%, 100% {
    opacity: 0.42;
    transform: translate3d(-50%, 0, 0) scale(0.94);
  }
  50% {
    opacity: 0.8;
    transform: translate3d(-50%, -10px, 0) scale(1.08);
  }
}

@media (max-width: 820px) {
  .login-cursor-light,
  .light-beam,
  .route-field {
    display: none;
  }

  .login-grid {
    background:
      linear-gradient(rgba(148, 163, 184, 0.06) 1px, transparent 1px),
      linear-gradient(90deg, rgba(148, 163, 184, 0.06) 1px, transparent 1px),
      radial-gradient(circle at 50% 18%, rgba(37, 99, 235, 0.18), transparent 36%),
      linear-gradient(150deg, #020617, #07111f 48%, #090f21);
    background-size: 36px 36px, 36px 36px, auto;
  }

  .login-grid::before {
    opacity: 0.86;
  }

  .login-grid::after {
    inset: auto auto 24% 50%;
    width: 250px;
    height: 250px;
    opacity: 0.68;
  }

  .ambient-layer-a {
    top: 8%;
    left: -18%;
    width: 280px;
    height: 280px;
    opacity: 0.44;
  }

  .ambient-layer-b {
    right: -20%;
    bottom: 10%;
    width: 320px;
    height: 320px;
    opacity: 0.34;
  }

  .ambient-layer-c {
    top: 46%;
    right: -10%;
    width: 180px;
    height: 180px;
    opacity: 0.28;
  }

  .particle-field span {
    width: 3px;
    height: 3px;
    animation-duration: 10.5s;
  }

  .particle-field span:nth-child(n + 6) {
    display: none;
  }
}

</style>
