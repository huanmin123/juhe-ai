import { rowActionColumnWidth } from './rowActions'

export type ResponsiveDataListColumn = Record<string, any>

export interface NormalizeResponsiveTableColumnsOptions {
  adaptiveColumnWidth: boolean
  measuredActionColumnWidth?: number
}

export function normalizeResponsiveTableColumns(
  columns: ResponsiveDataListColumn[],
  options: NormalizeResponsiveTableColumnsOptions
): ResponsiveDataListColumn[] {
  const flexColumnIndex = findFlexColumnIndex(columns)
  return columns.map((column, index) => normalizeTableColumn(column, index === flexColumnIndex, options))
}

function normalizeTableColumn(
  column: ResponsiveDataListColumn,
  isFlexColumn: boolean,
  options: NormalizeResponsiveTableColumnsOptions
): ResponsiveDataListColumn {
  if (Array.isArray(column.children)) {
    return {
      ...column,
      children: normalizeResponsiveTableColumns(column.children, options)
    }
  }
  if (isActionColumn(column)) {
    const width = resolveActionColumnWidth(column.width, column.actionCount, options.measuredActionColumnWidth)
    const { actionCount: _actionCount, ...restColumn } = column
    return withCellProps({
      ...restColumn,
      width,
      className: mergeClassName(column.className, 'responsive-data-list-actions-column')
    }, {
      class: 'responsive-data-list-actions-column'
    })
  }
  if (options.adaptiveColumnWidth && !column.fixed) {
    const minWidth = resolveColumnMinWidth(column)
    const { width: _width, ...restColumn } = column
    return withCellProps({
      ...restColumn,
      minWidth,
      className: mergeClassName(column.className, 'responsive-data-list-auto-column', isFlexColumn ? 'responsive-data-list-flex-column' : undefined)
    }, {
      class: mergeClassName('responsive-data-list-auto-column', isFlexColumn ? 'responsive-data-list-flex-column' : undefined),
      style: { minWidth: `${minWidth}px` }
    })
  }
  return column
}

function findFlexColumnIndex(columns: ResponsiveDataListColumn[]): number {
  let bestIndex = -1
  let bestScore = -1
  columns.forEach((column, index) => {
    if (Array.isArray(column.children) || isActionColumn(column) || column.fixed || column.responsiveFlex === false) return
    const score = column.responsiveFlex === true ? 1000 : flexColumnScore(column, index)
    if (score > bestScore) {
      bestScore = score
      bestIndex = index
    }
  })
  return bestIndex
}

function flexColumnScore(column: ResponsiveDataListColumn, index: number): number {
  const key = String(column.key ?? column.dataIndex ?? '')
  const title = String(column.title ?? '')
  if (key === 'description' || title.includes('说明') || title.includes('备注')) return 900
  if (key === 'notes' || key === 'remark') return 880
  if (key === 'usage' || key === 'usageTotal' || title.includes('用量')) return 760
  if (key === 'capabilities' || title.includes('能力')) return 740
  if (key === 'baseUrl' || key === 'host' || key === 'key') return 720
  if (key === 'resource' || key === 'group') return 700
  if (key === 'displayName') return 680
  if (key === 'name' || title.includes('名称')) return 660
  if (key === 'systemAccount') return 620
  return Math.max(1, 200 - index)
}

function isActionColumn(column: ResponsiveDataListColumn): boolean {
  return column.key === 'actions' || column.dataIndex === 'actions' || column.title === '操作'
}

function resolveActionColumnWidth(value: unknown, actionCount: unknown, measuredActionColumnWidth = 0): number | string {
  if (measuredActionColumnWidth > 0) return measuredActionColumnWidth
  if (typeof value === 'number' && Number.isFinite(value) && value > 0) return value
  if (typeof value === 'string') {
    const trimmedValue = value.trim()
    const numericWidth = Number(trimmedValue)
    if (Number.isFinite(numericWidth) && numericWidth > 0) return numericWidth
    if (Number.isFinite(Number.parseFloat(trimmedValue)) && Number.parseFloat(trimmedValue) > 0) return trimmedValue
  }
  const numericActionCount = typeof actionCount === 'number' ? actionCount : Number.parseFloat(String(actionCount ?? ''))
  return rowActionColumnWidth(Number.isFinite(numericActionCount) ? numericActionCount : undefined)
}

function resolveColumnMinWidth(column: ResponsiveDataListColumn): number {
  const minWidth = typeof column.minWidth === 'number' ? column.minWidth : Number.parseFloat(String(column.minWidth ?? ''))
  if (Number.isFinite(minWidth) && minWidth > 0) return minWidth
  const width = typeof column.width === 'number' ? column.width : Number.parseFloat(String(column.width ?? ''))
  if (Number.isFinite(width) && width > 0) return width
  return 160
}

function withCellProps(column: ResponsiveDataListColumn, propsToMerge: ResponsiveDataListColumn): ResponsiveDataListColumn {
  return {
    ...column,
    customHeaderCell: (...args: any[]) => mergeCellProps(column.customHeaderCell?.(...args), propsToMerge),
    customCell: (...args: any[]) => mergeCellProps(column.customCell?.(...args), propsToMerge)
  }
}

function mergeCellProps(baseProps: ResponsiveDataListColumn | undefined, propsToMerge: ResponsiveDataListColumn): ResponsiveDataListColumn {
  const base = baseProps ?? {}
  return {
    ...base,
    class: mergeClassName(base.class, propsToMerge.class),
    style: mergeStyle(base.style, propsToMerge.style)
  }
}

function mergeClassName(...values: unknown[]): string {
  return values.filter(Boolean).map(String).join(' ')
}

function mergeStyle(baseStyle: unknown, styleToMerge: ResponsiveDataListColumn | undefined): unknown {
  if (!baseStyle) return styleToMerge
  if (!styleToMerge) return baseStyle
  if (typeof baseStyle === 'string') {
    return `${baseStyle};${Object.entries(styleToMerge).map(([key, value]) => `${toKebabCase(key)}:${value}`).join(';')}`
  }
  if (Array.isArray(baseStyle)) return [...baseStyle, styleToMerge]
  if (typeof baseStyle === 'object') return { ...(baseStyle as ResponsiveDataListColumn), ...styleToMerge }
  return styleToMerge
}

function toKebabCase(value: string): string {
  return value.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)
}
