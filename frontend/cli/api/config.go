package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Config is what `wm login` keeps on this computer, in a file only its owner can read (0600).
type Config struct {
	APIKey string `json:"api_key,omitempty"`
}

// Format prints a Config without its key, whatever the verb.
func (c Config) Format(f fmt.State, _ rune) {
	state := "none"
	if c.APIKey != "" {
		state = "[hidden]"
	}
	_, _ = io.WriteString(f, "api.Config{api_key: "+state+"}")
}

// ConfigPath is where the settings file is: $XDG_CONFIG_HOME/wheelmachine/config.json, else
// ~/.config/wheelmachine/config.json. getenv is os.Getenv outside tests.
func ConfigPath(getenv func(string) string) (string, error) {
	if dir := getenv("XDG_CONFIG_HOME"); dir != "" && filepath.IsAbs(dir) {
		return filepath.Join(dir, "wheelmachine", "config.json"), nil
	}
	home := getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("no home folder to keep the settings in: set HOME or XDG_CONFIG_HOME")
		}
		home = h
	}
	return filepath.Join(home, ".config", "wheelmachine", "config.json"), nil
}

// LoadConfig reads the settings file; one that does not exist is an empty Config.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("the settings file %s could not be read: %w", path, err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("the settings file %s is not valid JSON: run `wm login` to write it again", path)
	}
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	return cfg, nil
}

// SaveConfig writes the settings file whole or not at all, readable by its owner only (0600), in a folder only
// its owner can enter (0700 when it is made).
func SaveConfig(path string, cfg Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("the settings folder %s could not be made: %w", dir, err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json") // made 0600
	if err != nil {
		return fmt.Errorf("the settings file could not be written in %s: %w", dir, err)
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fmt.Errorf("the settings file could not be made private: %w", err)
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("the settings file could not be written: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("the settings file could not be written: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("the settings file could not be written: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("the settings file %s could not be put in place: %w", path, err)
	}
	done = true
	return os.Chmod(path, 0o600)
}

// RemoveConfig deletes the settings file, saying whether there was one.
func RemoveConfig(path string) (bool, error) {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("the settings file %s could not be deleted: %w", path, err)
	}
	return true, nil
}
