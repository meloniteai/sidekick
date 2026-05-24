package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type DeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	OrgSlug      string `json:"org_slug"`
	AccountEmail string `json:"account_email"`
}

type StatusError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e StatusError) Error() string {
	if e.Code != "" {
		return e.Code
	}
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

func StartDeviceAuthorization(ctx context.Context, client *http.Client, apiBase, orgSlug, clientName string) (DeviceAuthorization, error) {
	var out DeviceAuthorization
	body := map[string]string{
		"org_slug":    strings.TrimSpace(orgSlug),
		"client_name": strings.TrimSpace(clientName),
	}
	if err := postJSON(ctx, client, RootAPIBase(apiBase)+"/cli/device-authorizations", "", body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func PollDeviceToken(ctx context.Context, client *http.Client, apiBase string, device DeviceAuthorization) (TokenResponse, error) {
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = time.Second
	}
	expires := time.Duration(device.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, expires+15*time.Second)
	defer cancel()

	for {
		token, err := ExchangeDeviceToken(ctx, client, apiBase, device.DeviceCode)
		if err == nil {
			return token, nil
		}
		var status StatusError
		if !errors.As(err, &status) {
			return TokenResponse{}, err
		}
		switch status.Code {
		case "authorization_pending":
		case "slow_down":
			interval += time.Second
		default:
			return TokenResponse{}, err
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return TokenResponse{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func ExchangeDeviceToken(ctx context.Context, client *http.Client, apiBase, deviceCode string) (TokenResponse, error) {
	var out TokenResponse
	body := map[string]string{"device_code": strings.TrimSpace(deviceCode)}
	if err := postJSON(ctx, client, RootAPIBase(apiBase)+"/cli/token", "", body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func RevokeToken(ctx context.Context, client *http.Client, apiBase, token string) error {
	return deleteJSON(ctx, client, RootAPIBase(apiBase)+"/cli/token", strings.TrimSpace(token))
}

func postJSON(ctx context.Context, client *http.Client, url, token string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(ctx, client, req, out)
}

func deleteJSON(ctx context.Context, client *http.Client, url, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(ctx, client, req, nil)
}

func do(_ context.Context, client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseStatusError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func parseStatusError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	msg := strings.TrimSpace(string(raw))
	var payload struct {
		Detail any `json:"detail"`
		Error  any `json:"error"`
	}
	if json.Unmarshal(raw, &payload) == nil {
		switch v := payload.Detail.(type) {
		case string:
			msg = v
		case map[string]any:
			if code, _ := v["code"].(string); code != "" {
				msg = code
			}
			if detail, _ := v["message"].(string); detail != "" {
				msg = detail
			}
		}
		if msg == "" {
			if v, ok := payload.Error.(string); ok {
				msg = v
			}
		}
	}
	return StatusError{StatusCode: resp.StatusCode, Code: msg, Message: msg}
}
