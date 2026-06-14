const responsiveDataListScrollLockClassName = 'responsive-data-list-scroll-lock'
const responsiveDataListScrollLockCountKey = '__responsiveDataListScrollLockCount'

export function changeResponsiveDataListBodyScrollLock(delta: number): void {
  if (typeof document === 'undefined') return
  const body = document.body as HTMLBodyElement & Record<string, any>
  const currentCount = Number(body[responsiveDataListScrollLockCountKey] ?? 0)
  const nextCount = Math.max(0, (Number.isFinite(currentCount) ? currentCount : 0) + delta)
  body[responsiveDataListScrollLockCountKey] = nextCount
  body.classList.toggle(responsiveDataListScrollLockClassName, nextCount > 0)
}
