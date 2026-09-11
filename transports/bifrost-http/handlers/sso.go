package handlers

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	frameworkoauth2 "github.com/maximhq/bifrost/framework/oauth2"
	"github.com/valyala/fasthttp"
	"golang.org/x/oauth2"
)

const (
	// defaultSSORoleClaim is Zitadel's project roles claim. Override via
	// auth_config.sso.role_claim for other identity providers.
	defaultSSORoleClaim = "urn:zitadel:iam:org:project:roles"
	// zitadelRolesScope asks Zitadel to embed the roles claim in the ID token.
	zitadelRolesScope = "urn:zitadel:iam:org:project:roles"
	defaultSSOScopes  = "openid profile email"
	ssoStateCookie    = "sso_state"
	ssoVerifierCookie = "sso_verifier"
	ssoFlowCookieTTL  = 10 * time.Minute
	ssoCallbackPath   = "/api/session/sso/callback"
	sessionTTLDays    = 30
)

// effectiveSSOScopes returns the scopes to request during the SSO redirect.
// The Zitadel roles scope is appended automatically when the role gate targets
// Zitadel's default claim, so the ID token actually carries the roles.
func effectiveSSOScopes(sso *configstore.AuthSSOConfig) []string {
	scopes := sso.Scopes
	if len(scopes) == 0 {
		scopes = strings.Fields(defaultSSOScopes)
	}
	roleClaim := sso.RoleClaim
	if roleClaim == "" {
		roleClaim = defaultSSORoleClaim
	}
	if len(sso.AllowedRoles) > 0 && roleClaim == defaultSSORoleClaim {
		found := false
		for _, s := range scopes {
			if s == zitadelRolesScope {
				found = true
				break
			}
		}
		if !found {
			scopes = append(append([]string{}, scopes...), zitadelRolesScope)
		}
	}
	return scopes
}

// extractSSORoles normalizes a role claim into a flat list of role IDs.
// Supported shapes: a single string, an array of strings, and Zitadel's
// object form where each key is a role ID ("role-id": {"org": "label"}).
func extractSSORoles(claimValue any) []string {
	switch v := claimValue.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []any:
		roles := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				roles = append(roles, s)
			}
		}
		return roles
	case map[string]any:
		roles := make([]string, 0, len(v))
		for key := range v {
			roles = append(roles, key)
		}
		return roles
	default:
		return nil
	}
}

// ssoRoleGate reports whether the presented roles satisfy the configured gate.
// An empty allowed list disables the gate.
func ssoRoleGate(presented []string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		for _, p := range presented {
			if a == p {
				return true
			}
		}
	}
	return false
}

// ssoStateMatches compares the state cookie against the query state
// in constant time.
func ssoStateMatches(stateCookie, queryState string) bool {
	if stateCookie == "" || queryState == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stateCookie), []byte(queryState)) == 1
}

// getEnabledSSOConfig returns the SSO config when SSO login is enabled, nil otherwise.
func (h *SessionHandler) getEnabledSSOConfig(ctx *fasthttp.RequestCtx) *configstore.AuthSSOConfig {
	if h.configStore == nil {
		return nil
	}
	authConfig, err := h.configStore.GetAuthConfig(ctx)
	if err != nil || authConfig == nil || authConfig.SSO == nil || !authConfig.SSO.Enabled || authConfig.SSO.IssuerURL == "" || authConfig.SSO.ClientID == "" {
		return nil
	}
	return authConfig.SSO
}

// getOIDCProvider discovers and caches the OIDC provider metadata per issuer.
func (h *SessionHandler) getOIDCProvider(ctx context.Context, issuerURL string) (*oidc.Provider, error) {
	h.ssoProvidersMu.Lock()
	defer h.ssoProvidersMu.Unlock()
	if h.ssoProviders == nil {
		h.ssoProviders = map[string]*oidc.Provider{}
	}
	if provider, ok := h.ssoProviders[issuerURL]; ok {
		return provider, nil
	}
	provider, err := oidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, err
	}
	h.ssoProviders[issuerURL] = provider
	return provider, nil
}

// ssoRedirectURI derives the callback URL from the incoming request unless an
// explicit redirect_url is configured.
func (h *SessionHandler) ssoRedirectURI(ctx *fasthttp.RequestCtx, sso *configstore.AuthSSOConfig) string {
	if sso.RedirectURL != "" {
		return sso.RedirectURL
	}
	proto := string(ctx.Request.Header.Peek("X-Forwarded-Proto"))
	if proto == "" {
		if ctx.IsTLS() {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	return fmt.Sprintf("%s://%s%s", proto, ctx.Request.Host(), ssoCallbackPath)
}

// setSSOFlowCookie writes a short-lived cookie scoped to the SSO endpoints.
func (h *SessionHandler) setSSOFlowCookie(ctx *fasthttp.RequestCtx, key, value string, expire time.Time) {
	cookie := fasthttp.AcquireCookie()
	defer fasthttp.ReleaseCookie(cookie)
	cookie.SetKey(key)
	cookie.SetValue(value)
	cookie.SetExpire(expire)
	cookie.SetPath("/api/session/sso/")
	cookie.SetHTTPOnly(true)
	cookie.SetSameSite(fasthttp.CookieSameSiteLaxMode)
	if string(ctx.Request.Header.Peek("X-Forwarded-Proto")) == "https" {
		cookie.SetSecure(true)
	}
	ctx.Response.Header.SetCookie(cookie)
}

// handleSSOLogin handles GET /api/session/sso/login - redirects the browser to
// the identity provider's authorization endpoint with PKCE (S256).
func (h *SessionHandler) handleSSOLogin(ctx *fasthttp.RequestCtx) {
	sso := h.getEnabledSSOConfig(ctx)
	if sso == nil {
		SendError(ctx, fasthttp.StatusForbidden, "SSO login is not enabled")
		return
	}
	provider, err := h.getOIDCProvider(ctx, sso.IssuerURL)
	if err != nil {
		logger.Error("SSO login failed to discover OIDC provider %s: %v", sso.IssuerURL, err)
		SendError(ctx, fasthttp.StatusBadGateway, "Failed to reach the identity provider")
		return
	}
	verifier, challenge, err := frameworkoauth2.GeneratePKCEChallenge()
	if err != nil {
		logger.Error("SSO login failed to generate PKCE challenge: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to start SSO login")
		return
	}
	state := uuid.NewString()
	redirectURI := h.ssoRedirectURI(ctx, sso)
	now := time.Now()
	h.setSSOFlowCookie(ctx, ssoStateCookie, state, now.Add(ssoFlowCookieTTL))
	h.setSSOFlowCookie(ctx, ssoVerifierCookie, verifier, now.Add(ssoFlowCookieTTL))
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", sso.ClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", strings.Join(effectiveSSOScopes(sso), " "))
	params.Set("state", state)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	ctx.Redirect(provider.Endpoint().AuthURL+"?"+params.Encode(), fasthttp.StatusFound)
}

// handleSSOCallback handles GET /api/session/sso/callback - exchanges the
// authorization code, verifies the ID token, applies the role gate, and
// establishes a dashboard session.
func (h *SessionHandler) handleSSOCallback(ctx *fasthttp.RequestCtx) {
	sso := h.getEnabledSSOConfig(ctx)
	if sso == nil {
		SendError(ctx, fasthttp.StatusForbidden, "SSO login is not enabled")
		return
	}
	expired := time.Now().Add(-time.Hour)
	defer func() {
		h.setSSOFlowCookie(ctx, ssoStateCookie, "", expired)
		h.setSSOFlowCookie(ctx, ssoVerifierCookie, "", expired)
	}()
	stateCookie := string(ctx.Request.Header.Cookie(ssoStateCookie))
	verifierCookie := string(ctx.Request.Header.Cookie(ssoVerifierCookie))
	state := string(ctx.QueryArgs().Peek("state"))
	code := string(ctx.QueryArgs().Peek("code"))
	if code == "" || !ssoStateMatches(stateCookie, state) || verifierCookie == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid or expired SSO login state")
		return
	}
	provider, err := h.getOIDCProvider(ctx, sso.IssuerURL)
	if err != nil {
		logger.Error("SSO callback failed to discover OIDC provider %s: %v", sso.IssuerURL, err)
		SendError(ctx, fasthttp.StatusBadGateway, "Failed to reach the identity provider")
		return
	}
	redirectURI := h.ssoRedirectURI(ctx, sso)
	oauth2Config := &oauth2.Config{
		ClientID:     sso.ClientID,
		ClientSecret: sso.ClientSecret.GetValue(),
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURI,
	}
	token, err := oauth2Config.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifierCookie))
	if err != nil {
		logger.Error("SSO token exchange failed: %v", err)
		SendError(ctx, fasthttp.StatusUnauthorized, "SSO login failed")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		SendError(ctx, fasthttp.StatusUnauthorized, "Identity provider did not return an ID token")
		return
	}
	idTokenVerifier := provider.Verifier(&oidc.Config{ClientID: sso.ClientID})
	idToken, err := idTokenVerifier.Verify(ctx, rawIDToken)
	if err != nil {
		logger.Error("SSO ID token verification failed: %v", err)
		SendError(ctx, fasthttp.StatusUnauthorized, "Invalid identity token")
		return
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		logger.Error("SSO ID token claims decode failed: %v", err)
		SendError(ctx, fasthttp.StatusUnauthorized, "Invalid identity token")
		return
	}
	if len(sso.AllowedRoles) > 0 {
		roleClaim := sso.RoleClaim
		if roleClaim == "" {
			roleClaim = defaultSSORoleClaim
		}
		roles := extractSSORoles(claims[roleClaim])
		if !ssoRoleGate(roles, sso.AllowedRoles) {
			logger.Warn("SSO login rejected for subject %q: none of roles %v are in allowed roles %v", idToken.Subject, roles, sso.AllowedRoles)
			SendError(ctx, fasthttp.StatusForbidden, "SSO login rejected: missing required role")
			return
		}
	}
	sessionToken := uuid.NewString()
	now := time.Now()
	session := &tables.SessionsTable{
		Token:     sessionToken,
		ExpiresAt: now.Add(time.Hour * 24 * sessionTTLDays),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := h.configStore.CreateSession(ctx, session); err != nil {
		logger.Error("SSO login failed to create session: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to create session")
		return
	}
	cookie := fasthttp.AcquireCookie()
	defer fasthttp.ReleaseCookie(cookie)
	cookie.SetKey("token")
	cookie.SetValue(sessionToken)
	cookie.SetExpire(now.Add(time.Hour * 24 * sessionTTLDays))
	cookie.SetPath("/")
	cookie.SetHTTPOnly(true)
	cookie.SetSameSite(fasthttp.CookieSameSiteLaxMode)
	if string(ctx.Request.Header.Peek("X-Forwarded-Proto")) == "https" {
		cookie.SetSecure(true)
	}
	ctx.Response.Header.SetCookie(cookie)
	ctx.Redirect("/workspace", fasthttp.StatusFound)
}
