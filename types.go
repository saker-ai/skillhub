package skillhub

import (
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/service"
)

// 本文件集中再导出（type alias）SkillHub 对外暴露的核心数据类型。
// 通过 alias 而非重新声明，保证嵌入方与内部 service / model 之间
// 不会因为类型不一致而需要额外转换。
//
// 后续阶段（特别是 Identity 抽象上线后）可能将其中部分别名替换为
// 真正的对外类型，本文件会成为兼容层的入口。

// PublishRequest 是发布一个 skill 版本的参数。
// 透传到内部 service.PublishRequest，保持兼容。
type PublishRequest = service.PublishRequest

// Skill 是 skill 的对外表示，包含 owner 信息。
type Skill = model.SkillWithOwner

// SkillVersion 是版本对外表示。
type SkillVersion = model.SkillVersion

// User 是用户对外表示（后续阶段会替换为 Principal 抽象）。
type User = model.User
