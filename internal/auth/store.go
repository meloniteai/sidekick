package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	if err := json.Unmarshal(raw, &st); err != nil {
		return st, err
	}
	if st.Current == "" && len(st.Profiles) == 0 {
		var legacy Profile
		if err := json.Unmarshal(raw, &legacy); err == nil && legacy.OrgSlug != "" && legacy.APIBase != "" && legacy.Token != "" {
			legacy = normalizeProfile(legacy)
			st = Store{
				Current:  ProfileID(legacy),
				Profiles: map[string]Profile{ProfileID(legacy): legacy},
			}
			return st, Save(path, st)
		}
	}
	if st.Profiles == nil {
		st.Profiles = map[string]Profile{}
	}
	if normalizeStore(&st) {
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
	profile = normalizeProfile(profile)
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
	id := ProfileID(profile)
	st.Profiles[id] = profile
	st.Current = id
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

func ListProfiles(path string) (Store, error) {
	st, err := Load(path)
	if err != nil {
		return Store{}, err
	}
	return st, nil
}

func UseProfile(path, id string) (Profile, bool, error) {
	st, err := Load(path)
	if err != nil {
		return Profile{}, false, err
	}
	id = strings.TrimSpace(id)
	profile, ok := st.Profiles[id]
	if !ok {
		return Profile{}, false, nil
	}
	st.Current = id
	if err := Save(path, st); err != nil {
		return Profile{}, false, err
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
	ids := sortedProfileIDs(st.Profiles)
	if len(ids) > 0 {
		st.Current = ids[0]
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
	profile, ok := selectProfile(st, configuredAPIBase)
	if !ok {
		return BackendTarget{}, false, nil
	}
	apiBase := RootAPIBase(configuredAPIBase)
	if apiBase == "" {
		apiBase = profile.APIBase
	}
	return BackendTarget{
		APIBase: OrgScopedAPIBase(apiBase, profile.OrgSlug),
		Token:   profile.Token,
		OrgSlug: profile.OrgSlug,
	}, true, nil
}

func ProfileID(profile Profile) string {
	profile = normalizeProfile(profile)
	if profile.OrgSlug == "" || profile.APIBase == "" {
		return strings.TrimSpace(profile.OrgSlug)
	}
	return profile.OrgSlug + "@" + profile.APIBase
}

func normalizeProfile(profile Profile) Profile {
	profile.OrgSlug = strings.TrimSpace(profile.OrgSlug)
	profile.APIBase = RootAPIBase(profile.APIBase)
	profile.Token = strings.TrimSpace(profile.Token)
	profile.AccountEmail = strings.TrimSpace(profile.AccountEmail)
	return profile
}

func normalizeStore(st *Store) bool {
	changed := false
	if st.Profiles == nil {
		st.Profiles = map[string]Profile{}
		changed = true
	}
	profiles := make(map[string]Profile, len(st.Profiles))
	current := st.Current
	for id, profile := range st.Profiles {
		profile = normalizeProfile(profile)
		newID := ProfileID(profile)
		if newID == "" {
			newID = strings.TrimSpace(id)
		}
		if id != newID {
			changed = true
			if current == id {
				current = newID
			}
		}
		if profile != st.Profiles[id] {
			changed = true
		}
		profiles[newID] = profile
	}
	st.Profiles = profiles
	if current != "" {
		if _, ok := st.Profiles[current]; !ok {
			current = ""
			changed = true
		}
	}
	if current == "" && len(st.Profiles) > 0 {
		ids := sortedProfileIDs(st.Profiles)
		current = ids[0]
		changed = true
	}
	if st.Current != current {
		st.Current = current
		changed = true
	}
	return changed
}

func selectProfile(st Store, configuredAPIBase string) (Profile, bool) {
	configuredRoot := RootAPIBase(configuredAPIBase)
	if configuredRoot != "" {
		for _, id := range sortedProfileIDs(st.Profiles) {
			profile := st.Profiles[id]
			if RootAPIBase(profile.APIBase) == configuredRoot {
				return profile, true
			}
		}
	}
	if st.Current == "" {
		return Profile{}, false
	}
	profile, ok := st.Profiles[st.Current]
	return profile, ok
}

func sortedProfileIDs(profiles map[string]Profile) []string {
	ids := make([]string, 0, len(profiles))
	for id := range profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
