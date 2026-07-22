import { spawn, spawnSync, type ChildProcess } from 'node:child_process'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

const port = 41799
const debuggingPort = 41800
const frontendDirectory = fileURLToPath(new URL('../../..', import.meta.url))
const packageCommand = process.platform === 'win32' ? 'pnpm.cmd' : 'pnpm'
const server = spawn(packageCommand, ['exec', 'vite', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: frontendDirectory, stdio: 'ignore', shell: process.platform === 'win32' })
const profile = mkdtempSync(join(tmpdir(), 'juhe-chat-idb-'))

function wait(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}

function waitForProcessClose(child: ChildProcess, timeoutMilliseconds: number): Promise<boolean> {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve(true)
  return new Promise((resolve) => {
    const timeout = setTimeout(() => { child.off('close', onClose); resolve(false) }, timeoutMilliseconds)
    const onClose = () => { clearTimeout(timeout); resolve(true) }
    child.once('close', onClose)
  })
}

async function closeBrowserOverCdp(browser: ChildProcess): Promise<boolean> {
  let socket: WebSocket | undefined
  try {
    const version = await (await fetch(`http://127.0.0.1:${debuggingPort}/json/version`)).json() as { webSocketDebuggerUrl?: string }
    if (!version.webSocketDebuggerUrl) return false
    socket = new WebSocket(version.webSocketDebuggerUrl)
    await new Promise<void>((resolve, reject) => {
      socket!.addEventListener('open', () => resolve(), { once: true })
      socket!.addEventListener('error', () => reject(new Error('Chrome browser debugging connection failed')), { once: true })
    })
    const closed = waitForProcessClose(browser, 10_000)
    socket.send(JSON.stringify({ id: 1, method: 'Browser.close' }))
    return await closed
  } catch {
    return false
  } finally {
    if (socket?.readyState === WebSocket.OPEN) socket.close()
  }
}

function browserPath(): string {
  const configured = process.env.CHROME_PATH
  const candidates = [configured, process.platform === 'win32' ? 'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe' : undefined, process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : undefined, '/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser']
  const found = candidates.find((candidate): candidate is string => Boolean(candidate && existsSync(candidate)))
  if (!found) throw new Error('Chrome/Chromium unavailable; set CHROME_PATH')
  return found
}

async function waitForServer(): Promise<void> {
  for (let attempt = 0; attempt < 80; attempt += 1) {
    if (server.exitCode !== null) throw new Error(`Vite exited with ${server.exitCode}`)
    try { if ((await fetch(`http://127.0.0.1:${port}/__aisys__/chat-local-cache-indexeddb-regression.html`)).ok) return } catch { /* retry */ }
    await wait(100)
  }
  throw new Error('Timed out waiting for Vite')
}

try {
  await waitForServer()
  const browser = spawn(browserPath(), ['--headless=new', '--disable-gpu', '--disable-crash-reporter', '--disable-breakpad', '--no-first-run', '--no-default-browser-check', `--remote-debugging-port=${debuggingPort}`, `--user-data-dir=${profile}`, `http://127.0.0.1:${port}/__aisys__/chat-local-cache-indexeddb-regression.html`], { stdio: 'ignore' })
  try {
    let target: { webSocketDebuggerUrl?: string } | undefined
    for (let attempt = 0; attempt < 100; attempt += 1) {
      try { const targets = await (await fetch(`http://127.0.0.1:${debuggingPort}/json/list`)).json() as Array<{ url?: string; webSocketDebuggerUrl?: string }>; target = targets.find((item) => item.url?.includes('chat-local-cache-indexeddb-regression.html')); if (target?.webSocketDebuggerUrl) break } catch { /* retry */ }
      await wait(100)
    }
    if (!target?.webSocketDebuggerUrl) throw new Error('Timed out waiting for Chrome target')
    const socket = new WebSocket(target.webSocketDebuggerUrl)
    await new Promise<void>((resolve, reject) => { socket.addEventListener('open', () => resolve(), { once: true }); socket.addEventListener('error', () => reject(new Error('Chrome debugging connection failed')), { once: true }) })
    let nextId = 0
    const pending = new Map<number, (value: { result?: { result?: { value?: { status?: string; text?: string } }; exceptionDetails?: unknown }; error?: unknown }) => void>()
    socket.addEventListener('message', (event) => { const value = JSON.parse(String(event.data)) as { id?: number }; if (value.id !== undefined) { pending.get(value.id)?.(value as never); pending.delete(value.id) } })
    const evaluate = () => new Promise<{ result?: { result?: { value?: { status?: string; text?: string } }; exceptionDetails?: unknown }; error?: unknown }>((resolve) => { const id = ++nextId; pending.set(id, resolve); socket.send(JSON.stringify({ id, method: 'Runtime.evaluate', params: { returnByValue: true, expression: `({ status: document.body?.dataset.status, text: document.querySelector('#result')?.textContent })` } })) })
    let response: Awaited<ReturnType<typeof evaluate>> | undefined
    for (let attempt = 0; attempt < 300; attempt += 1) { response = await evaluate(); if (response.result?.result?.value?.status) break; await wait(100) }
    socket.close()
    const value = response?.result?.result?.value
    if (response?.result?.exceptionDetails || value?.status !== 'passed') throw new Error(`Native IndexedDB browser regression failed: ${value?.text ?? JSON.stringify(response).slice(0, 600)}`)
  } finally {
    const closedGracefully = await closeBrowserOverCdp(browser)
    if (!closedGracefully) {
      if (process.platform === 'win32' && browser.pid) spawnSync('taskkill.exe', ['/pid', String(browser.pid), '/t', '/f'], { stdio: 'ignore' })
      else browser.kill('SIGTERM')
      await waitForProcessClose(browser, 10_000)
    }
  }
  console.log('AI 问答原生 IndexedDB 浏览器回归通过')
} finally {
  if (process.platform === 'win32' && server.pid) spawnSync('taskkill.exe', ['/pid', String(server.pid), '/t', '/f'], { stdio: 'ignore' })
  else server.kill('SIGTERM')
  rmSync(profile, { recursive: true, force: true, maxRetries: 20, retryDelay: 100 })
}
