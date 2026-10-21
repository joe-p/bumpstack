package dockerconfig

import (
	"encoding/base64"
	"testing"
)

func TestAcceptsRegistryKeys(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"https://index.docker.io/v1/", "docker.io"},
		{"index.docker.io", "docker.io"},
		{"ghcr.io", "ghcr.io"},
		{"https://ghcr.io", "ghcr.io"},
	}
	for _, c := range cases {
		got, ok := credentialKeyHost(c.key)
		if !ok || got != c.want {
			t.Errorf("credentialKeyHost(%q) = %q, %v; want %q, true", c.key, got, ok, c.want)
		}
	}
}

func TestRejectsSubResourceKeys(t *testing.T) {
	// Written by tools such as buildx; must not shadow the real entry.
	for _, key := range []string{
		"https://index.docker.io/v1/access-token",
		"https://index.docker.io/v1/refresh-token",
	} {
		if got, ok := credentialKeyHost(key); ok {
			t.Errorf("credentialKeyHost(%q) = %q, true; want _, false", key, got)
		}
	}
}

func TestDecodesAuthPairs(t *testing.T) {
	auth := base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	cred, ok := decodeEntry(authEntry{Auth: auth})
	if !ok {
		t.Fatal("decodeEntry failed")
	}
	if cred.Username != "alice" || cred.Secret != "s3cret" {
		t.Errorf("cred = %+v", cred)
	}
	if got := cred.BasicHeader(); len(got) < 6 || got[:6] != "Basic " {
		t.Errorf("basic header = %q", got)
	}
}
