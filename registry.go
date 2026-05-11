package skillhub

import "sync"

// 阶段 3 会启用本文件，定义 Store / Search / Identity 的 driver 注册模式。
// 阶段 1 仅占位，确保后续 PR 不需要新建文件，也不会改动包对外可见的符号集合。

var (
	// registryMu 预留给后续阶段的 driver 注册表使用。
	// 阶段 1 仅声明，不参与任何并发逻辑。
	registryMu sync.RWMutex //nolint:unused // 阶段 3 启用
)
