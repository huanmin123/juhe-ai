export interface ChatSvgPreviewSize {
  width: number
  height: number
}

export function isCompleteStaticSvg(source: string): boolean {
  const value = source.trim()
  return /^<svg\b[\s\S]*<\/svg>$/iu.test(value)
}

export function chatSvgHasViewBox(source: string): boolean {
  return /\bviewBox\s*=/iu.test(source)
}

export function resolveChatSvgPreviewSize(source: string): ChatSvgPreviewSize {
  const root = source.match(/^<svg\b([^>]*)>/iu)?.[1] ?? ''
  const viewBox = root.match(/\bviewBox\s*=\s*["']\s*[-+]?\d+(?:\.\d+)?\s+[-+]?\d+(?:\.\d+)?\s+([\d.]+)\s+([\d.]+)\s*["']/iu)
  const width = boundedDimension(root.match(/\bwidth\s*=\s*["']\s*([\d.]+)/iu)?.[1], viewBox?.[1], 640)
  const height = boundedDimension(root.match(/\bheight\s*=\s*["']\s*([\d.]+)/iu)?.[1], viewBox?.[2], 360)
  return { width, height }
}

function boundedDimension(primary: string | undefined, secondary: string | undefined, fallback: number): number {
  const value = Number(primary ?? secondary)
  return Number.isFinite(value) && value > 0 ? Math.min(960, Math.max(120, Math.round(value))) : fallback
}

// iframe 是独立文档，不继承 global.css 的统一滚动条样式，srcdoc 需内联同一套规则；HTML 预览共用。
export const CHAT_PREVIEW_SCROLLBAR_STYLE = [
  'html{scrollbar-color:rgba(100,116,139,.42) transparent;scrollbar-width:thin}',
  '::-webkit-scrollbar{width:10px;height:10px}',
  '::-webkit-scrollbar-track{background:transparent}',
  '::-webkit-scrollbar-thumb{min-height:36px;background-color:rgba(100,116,139,.36);background-clip:content-box;border:2px solid transparent;border-radius:999px}',
  '::-webkit-scrollbar-thumb:hover{background-color:rgba(100,116,139,.56)}',
  '::-webkit-scrollbar-corner{background:transparent}',
  '::-webkit-scrollbar-button{width:0;height:0;display:none}'
].join('')

// 预览内悬浮"新窗口打开"按钮：默认隐藏，悬浮预览时才显示（不遮挡画面）；触屏设备无 hover，常显低透明度兜底。
const CHAT_PREVIEW_OPEN_WINDOW_STYLE = '.chat-preview-open-window{position:fixed;top:6px;right:6px;z-index:2147483647;width:20px;height:20px;padding:0;border:1px solid rgba(15,23,42,.12);border-radius:5px;background:rgba(255,255,255,.6);color:#334155;cursor:pointer;line-height:0;opacity:0;pointer-events:none;box-shadow:0 1px 3px rgba(15,23,42,.08)}body:hover .chat-preview-open-window{opacity:.55;pointer-events:auto}.chat-preview-open-window:hover{opacity:.9;pointer-events:auto}@media (hover: none){.chat-preview-open-window{opacity:.35;pointer-events:auto}}'

// SVG 预览内悬浮按钮由脚本注入，仅在新窗口里（parent === window）不显示。沙箱 iframe 不能直接写弹窗 document
// （不透明源会被浏览器拒访，得到空 about:blank），因此点击只 postMessage 请求父页开窗，由父页写入
// "仅含全屏 sandbox srcdoc iframe 的包装页"，生成内容仍只在沙箱内运行。
export const CHAT_PREVIEW_OPEN_WINDOW_SCRIPT = `<script>(function(){if(window.parent===window)return;var d=document;function add(){var b=d.createElement("button");b.type="button";b.className="chat-preview-open-window";b.title="新窗口打开完整预览";b.setAttribute("aria-label","新窗口打开完整预览");b.innerHTML='<svg width="11" height="11" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8.2 1.8h4v4M12.2 1.8 7.6 6.4M5.8 12.2h-4v-4M1.8 12.2l4.6-4.6"/></svg>';b.addEventListener("click",function(){try{parent.postMessage({type:"juhe-ai-chat-svg-preview-open-window"},"*")}catch(error){}});(d.body||d.documentElement).appendChild(b)}if(d.readyState==="loading"){d.addEventListener("DOMContentLoaded",add)}else{add()}})()</script>`

// SVG 预览：fit 时按视口等比缩放（viewBox + 默认 preserveAspectRatio meet），完整可见、无滚动条；
// 无 viewBox 无法等比缩放，退回自然尺寸避免截断。fit=false 用于新窗口：自然尺寸渲染，不注入测量/按钮脚本。
export function buildChatSvgPreviewDocument(svg: string, fit = true): string {
  const fitStyle = fit && chatSvgHasViewBox(svg)
    ? 'html,body{margin:0;width:100%;height:100%}svg{width:100%;height:100%;display:block}'
    : 'html,body{margin:0}svg{display:block}'
  const scripts = fit ? CHAT_PREVIEW_OPEN_WINDOW_SCRIPT : ''
  return `<!doctype html><html><head><meta charset="utf-8"><style>${CHAT_PREVIEW_SCROLLBAR_STYLE}${CHAT_PREVIEW_OPEN_WINDOW_STYLE}${fitStyle}</style></head><body>${svg}${scripts}</body></html>`
}
