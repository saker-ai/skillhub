// Package repository: SkillCache 是 SkillRepo 的内存级查询缓存。
//
// 为什么放进 repository 包而不是独立 cache 包：cache 的失效语义跟
// SkillRepo.GetBySlug / GetBySlugOrAlias 的访问路径绑死，service 层只看到
// SkillRepo.InvalidateCache(slug) 这一个出口；如果拆成独立包，service 层
// 还要分别注入 *Cache，反而泄露内部细节。
//
// 选型：基于 hashicorp/golang-lru/v2 的 expirable.LRU——它是社区里最成熟、
// 多年生产验证过的 LRU 实现（Vault / Nomad / Terraform 都在用）。
// 我们没自己用 list+map 拼一个，原因是：
//   - thread safety 已在底层用 mutex 实现，省掉一层锁推理；
//   - TTL 过期是按 lazy + tick 机制清理的，不会单独起 goroutine；
//   - 0 容量场景下底层会 panic（NewLRU(0,...) 不允许），构造方在 size<=0
//     时直接返回 nil，让 repo 走"无缓存"路径。
package repository

import (
	"time"

	"github.com/cinience/skillhub/pkg/model"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/prometheus/client_golang/prometheus"
)

// SkillCache 包装 *expirable.LRU 加上可选的命中/未命中 prometheus 计数器。
// 所有方法在接收者为 nil 时安全（caller 不必判空）——这让 SkillRepo
// 在配置关闭缓存时只持有 nil 指针即可，完全不需要 if c != nil 散落各处。
type SkillCache struct {
	lru    *lru.LRU[string, *model.SkillWithOwner]
	hits   prometheus.Counter
	misses prometheus.Counter
}

// NewSkillCache 创建一个固定容量 + TTL 的缓存。size <= 0 返回 nil。
//
// hits/misses 可为 nil（嵌入方未注入 metrics 时）；非 nil 时每次 Get 调用
// 都会按命中状态自增。
func NewSkillCache(size int, ttl time.Duration, hits, misses prometheus.Counter) *SkillCache {
	if size <= 0 {
		return nil
	}
	// expirable.NewLRU 第三个参数是 onEvict 回调，缓存条目被 LRU 淘汰或 TTL
	// 过期时触发——我们没用，传 nil；ttl <= 0 时 LRU 退化为不带 TTL 的纯 LRU。
	return &SkillCache{
		lru:    lru.NewLRU[string, *model.SkillWithOwner](size, nil, ttl),
		hits:   hits,
		misses: misses,
	}
}

// Get 返回缓存条目的浅拷贝。返回浅拷贝是因为上层 service 会对返回结果做
// 字段重赋值（PublishVersion 里 skill.DisplayName = &req.DisplayName）——
// 如果直接把内部指针交出去，下次同 slug 命中会读到被前一次调用篡改的字段。
//
// 对于切片 / 指针字段（Tags / DisplayName 等），浅拷贝只复制 header / 指针本身，
// caller 的*reassign*操作（写新指针）安全；caller 如果自己 mutate 切片元素
// 会污染缓存——目前 service 层没这种用法，按浅拷贝足够。
func (c *SkillCache) Get(key string) (*model.SkillWithOwner, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.lru.Get(key)
	if ok {
		if c.hits != nil {
			c.hits.Inc()
		}
		cp := *v
		return &cp, true
	}
	if c.misses != nil {
		c.misses.Inc()
	}
	return nil, false
}

// Set 写入缓存。value == nil 时 noop——Service 层"slug 不存在"返回 nil,
// 我们不希望把"不存在"也缓存起来：之后用户立刻发布同名 skill 应该立刻可见。
func (c *SkillCache) Set(key string, value *model.SkillWithOwner) {
	if c == nil || value == nil {
		return
	}
	cp := *value
	c.lru.Add(key, &cp)
}

// Invalidate 删除一组 key（通常是同一个 skill 的 slug 加旧 alias）。
// 不存在的 key 静默跳过。
func (c *SkillCache) Invalidate(keys ...string) {
	if c == nil {
		return
	}
	for _, k := range keys {
		c.lru.Remove(k)
	}
}

// Purge 清空整个缓存。仅供测试 / 运维场景使用，正常路径都走 Invalidate。
func (c *SkillCache) Purge() {
	if c == nil {
		return
	}
	c.lru.Purge()
}
