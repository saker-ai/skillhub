package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/saker-ai/internaljwt"
	"github.com/saker-ai/skillhub/pkg/model"
)

type SynapseJWTConfig struct {
	Issuer                     string
	Audience                   string
	MasterSecret               string
	TTL                        time.Duration
	ClockSkew                  time.Duration
	AllowAuthorizationFallback bool
}

type SynapseJWTIdentityProvider struct {
	verifier *internaljwt.Verifier
}

func NewSynapseJWTIdentityProvider(cfg SynapseJWTConfig) (*SynapseJWTIdentityProvider, error) {
	verifier, err := internaljwt.NewVerifier(internaljwt.VerifierOptions{
		Issuer:                     cfg.Issuer,
		Audience:                   cfg.Audience,
		MasterSecret:               cfg.MasterSecret,
		TTL:                        cfg.TTL,
		ClockSkew:                  cfg.ClockSkew,
		AllowAuthorizationFallback: cfg.AllowAuthorizationFallback,
	})
	if err != nil {
		return nil, fmt.Errorf("synapse jwt verifier: %w", err)
	}
	return &SynapseJWTIdentityProvider{verifier: verifier}, nil
}

func (p *SynapseJWTIdentityProvider) Identify(_ context.Context, r *http.Request) (*model.User, string, *uuid.UUID, error) {
	principal, err := p.verifier.VerifyRequest(r)
	if err != nil {
		if internaljwt.IsAuthError(err) {
			return nil, "", nil, nil
		}
		return nil, "", nil, err
	}
	user := userFromPrincipal(principal)
	scope := skillhubScope(principal.Scopes)
	var namespaceID *uuid.UUID
	if principal.ResourceType == "namespace" && principal.ResourceID != "" {
		if id, err := uuid.Parse(principal.ResourceID); err == nil {
			namespaceID = &id
		}
	}
	return user, scope, namespaceID, nil
}

func userFromPrincipal(principal *internaljwt.Principal) *model.User {
	id, err := uuid.Parse(principal.ID)
	if err != nil {
		id = uuid.NewSHA1(uuid.NameSpaceOID, []byte("saker-internal:"+principal.ID))
	}
	handle := strings.TrimSpace(principal.Claims.Handle)
	if handle == "" {
		handle = strings.TrimSpace(principal.Claims.Email)
	}
	if handle == "" {
		handle = principal.ID
	}
	displayName := strings.TrimSpace(principal.Claims.Name)
	email := strings.TrimSpace(principal.Claims.Email)
	role := "user"
	for _, r := range principal.Roles {
		switch r {
		case internaljwt.RoleAdmin:
			role = internaljwt.RoleAdmin
		case internaljwt.RoleModerator:
			if role != internaljwt.RoleAdmin {
				role = internaljwt.RoleModerator
			}
		}
	}
	user := &model.User{
		ID:     id,
		Handle: handle,
		Role:   role,
	}
	if displayName != "" {
		user.DisplayName = &displayName
	}
	if email != "" {
		user.Email = &email
	}
	return user
}

func skillhubScope(scopes []string) string {
	out := "read"
	for _, scope := range scopes {
		switch scope {
		case internaljwt.ScopeSkillHubAdmin:
			return "full"
		case internaljwt.ScopeSkillHubWrite:
			out = "full"
		case internaljwt.ScopeSkillHubRead:
			if out == "" {
				out = "read"
			}
		}
	}
	return out
}
