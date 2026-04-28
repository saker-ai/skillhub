// Package security provides pluggable artifact verification.
//
// Sigstore verifier:
// We deliberately do not vendor cosign/Rekor clients into the core server —
// they are heavy and require Fulcio root configuration that varies by
// deployment (public-good vs. private Fulcio). Instead we expose a small
// Verifier interface so operators can inject a real verifier (calling out
// to cosign verify-blob, sigstore-go, or a private attestation service).
//
// The default NopVerifier records the bundle but does not verify it,
// surfacing a "unverified" status to clients. This lets the upload path
// land before signing infrastructure is fully wired.
package security

import (
	"context"
	"encoding/json"
	"fmt"
)

// VerifyResult describes the outcome of a signature verification.
type VerifyResult struct {
	// Status: "verified" | "unverified" | "invalid"
	Status string
	// Subject identity from the signing certificate (typically an email).
	Subject string
	// Issuer is the OIDC issuer of the signing certificate.
	Issuer string
	// Reason carries an explanation when Status is "invalid".
	Reason string
}

// SignatureVerifier verifies a Sigstore bundle against a content digest.
//
// fingerprint is the SHA-256 of the artifact (as stored on SkillVersion).
// bundleJSON is the raw .sigstore bundle uploaded by the publisher.
//
// Implementations must NOT block indefinitely; honor ctx deadlines.
type SignatureVerifier interface {
	Verify(ctx context.Context, fingerprint string, bundleJSON []byte) (*VerifyResult, error)
}

// NopVerifier is the default verifier: it sanity-checks that the bundle is
// valid JSON and reports "unverified" without contacting Fulcio/Rekor.
//
// Operators wanting real verification should swap this for an implementation
// backed by sigstore-go or `cosign verify-blob`.
type NopVerifier struct{}

// Verify validates the bundle is well-formed JSON, then marks it unverified.
func (NopVerifier) Verify(_ context.Context, fingerprint string, bundleJSON []byte) (*VerifyResult, error) {
	if len(bundleJSON) == 0 {
		return nil, fmt.Errorf("empty signature bundle")
	}
	var probe map[string]any
	if err := json.Unmarshal(bundleJSON, &probe); err != nil {
		return &VerifyResult{Status: "invalid", Reason: "malformed bundle JSON"}, nil
	}
	// Bundle accepted as-is; verification deferred.
	return &VerifyResult{Status: "unverified"}, nil
}
