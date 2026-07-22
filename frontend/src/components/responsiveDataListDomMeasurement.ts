import { rowActionColumnWidth } from './rowActions'

export interface ResponsiveDataListScrollbarPlaceholderState {
  hasOverlayScrollbarPlaceholder: boolean
  scrollbarPlaceholderWidth: number
}

export function measureResponsiveDataListActionColumnWidth(root: HTMLElement, isMobile: boolean): number {
  if (isMobile) return 0
  const actionRoots = root.querySelectorAll<HTMLElement>('.responsive-data-list-actions-column .row-actions[data-row-action-slots]')
  let nextSlotCount = 0
  actionRoots.forEach((element) => {
    const slotCount = Number.parseInt(element.dataset.rowActionSlots ?? '', 10)
    if (Number.isFinite(slotCount) && slotCount > nextSlotCount) {
      nextSlotCount = slotCount
    }
  })
  return Math.max(
    nextSlotCount > 0 ? rowActionColumnWidth(nextSlotCount) : 0,
    measureResponsiveDataListActionColumnContentWidth(root)
  )
}

export function measureResponsiveDataListScrollbarPlaceholder(input: {
  tableScrollEnabled: boolean
  isMobile: boolean
  root?: HTMLElement
  currentPlaceholderWidth: number
}): ResponsiveDataListScrollbarPlaceholderState {
  if (!input.tableScrollEnabled || input.isMobile) {
    return emptyScrollbarPlaceholderState()
  }
  const body = input.root?.querySelector<HTMLElement>('.ant-table-body')
  const scrollbarCell = input.root?.querySelector<HTMLElement>('.ant-table-cell-scrollbar')
  if (!body || !scrollbarCell) {
    return emptyScrollbarPlaceholderState()
  }

  const actualScrollbarWidth = Math.max(0, body.offsetWidth - body.clientWidth)
  const measuredPlaceholderWidth = Math.round(scrollbarCell.getBoundingClientRect().width)
  const placeholderWidth = measuredPlaceholderWidth > 0 ? measuredPlaceholderWidth : input.currentPlaceholderWidth
  const hasOverlayScrollbarPlaceholder = placeholderWidth > 0 && actualScrollbarWidth <= 1
  return {
    hasOverlayScrollbarPlaceholder,
    scrollbarPlaceholderWidth: hasOverlayScrollbarPlaceholder ? placeholderWidth : 0
  }
}

function measureResponsiveDataListActionColumnContentWidth(root: HTMLElement): number {
  const cells = root.querySelectorAll<HTMLElement>('.ant-table-tbody .responsive-data-list-actions-column')
  let maxContentWidth = 0
  cells.forEach((cell) => {
    const rects = Array.from(cell.children)
      .map((child) => child.getBoundingClientRect())
      .filter((rect) => rect.width > 0)
    if (!rects.length) return
    const left = Math.min(...rects.map((rect) => rect.left))
    const right = Math.max(...rects.map((rect) => rect.right))
    maxContentWidth = Math.max(maxContentWidth, Math.ceil(right - left) + 16)
  })
  return maxContentWidth
}

function emptyScrollbarPlaceholderState(): ResponsiveDataListScrollbarPlaceholderState {
  return {
    hasOverlayScrollbarPlaceholder: false,
    scrollbarPlaceholderWidth: 0
  }
}
