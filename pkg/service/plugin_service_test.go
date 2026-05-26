package service

import (
	"fmt"
	"testing"
)

func TestComputePluginFingerprint_Deterministic(t *testing.T) {
	files := map[string][]byte{
		"plugin.json": []byte(`{"name":"test","version":"1.0.0"}`),
		"skills/a/SKILL.md": []byte("# skill a"),
	}

	fp1 := computePluginFingerprint(files)
	fp2 := computePluginFingerprint(files)

	if fp1 != fp2 {
		t.Errorf("fingerprint not deterministic: %s != %s", fp1, fp2)
	}
	if len(fp1) != 64 {
		t.Errorf("fingerprint length = %d, want 64 (sha256 hex)", len(fp1))
	}
}

func TestComputePluginFingerprint_DifferentFiles(t *testing.T) {
	files1 := map[string][]byte{
		"plugin.json": []byte(`{"name":"a","version":"1.0.0"}`),
	}
	files2 := map[string][]byte{
		"plugin.json": []byte(`{"name":"b","version":"1.0.0"}`),
	}

	fp1 := computePluginFingerprint(files1)
	fp2 := computePluginFingerprint(files2)

	if fp1 == fp2 {
		t.Error("different files should produce different fingerprints")
	}
}

func TestComputePluginFingerprint_OrderIndependent(t *testing.T) {
	files1 := map[string][]byte{
		"a.txt": []byte("aaa"),
		"b.txt": []byte("bbb"),
		"c.txt": []byte("ccc"),
	}
	files2 := map[string][]byte{
		"c.txt": []byte("ccc"),
		"a.txt": []byte("aaa"),
		"b.txt": []byte("bbb"),
	}

	fp1 := computePluginFingerprint(files1)
	fp2 := computePluginFingerprint(files2)

	if fp1 != fp2 {
		t.Errorf("fingerprint should be order-independent: %s != %s", fp1, fp2)
	}
}

func TestBuildFilesManifest(t *testing.T) {
	files := map[string][]byte{
		"plugin.json": []byte(`{"name":"test"}`),
		"skills/a/SKILL.md": []byte("content"),
	}

	manifest := buildFilesManifest(files)
	if len(manifest) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(manifest))
	}

	// Should be sorted alphabetically
	if manifest[0].Path != "plugin.json" {
		t.Errorf("manifest[0].Path = %q, want %q", manifest[0].Path, "plugin.json")
	}
	if manifest[1].Path != "skills/a/SKILL.md" {
		t.Errorf("manifest[1].Path = %q", manifest[1].Path)
	}

	// Size should match
	if manifest[0].Size != len(`{"name":"test"}`) {
		t.Errorf("manifest[0].Size = %d, want %d", manifest[0].Size, len(`{"name":"test"}`))
	}

	// SHA256 should be non-empty
	if len(manifest[0].SHA256) != 64 {
		t.Errorf("manifest[0].SHA256 length = %d, want 64", len(manifest[0].SHA256))
	}
}

func TestValidatePluginManifest_Valid(t *testing.T) {
	manifest := []byte(`{
		"name": "test-plugin",
		"version": "1.0.0",
		"skills": {"entries": ["greet"]}
	}`)
	files := map[string][]byte{
		"plugin.json": manifest,
		"skills/greet/SKILL.md": []byte("# greet"),
	}

	err := validatePluginManifest(manifest, files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePluginManifest_MissingName(t *testing.T) {
	manifest := []byte(`{"version": "1.0.0"}`)
	files := map[string][]byte{"plugin.json": manifest}

	err := validatePluginManifest(manifest, files)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestValidatePluginManifest_MissingVersion(t *testing.T) {
	manifest := []byte(`{"name": "test"}`)
	files := map[string][]byte{"plugin.json": manifest}

	err := validatePluginManifest(manifest, files)
	if err == nil {
		t.Fatal("expected error for missing version")
	}
}

func TestValidatePluginManifest_MissingSkillFile(t *testing.T) {
	manifest := []byte(`{
		"name": "test",
		"version": "1.0.0",
		"skills": {"entries": ["missing"]}
	}`)
	files := map[string][]byte{"plugin.json": manifest}

	err := validatePluginManifest(manifest, files)
	if err == nil {
		t.Fatal("expected error for missing skill file")
	}
}

func TestValidatePluginManifest_CustomSkillsPath(t *testing.T) {
	manifest := []byte(`{
		"name": "test",
		"version": "1.0.0",
		"skills": {"path": "my-skills", "entries": ["tool"]}
	}`)
	files := map[string][]byte{
		"plugin.json": manifest,
		"my-skills/tool/SKILL.md": []byte("# tool"),
	}

	err := validatePluginManifest(manifest, files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePluginManifest_InvalidJSON(t *testing.T) {
	err := validatePluginManifest([]byte(`{not json`), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("record not found"), true},
		{fmt.Errorf("some: record not found in db"), true},
		{fmt.Errorf("other error"), false},
	}

	for _, tt := range tests {
		got := isNotFound(tt.err)
		if got != tt.want {
			t.Errorf("isNotFound(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
