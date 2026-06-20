package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/saker-ai/saker-common/internaljwt"
	"github.com/saker-ai/skillhub/pkg/model"
	"github.com/saker-ai/skillhub/pkg/repository"
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
	userRepo *repository.UserRepo
}

func NewSynapseJWTIdentityProvider(cfg SynapseJWTConfig, userRepo ...*repository.UserRepo) (*SynapseJWTIdentityProvider, error) {
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
	var repo *repository.UserRepo
	if len(userRepo) > 0 {
		repo = userRepo[0]
	}
	return &SynapseJWTIdentityProvider{verifier: verifier, userRepo: repo}, nil
}

func (p *SynapseJWTIdentityProvider) Identify(ctx context.Context, r *http.Request) (*model.User, string, *uuid.UUID, error) {
	principal, err := p.verifier.VerifyRequest(r)
	if err != nil {
		if internaljwt.IsAuthError(err) {
			return nil, "", nil, nil
		}
		return nil, "", nil, err
	}
	user := userFromPrincipal(principal)
	if p.userRepo != nil {
		if err := p.userRepo.UpsertIdentity(ctx, user); err != nil {
			return nil, "", nil, err
		}
	}
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
