package recordmaintenance

import (
)

// drain 循环的可达错误臂：上下文取消、普通任务 RunOnce 失败、快照任务段
// RunOnce 失败。均复用 mockRunner 注入失败 + SQLite 种子行。

