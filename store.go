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
func saveCreds(c Creds) error {
	p, err := credsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(p, data, 0o600)
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
