package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Creds is the small bit of state persisted between launches: enough to relaunch
// without a browser, and to remember which character was last used.
type Creds struct {
	RefreshToken string `json:"refresh_token"`
	SessionID    string `json:"session_id"`
	AccountID    string `json:"account_id"`
}

// credsPath returns $XDG_DATA_HOME/osrs-login/creds.json (or the ~/.local/share
// fallback).
func credsPath() (string, error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "osrs-login", "creds.json"), nil
}

// loadCreds reads saved credentials. A missing file yields a zero Creds, not an
// error — the caller treats that as "needs login".
func loadCreds() (Creds, error) {
	var c Creds
	p, err := credsPath()
	if err != nil {
		return c, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return c, err
	}
	_ = json.Unmarshal(data, &c)
	return c, nil
}

// saveCreds writes credentials with 0600 permissions (they grant account access).
// It writes a temp file and renames it into place, so a crash mid-write can't
// leave a truncated file and lose the (rotating) refresh token.
func saveCreds(c Creds) error {
	p, err := credsPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	f, err := os.CreateTemp(dir, ".creds-*.json") // created 0600
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // no-op once renamed
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

// clearCreds removes the saved credentials, if any.
func clearCreds() error {
	p, err := credsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
