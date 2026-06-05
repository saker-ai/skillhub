package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/saker-ai/skillhub/pkg/cli"
)

func TestReadPluginDirFilesIncludesCodexPluginManifest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, ".codex-plugin", "plugin.json"), `{"name":"demo","version":"1.0.0"}`)
	mustWriteFile(t, filepath.Join(dir, ".git", "config"), "ignored")
	mustWriteFile(t, filepath.Join(dir, ".env"), "ignored")
	mustWriteFile(t, filepath.Join(dir, "skills", "demo", "SKILL.md"), "---\nname: demo\n---\n")

	files, err := cli.ReadPluginDirFiles(dir)
	if err != nil {
		t.Fatalf("ReadPluginDirFiles: %v", err)
	}
	if string(files[".codex-plugin/plugin.json"]) == "" {
		t.Fatalf(".codex-plugin/plugin.json was not included: %+v", files)
	}
	if string(files["skills/demo/SKILL.md"]) == "" {
		t.Fatalf("skills/demo/SKILL.md was not included: %+v", files)
	}
	if _, ok := files[".git/config"]; ok {
		t.Fatalf(".git/config should be ignored")
	}
	if _, ok := files[".env"]; ok {
		t.Fatalf(".env should be ignored")
	}
}

func mustWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
