package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func Plugins(args []string) {
	if len(args) == 0 {
		printPluginUsage()
		return
	}
	switch args[0] {
	case "publish":
		pluginPublish(args[1:])
	case "list", "ls":
		pluginList(args[1:])
	case "inspect", "show":
		pluginInspect(args[1:])
	case "install":
		pluginInstall(args[1:])
	case "uninstall", "remove", "rm":
		pluginUninstall(args[1:])
	case "installed":
		pluginInstalled(args[1:])
	case "update":
		pluginUpdate(args[1:])
	case "delete":
		pluginDelete(args[1:])
	case "undelete":
		pluginUndelete(args[1:])
	case "yank":
		pluginYank(args[1:])
	case "unyank":
		pluginUnyank(args[1:])
	case "help", "--help", "-h":
		printPluginUsage()
	default:
		exitWithError(fmt.Sprintf("unknown plugin subcommand: %s\n\nRun `skillhub plugin help` for usage.", args[0]))
	}
}

func printPluginUsage() {
	fmt.Println(`Usage: skillhub plugin <command> [arguments]

Commands:
  publish <path> [flags]                   Publish a Codex plugin directory
    --slug <slug>                           Plugin slug (or manifest name)
    --version <version>                     Semantic version (or manifest version)
    --namespace <namespace>                 Publish under a namespace
    --category <category>                   Category
    --tags <tags>                           Comma-separated tags
    --summary <text>                        Short description
    --changelog <text>                      Changelog for this version

  list [--sort <field>]                     List public plugins from the registry
  inspect <slug|@namespace/slug>            Show plugin metadata and versions
  install <slug|@namespace/slug> [flags]    Install a plugin locally
    --version <version>                     Version to install (default: latest)
    --dir <path>                            Parent plugin directory (default: plugins_dir or ~/plugins)
  uninstall <slug> [--dir <path>]           Remove a locally installed plugin
  installed [--dir <path>]                  List locally installed plugins
  update [--all] [slug...] [--dir <path>]   Update installed plugins

  delete <slug|@namespace/slug>             Soft-delete a plugin in the registry
  undelete <slug|@namespace/slug>           Restore a deleted plugin
  yank <slug|@namespace/slug> <version>     Yank a plugin version
    --reason <text>                         Optional yank reason
  unyank <slug|@namespace/slug> <version>   Unyank a plugin version`)
}

func pluginPublish(args []string) {
	if len(args) == 0 {
		exitWithError("Usage: skillhub plugin publish <path> [--slug <slug>] [--version <version>]")
	}
	dirPath := args[0]
	rest := args[1:]
	slug := getFlag(rest, "--slug")
	version := getFlag(rest, "--version")
	namespace := getFlag(rest, "--namespace")
	tags := getFlag(rest, "--tags")
	summary := getFlag(rest, "--summary")
	changelog := getFlag(rest, "--changelog")
	category := getFlag(rest, "--category")

	info, err := os.Stat(dirPath)
	if err != nil || !info.IsDir() {
		exitWithError(fmt.Sprintf("%q is not a valid directory", dirPath))
	}
	files, err := ReadPluginDirFiles(dirPath)
	if err != nil {
		exitWithError(fmt.Sprintf("reading directory: %v", err))
	}
	if len(files) == 0 {
		exitWithError("no files found in directory")
	}
	manifest := pluginManifestForPublish(files)
	if manifest == nil {
		exitWithError("plugin.json or .codex-plugin/plugin.json is required")
	}
	if _, ok := files["plugin.json"]; !ok {
		files["plugin.json"] = manifest
	}
	if slug == "" {
		slug = extractJSONField(manifest, "name")
	}
	if version == "" {
		version = extractJSONField(manifest, "version")
	}
	if slug == "" {
		exitWithError("--slug is required (or set name in plugin manifest)")
	}
	if version == "" {
		exitWithError("--version is required (or set version in plugin manifest)")
	}

	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}
	fmt.Printf("Publishing plugin %s@%s...\n", slug, version)
	fmt.Printf("  Files: %d\n", len(files))
	result, err := client.PublishPlugin(slug, version, summary, tags, changelog, category, namespace, files)
	if err != nil {
		exitWithError(err.Error())
	}
	if vMap, ok := result["version"].(map[string]interface{}); ok {
		printSuccess(fmt.Sprintf("Published plugin %s@%s", slug, getStr(vMap, "version")))
		return
	}
	printSuccess(fmt.Sprintf("Published plugin %s", slug))
}

func pluginList(args []string) {
	sort := "created"
	for i, a := range args {
		if a == "--sort" && i+1 < len(args) {
			sort = args[i+1]
		}
	}
	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}
	result, err := client.ListPlugins(sort, 20)
	if err != nil {
		exitWithError(err.Error())
	}
	plugins := getSlice(result, "data")
	if len(plugins) == 0 {
		fmt.Println("No plugins found.")
		return
	}
	headers := []string{"SLUG", "NAME", "SUMMARY", "DOWNLOADS", "STARS"}
	widths := []int{25, 25, 35, 10, 6}
	var rows [][]string
	for _, p := range plugins {
		m, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		rows = append(rows, []string{
			pluginDisplayRef(m),
			getStr(m, "displayName"),
			getStr(m, "summary"),
			getNum(m, "downloads"),
			getNum(m, "starsCount"),
		})
	}
	printTable(headers, widths, rows)
}

func pluginInspect(args []string) {
	if len(args) == 0 {
		exitWithError("Usage: skillhub plugin inspect <slug|@namespace/slug>")
	}
	ref := args[0]
	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}
	plugin, err := client.GetPlugin(ref)
	if err != nil {
		exitWithError(err.Error())
	}
	versions, _ := client.GetPluginVersions(ref)
	printHeader(pluginDisplayRef(plugin))
	if name := getStr(plugin, "displayName"); name != "" {
		printField("Name", name)
	}
	if summary := getStr(plugin, "summary"); summary != "" {
		printField("Summary", summary)
	}
	printField("Owner", getStr(plugin, "ownerHandle"))
	printField("Namespace", getStr(plugin, "namespaceSlug"))
	printField("Downloads", getNum(plugin, "downloads"))
	printField("Stars", getNum(plugin, "starsCount"))
	printField("Visibility", getStr(plugin, "visibility"))
	printField("Created", getStr(plugin, "createdAt"))
	if versions != nil {
		if vList := getSlice(versions, "versions"); len(vList) > 0 {
			fmt.Println()
			printHeader("Versions")
			headers := []string{"VERSION", "FINGERPRINT", "CREATED", "YANKED"}
			widths := []int{12, 20, 25, 8}
			var rows [][]string
			for _, v := range vList {
				m, ok := v.(map[string]interface{})
				if !ok {
					continue
				}
				fp := getStr(m, "fingerprint")
				if len(fp) > 18 {
					fp = fp[:18] + "..."
				}
				yanked := "no"
				if getStr(m, "yankedAt") != "" && getStr(m, "yankedAt") != "<nil>" {
					yanked = "yes"
				}
				rows = append(rows, []string{getStr(m, "version"), fp, getStr(m, "createdAt"), yanked})
			}
			printTable(headers, widths, rows)
		}
	}
}

func pluginInstall(args []string) {
	if len(args) == 0 {
		exitWithError("Usage: skillhub plugin install <slug|@namespace/slug> [--version <version>] [--dir <path>]")
	}
	ref := args[0]
	version := "latest"
	parentDir := getPluginDir(args[1:])
	if v := getFlag(args[1:], "--version"); v != "" {
		version = v
	}
	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}
	fmt.Printf("Downloading plugin %s@%s...\n", ref, version)
	body, err := client.DownloadPlugin(ref, version)
	if err != nil {
		exitWithError(err.Error())
	}
	defer body.Close()
	zipData, err := io.ReadAll(body)
	if err != nil {
		exitWithError(fmt.Sprintf("reading download: %v", err))
	}
	dirName := localSlug(ref)
	pluginDir := filepath.Join(parentDir, dirName)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		exitWithError(fmt.Sprintf("creating directory: %v", err))
	}
	clearPluginDir(pluginDir)
	if err := extractZipToDir(zipData, pluginDir); err != nil {
		exitWithError(err.Error())
	}
	meta := map[string]interface{}{
		"version":     version,
		"installedAt": time.Now().UTC().Format(time.RFC3339),
		"slug":        dirName,
		"ref":         ref,
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	_ = os.WriteFile(filepath.Join(pluginDir, ".installed.json"), metaData, 0o644)
	printSuccess(fmt.Sprintf("Installed plugin %s@%s to %s", ref, version, pluginDir))
}

func pluginUninstall(args []string) {
	if len(args) == 0 {
		exitWithError("Usage: skillhub plugin uninstall <slug> [--dir <path>]")
	}
	pluginDir := filepath.Join(getPluginDir(args[1:]), args[0])
	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		exitWithError(fmt.Sprintf("plugin %q is not installed", args[0]))
	}
	if err := os.RemoveAll(pluginDir); err != nil {
		exitWithError(fmt.Sprintf("removing plugin: %v", err))
	}
	printSuccess(fmt.Sprintf("Uninstalled plugin %s", args[0]))
}

func pluginInstalled(args []string) {
	dir := getPluginDir(args)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No plugins installed.")
			return
		}
		exitWithError(fmt.Sprintf("reading plugins directory: %v", err))
	}
	headers := []string{"SLUG", "VERSION", "INSTALLED AT"}
	widths := []int{30, 15, 25}
	var rows [][]string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metaPath := filepath.Join(dir, entry.Name(), ".installed.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			rows = append(rows, []string{entry.Name(), "unknown", "unknown"})
			continue
		}
		var meta map[string]interface{}
		_ = json.Unmarshal(data, &meta)
		rows = append(rows, []string{entry.Name(), getStr(meta, "version"), getStr(meta, "installedAt")})
	}
	if len(rows) == 0 {
		fmt.Println("No plugins installed.")
		return
	}
	printTable(headers, widths, rows)
}

func pluginUpdate(args []string) {
	parentDir := getPluginDir(args)
	updateAll := false
	targets := map[string]bool{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--all":
			updateAll = true
		case "--dir":
			i++
		default:
			if !strings.HasPrefix(args[i], "-") {
				targets[args[i]] = true
			}
		}
	}
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No plugins installed.")
			return
		}
		exitWithError(fmt.Sprintf("reading plugins directory: %v", err))
	}
	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}
	updated := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !updateAll && len(targets) > 0 && !targets[entry.Name()] {
			continue
		}
		metaPath := filepath.Join(parentDir, entry.Name(), ".installed.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta map[string]interface{}
		_ = json.Unmarshal(data, &meta)
		ref := getStr(meta, "ref")
		if ref == "" {
			ref = entry.Name()
		}
		current := getStr(meta, "version")
		versions, err := client.GetPluginVersions(ref)
		if err != nil {
			fmt.Printf("  %s: failed to check (%v)\n", ref, err)
			continue
		}
		vList := getSlice(versions, "versions")
		if len(vList) == 0 {
			continue
		}
		latest, _ := vList[0].(map[string]interface{})
		latestVer := getStr(latest, "version")
		if latestVer == "" || latestVer == current {
			fmt.Printf("  %s: up to date (%s)\n", ref, current)
			continue
		}
		fmt.Printf("  %s: %s -> %s, updating...\n", ref, current, latestVer)
		pluginInstall([]string{ref, "--version", latestVer, "--dir", parentDir})
		updated++
	}
	if updated == 0 {
		fmt.Println("All plugins are up to date.")
	} else {
		printSuccess(fmt.Sprintf("Updated %d plugin(s).", updated))
	}
}

func pluginDelete(args []string) {
	if len(args) == 0 {
		exitWithError("Usage: skillhub plugin delete <slug|@namespace/slug>")
	}
	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}
	if err := client.DeletePlugin(args[0]); err != nil {
		exitWithError(err.Error())
	}
	printSuccess(fmt.Sprintf("Deleted plugin %s", args[0]))
}

func pluginUndelete(args []string) {
	if len(args) == 0 {
		exitWithError("Usage: skillhub plugin undelete <slug|@namespace/slug>")
	}
	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}
	if err := client.UndeletePlugin(args[0]); err != nil {
		exitWithError(err.Error())
	}
	printSuccess(fmt.Sprintf("Restored plugin %s", args[0]))
}

func pluginYank(args []string) {
	if len(args) < 2 {
		exitWithError("Usage: skillhub plugin yank <slug|@namespace/slug> <version> [--reason <text>]")
	}
	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}
	reason := getFlag(args[2:], "--reason")
	if err := client.YankPluginVersion(args[0], args[1], reason); err != nil {
		exitWithError(err.Error())
	}
	printSuccess(fmt.Sprintf("Yanked plugin %s@%s", args[0], args[1]))
}

func pluginUnyank(args []string) {
	if len(args) < 2 {
		exitWithError("Usage: skillhub plugin unyank <slug|@namespace/slug> <version>")
	}
	client, err := NewClientFromConfig()
	if err != nil {
		exitWithError(err.Error())
	}
	if err := client.UnyankPluginVersion(args[0], args[1]); err != nil {
		exitWithError(err.Error())
	}
	printSuccess(fmt.Sprintf("Unyanked plugin %s@%s", args[0], args[1]))
}

func getPluginDir(args []string) string {
	if dir := getFlag(args, "--dir"); dir != "" {
		if strings.HasPrefix(dir, "~/") {
			home, _ := os.UserHomeDir()
			return filepath.Join(home, dir[2:])
		}
		return dir
	}
	cfg, _ := LoadConfig()
	return PluginsDir(cfg)
}

func loadPluginsDir() string {
	cfg, _ := LoadConfig()
	return PluginsDir(cfg)
}

// ReadPluginDirFiles reads a Codex plugin directory. Unlike skill packages,
// Codex plugin manifests live under the hidden .codex-plugin directory.
func ReadPluginDirFiles(dirPath string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if strings.HasPrefix(info.Name(), ".") && path != dirPath && info.Name() != ".codex-plugin" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}

		rel, err := filepath.Rel(dirPath, path)
		if err != nil {
			return err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", rel, err)
		}
		files[rel] = data
		return nil
	})
	return files, err
}

func pluginManifestForPublish(files map[string][]byte) []byte {
	if data, ok := files["plugin.json"]; ok {
		return data
	}
	if data, ok := files[".codex-plugin/plugin.json"]; ok {
		return data
	}
	return nil
}

func extractJSONField(data []byte, field string) string {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	return getStr(m, field)
}

func pluginDisplayRef(m map[string]interface{}) string {
	slug := getStr(m, "slug")
	ns := getStr(m, "namespaceSlug")
	if ns == "" {
		return slug
	}
	return "@" + ns + "/" + slug
}

func clearPluginDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == ".installed.json" {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

func extractZipToDir(zipData []byte, targetDir string) error {
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return fmt.Errorf("reading zip: %v", err)
	}
	stripPrefix := detectZipPrefix(zipReader)
	for _, f := range zipReader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := f.Name
		if stripPrefix != "" {
			name = strings.TrimPrefix(name, stripPrefix)
			if name == "" {
				continue
			}
		}
		targetPath := filepath.Join(targetDir, name)
		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(targetDir)+string(os.PathSeparator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("creating directory: %v", err)
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("opening zip entry: %v", err)
		}
		outFile, err := os.Create(targetPath)
		if err != nil {
			_ = rc.Close()
			return fmt.Errorf("creating file: %v", err)
		}
		_, copyErr := io.Copy(outFile, rc)
		closeErr := outFile.Close()
		_ = rc.Close()
		if copyErr != nil {
			return fmt.Errorf("extracting file: %v", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("closing file: %v", closeErr)
		}
	}
	return nil
}
