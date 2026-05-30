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
	key := "https://sidekick.example/api|acme"
	if st.Current != key {
		t.Fatalf("Current = %q, want %q", st.Current, key)
	}
	profile := st.Profiles[key]
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

func TestLoadMigratesLegacyFlatAuthFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	issuedAt := time.Unix(1, 0).UTC()
	raw, err := json.Marshal(Profile{
		OrgSlug:      "acme",
		APIBase:      "https://sidekick.example/api/orgs/acme",
		Token:        "sk_live_token",
		AccountEmail: "dev@example.com",
		IssuedAt:     issuedAt,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	key := "https://sidekick.example/api|acme"
	if st.Current != key {
		t.Fatalf("Current = %q, want %q", st.Current, key)
	}
	profile, ok := st.Profiles[key]
	if !ok {
		t.Fatalf("migrated profile missing: %+v", st.Profiles)
	}
	if profile.APIBase != "https://sidekick.example/api" || profile.Token != "sk_live_token" || profile.AccountEmail != "dev@example.com" {
		t.Fatalf("profile = %+v", profile)
	}

	var persisted Store
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("persisted auth file should be store-shaped JSON: %v", err)
	}
	if persisted.Current != key || len(persisted.Profiles) != 1 {
		t.Fatalf("persisted = %+v", persisted)
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

func TestLoadMigratesOrgKeyedProfilesToBackendScopedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	old := Store{
		Current: "acme",
		Profiles: map[string]Profile{
			"acme": {
				OrgSlug: "acme",
				APIBase: "https://sidekick.example/api",
				Token:   "sk_live_token",
			},
		},
	}
	if err := Save(path, old); err != nil {
		t.Fatalf("Save: %v", err)
	}

	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	key := "https://sidekick.example/api|acme"
	if st.Current != key {
		t.Fatalf("Current = %q, want %q", st.Current, key)
	}
	if _, ok := st.Profiles[key]; !ok {
		t.Fatalf("profile key missing from %+v", st.Profiles)
	}
	if _, ok := st.Profiles["acme"]; ok {
		t.Fatalf("legacy profile key was not migrated: %+v", st.Profiles)
	}
}

func TestPutProfileKeepsSameOrgOnMultipleBackends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := PutProfile(path, Profile{OrgSlug: "acme", APIBase: "https://one.example/api", Token: "sk_one"}); err != nil {
		t.Fatalf("PutProfile one: %v", err)
	}
	if err := PutProfile(path, Profile{OrgSlug: "acme", APIBase: "https://two.example/api", Token: "sk_two"}); err != nil {
		t.Fatalf("PutProfile two: %v", err)
	}

	st, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(st.Profiles) != 2 {
		t.Fatalf("profiles = %+v, want two backend-scoped profiles", st.Profiles)
	}

	target, ok, err := ResolveBackendTarget(path, "https://one.example/api")
	if err != nil {
		t.Fatalf("ResolveBackendTarget one: %v", err)
	}
	if !ok || target.Token != "sk_one" || target.APIBase != "https://one.example/api/orgs/acme" {
		t.Fatalf("target one = %+v ok=%v", target, ok)
	}
	target, ok, err = ResolveBackendTarget(path, "https://two.example/api")
	if err != nil {
		t.Fatalf("ResolveBackendTarget two: %v", err)
	}
	if !ok || target.Token != "sk_two" || target.APIBase != "https://two.example/api/orgs/acme" {
		t.Fatalf("target two = %+v ok=%v", target, ok)
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
