package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
)

func TestExtractSSORoles(t *testing.T) {
	tests := []struct {
		name   string
		claim  any
		expect []string
	}{
		{
			name:   "zitadel object form yields role ids",
			claim:  map[string]any{"admin": map[string]any{"123": "Admin"}, "viewer": map[string]any{"123": "Viewer"}},
			expect: []string{"admin", "viewer"},
		},
		{
			name:   "array of strings",
			claim:  []any{"admin", "editor"},
			expect: []string{"admin", "editor"},
		},
		{
			name:   "single string",
			claim:  "admin",
			expect: []string{"admin"},
		},
		{
			name:   "empty string yields none",
			claim:  "",
			expect: nil,
		},
		{
			name:   "nil yields none",
			claim:  nil,
			expect: nil,
		},
		{
			name:   "unsupported type yields none",
			claim:  42,
			expect: nil,
		},
		{
			name:   "array with non-strings skips them",
			claim:  []any{"admin", 42, nil},
			expect: []string{"admin"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			roles := extractSSORoles(tc.claim)
			if len(roles) != len(tc.expect) {
				t.Fatalf("expected %v roles, got %v", tc.expect, roles)
			}
			for _, want := range tc.expect {
				found := false
				for _, got := range roles {
					if got == want {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected role %q in %v", want, roles)
				}
			}
		})
	}
}

func TestSSORoleGate(t *testing.T) {
	if !ssoRoleGate([]string{"admin"}, nil) {
		t.Error("empty allowed list must disable the gate")
	}
	if !ssoRoleGate([]string{"admin", "viewer"}, []string{"admin"}) {
		t.Error("intersection must pass")
	}
	if ssoRoleGate([]string{"viewer"}, []string{"admin"}) {
		t.Error("no intersection must fail")
	}
	if ssoRoleGate(nil, []string{"admin"}) {
		t.Error("no roles presented must fail a gated login")
	}
}

func TestSSOStateMatches(t *testing.T) {
	if !ssoStateMatches("abc", "abc") {
		t.Error("equal states must match")
	}
	if ssoStateMatches("abc", "abd") {
		t.Error("different states must not match")
	}
	if ssoStateMatches("", "abc") || ssoStateMatches("abc", "") {
		t.Error("empty state on either side must not match")
	}
}

func TestEffectiveSSOScopes(t *testing.T) {
	sso := &configstore.AuthSSOConfig{IssuerURL: "https://idp.example.com", ClientID: "client"}
	scopes := effectiveSSOScopes(sso)
	if len(scopes) != 3 {
		t.Fatalf("default scopes expected 3 entries, got %v", scopes)
	}
	hasOpenID := false
	for _, s := range scopes {
		if s == "openid" {
			hasOpenID = true
		}
	}
	if !hasOpenID {
		t.Fatalf("openid scope required, got %v", scopes)
	}

	gated := &configstore.AuthSSOConfig{IssuerURL: "https://zitadel.example.com", ClientID: "client", AllowedRoles: []string{"admin"}}
	gatedScopes := effectiveSSOScopes(gated)
	foundRolesScope := false
	for _, s := range gatedScopes {
		if s == "urn:zitadel:iam:org:project:roles" {
			foundRolesScope = true
		}
	}
	if !foundRolesScope {
		t.Fatalf("gated login with default role claim must request the Zitadel roles scope, got %v", gatedScopes)
	}

	custom := &configstore.AuthSSOConfig{IssuerURL: "https://idp.example.com", ClientID: "client", Scopes: []string{"openid", "groups"}}
	customScopes := effectiveSSOScopes(custom)
	if len(customScopes) != 2 || customScopes[1] != "groups" {
		t.Fatalf("custom scopes must be passed through untouched, got %v", customScopes)
	}
}

func TestDashboardAuthType(t *testing.T) {
	if got := dashboardAuthType(nil); got != "none" {
		t.Errorf("nil config: expected none, got %s", got)
	}
	if got := dashboardAuthType(&configstore.AuthConfig{}); got != "none" {
		t.Errorf("disabled config: expected none, got %s", got)
	}
	if got := dashboardAuthType(&configstore.AuthConfig{IsEnabled: true}); got != "password" {
		t.Errorf("password config: expected password, got %s", got)
	}
	if got := dashboardAuthType(&configstore.AuthConfig{IsEnabled: true, SSO: &configstore.AuthSSOConfig{Enabled: true, IssuerURL: "https://idp", ClientID: "c"}}); got != "sso" {
		t.Errorf("sso config: expected sso, got %s", got)
	}
	if got := dashboardAuthType(&configstore.AuthConfig{IsEnabled: true, SSO: &configstore.AuthSSOConfig{Enabled: false}}); got != "password" {
		t.Errorf("disabled sso: expected password, got %s", got)
	}
}
