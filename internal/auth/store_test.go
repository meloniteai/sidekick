package auth

import (
	"encoding/json"
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
	id := "acme@https://sidekick.example/api"
	if st.Current != id {
		t.Fatalf("Current = %q, want %q", st.Current, id)
	}
	profile := st.Profiles[id]
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

func TestLoadMigratesLegacySingleProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	legacy := Profile{
		OrgSlug:      " acme ",
		APIBase:      "https://sidekick.example/api/orgs/acme",
		Token:        " sk_live_token ",
		AccountEmail: " dev@example.com ",
		IssuedAt:     time.Unix(1, 0),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	id := "acme@https://sidekick.example/api"
	if st.Current != id {
		t.Fatalf("Current = %q, want %q", st.Current, id)
	}
	profile, ok := st.Profiles[id]
	if !ok {
		t.Fatalf("missing migrated profile %q in %+v", id, st.Profiles)
	}
	if profile.OrgSlug != "acme" || profile.APIBase != "https://sidekick.example/api" || profile.Token != "sk_live_token" || profile.AccountEmail != "dev@example.com" {
		t.Fatalf("profile = %+v", profile)
	}

	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Store
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("persisted auth is not store JSON: %v", err)
	}
	if persisted.Current != id || len(persisted.Profiles) != 1 {
		t.Fatalf("persisted = %+v", persisted)
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

func TestResolveBackendTargetUsesMatchingBackendProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := PutProfile(path, Profile{
		OrgSlug: "acme",
		APIBase: "https://prod.example/api",
		Token:   "sk_live_prod",
	}); err != nil {
		t.Fatalf("PutProfile prod: %v", err)
	}
	if err := PutProfile(path, Profile{
		OrgSlug: "acme",
		APIBase: "http://localhost:3000/api",
		Token:   "sk_live_dev",
	}); err != nil {
		t.Fatalf("PutProfile dev: %v", err)
	}

	target, ok, err := ResolveBackendTarget(path, "https://prod.example/api")
	if err != nil {
		t.Fatalf("ResolveBackendTarget: %v", err)
	}
	if !ok {
		t.Fatalf("ResolveBackendTarget ok = false")
	}
	if target.Token != "sk_live_prod" {
		t.Fatalf("Token = %q, want prod token", target.Token)
	}
	if target.APIBase != "https://prod.example/api/orgs/acme" {
		t.Fatalf("APIBase = %q", target.APIBase)
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
