package reference

import "testing"

func TestParsesDockerHubShortForm(t *testing.T) {
	r, err := Parse("redis:7.2")
	if err != nil {
		t.Fatal(err)
	}
	if r.Registry != "docker.io" {
		t.Errorf("registry = %q, want docker.io", r.Registry)
	}
	if r.RegistryWritten != "" {
		t.Errorf("registryWritten = %q, want empty", r.RegistryWritten)
	}
	if r.Repository != "library/redis" {
		t.Errorf("repository = %q, want library/redis", r.Repository)
	}
	if r.RepositoryWritten != "redis" {
		t.Errorf("repositoryWritten = %q, want redis", r.RepositoryWritten)
	}
	if r.Tag != "7.2" {
		t.Errorf("tag = %q, want 7.2", r.Tag)
	}
	if r.Digest != "" {
		t.Errorf("digest = %q, want empty", r.Digest)
	}
	if got := r.Render("7.4", "sha256:aa"); got != "redis:7.4@sha256:aa" {
		t.Errorf("render = %q", got)
	}
}

func TestParsesUserRepoWithDigest(t *testing.T) {
	r, err := Parse("qmcgaw/gluetun:v3.41@sha256:fa19cc76b2af13d57a8d3dc3066f2ada061b1c761b8aecf989b3877c0486e027")
	if err != nil {
		t.Fatal(err)
	}
	if r.Registry != "docker.io" || r.Repository != "qmcgaw/gluetun" {
		t.Errorf("unexpected registry/repository: %q %q", r.Registry, r.Repository)
	}
	if r.Tag != "v3.41" {
		t.Errorf("tag = %q, want v3.41", r.Tag)
	}
	if r.Digest != "sha256:fa19cc76b2af13d57a8d3dc3066f2ada061b1c761b8aecf989b3877c0486e027" {
		t.Errorf("digest = %q", r.Digest)
	}
	if got := r.Render("v3.42", "sha256:deadbeef"); got != "qmcgaw/gluetun:v3.42@sha256:deadbeef" {
		t.Errorf("render = %q", got)
	}
}

func TestParsesExplicitRegistryWithPort(t *testing.T) {
	r, err := Parse("localhost:5000/team/app:1.0")
	if err != nil {
		t.Fatal(err)
	}
	if r.Registry != "localhost:5000" {
		t.Errorf("registry = %q", r.Registry)
	}
	if r.RegistryWritten != "localhost:5000" {
		t.Errorf("registryWritten = %q", r.RegistryWritten)
	}
	if r.Repository != "team/app" {
		t.Errorf("repository = %q", r.Repository)
	}
	if r.Tag != "1.0" {
		t.Errorf("tag = %q", r.Tag)
	}
	if got := r.Render("1.1", "sha256:aa"); got != "localhost:5000/team/app:1.1@sha256:aa" {
		t.Errorf("render = %q", got)
	}
}

func TestGhcrWithoutLibraryPrefix(t *testing.T) {
	r, err := Parse("ghcr.io/owner/app")
	if err != nil {
		t.Fatal(err)
	}
	if r.Registry != "ghcr.io" || r.Repository != "owner/app" {
		t.Errorf("unexpected registry/repository: %q %q", r.Registry, r.Repository)
	}
	if r.Tag != "" {
		t.Errorf("tag = %q, want empty", r.Tag)
	}
	if r.TagOrLatest() != "latest" {
		t.Errorf("tagOrLatest = %q", r.TagOrLatest())
	}
}

func TestNormalisesHubAliases(t *testing.T) {
	r, err := Parse("docker.io/library/redis:7")
	if err != nil {
		t.Fatal(err)
	}
	if r.Registry != "docker.io" || r.Repository != "library/redis" {
		t.Errorf("unexpected registry/repository: %q %q", r.Registry, r.Repository)
	}
	if r.RegistryWritten != "docker.io" {
		t.Errorf("registryWritten = %q", r.RegistryWritten)
	}
	if got := NormalizeRegistryHost("https://index.docker.io/v1/"); got != "docker.io" {
		t.Errorf("normalize = %q, want docker.io", got)
	}
	if got := NormalizeRegistryHost("ghcr.io"); got != "ghcr.io" {
		t.Errorf("normalize = %q, want ghcr.io", got)
	}
}
