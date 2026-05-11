package store_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cinience/skillhub/pkg/store"
)

// fakeBackend 是只用于测试的 store.Store 实现。
type fakeBackend struct{ name string }

func (f *fakeBackend) Publish(context.Context, store.PublishOpts) (string, error) {
	return f.name, nil
}
func (*fakeBackend) Archive(string, string, string) (io.ReadCloser, error)  { return nil, nil }
func (*fakeBackend) GetFile(string, string, string, string) ([]byte, error) { return nil, nil }
func (*fakeBackend) ListVersions(string, string) ([]string, error)          { return nil, nil }
func (*fakeBackend) Exists(string, string) bool                             { return false }
func (*fakeBackend) Rename(string, string, string) error                    { return nil }

// 阶段 3 注册表覆盖：name 默认值 + 未知 driver + 重复注册 panic。
//
// 使用一次性 driver name "fake-driver-test"——避免与 git/s3/oss 的 init() 撞车。
// 由于 driver 表是包级全局，本测试不能 t.Parallel()。
func TestDriverRegistry(t *testing.T) {
	store.Register("fake-driver-test", func(store.OpenContext) (store.Store, error) {
		return &fakeBackend{name: "fake"}, nil
	})

	// "" 必须默认走 git；本测试中 git 未注册导致 git lookup 失败也算通过——
	// 我们只验证默认逻辑命中 "git" 这个 name。
	_, err := store.Open("", store.OpenContext{})
	if err == nil {
		// 如果意外 git 已注册（init 顺序），开放调用也算成功。
		// 我们只关心「不应当返回 unknown backend ""」。
	} else if !strings.Contains(err.Error(), `"git"`) {
		t.Fatalf("Open(\"\") should default to git lookup, got: %v", err)
	}

	// 命中已注册 driver。
	got, err := store.Open("fake-driver-test", store.OpenContext{})
	if err != nil {
		t.Fatalf("Open(fake-driver-test) error: %v", err)
	}
	out, _ := got.Publish(context.Background(), store.PublishOpts{})
	if out != "fake" {
		t.Fatalf("expected fake-driver-test backend, got %q", out)
	}

	// 未知 driver 必须给出明确错误并附带已注册列表。
	_, err = store.Open("nonexistent-driver", store.OpenContext{})
	if err == nil {
		t.Fatal("Open(nonexistent) should error")
	}
	if !strings.Contains(err.Error(), "unknown backend") || !strings.Contains(err.Error(), "fake-driver-test") {
		t.Fatalf("error should include unknown-backend hint and registered list, got: %v", err)
	}

	// 重复注册 panic。
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Register should panic on duplicate name")
		}
	}()
	store.Register("fake-driver-test", func(store.OpenContext) (store.Store, error) {
		return nil, errors.New("dup")
	})
}

func TestSanitizeStorePath(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"foo/bar":          "foo/bar",
		"/foo/bar":         "foo/bar",
		"./foo/bar":        "foo/bar",
		"foo/../bar":       "bar",
		"../etc/passwd":    "invalid",
		"foo/../../secret": "invalid",
	}
	for in, want := range cases {
		if got := store.SanitizeStorePath(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
