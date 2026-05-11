// Package git 是 SkillHub 的 git backend 子包。
//
// 通过 init() 自注册到 store 驱动表，调用方只需 blank import：
//
//	import _ "github.com/cinience/skillhub/pkg/store/git"
//
// 即可让 cfg.Store.Backend == "" / "git" 自动解析到本 backend。
package git

import (
	"context"
	"fmt"
	"io"

	"github.com/cinience/skillhub/pkg/gitstore"
	"github.com/cinience/skillhub/pkg/store"
)

func init() {
	store.Register("git", openGit)
}

// openGit 是 store.Factory 实现：从 OpenContext 取已构造好的 gitstore，
// 包成符合 store.Store 接口的 Backend。
func openGit(oc store.OpenContext) (store.Store, error) {
	if oc.GS == nil {
		return nil, fmt.Errorf("store/git: gitstore is nil — caller must construct it before Open")
	}
	return &Backend{gs: oc.GS}, nil
}

// Backend wraps *gitstore.GitStore to implement the store.Store interface.
type Backend struct {
	gs *gitstore.GitStore
}

// New 是直接构造入口，便于宿主代码不走 driver registry 时复用。
// 推荐路径仍是 store.Open("git", ...)。
func New(gs *gitstore.GitStore) *Backend {
	return &Backend{gs: gs}
}

func (g *Backend) Publish(ctx context.Context, opts store.PublishOpts) (string, error) {
	return g.gs.Publish(ctx, gitstore.PublishOpts{
		Owner:   opts.Owner,
		Slug:    opts.Slug,
		Version: opts.Version,
		Files:   opts.Files,
		Author:  opts.Author,
		Email:   opts.Email,
		Message: opts.Message,
		Tags:    opts.Tags,
	})
}

func (g *Backend) Archive(owner, slug, version string) (io.ReadCloser, error) {
	return g.gs.Archive(owner, slug, version)
}

func (g *Backend) GetFile(owner, slug, version, path string) ([]byte, error) {
	return g.gs.GetFile(owner, slug, version, path)
}

func (g *Backend) ListVersions(owner, slug string) ([]string, error) {
	return g.gs.ListTags(owner, slug)
}

func (g *Backend) Exists(owner, slug string) bool {
	return g.gs.Exists(owner, slug)
}

func (g *Backend) Rename(owner, oldSlug, newSlug string) error {
	return g.gs.Rename(owner, oldSlug, newSlug)
}
