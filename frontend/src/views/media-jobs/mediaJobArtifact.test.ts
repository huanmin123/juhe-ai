import { describe, expect, it } from 'vitest'
import { safeArtifactContentUrl } from './mediaJobArtifact'

describe('safeArtifactContentUrl 产物地址白名单', () => {
  it('http/https 绝对地址原样返回', () => {
    expect(safeArtifactContentUrl('https://upstream.example/content.mp4?token=abc')).toBe(
      'https://upstream.example/content.mp4?token=abc'
    )
    expect(safeArtifactContentUrl('http://upstream.example/content.mp4')).toBe('http://upstream.example/content.mp4')
    // 大小写 scheme 由 URL 归一，仍属 http/https 白名单。
    expect(safeArtifactContentUrl('HTTPS://upstream.example/content.mp4')).toBe('HTTPS://upstream.example/content.mp4')
    expect(safeArtifactContentUrl('  https://upstream.example/content.mp4  ')).toBe('https://upstream.example/content.mp4')
  })

  it('可执行 scheme 与相对路径返回空串', () => {
    expect(safeArtifactContentUrl('javascript:alert(1)')).toBe('')
    expect(safeArtifactContentUrl('data:text/html;base64,PHNjcmlwdD4=')).toBe('')
    expect(safeArtifactContentUrl('file:///etc/passwd')).toBe('')
    expect(safeArtifactContentUrl('ftp://upstream.example/content.mp4')).toBe('')
    expect(safeArtifactContentUrl('/content/video.mp4')).toBe('')
    expect(safeArtifactContentUrl('upstream.example/content.mp4')).toBe('')
    expect(safeArtifactContentUrl('//upstream.example/content.mp4')).toBe('')
  })

  it('空值与不可解析地址返回空串', () => {
    expect(safeArtifactContentUrl('')).toBe('')
    expect(safeArtifactContentUrl('   ')).toBe('')
    expect(safeArtifactContentUrl(null)).toBe('')
    expect(safeArtifactContentUrl(undefined)).toBe('')
    expect(safeArtifactContentUrl('https://')).toBe('')
  })
})
