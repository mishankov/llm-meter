package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mishankov/llm-meter/internal/relay"
)

const (
	appDirName    = "LLM Meter Relay"
	configFile    = "config.json"
	queuesDirName = "queues"
)

// AppConfig holds non-secret desktop relay configuration persisted in config.json.
// Only the native key store holds the raw credential.
type AppConfig struct {
	Target    string `json:"target"`
	Mode      string `json:"mode"`
	Listen    string `json:"listen"`
	Provider  string `json:"provider"`
	Upstream  string `json:"upstream"`
	AutoStart bool   `json:"autoStart"`
}

func defaultAppConfig() AppConfig {
	return AppConfig{
		Mode:      relay.ModeUsageOnly,
		Listen:    relay.DefaultListen,
		AutoStart: true,
	}
}

type storedConfig struct {
	AppConfig
	KeyID string      `json:"keyId,omitempty"`
	Kept  []keptQueue `json:"keptQueues,omitempty"`
}

// A kept queue owns a reference to its original credential until it drains
// or becomes the active usage-only queue again.
type keptQueue struct {
	ID     string    `json:"id"`
	KeyID  string    `json:"keyId"`
	Config AppConfig `json:"config"`
}

type keyStore interface {
	get(id string) (string, error)
	add(id, key string) error
	remove(id string) error
}

type ConfigStore struct {
	dir  string
	keys keyStore
}

func DefaultConfigStore() (*ConfigStore, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locate user config directory: %w", err)
	}
	dir := filepath.Join(base, appDirName)
	return &ConfigStore{dir: dir, keys: platformKeyStore(dir)}, nil
}

func (s *ConfigStore) LoadConfig() (storedConfig, error) {
	cfg := storedConfig{AppConfig: defaultAppConfig()}
	data, err := os.ReadFile(filepath.Join(s.dir, configFile))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Stage a new credential before switching the config file. Even if the app
// exits during rotation, the on-disk settings still point at a matching key.
func (s *ConfigStore) SaveConfig(cfg storedConfig, newKey string) (storedConfig, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return cfg, fmt.Errorf("create config directory: %w", err)
	}
	if newKey != "" {
		cfg.KeyID = rand.Text()
		if err := s.keys.add(cfg.KeyID, newKey); err != nil {
			return cfg, fmt.Errorf("store proxy key: %w", err)
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err == nil {
		err = writeFileAtomic(filepath.Join(s.dir, configFile), append(data, '\n'), 0o600)
	}
	if err != nil && newKey != "" {
		err = errors.Join(err, s.keys.remove(cfg.KeyID))
	}
	return cfg, err
}

func (s *ConfigStore) LoadKey(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	key, err := s.keys.get(id)
	if err != nil {
		return "", fmt.Errorf("load proxy key: %w", err)
	}
	if key == "" {
		return "", errors.New("the saved proxy key is missing from the key store")
	}
	return key, nil
}

// QueueDir returns the isolated SQLite outbox directory for a (target, key) pair.
func (s *ConfigStore) QueueDir(target, key string) (string, error) {
	id, err := relay.ConnectionID(target, key)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.dir, queuesDirName, id), nil
}

// KeyPrefix returns a safe non-secret prefix matching the dashboard's 13-character preview.
func KeyPrefix(key string) string {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 13 {
		return "••••"
	}
	return trimmed[:13] + "…"
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	cleanup = false
	return nil
}

// DetectAntigravityBinary searches common macOS locations and PATH for an installed Antigravity CLI.
func DetectAntigravityBinary() string {
	candidates := []string{
		"/Applications/Antigravity.app/Contents/Resources/app/bin/antigravity",
		"/Applications/Antigravity.app/Contents/MacOS/antigravity",
		"/usr/local/bin/antigravity",
		"/opt/homebrew/bin/antigravity",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return c
		}
	}
	for _, name := range []string{"antigravity", "agy"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// Put the generated launcher outside PATH so it never shadows the installed CLI.
func DefaultLauncherPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, appDirName, "bin", "antigravity")
}

const launcherHeader = "#!/bin/sh\n# Generated by LLM Meter Relay.\n"

// BuildAntigravityLauncher produces the T3 Code launcher script pointing at listenAddr.
func BuildAntigravityLauncher(listenAddr, binaryPath string) (string, error) {
	if strings.TrimSpace(listenAddr) == "" {
		listenAddr = relay.DefaultListen
	}
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" {
		binaryPath = "/absolute/path/to/antigravity"
	} else if !filepath.IsAbs(binaryPath) {
		return "", errors.New("Antigravity binary path must be absolute")
	} else if strings.ContainsAny(binaryPath, "\r\n\x00") {
		return "", errors.New("Antigravity binary path contains invalid characters")
	}
	quotedBin := posixSingleQuote(binaryPath)
	quotedEndpoint := posixSingleQuote("http://" + listenAddr)
	return fmt.Sprintf(launcherHeader+`if [ "$1" = acp ]; then
  shift
  set -- acp --cloud_code_endpoint %s "$@"
fi
exec %s "$@"
`, quotedEndpoint, quotedBin), nil
}

// InstallAntigravityLauncher writes an executable launcher named antigravity or agy.
func InstallAntigravityLauncher(destPath, listenAddr, binaryPath string) (string, error) {
	destPath = strings.TrimSpace(destPath)
	if destPath == "" {
		destPath = DefaultLauncherPath()
	}
	if destPath == "" || !filepath.IsAbs(destPath) {
		return "", errors.New("launcher path must be absolute")
	}
	base := filepath.Base(destPath)
	if base != "antigravity" && base != "agy" {
		return "", errors.New("launcher file must be named antigravity or agy for T3 Code")
	}
	binaryPath = strings.TrimSpace(binaryPath)
	if binaryPath == "" || !filepath.IsAbs(binaryPath) {
		return "", errors.New("select the installed Antigravity executable first")
	}
	if filepath.Clean(destPath) == filepath.Clean(binaryPath) {
		return "", errors.New("launcher destination cannot overwrite the installed Antigravity executable")
	}
	source, err := os.Stat(binaryPath)
	if err != nil || !source.Mode().IsRegular() || source.Mode().Perm()&0o111 == 0 {
		return "", errors.New("select an existing executable Antigravity file")
	}
	// Regenerate our own scripts, but preserve unrelated binaries, symlinks,
	// and scripts that happen to use the same name.
	if dest, err := os.Lstat(destPath); err == nil {
		if !dest.Mode().IsRegular() || os.SameFile(source, dest) {
			return "", errors.New("choose a separate destination for the launcher")
		}
		f, err := os.Open(destPath)
		if err != nil {
			return "", err
		}
		header, err := io.ReadAll(io.LimitReader(f, int64(len(launcherHeader))))
		f.Close()
		if err != nil || string(header) != launcherHeader {
			return "", errors.New("the destination already contains another file; choose a new launcher path")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	script, err := BuildAntigravityLauncher(listenAddr, binaryPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return "", fmt.Errorf("create launcher directory: %w", err)
	}
	if err := writeFileAtomic(destPath, []byte(script), 0o755); err != nil {
		return "", err
	}
	return destPath, nil
}

func posixSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
