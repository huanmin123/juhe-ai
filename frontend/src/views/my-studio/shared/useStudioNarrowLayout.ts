import { onBeforeUnmount, onMounted, ref } from 'vue'

/**
 * 工作台断点（规划 F2：工作台页 992px 口径）：992px 以下参数区折叠为顶部
 * 形态。AppLayout 的 isMobile 属布局内部状态，工作台用独立媒体查询。
 */
export function useStudioNarrowLayout(breakpointPx = 992) {
  const isNarrow = ref(false)
  let mediaQuery: MediaQueryList | undefined

  function update(event: MediaQueryList | MediaQueryListEvent): void {
    isNarrow.value = event.matches
  }

  onMounted(() => {
    mediaQuery = window.matchMedia(`(max-width: ${breakpointPx}px)`)
    update(mediaQuery)
    mediaQuery.addEventListener('change', update)
  })

  onBeforeUnmount(() => {
    mediaQuery?.removeEventListener('change', update)
    mediaQuery = undefined
  })

  return { isNarrow }
}
