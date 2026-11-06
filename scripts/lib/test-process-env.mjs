// 测试子进程环境：Windows 开发机的 Go 提交内存约束处理。
//
// 2026-11-06 全量基线实测：默认 GOGC/GOMAXPROCS 下，gateway internal 的
// 74 包串行编译在第 12 个包起连续 `VirtualAlloc failed` / `cannot allocate
// memory`（build failed，非测试失败）；同一批包改用 GOGC=20、GOMAXPROCS=1
// 后全部通过。这里只在 win32 且调用方未显式设置时补默认值，Linux/CI 行为
// 不变，也不改变测试范围与断言。
export function testProcessEnv(base = process.env) {
  const env = { ...base }
  if (process.platform === 'win32') {
    env.GOGC ??= '20'
    env.GOMAXPROCS ??= '1'
  }
  return env
}
