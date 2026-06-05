package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Client struct {
	BaseURL    string
	Token      string
	httpClient *http.Client
}

func NewClient(cfg *CLIConfig) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(cfg.Registry, "/"),
		Token:      cfg.Token,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func NewClientFromConfig() (*Client, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	return NewClient(cfg), nil
}

func (c *Client) do(method, path string, body io.Reader, contentType string) (*http.Response, error) {
	u := c.BaseURL + path
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "skillhub-cli/1.0")
	return c.httpClient.Do(req)
}

func (c *Client) getJSON(path string, out interface{}) error {
	resp, err := c.do("GET", path, nil, "")
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return parseAPIError(resp)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func parseAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error)
	}
	return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
}

// WhoAmI calls GET /api/v1/whoami
func (c *Client) WhoAmI() (map[string]interface{}, error) {
	var result map[string]interface{}
	err := c.getJSON("/api/v1/whoami", &result)
	return result, err
}

// Search calls GET /api/v1/search?q=...
func (c *Client) Search(query string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := c.getJSON("/api/v1/search?q="+url.QueryEscape(query), &result)
	return result, err
}

// skillPath builds the API path for a skill reference.
// Accepts "@namespace/slug" or bare "slug".
func skillPath(ref string) string {
	if strings.HasPrefix(ref, "@") {
		if idx := strings.IndexByte(ref[1:], '/'); idx > 0 {
			ns := ref[1 : idx+1]
			slug := ref[idx+2:]
			return "/api/v1/skills/@" + url.PathEscape(ns) + "/" + url.PathEscape(slug)
		}
	}
	return "/api/v1/skills/" + url.PathEscape(ref)
}

func splitNamespaceRef(ref string) (namespace, slug string) {
	if strings.HasPrefix(ref, "@") {
		if idx := strings.IndexByte(ref[1:], '/'); idx > 0 {
			return ref[1 : idx+1], ref[idx+2:]
		}
	}
	return "", ref
}

func pluginPath(ref string) string {
	ns, slug := splitNamespaceRef(ref)
	if ns != "" {
		return "/api/v1/plugins/@" + url.PathEscape(ns) + "/" + url.PathEscape(slug)
	}
	return "/api/v1/plugins/" + url.PathEscape(slug)
}

// ListSkills calls GET /api/v1/skills
func (c *Client) ListSkills(sort string, limit int) (map[string]interface{}, error) {
	path := fmt.Sprintf("/api/v1/skills?sort=%s&limit=%d", url.QueryEscape(sort), limit)
	var result map[string]interface{}
	err := c.getJSON(path, &result)
	return result, err
}

// GetSkill calls GET /api/v1/skills/:slug or /api/v1/skills/@:namespace/:slug
func (c *Client) GetSkill(ref string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := c.getJSON(skillPath(ref), &result)
	return result, err
}

// GetVersions calls GET /api/v1/skills/:slug/versions (supports @namespace/slug)
func (c *Client) GetVersions(ref string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := c.getJSON(skillPath(ref)+"/versions", &result)
	return result, err
}

// Download calls GET /api/v1/download and returns the response body (ZIP).
// ref supports "@namespace/slug" or bare "slug".
func (c *Client) Download(ref, version string) (io.ReadCloser, error) {
	params := url.Values{"version": {version}}
	if strings.HasPrefix(ref, "@") {
		if idx := strings.IndexByte(ref[1:], '/'); idx > 0 {
			params.Set("namespace", ref[1:idx+1])
			params.Set("slug", ref[idx+2:])
		} else {
			params.Set("slug", ref)
		}
	} else {
		params.Set("slug", ref)
	}
	path := "/api/v1/download?" + params.Encode()
	resp, err := c.do("GET", path, nil, "")
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, parseAPIError(resp)
	}
	return resp.Body, nil
}

// Publish calls POST /api/v1/skills with multipart form
func (c *Client) Publish(slug, version, summary, tags, changelog, category string, files map[string][]byte) (map[string]interface{}, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	if slug != "" {
		writer.WriteField("slug", slug)
	}
	if version != "" {
		writer.WriteField("version", version)
	}
	if summary != "" {
		writer.WriteField("summary", summary)
	}
	if tags != "" {
		writer.WriteField("tags", tags)
	}
	if changelog != "" {
		writer.WriteField("changelog", changelog)
	}
	if category != "" {
		writer.WriteField("category", category)
	}

	for name, content := range files {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			return nil, fmt.Errorf("creating form file: %w", err)
		}
		if _, err := part.Write(content); err != nil {
			return nil, fmt.Errorf("writing file content: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("closing multipart writer: %w", err)
	}

	resp, err := c.do("POST", "/api/v1/skills", &buf, writer.FormDataContentType())
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, parseAPIError(resp)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return result, nil
}

// ListPlugins calls GET /api/v1/plugins.
func (c *Client) ListPlugins(sort string, limit int) (map[string]interface{}, error) {
	path := fmt.Sprintf("/api/v1/plugins?sort=%s&limit=%d", url.QueryEscape(sort), limit)
	var result map[string]interface{}
	err := c.getJSON(path, &result)
	return result, err
}

// GetPlugin calls GET /api/v1/plugins/:slug or /api/v1/plugins/@:namespace/:slug.
func (c *Client) GetPlugin(ref string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := c.getJSON(pluginPath(ref), &result)
	return result, err
}

// GetPluginVersions calls GET /api/v1/plugins/:slug/versions.
func (c *Client) GetPluginVersions(ref string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := c.getJSON(pluginPath(ref)+"/versions", &result)
	return result, err
}

// GetPluginFile calls GET /api/v1/plugins/file.
func (c *Client) GetPluginFile(ref, version, filePath string) ([]byte, error) {
	ns, slug := splitNamespaceRef(ref)
	params := url.Values{
		"slug":    {slug},
		"version": {version},
		"path":    {filePath},
	}
	if ns != "" {
		params.Set("namespace", ns)
	}
	resp, err := c.do("GET", "/api/v1/plugins/file?"+params.Encode(), nil, "")
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, parseAPIError(resp)
	}
	return io.ReadAll(resp.Body)
}

// DownloadPlugin calls GET /api/v1/plugins/download and returns the ZIP stream.
func (c *Client) DownloadPlugin(ref, version string) (io.ReadCloser, error) {
	ns, slug := splitNamespaceRef(ref)
	params := url.Values{"slug": {slug}, "version": {version}}
	if ns != "" {
		params.Set("namespace", ns)
	}
	resp, err := c.do("GET", "/api/v1/plugins/download?"+params.Encode(), nil, "")
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, parseAPIError(resp)
	}
	return resp.Body, nil
}

// PublishPlugin calls POST /api/v1/plugins with multipart form.
func (c *Client) PublishPlugin(slug, version, summary, tags, changelog, category, namespace string, files map[string][]byte) (map[string]interface{}, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	for k, v := range map[string]string{
		"slug":      slug,
		"version":   version,
		"summary":   summary,
		"tags":      tags,
		"changelog": changelog,
		"category":  category,
		"namespace": namespace,
	} {
		if v != "" {
			_ = writer.WriteField(k, v)
		}
	}
	for name, content := range files {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			return nil, fmt.Errorf("creating form file: %w", err)
		}
		if _, err := part.Write(content); err != nil {
			return nil, fmt.Errorf("writing file content: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("closing multipart writer: %w", err)
	}

	resp, err := c.do("POST", "/api/v1/plugins", &buf, writer.FormDataContentType())
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, parseAPIError(resp)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return result, nil
}

func (c *Client) DeletePlugin(ref string) error {
	return c.pluginAction("DELETE", pluginPath(ref), nil)
}

func (c *Client) UndeletePlugin(ref string) error {
	return c.pluginAction("POST", pluginPath(ref)+"/undelete", nil)
}

func (c *Client) YankPluginVersion(ref, version, reason string) error {
	payload, _ := json.Marshal(map[string]string{"reason": reason})
	return c.pluginAction("POST", pluginPath(ref)+"/versions/"+url.PathEscape(version)+"/yank", bytes.NewReader(payload))
}

func (c *Client) UnyankPluginVersion(ref, version string) error {
	return c.pluginAction("DELETE", pluginPath(ref)+"/versions/"+url.PathEscape(version)+"/yank", nil)
}

func (c *Client) pluginAction(method, path string, body io.Reader) error {
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	resp, err := c.do(method, path, body, contentType)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return parseAPIError(resp)
	}
	return nil
}

// ============================================================================
// Team-token endpoints — /api/v1/namespaces/:slug/tokens
// ============================================================================

// ListTeamTokens calls GET /api/v1/namespaces/:slug/tokens.
func (c *Client) ListTeamTokens(nsSlug string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := c.getJSON("/api/v1/namespaces/"+url.PathEscape(nsSlug)+"/tokens", &result)
	return result, err
}

// CreateTeamToken calls POST /api/v1/namespaces/:slug/tokens.
// expiresIn is required server-side; pass strings like "720h" or "90d-style"
// (the server uses time.ParseDuration so "d" is NOT a valid suffix — pass hours).
func (c *Client) CreateTeamToken(nsSlug, label, scope, expiresIn string) (map[string]interface{}, error) {
	body := map[string]string{}
	if label != "" {
		body["label"] = label
	}
	if scope != "" {
		body["scope"] = scope
	}
	if expiresIn != "" {
		body["expiresIn"] = expiresIn
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	resp, err := c.do("POST", "/api/v1/namespaces/"+url.PathEscape(nsSlug)+"/tokens",
		bytes.NewReader(payload), "application/json")
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Created (201) is the success path; anything else is an API error.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, parseAPIError(resp)
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return result, nil
}

// RevokeTeamToken calls DELETE /api/v1/namespaces/:slug/tokens/:id.
func (c *Client) RevokeTeamToken(nsSlug, tokenID string) error {
	resp, err := c.do("DELETE",
		"/api/v1/namespaces/"+url.PathEscape(nsSlug)+"/tokens/"+url.PathEscape(tokenID),
		nil, "")
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return parseAPIError(resp)
	}
	return nil
}

// ReadDirFiles reads all files from a directory path, returning a map of relative path -> content.
func ReadDirFiles(dirPath string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip hidden directories
			if strings.HasPrefix(info.Name(), ".") && path != dirPath {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip hidden files
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
