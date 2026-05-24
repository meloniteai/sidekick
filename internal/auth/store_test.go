package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestPutProfilePersistsAuthFileWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")

	if err := PutProfile(path, Profile{
		OrgSlug:  "acme",
		APIBase:  "https://sidekick.example/api/orgs/acme",
		Token:    "sk_live_token",
		IssuedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}

	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st.Current != "acme" {
		t.Fatalf("Current = %q, want acme", st.Current)
	}
	profile := st.Profiles["acme"]
	if profile.APIBase != "https://sidekick.example/api" {
		t.Fatalf("APIBase = %q, want root api base", profile.APIBase)
	}
	if profile.Token != "sk_live_token" {
		t.Fatalf("Token was not persisted")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat auth file: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("auth file mode = %v, want 0600", got)
		}
	}
}

func TestResolveBackendTargetUsesCurrentProfileOrg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := PutProfile(path, Profile{
		OrgSlug: "acme",
		APIBase: "https://sidekick.example/api",
		Token:   "sk_live_token",
	}); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}

	target, ok, err := ResolveBackendTarget(path, "https://override.example/api")
	if err != nil {
		t.Fatalf("ResolveBackendTarget: %v", err)
	}
	if !ok {
		t.Fatalf("ResolveBackendTarget ok = false")
	}
	if target.APIBase != "https://override.example/api/orgs/acme" {
		t.Fatalf("APIBase = %q", target.APIBase)
	}
	if target.Token != "sk_live_token" || target.OrgSlug != "acme" {
		t.Fatalf("target = %+v", target)
	}
}

func TestRemoveCurrentProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := PutProfile(path, Profile{OrgSlug: "acme", APIBase: "https://sidekick.example/api", Token: "sk_live_token"}); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}

	profile, ok, err := RemoveCurrentProfile(path)
	if err != nil {
		t.Fatalf("RemoveCurrentProfile: %v", err)
	}
	if !ok || profile.OrgSlug != "acme" {
		t.Fatalf("removed = %+v ok=%v", profile, ok)
	}
	if _, ok, err := CurrentProfile(path); err != nil || ok {
		t.Fatalf("CurrentProfile after remove ok=%v err=%v", ok, err)
	}
}
