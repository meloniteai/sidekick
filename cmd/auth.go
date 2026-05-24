package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	skauth "github.com/meloniteai/sidekick/internal/auth"
)

const defaultAPIBase = "https://sidekick-api-origin.melonite.ai/api"

var openBrowser = defaultOpenBrowser

func newLoginCmd() *cobra.Command {
	var orgSlug string
	var apiBase string
	c := &cobra.Command{
		Use:   "login",
		Short: "Pair this CLI with a Sidekick org",
		RunE: func(cmd *cobra.Command, _ []string) error {
			orgSlug = strings.TrimSpace(orgSlug)
			if orgSlug == "" {
				return fmt.Errorf("--org is required")
			}
			apiBase = loginAPIBase(apiBase)
			authPath, err := skauth.AuthFilePath()
			if err != nil {
				return err
			}
			client := &http.Client{Timeout: 10 * time.Second}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			hostname, _ := os.Hostname()
			if hostname == "" {
				hostname = runtime.GOOS
			}
			device, err := skauth.StartDeviceAuthorization(ctx, client, apiBase, orgSlug, hostname)
			if err != nil {
				return fmt.Errorf("start device login: %w", err)
			}
			url := device.VerificationURIComplete
			if url == "" {
				url = device.VerificationURI
			}
			if url == "" {
				return fmt.Errorf("device login response did not include a verification URL")
			}
			if err := openBrowser(url); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "open this URL to finish login:\n%s\n", url)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "opened browser for sidekick login\n")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "waiting for approval for org %s...\n", orgSlug)
			token, err := skauth.PollDeviceToken(ctx, client, apiBase, device)
			if err != nil {
				return fmt.Errorf("device login: %w", err)
			}
			profileOrg := token.OrgSlug
			if profileOrg == "" {
				profileOrg = orgSlug
			}
			if err := skauth.PutProfile(authPath, skauth.Profile{
				OrgSlug:      profileOrg,
				APIBase:      apiBase,
				Token:        token.AccessToken,
				AccountEmail: token.AccountEmail,
				IssuedAt:     time.Now(),
			}); err != nil {
				return err
			}
			if token.AccountEmail != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "paired %s as %s\n", profileOrg, token.AccountEmail)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "paired %s\n", profileOrg)
			}
			return nil
		},
	}
	c.Flags().StringVar(&orgSlug, "org", "", "org slug to pair with")
	c.Flags().StringVar(&apiBase, "api-base", "", "Sidekick API base URL")
	return c
}

func newAuthCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Manage Sidekick CLI authentication",
	}
	c.AddCommand(newAuthStatusCmd())
	return c
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the active Sidekick CLI pairing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			authPath, err := skauth.AuthFilePath()
			if err != nil {
				return err
			}
			profile, ok, err := skauth.CurrentProfile(authPath)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Fprintf(cmd.OutOrStdout(), "not logged in\n")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "logged in to %s at %s\n", profile.OrgSlug, profile.APIBase)
			if profile.AccountEmail != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "account: %s\n", profile.AccountEmail)
			}
			return nil
		},
	}
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the active Sidekick CLI pairing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			authPath, err := skauth.AuthFilePath()
			if err != nil {
				return err
			}
			profile, ok, err := skauth.RemoveCurrentProfile(authPath)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Fprintf(cmd.OutOrStdout(), "not logged in\n")
				return nil
			}
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := skauth.RevokeToken(ctx, &http.Client{Timeout: 5 * time.Second}, profile.APIBase, profile.Token); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "[sidekick] token revoke failed: %v\n", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "logged out of %s\n", profile.OrgSlug)
			return nil
		},
	}
}

func loginAPIBase(flagValue string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return skauth.RootAPIBase(v)
	}
	if v := strings.TrimSpace(os.Getenv("SIDEKICK_API_BASE")); v != "" {
		return skauth.RootAPIBase(v)
	}
	if v := strings.TrimSpace(os.Getenv("SIDEKICK_BACKEND_URL")); v != "" {
		return skauth.RootAPIBase(v)
	}
	return defaultAPIBase
}

func defaultOpenBrowser(url string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
		args = []string{url}
	case "windows":
		command = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		command = "xdg-open"
		args = []string{url}
	}
	return exec.Command(command, args...).Start()
}
