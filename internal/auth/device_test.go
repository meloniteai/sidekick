package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeviceFlowPollsUntilApproved(t *testing.T) {
	tokenCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/device-authorizations":
			if r.Method != http.MethodPost {
				t.Fatalf("device auth method = %s", r.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode device body: %v", err)
			}
			if body["org_slug"] != "acme" || body["client_name"] != "laptop" {
				t.Fatalf("device body = %+v", body)
			}
			writeJSON(w, 200, map[string]any{
				"device_code":               "device-1",
				"user_code":                 "ABCD-EFGH",
				"verification_uri":          srvURL(r) + "/login/device",
				"verification_uri_complete": srvURL(r) + "/login/device?code=ABCD-EFGH",
				"expires_in":                60,
				"interval":                  0,
			})
		case "/api/cli/token":
			tokenCalls++
			if tokenCalls == 1 {
				writeJSON(w, 400, map[string]any{"detail": "authorization_pending"})
				return
			}
			writeJSON(w, 200, map[string]any{
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

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	device, err := StartDeviceAuthorization(ctx, srv.Client(), srv.URL+"/api", "acme", "laptop")
	if err != nil {
		t.Fatalf("StartDeviceAuthorization: %v", err)
	}
	token, err := PollDeviceToken(ctx, srv.Client(), srv.URL+"/api", device)
	if err != nil {
		t.Fatalf("PollDeviceToken: %v", err)
	}
	if token.AccessToken != "sk_live_token" || token.OrgSlug != "acme" || token.AccountEmail != "dev@example.com" {
		t.Fatalf("token = %+v", token)
	}
	if tokenCalls != 2 {
		t.Fatalf("tokenCalls = %d, want 2", tokenCalls)
	}
}

func TestRevokeTokenSendsBearerHeader(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cli/token" || r.Method != http.MethodDelete {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		writeJSON(w, 200, map[string]any{"ok": true})
	}))
	defer srv.Close()

	if err := RevokeToken(context.Background(), srv.Client(), srv.URL+"/api", "sk_live_token"); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if auth != "Bearer sk_live_token" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
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
