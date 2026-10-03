// 图表分类配色（样式规范 §6.1「纸墨澄明」的低饱和延伸）：全部为主题色族的
// 中性浅调（饱和度 ≤ ~30%、明度中段），替代 ECharts/antd 彩虹预设——图表是
// 数据可视化，不是配色展示；强调/语义色单独定义，不进分类循环。
export const chartPalette: string[] = [
  '#53696b', // 青灰（主题主色）
  '#a98548', // 琥珀（主题 warn）
  '#6f8f7a', // 灰绿（主题 ok）
  '#7a6d8f', // 灰紫（主题 violet）
  '#9d5547', // 陶红（主题 danger）
  '#758789', // 灰青蓝（主题 accent-mid）
  '#93a39c', // 浅灰绿
  '#c2b28c', // 浅驼
  '#b58f85', // 浅陶粉
  '#8b9db3', // 灰蓝
  '#a4a5a0', // 中性灰
  '#a87d92' // 灰玫
]

// 语义色（图表内固定含义，不参与分类循环）：失败/错误、耗时/警示、悬浮强调。
export const chartSemantic = {
  danger: '#9d5547',
  warning: '#a98548',
  // 柱状图悬浮强调：主色深半档，替代默认亮蓝。
  emphasis: '#425558'
} as const

// 图表坐标轴与网格中性色（与主题 --juhe-muted / 边框一族对齐）。
export const chartNeutral = {
  axisLabel: '#6d7372',
  axisLine: '#d9d8d4',
  splitLine: '#eceae6',
  labelStrong: '#3c4447'
} as const
