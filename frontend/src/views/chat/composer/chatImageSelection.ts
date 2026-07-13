export const maxChatImageCount = 4
export const maxChatImageBytes = 4 * 1024 * 1024

export function selectChatImageFiles(files: readonly File[], existingCount: number): File[] {
  const remaining = Math.max(0, maxChatImageCount - Math.max(0, existingCount))
  if (remaining === 0) return []
  return files
    .filter((file) => file.type.startsWith('image/') && file.size <= maxChatImageBytes)
    .slice(0, remaining)
}
