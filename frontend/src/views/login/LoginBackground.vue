<template>
  <div class="login-bg-grid" />
  <div class="login-orb login-orb-blue" />
  <div class="login-orb login-orb-cyan" />
  <div class="login-mouse-glow" />
  <div class="login-scanline" />
  <div class="login-data-streams" aria-hidden="true">
    <span v-for="stream in dataStreams" :key="stream" />
  </div>
</template>

<script setup lang="ts">
const dataStreams = Array.from({ length: 9 }, (_, index) => index)
</script>

<style scoped>
.login-bg-grid {
  position: absolute;
  inset: 0;
  background:
    linear-gradient(rgba(148, 163, 184, 0.08) 1px, transparent 1px),
    linear-gradient(90deg, rgba(148, 163, 184, 0.08) 1px, transparent 1px),
    radial-gradient(circle at 20% 20%, rgba(22, 119, 255, 0.26), transparent 34%),
    radial-gradient(circle at 75% 65%, rgba(34, 211, 238, 0.16), transparent 30%);
  background-size: 42px 42px, 42px 42px, auto, auto;
  animation: gridDrift 18s linear infinite;
}

.login-orb {
  position: absolute;
  width: 260px;
  height: 260px;
  border-radius: 999px;
  filter: blur(8px);
  opacity: 0.38;
}

.login-orb-blue {
  top: 11%;
  right: 18%;
  background: radial-gradient(circle, rgba(59, 130, 246, 0.72), transparent 66%);
  animation: orbFloat 9s ease-in-out infinite;
}

.login-orb-cyan {
  bottom: 8%;
  left: 9%;
  background: radial-gradient(circle, rgba(45, 212, 191, 0.56), transparent 68%);
  animation: orbFloat 11s ease-in-out infinite reverse;
}

.login-mouse-glow {
  position: absolute;
  inset: 0;
  pointer-events: none;
  background: radial-gradient(circle at var(--mouse-x) var(--mouse-y), rgba(56, 189, 248, 0.18), rgba(37, 99, 235, 0.08) 16%, transparent 34%);
  mix-blend-mode: screen;
  transition: background 0.12s ease-out;
}

.login-scanline {
  position: absolute;
  inset: 0;
  pointer-events: none;
  background: linear-gradient(180deg, transparent 0%, rgba(125, 211, 252, 0.08) 48%, transparent 56%);
  opacity: 0.38;
  animation: scanlineMove 8s linear infinite;
}

.login-data-streams {
  position: absolute;
  inset: 0;
  overflow: hidden;
  pointer-events: none;
}

.login-data-streams span {
  position: absolute;
  top: -18%;
  width: 1px;
  height: 22vh;
  background: linear-gradient(180deg, transparent, rgba(96, 165, 250, 0.42), transparent);
  opacity: 0.28;
  animation: streamFall 7s linear infinite;
}

.login-data-streams span:nth-child(1) { left: 8%; animation-delay: 0s; }
.login-data-streams span:nth-child(2) { left: 18%; animation-delay: 1.4s; height: 18vh; }
.login-data-streams span:nth-child(3) { left: 31%; animation-delay: 2.8s; }
.login-data-streams span:nth-child(4) { left: 44%; animation-delay: 0.8s; height: 26vh; }
.login-data-streams span:nth-child(5) { left: 58%; animation-delay: 3.6s; }
.login-data-streams span:nth-child(6) { left: 69%; animation-delay: 1.9s; height: 16vh; }
.login-data-streams span:nth-child(7) { left: 78%; animation-delay: 4.5s; }
.login-data-streams span:nth-child(8) { left: 87%; animation-delay: 2.2s; height: 20vh; }
.login-data-streams span:nth-child(9) { left: 95%; animation-delay: 5.2s; }

@media (max-width: 820px) {
  .login-orb,
  .login-mouse-glow,
  .login-scanline,
  .login-data-streams {
    display: none;
  }

  .login-bg-grid {
    background:
      linear-gradient(rgba(148, 163, 184, 0.06) 1px, transparent 1px),
      linear-gradient(90deg, rgba(148, 163, 184, 0.06) 1px, transparent 1px),
      radial-gradient(circle at 50% 18%, rgba(37, 99, 235, 0.18), transparent 44%);
    background-size: 36px 36px, 36px 36px, auto;
    animation: none;
  }
}

@keyframes gridDrift {
  from {
    background-position: 0 0, 0 0, center, center;
  }
  to {
    background-position: 42px 42px, 42px 42px, center, center;
  }
}

@keyframes orbFloat {
  0%, 100% {
    transform: translate3d(0, 0, 0) scale(1);
  }
  50% {
    transform: translate3d(18px, -16px, 0) scale(1.08);
  }
}

@keyframes scanlineMove {
  from {
    transform: translateY(-120%);
  }
  to {
    transform: translateY(120%);
  }
}

@keyframes streamFall {
  from {
    transform: translateY(-28vh);
  }
  to {
    transform: translateY(128vh);
  }
}

@media (prefers-reduced-motion: reduce) {
  .login-bg-grid,
  .login-orb,
  .login-scanline,
  .login-data-streams span {
    animation: none;
  }
}
</style>
