import { animate, type JSAnimation } from 'animejs'
import { nextTick, onBeforeUnmount, onMounted, ref } from 'vue'

const orbitParticleSelector = '.particle-dot'

export function useLoginBrandPanelOrbitAnimations() {
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

    showcase.querySelectorAll<HTMLElement>(orbitParticleSelector).forEach((element, index) => {
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

  return {
    showcaseRef
  }
}
