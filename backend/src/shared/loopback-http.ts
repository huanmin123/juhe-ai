const loopbackHostnames = new Set(['localhost', '127.0.0.1', '::1', '[::1]'])

// parseLoopbackHttpUrl is the common boundary for the intentionally local
// Node -> Go handoff. Callers must not turn a configuration typo into an
// implicit cross-service request.
export function parseLoopbackHttpUrl(value: string, label: string): URL {
  let url: URL
  try {
    url = new URL(value)
  } catch {
    throw new Error(`${label} 必须是有效 URL`)
  }
  if (
    url.protocol !== 'http:' ||
    !loopbackHostnames.has(url.hostname) ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    throw new Error(`${label} 必须是本机 loopback HTTP 地址`)
  }
  return url
}
