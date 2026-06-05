package cli_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saker-ai/skillhub"
	"github.com/saker-ai/skillhub/pkg/cli"
	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	// Blank import: matches cmd/skillhub/main.go default backend wiring.
	_ "github.com/saker-ai/skillhub/pkg/store/git"
)

// stubIDP returns the same user object whenever a non-empty Bearer token is
// presented. The cli.Client always sends Authorization: Bearer <token>, so
// this lets us swap "who am I" with one struct without touching the DB.
//
// scope="full" because tests need to mint team tokens (which requires
// owner/admin namespace role, not a token-scope check).
type stubIDP struct {
	user *model.User
}

func (s *stubIDP) Identify(_ context.Context, r *http.Request) (*model.User, string, *uuid.UUID, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, "", nil, nil
	}
	if strings.TrimPrefix(header, "Bearer ") == "" {
		return nil, "", nil, nil
	}
	return s.user, "full", nil, nil
}

// startTestRegistry boots an in-process Hub backed by sqlite + temp dirs and
// fronts it with httptest.NewServer so cli.Client can dial a real http URL.
//
// 之所以要真 httptest.Server 而不是 engine.ServeHTTP：cli.Client 用
// http.Client.Do(real-URL)，必须有真正的 Listener。
func startTestRegistry(t *testing.T, asUser *model.User) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()

	cfg := config.DefaultConfig()
	cfg.Server.Port = 0
	cfg.Database.Driver = "sqlite"
	cfg.Database.URL = filepath.Join(tmp, "skillhub.db")
	cfg.Database.AutoMigrate = true
	cfg.Search.IndexPath = filepath.Join(tmp, "bleve.idx")
	cfg.GitStore.BasePath = filepath.Join(tmp, "repos")

	hub, err := skillhub.New(context.Background(),
		skillhub.WithConfig(cfg),
		skillhub.WithIdentityProvider(&stubIDP{user: asUser}),
	)
	if err != nil {
		t.Fatalf("skillhub.New: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })

	engine := gin.New()
	hub.RegisterRoutes(engine)
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)

	// Bootstrap: have alice create the namespace via the same REST surface
	// the CLI uses. sqlite has FKs off by default, so the missing user-row
	// in the DB is harmless here.
	body := strings.NewReader(`{"slug":"acme","displayName":"Acme","type":"team"}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/namespaces", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer alice-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create namespace: status=%d body=%s", resp.StatusCode, b)
	}
	return srv
}

// TestCLI_TeamTokens_Lifecycle exercises cli.Client.{ListTeamTokens,
// CreateTeamToken, RevokeTeamToken} against a real httptest.Server backed by
// the in-process Hub.
//
// Coverage focus:
//   - URL escaping of namespace slugs and token IDs
//   - 201/200 acceptance in CreateTeamToken
//   - parseAPIError on 4xx
//   - Idempotent ergonomics: list-empty, create, list-one, revoke, list-empty
func TestCLI_TeamTokens_Lifecycle(t *testing.T) {
	t.Parallel()
	owner := &model.User{ID: uuid.New(), Handle: "alice", Role: "user"}
	srv := startTestRegistry(t, owner)

	c := cli.NewClient(&cli.CLIConfig{Registry: srv.URL, Token: "alice-token"})

	// 1) list before create — empty data slice
	first, err := c.ListTeamTokens("acme")
	if err != nil {
		t.Fatalf("initial list: %v", err)
	}
	if d, _ := first["data"].([]interface{}); len(d) != 0 {
		t.Errorf("initial list expected empty, got %+v", d)
	}

	// 2) create with valid params — must populate raw token + metadata.id
	created, err := c.CreateTeamToken("acme", "ci-runner", "publish", "720h")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rawToken, _ := created["token"].(string)
	if rawToken == "" {
		t.Fatalf("created.token empty: %+v", created)
	}
	meta, _ := created["metadata"].(map[string]interface{})
	if meta == nil {
		t.Fatalf("created.metadata missing: %+v", created)
	}
	tokenID, _ := meta["id"].(string)
	if tokenID == "" {
		t.Fatalf("metadata.id empty: %+v", meta)
	}
	if scope, _ := meta["scope"].(string); scope != "publish" {
		t.Errorf("scope = %q, want publish", scope)
	}

	// 3) list after create — exactly one row, matching ID
	second, err := c.ListTeamTokens("acme")
	if err != nil {
		t.Fatalf("list after create: %v", err)
	}
	rows, _ := second["data"].([]interface{})
	if len(rows) != 1 {
		t.Fatalf("list after create: got %d rows, want 1", len(rows))
	}
	row, _ := rows[0].(map[string]interface{})
	if id, _ := row["id"].(string); id != tokenID {
		t.Errorf("listed id = %q, want %q", id, tokenID)
	}

	// 4) error path — empty expiresIn must surface server's 400 verbatim
	if _, err := c.CreateTeamToken("acme", "bad", "publish", ""); err == nil {
		t.Errorf("expected error for empty expiresIn, got nil")
	} else if !strings.Contains(err.Error(), "expiresIn") {
		t.Errorf("error message missing 'expiresIn': %v", err)
	}

	// 5) error path — scope=full rejected by server's tightened validator
	if _, err := c.CreateTeamToken("acme", "bad", "full", "24h"); err == nil {
		t.Errorf("expected error for scope=full, got nil")
	} else if !strings.Contains(err.Error(), "scope") {
		t.Errorf("error message missing 'scope': %v", err)
	}

	// 6) revoke succeeds, list goes back to empty
	if err := c.RevokeTeamToken("acme", tokenID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	final, err := c.ListTeamTokens("acme")
	if err != nil {
		t.Fatalf("list after revoke: %v", err)
	}
	if d, _ := final["data"].([]interface{}); len(d) != 0 {
		t.Errorf("list after revoke expected empty, got %+v", d)
	}

	// 7) revoking an unknown id returns a useful error (404 from server)
	if err := c.RevokeTeamToken("acme", uuid.NewString()); err == nil {
		t.Errorf("expected error revoking unknown token, got nil")
	}

	// 8) JSON sanity — make sure raw token round-trips as a printable string
	if !json.Valid([]byte(`"` + rawToken + `"`)) {
		t.Errorf("raw token contains invalid JSON chars: %q", rawToken)
	}
}
