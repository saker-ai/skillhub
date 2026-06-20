package auth

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/saker-ai/saker-common/internaljwt"
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
