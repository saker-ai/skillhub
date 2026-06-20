package auth

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/saker-ai/saker-common/internaljwt"
	"github.com/saker-ai/skillhub/pkg/config"
	"github.com/saker-ai/skillhub/pkg/repository"
)

func TestSynapseJWTIdentityProviderIdentify(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	namespaceID := uuid.New()
	signer, err := internaljwt.NewSigner("synapse", secret, 5*time.Minute)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, _, err := signer.Sign(internaljwt.SignInput{
		Audience:      "skillhub",
		TenantID:      "tenant-a",
		PrincipalType: "user",
		PrincipalID:   uuid.NewString(),
		Email:         "user@example.com",
		Name:          "User Example",
		Roles:         []string{"admin"},
		Scopes:        []string{"skillhub:write"},
		Resource:      &internaljwt.ResourceRef{Type: "namespace", ID: namespaceID.String()},
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	idp, err := NewSynapseJWTIdentityProvider(SynapseJWTConfig{
		Issuer:       "synapse",
		Audience:     "skillhub",
		MasterSecret: secret,
		TTL:          5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("idp: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(internaljwt.HeaderInternalAuthorization, "Bearer "+token)
	user, scope, nsID, err := idp.Identify(req.Context(), req)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if user == nil || user.Role != "admin" || user.Email == nil || *user.Email != "user@example.com" {
		t.Fatalf("user = %#v", user)
	}
	if scope != "full" {
		t.Fatalf("scope = %q, want full", scope)
	}
	if nsID == nil || *nsID != namespaceID {
		t.Fatalf("namespace = %v, want %s", nsID, namespaceID)
	}
}

func TestSynapseJWTIdentityProviderIdentifyUpsertsUser(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	principalID := uuid.New()
	signer, err := internaljwt.NewSigner("warden", secret, 5*time.Minute)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	token, _, err := signer.Sign(internaljwt.SignInput{
		Audience:      "skillhub",
		TenantID:      "tenant-a",
		PrincipalType: "user",
		PrincipalID:   principalID.String(),
		Handle:        "jwt-user",
		Email:         "jwt-user@example.com",
		Name:          "JWT User",
		Roles:         []string{"user"},
		Scopes:        []string{"skillhub:read"},
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	db, err := repository.NewDB(config.DatabaseConfig{
		Driver:      "sqlite",
		URL:         filepath.Join(t.TempDir(), "skillhub.db"),
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	userRepo := repository.NewUserRepo(db)
	idp, err := NewSynapseJWTIdentityProvider(SynapseJWTConfig{
		Issuer:       "warden",
		Audience:     "skillhub",
		MasterSecret: secret,
		TTL:          5 * time.Minute,
	}, userRepo)
	if err != nil {
		t.Fatalf("idp: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(internaljwt.HeaderInternalAuthorization, "Bearer "+token)
	user, scope, _, err := idp.Identify(req.Context(), req)
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if user == nil || user.ID != principalID || user.Handle != "jwt-user" {
		t.Fatalf("user = %#v", user)
	}
	if scope != "read" {
		t.Fatalf("scope = %q, want read", scope)
	}

	stored, err := userRepo.GetByID(req.Context(), principalID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored == nil || stored.Handle != "jwt-user" || stored.Email == nil || *stored.Email != "jwt-user@example.com" {
		t.Fatalf("stored user = %#v", stored)
	}
}
