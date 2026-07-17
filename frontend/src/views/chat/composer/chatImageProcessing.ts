import Compressor from 'compressorjs'

export const chatImageUploadMaxEdge = 1024
export const chatImageUploadJpegQuality = 0.85
export const chatImageUploadMaxBytes = 1024 * 1024

export interface ChatDecodedImage {
  width: number
  height: number
  source?: CanvasImageSource
  close?: () => void
}

export interface ChatImageProcessingRuntime {
  decode: (file: File) => Promise<ChatDecodedImage>
  encodeJpeg: (
    image: ChatDecodedImage,
    size: { width: number; height: number },
    quality: number
  ) => Promise<Blob>
}

export function resolveChatImageUploadSize(width: number, height: number): { width: number; height: number } {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) {
    throw new Error('图片尺寸无效')
  }
  const scale = Math.min(1, chatImageUploadMaxEdge / Math.max(width, height))
  return {
    width: Math.max(1, Math.round(width * scale)),
    height: Math.max(1, Math.round(height * scale))
  }
}

export async function prepareChatImageForUpload(
  file: File,
  runtime?: ChatImageProcessingRuntime
): Promise<File> {
  let blob: Blob
  if (runtime) {
    const decoded = await runtime.decode(file)
    try {
      blob = await runtime.encodeJpeg(decoded, resolveChatImageUploadSize(decoded.width, decoded.height), chatImageUploadJpegQuality)
    } finally {
      decoded.close?.()
    }
  } else {
    blob = await compressWithCompressorJs(file)
  }
  if (blob.type !== 'image/jpeg' || blob.size <= 0) throw new Error('图片压缩失败')
  if (blob.size > chatImageUploadMaxBytes) {
    throw new Error('图片压缩后仍超过 1 MiB，请裁剪图片后重试')
  }
  return new File([blob], jpegFilename(file.name), {
    type: 'image/jpeg',
    lastModified: file.lastModified
  })
}

function compressWithCompressorJs(file: File): Promise<Blob> {
  return new Promise((resolve, reject) => {
    new Compressor(file, {
      strict: false,
      checkOrientation: true,
      retainExif: false,
      maxWidth: chatImageUploadMaxEdge,
      maxHeight: chatImageUploadMaxEdge,
      mimeType: 'image/jpeg',
      quality: chatImageUploadJpegQuality,
      convertSize: 0,
      beforeDraw(context, canvas) {
        context.fillStyle = '#ffffff'
        context.fillRect(0, 0, canvas.width, canvas.height)
      },
      success: resolve,
      error: reject
    })
  })
}

function jpegFilename(filename: string): string {
  const base = filename.trim().replace(/\.[^.]+$/, '') || '图片'
  return `${base}.jpg`
}
