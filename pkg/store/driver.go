package store

import (
	"fmt"
	"sort"
	"sync"

	"github.com/cinience/skillhub/pkg/config"
	"github.com/cinience/skillhub/pkg/gitstore"
)

// OpenContext 汇集了所有 backend 在构造时可能用到的依赖。
//
// 每个 backend 只读取自己关心的字段：
//   - git    → GS (gitstore 实例)
//   - s3     → Cfg.S3
//   - oss    → Cfg.OSS
//   - 自定义 → 嵌入方可通过 store.Register 接管，自行从 Cfg 读取
//
// 引入 OpenContext 而不是直接传 *config.Config 是为了：
// 1) 保持 store 包不依赖 server/handler；
// 2) 嵌入方扩展自定义 backend 时无需感知 SkillHub 完整 Config 结构。
type OpenContext struct {
	// Cfg 是 store 子树的配置（含 Backend 名、S3、OSS 等子字段）。
	Cfg config.StoreConfig

	// GS 是已经构造好的 gitstore 实例。即使 backend 不是 git，
	// 也会被传入——例如 Mirror / Import 仍然依赖 git 仓库镜像。
	GS *gitstore.GitStore
}

// Factory 是某个 backend 的构造函数。返回的 Store 实例由调用方持有；
// 构造失败时返回 (nil, err)。
type Factory func(OpenContext) (Store, error)

var (
	driversMu sync.RWMutex
	drivers   = map[string]Factory{}
)

// Register 在驱动表中登记一个 backend。
//
// 同名驱动重复注册会 panic，与 database/sql、image.RegisterFormat 行为一致——
// 这种错误几乎一定是导入路径写错或两个包注册了同一个 name，
// 静默覆盖会让定位问题变得非常困难。
//
// 一般在子包 init() 中调用，cmd/skillhub 通过 blank import 触发注册。
func Register(name string, f Factory) {
	if f == nil {
		panic("store: Register factory is nil")
	}
	driversMu.Lock()
	defer driversMu.Unlock()
	if _, dup := drivers[name]; dup {
		panic(fmt.Sprintf("store: Register called twice for driver %q", name))
	}
	drivers[name] = f
}

// Open 根据 name 在驱动表中查找并实例化对应 backend。
//
// 历史兼容：name == "" 视为 "git"，与重构前 server.go switch 默认分支一致。
//
// 找不到驱动时返回明确的错误并附带已注册驱动列表，便于嵌入方排查
// 是否漏了 blank import（最常见的踩坑场景）。
func Open(name string, oc OpenContext) (Store, error) {
	if name == "" {
		name = "git"
	}
	driversMu.RLock()
	f, ok := drivers[name]
	driversMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("store: unknown backend %q (registered: %v)", name, Drivers())
	}
	return f(oc)
}

// Drivers 返回当前已注册的 backend 名列表（按字典序），用于诊断与单测。
func Drivers() []string {
	driversMu.RLock()
	defer driversMu.RUnlock()
	out := make([]string, 0, len(drivers))
	for k := range drivers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
