package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	skauth "github.com/meloniteai/sidekick/internal/auth"
)

func TestLoginPairsAndStoresToken(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("SIDEKICK_AUTH_FILE", authFile)
	var opened string
	oldOpenBrowser := openBrowser
	openBrowser = func(url string) error {
		opened = url
		return nil
	}
	t.Cleanup(func() { openBrowser = oldOpenBrowser })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/device-authorizations":
			writeCmdJSON(w, 200, map[string]any{
				"device_code":               "device-1",
				"user_code":                 "ABCD-EFGH",
				"verification_uri":          srvURL(r) + "/login/device",
				"verification_uri_complete": srvURL(r) + "/login/device?code=ABCD-EFGH",
				"expires_in":                60,
				"interval":                  0,
			})
		case "/api/cli/token":
			writeCmdJSON(w, 200, map[string]any{
				"access_token":  "sk_live_token",
				"token_type":    "bearer",
				"org_slug":      "acme",
				"account_email": "dev@example.com",
			})
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	root := New("test", nil)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"login", "--org", "acme", "--api-base", srv.URL + "/api"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute login: %v", err)
	}

	if opened != srv.URL+"/login/device?code=ABCD-EFGH" {
		t.Fatalf("opened = %q", opened)
	}
	profile, ok, err := skauth.CurrentProfile(authFile)
	if err != nil {
		t.Fatalf("CurrentProfile: %v", err)
	}
	if !ok || profile.OrgSlug != "acme" || profile.Token != "sk_live_token" || profile.APIBase != srv.URL+"/api" {
		t.Fatalf("profile = %+v ok=%v", profile, ok)
	}
	if !bytes.Contains(out.Bytes(), []byte("paired acme as dev@example.com")) {
		t.Fatalf("output = %s", out.String())
	}
}

func TestLoginPrintsFallbackURLWhenBrowserOpenFails(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("SIDEKICK_AUTH_FILE", authFile)
	oldOpenBrowser := openBrowser
	openBrowser = func(string) error { return errors.New("no browser") }
	t.Cleanup(func() { openBrowser = oldOpenBrowser })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/device-authorizations":
			writeCmdJSON(w, 200, map[string]any{
				"device_code":               "device-1",
				"user_code":                 "ABCD-EFGH",
				"verification_uri_complete": srvURL(r) + "/login/device?code=ABCD-EFGH",
				"expires_in":                60,
				"interval":                  0,
			})
		case "/api/cli/token":
			writeCmdJSON(w, 200, map[string]any{"access_token": "sk_live_token", "org_slug": "acme"})
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	root := New("test", nil)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"login", "--org", "acme", "--api-base", srv.URL + "/api"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute login: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("open this URL to finish login")) {
		t.Fatalf("fallback output = %s", out.String())
	}
}

func TestLoginAPIBaseDefaultsToMeloniteAPI(t *testing.T) {
	t.Setenv("SIDEKICK_API_BASE", "")
	t.Setenv("SIDEKICK_BACKEND_URL", "")

	if got := loginAPIBase(""); got != "https://sidekick.melonite.ai/api" {
		t.Fatalf("loginAPIBase default = %q", got)
	}
}

func TestLogoutRevokesAndRemovesCurrentToken(t *testing.T) {
	authFile := filepath.Join(t.TempDir(), "auth.json")
	t.Setenv("SIDEKICK_AUTH_FILE", authFile)
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/token" || r.Method != http.MethodDelete {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		authHeader = r.Header.Get("Authorization")
		writeCmdJSON(w, 200, map[string]any{"ok": true})
	}))
	defer srv.Close()
	if err := skauth.PutProfile(authFile, skauth.Profile{OrgSlug: "acme", APIBase: srv.URL + "/api", Token: "sk_live_token"}); err != nil {
		t.Fatalf("PutProfile: %v", err)
	}

	var out bytes.Buffer
	root := New("test", nil)
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"logout"})
	if err := root.Execute(); err != nil {
		t.Fatalf("Execute logout: %v", err)
	}

	if authHeader != "Bearer sk_live_token" {
		t.Fatalf("Authorization = %q", authHeader)
	}
	if _, ok, err := skauth.CurrentProfile(authFile); err != nil || ok {
		t.Fatalf("CurrentProfile after logout ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(authFile); err != nil {
		t.Fatalf("auth file should remain readable: %v", err)
	}
}

func writeCmdJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func srvURL(r *http.Request) string {
	if r.TLS != nil {
		return "https://" + r.Host
	}
	return "http://" + r.Host
}
