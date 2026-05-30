package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const envAuthFile = "SIDEKICK_AUTH_FILE"

type Store struct {
	Current  string             `json:"current,omitempty"`
	Profiles map[string]Profile `json:"profiles,omitempty"`
}

type Profile struct {
	OrgSlug      string    `json:"org_slug"`
	APIBase      string    `json:"api_base"`
	Token        string    `json:"token"`
	AccountEmail string    `json:"account_email,omitempty"`
	IssuedAt     time.Time `json:"issued_at"`
}

func AuthFilePath() (string, error) {
	if v := strings.TrimSpace(os.Getenv(envAuthFile)); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sidekick", "auth.json"), nil
}

func Load(path string) (Store, error) {
	var st Store
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Store{Profiles: map[string]Profile{}}, nil
	}
	if err != nil {
		return st, err
	}
	migrated := false
	if legacy, ok := legacyProfile(raw); ok {
		st = Store{
			Current:  profileKey(legacy),
			Profiles: map[string]Profile{profileKey(legacy): legacy},
		}
		migrated = true
	} else if err := json.Unmarshal(raw, &st); err != nil {
		return st, err
	}
	if st.Profiles == nil {
		st.Profiles = map[string]Profile{}
	}
	if canonicalizeStore(&st) {
		migrated = true
	}
	if migrated {
		if err := Save(path, st); err != nil {
			return Store{}, err
		}
	}
	return st, nil
}

func Save(path string, st Store) error {
	if st.Profiles == nil {
		st.Profiles = map[string]Profile{}
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func PutProfile(path string, profile Profile) error {
	profile.OrgSlug = strings.TrimSpace(profile.OrgSlug)
	profile.APIBase = RootAPIBase(profile.APIBase)
	profile.Token = strings.TrimSpace(profile.Token)
	if profile.OrgSlug == "" {
		return fmt.Errorf("auth: org slug is required")
	}
	if profile.APIBase == "" {
		return fmt.Errorf("auth: api base is required")
	}
	if profile.Token == "" {
		return fmt.Errorf("auth: token is required")
	}
	if profile.IssuedAt.IsZero() {
		profile.IssuedAt = time.Now()
	}
	st, err := Load(path)
	if err != nil {
		return err
	}
	key := profileKey(profile)
	st.Profiles[key] = profile
	st.Current = key
	return Save(path, st)
}

func CurrentProfile(path string) (Profile, bool, error) {
	st, err := Load(path)
	if err != nil {
		return Profile{}, false, err
	}
	if st.Current == "" {
		return Profile{}, false, nil
	}
	profile, ok := st.Profiles[st.Current]
	if !ok {
		return Profile{}, false, nil
	}
	return profile, true, nil
}

func RemoveCurrentProfile(path string) (Profile, bool, error) {
	st, err := Load(path)
	if err != nil {
		return Profile{}, false, err
	}
	if st.Current == "" {
		return Profile{}, false, nil
	}
	profile, ok := st.Profiles[st.Current]
	delete(st.Profiles, st.Current)
	st.Current = ""
	for slug := range st.Profiles {
		st.Current = slug
		break
	}
	if err := Save(path, st); err != nil {
		return Profile{}, false, err
	}
	return profile, ok, nil
}

type BackendTarget struct {
	APIBase string
	Token   string
	OrgSlug string
}

func ResolveBackendTarget(path, configuredAPIBase string) (BackendTarget, bool, error) {
	st, err := Load(path)
	if err != nil {
		return BackendTarget{}, false, err
	}
	apiBase := strings.TrimSpace(configuredAPIBase)
	profile, ok := st.profileForAPIBase(apiBase)
	if !ok {
		return BackendTarget{}, false, nil
	}
	if apiBase == "" {
		apiBase = profile.APIBase
	}
	return BackendTarget{
		APIBase: OrgScopedAPIBase(apiBase, profile.OrgSlug),
		Token:   profile.Token,
		OrgSlug: profile.OrgSlug,
	}, true, nil
}

func legacyProfile(raw []byte) (Profile, bool) {
	var profile Profile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return Profile{}, false
	}
	profile.OrgSlug = strings.TrimSpace(profile.OrgSlug)
	profile.APIBase = RootAPIBase(profile.APIBase)
	profile.Token = strings.TrimSpace(profile.Token)
	return profile, profile.OrgSlug != "" && profile.APIBase != "" && profile.Token != ""
}

func canonicalizeStore(st *Store) bool {
	migrated := false
	next := make(map[string]Profile, len(st.Profiles))
	currentKey := ""
	for oldKey, profile := range st.Profiles {
		profile.OrgSlug = strings.TrimSpace(profile.OrgSlug)
		profile.APIBase = RootAPIBase(profile.APIBase)
		profile.Token = strings.TrimSpace(profile.Token)
		if profile.OrgSlug == "" || profile.APIBase == "" {
			continue
		}
		key := profileKey(profile)
		next[key] = profile
		if oldKey != key {
			migrated = true
		}
		if oldKey == st.Current || key == st.Current {
			currentKey = key
		}
	}
	if len(next) != len(st.Profiles) {
		migrated = true
	}
	if st.Current != "" && currentKey == "" {
		migrated = true
	}
	if currentKey == "" && len(next) == 1 {
		for key := range next {
			currentKey = key
		}
		migrated = true
	}
	if st.Current != currentKey {
		migrated = true
	}
	st.Profiles = next
	st.Current = currentKey
	return migrated
}

func profileKey(profile Profile) string {
	return RootAPIBase(profile.APIBase) + "|" + strings.TrimSpace(profile.OrgSlug)
}

func (st Store) profileForAPIBase(apiBase string) (Profile, bool) {
	if st.Current != "" {
		profile, ok := st.Profiles[st.Current]
		if ok && (apiBase == "" || RootAPIBase(profile.APIBase) == RootAPIBase(apiBase)) {
			return profile, true
		}
	}
	if apiBase != "" {
		keyBase := RootAPIBase(apiBase)
		for _, profile := range st.Profiles {
			if RootAPIBase(profile.APIBase) == keyBase {
				return profile, true
			}
		}
	}
	if st.Current != "" {
		profile, ok := st.Profiles[st.Current]
		return profile, ok
	}
	return Profile{}, false
}
