import { CHAT_PREVIEW_SCROLLBAR_STYLE } from './chatSvgPreview'

// 生成页面常用 html,body{height:100%} 加 flex 垂直居中；预览容器小于内容时，居中布局会把顶部溢出裁掉且滚动不到。
// 强制高度 auto 后内容自然向下延伸、溢出全部可滚动到达；min-height:100% 保持短内容页面撑满的观感。
const CHAT_HTML_PREVIEW_LAYOUT_STYLE = 'html,body{height:auto!important;min-height:100%!important}'

// 先隐藏文档再校准显示：头部脚本在 body 解析前隐藏 html，body 末尾的测量脚本完成首段缩放后立即恢复，
// 用户只会看到最终缩放状态，不会出现"先原尺寸、后缩小"的二次缩放过程。两段脚本来自同一注入串：
// 内联脚本被 CSP 拦截时两段都不执行，不会白屏。
const CHAT_HTML_PREVIEW_GUARD_SCRIPT = '<script>try{document.documentElement.style.visibility="hidden"}catch(error){}</script>'

// 预览是固定高度缩略盒（min(60vh,520px)），不调整 iframe 高度、无预览抖动。
// 测量脚本先还原缩放再测内容包围盒（含居中布局被裁掉的顶部溢出），内容超高或超宽时按比例 zoom 等比缩小到
// 完整可见（无滚动条、不截断）；一次性校准不做持续观察，避免动画页面反复变化；zoom 不被支持时回落预览内部滚动。
// 新窗口（parent === window）里直接跳过，保持自然尺寸渲染。
const CHAT_HTML_PREVIEW_MEASURE_SCRIPT = `<script>(function(){var d=document;if(window.parent===window)return;function union(){var min=0,max=0,right=0,i,box;var nodes=d.body?d.body.querySelectorAll("*"):[];for(i=0;i<nodes.length;i++){box=nodes[i].getBoundingClientRect();if(!box.width&&!box.height)continue;if(box.top<min)min=box.top;if(box.bottom>max)max=box.bottom;if(box.right>right)right=box.right}return{h:max-min,b:max,r:right}}function apply(){d.documentElement.style.zoom="";var u=union(),vw=d.documentElement.clientWidth,vh=d.documentElement.clientHeight;if(vw<=0||vh<=0)return;var h=Math.max(u.h,u.b,d.documentElement.scrollHeight),z=1;if(u.r>vw)z=Math.min(z,vw/u.r);if(h>vh)z=Math.min(z,vh/h);if(z<1){z=Math.floor(z*1000)/1000;d.documentElement.style.zoom=z}}try{apply()}catch(error){}d.documentElement.style.visibility="";d.addEventListener("DOMContentLoaded",apply);window.addEventListener("load",function(){apply();setTimeout(apply,300)})})()</script>`

// HTML 预览是完整文档，不能像 SVG 一样整体包装，只把统一滚动条样式注入 head（无 head 时按 html 标签或前置注入）。
// fit=false 用于新窗口：自然尺寸渲染，不注入测量脚本；"打开"入口在代码块工具栏，不在 iframe 内。
export function buildChatHtmlPreviewDocument(source: string, fit = true): string {
  const style = `<style>${CHAT_PREVIEW_SCROLLBAR_STYLE}${CHAT_HTML_PREVIEW_LAYOUT_STYLE}</style>`
  const head = fit ? `${style}${CHAT_HTML_PREVIEW_GUARD_SCRIPT}` : style
  let output = source
  if (/<head[^>]*>/i.test(output)) output = output.replace(/<head[^>]*>/i, (match) => `${match}${head}`)
  else if (/<html[^>]*>/i.test(output)) output = output.replace(/<html[^>]*>/i, (match) => `${match}${head}`)
  else output = `${head}${output}`
  if (!fit) return output
  if (/<\/body>/i.test(output)) return output.replace(/<\/body>/i, (match) => `${CHAT_HTML_PREVIEW_MEASURE_SCRIPT}${match}`)
  return `${output}${CHAT_HTML_PREVIEW_MEASURE_SCRIPT}`
}
