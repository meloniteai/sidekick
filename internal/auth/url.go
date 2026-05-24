package auth

import (
	"net/url"
	"strings"
)

func RootAPIBase(apiBase string) string {
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" {
		return ""
	}
	if i := strings.Index(base, "/orgs/"); i >= 0 {
		return base[:i]
	}
	return base
}

func OrgScopedAPIBase(apiBase, orgSlug string) string {
	base := strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if base == "" || strings.TrimSpace(orgSlug) == "" {
		return base
	}
	if strings.Contains(base, "/orgs/") {
		return base
	}
	return base + "/orgs/" + url.PathEscape(strings.TrimSpace(orgSlug))
}
