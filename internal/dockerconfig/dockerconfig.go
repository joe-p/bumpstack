// Package dockerconfig loads registry credentials from ~/.docker/config.json.
//
// It supports inline `auth` / `username`+`password` entries as well as the
// `credsStore` and `credHelpers` mechanisms, which shell out to the
// `docker-credential-*` helpers so `docker login` credentials just work.
package dockerconfig

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joe-p/bumpstack/internal/reference"
)

// Credential is a username/password pair usable for HTTP Basic auth.
type Credential struct {
	Username string
	Secret   string
}

// BasicHeader renders an `Authorization: Basic ...` header value.
func (c Credential) BasicHeader() string {
	raw := c.Username + ":" + c.Secret
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
}

// Store holds the credentials loaded from the Docker CLI configuration.
type Store struct {
	auths       map[string]Credential
	credHelpers map[string]string
	credsStore  string
	verbose     bool
}

// Load reads the Docker configuration, treating any failure as "no
// credentials".
func Load(verbose bool) *Store {
	store := &Store{
		auths:       map[string]Credential{},
		credHelpers: map[string]string{},
		verbose:     verbose,
	}

	path := configPath()
	if path == "" {
		return store
	}
	text, err := os.ReadFile(path)
	if err != nil {
		return store
	}

	var cfg dockerConfig
	if err := json.Unmarshal(text, &cfg); err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "warning: ignoring %s: %v\n", path, err)
		}
		return store
	}

	for key, entry := range cfg.Auths {
		host, ok := credentialKeyHost(key)
		if !ok {
			continue
		}
		if cred, ok := decodeEntry(entry); ok {
			store.auths[host] = cred
		}
	}
	for host, helper := range cfg.CredHelpers {
		if h, ok := credentialKeyHost(host); ok {
			store.credHelpers[h] = helper
		}
	}
	store.credsStore = cfg.CredsStore
	return store
}

// Get looks up credentials for a registry host.
func (s *Store) Get(registryHost string) (Credential, bool) {
	host := reference.NormalizeRegistryHost(registryHost)

	helper := s.credHelpers[host]
	if helper == "" {
		helper = s.credsStore
	}
	if helper != "" {
		cred, err := runHelper(helper, host)
		switch {
		case err != nil:
			if s.verbose {
				fmt.Fprintf(os.Stderr, "warning: docker-credential-%s failed: %v\n", helper, err)
			}
		case cred != nil:
			return *cred, true
		}
	}

	cred, ok := s.auths[host]
	return cred, ok
}

// credentialKeyHost maps a Docker config key to a registry host, rejecting keys
// that point at a sub-resource rather than the registry itself.
//
// ~/.docker/config.json normally stores `https://index.docker.io/v1/`, but
// tooling such as buildx also writes sibling keys like
// `https://index.docker.io/v1/access-token`. Those must not be mistaken for the
// registry's credentials.
func credentialKeyHost(key string) (string, bool) {
	trimmed := strings.TrimSpace(key)
	withoutScheme := strings.TrimPrefix(trimmed, "https://")
	withoutScheme = strings.TrimPrefix(withoutScheme, "http://")

	host, path, _ := strings.Cut(withoutScheme, "/")
	if host == "" {
		return "", false
	}
	normalizedPath := strings.Trim(path, "/")
	if normalizedPath != "" && normalizedPath != "v1" {
		return "", false
	}
	return reference.NormalizeRegistryHost(host), true
}

func decodeEntry(entry authEntry) (Credential, bool) {
	if entry.Auth != "" {
		if decoded, err := base64.StdEncoding.DecodeString(entry.Auth); err == nil {
			if username, secret, ok := strings.Cut(string(decoded), ":"); ok {
				return Credential{Username: username, Secret: secret}, true
			}
		}
	}
	if entry.Username != nil && entry.Password != nil {
		return Credential{Username: *entry.Username, Secret: *entry.Password}, true
	}
	return Credential{}, false
}

// runHelper invokes a `docker-credential-*` helper, feeding it the server URL
// on stdin. A missing helper or a non-zero exit is reported as "no credential".
func runHelper(helper, host string) (*Credential, error) {
	server := host
	if host == "docker.io" {
		server = "https://index.docker.io/v1/"
	}

	var stdout bytes.Buffer
	cmd := exec.Command("docker-credential-"+helper, "get")
	cmd.Stdin = strings.NewReader(server)
	cmd.Stdout = &stdout
	cmd.Stderr = nil // /dev/null

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, nil
		}
		return nil, err
	}

	var response helperResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return nil, nil
	}
	if response.Username == "" && response.Secret == "" {
		return nil, nil
	}
	return &Credential{Username: response.Username, Secret: response.Secret}, nil
}

func configPath() string {
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		return filepath.Join(dir, "config.json")
	}
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".docker", "config.json")
}

type dockerConfig struct {
	Auths       map[string]authEntry `json:"auths"`
	CredsStore  string               `json:"credsStore"`
	CredHelpers map[string]string    `json:"credHelpers"`
}

type authEntry struct {
	Auth     string  `json:"auth"`
	Username *string `json:"username"`
	Password *string `json:"password"`
}

type helperResponse struct {
	Username string `json:"Username"`
	Secret   string `json:"Secret"`
}
