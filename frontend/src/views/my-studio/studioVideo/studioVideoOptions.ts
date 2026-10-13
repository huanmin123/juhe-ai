import type { StudioWorkbenchDomain } from '@/types/domain'

/** 视频工作台常量（规划 F2.3）：作用域、域、任务分页与上传上限。 */
export const VIDEO_WORKBENCH_SCOPE = 'my-studio/video'

export const videoWorkbenchDomain: StudioWorkbenchDomain = 'video'

export const studioVideoTaskPageSize = 10

/** 首帧上传大小口径（§4.2 上传单文件上限 25MB）。 */
export const studioVideoFirstFrameMaxBytes = 25 * 1024 * 1024
