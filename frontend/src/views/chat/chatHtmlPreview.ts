import { CHAT_PREVIEW_SCROLLBAR_STYLE } from './chatSvgPreview'

// 生成页面常用 html,body{height:100%} 加 flex 垂直居中；预览容器小于内容时，居中布局会把顶部溢出裁掉且滚动不到。
// 强制高度 auto 后内容自然向下延伸、溢出全部可滚动到达；min-height:100% 保持短内容页面撑满的观感。
const CHAT_HTML_PREVIEW_LAYOUT_STYLE = 'html,body{height:auto!important;min-height:100%!important}'

// 高度自适应：测量内容包围盒（含居中布局被裁掉的顶部溢出）后上报父页调整 iframe 高度；
// 内容超过 900px 高或超出视口宽时按比例 zoom 缩小到完整可见（无滚动条、等比不截断），缩放后再复测上报实际高度。
// 一次性测量不做持续观察，避免动画页面高度抖动；zoom 不被支持时上报原高度，回落为预览内部滚动。
// 新窗口（parent === window）里直接跳过，保持自然尺寸渲染。
const CHAT_HTML_PREVIEW_MEASURE_SCRIPT = `<script>(function(){var d=document;if(window.parent===window)return;function union(){var min=0,max=0,right=0,i,box;var nodes=d.body?d.body.querySelectorAll("*"):[];for(i=0;i<nodes.length;i++){box=nodes[i].getBoundingClientRect();if(!box.width&&!box.height)continue;if(box.top<min)min=box.top;if(box.bottom>max)max=box.bottom;if(box.right>right)right=box.right}return{h:max-min,b:max,r:right}}function apply(){var u=union(),vh=Math.max(u.h,u.b,d.documentElement.scrollHeight),vw=d.documentElement.clientWidth,z=1;if(vh>900)z=900/vh;if(u.r>vw&&vw>0)z=Math.min(z,vw/u.r);if(z<1){z=Math.floor(z*1000)/1000;d.documentElement.style.zoom=z;u=union();vh=Math.max(u.h,u.b,d.documentElement.scrollHeight)}var height=Math.min(900,Math.max(240,Math.ceil(vh)));try{parent.postMessage({type:"juhe-ai-chat-html-preview-height",height:height},"*")}catch(error){}}d.addEventListener("DOMContentLoaded",apply);window.addEventListener("load",function(){apply();setTimeout(apply,300)})})()</script>`

// HTML 预览是完整文档，不能像 SVG 一样整体包装，只把统一滚动条样式注入 head（无 head 时按 html 标签或前置注入）。
// fit=false 用于新窗口：自然尺寸渲染，不注入测量脚本；"打开"入口在代码块工具栏，不在 iframe 内。
export function buildChatHtmlPreviewDocument(source: string, fit = true): string {
  const style = `<style>${CHAT_PREVIEW_SCROLLBAR_STYLE}${CHAT_HTML_PREVIEW_LAYOUT_STYLE}</style>`
  let output = source
  if (/<head[^>]*>/i.test(output)) output = output.replace(/<head[^>]*>/i, (match) => `${match}${style}`)
  else if (/<html[^>]*>/i.test(output)) output = output.replace(/<html[^>]*>/i, (match) => `${match}${style}`)
  else output = `${style}${output}`
  if (!fit) return output
  if (/<\/body>/i.test(output)) return output.replace(/<\/body>/i, (match) => `${CHAT_HTML_PREVIEW_MEASURE_SCRIPT}${match}`)
  return `${output}${CHAT_HTML_PREVIEW_MEASURE_SCRIPT}`
}
