import type { StudioWorkbenchDomain } from '@/types/domain'

/** 图像工作台常量（规划 F2.1）：轮询作用域、域、画廊分页与断点口径。 */
export const IMAGE_WORKBENCH_SCOPE = 'my-studio/images'

export const imageWorkbenchDomain: StudioWorkbenchDomain = 'image'

export const studioImageGalleryPageSize = 24

/** 参考图上传大小口径对齐 /v1 同步上传体事实约束（§4.2 单文件上限 25MB）。 */
export const studioImageMaxUploadBytes = 25 * 1024 * 1024
