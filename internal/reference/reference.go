// Package reference parses OCI / Docker image references.
//
// A reference looks like [registry[:port]/]repository[:tag][@digest]. The
// registry component is only present when the first path segment looks like a
// hostname (contains ".", ":" or is "localhost"); otherwise Docker Hub is
// implied and official images gain an implicit "library/" namespace.
package reference

import (
	"fmt"
	"strings"
)

// NormalizeRegistryHost collapses the Docker Hub aliases to "docker.io" and
// lower-cases everything else, dropping any scheme and trailing path.
func NormalizeRegistryHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	host = strings.ToLower(host)
	switch host {
	case "index.docker.io", "registry-1.docker.io", "docker.io":
		return "docker.io"
	}
	return host
}

// Reference is a parsed image reference.
type Reference struct {
	// Registry is the canonical registry host, e.g. "docker.io" or "ghcr.io".
	Registry string
	// RegistryWritten is the registry as it appeared in the compose file, or
	// "" when Docker Hub was implied and nothing should be prepended.
	RegistryWritten string
	// Repository is the repository as used for API calls, e.g. "library/redis".
	Repository string
	// RepositoryWritten is the repository exactly as written, e.g. "redis".
	RepositoryWritten string
	// Tag is the tag, or "" when none was present.
	Tag string
	// Digest is the digest ("sha256:..."), or "" when none was present.
	Digest string
}

// Parse parses a reference such as "qmcgaw/gluetun:v3.41@sha256:...".
func Parse(input string) (Reference, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Reference{}, fmt.Errorf("empty image reference")
	}

	nameTag := input
	digest := ""
	if name, d, ok := strings.Cut(input, "@"); ok {
		if d == "" {
			return Reference{}, fmt.Errorf("image reference `%s` has an empty digest", input)
		}
		nameTag, digest = name, d
	}

	// A tag is a colon that appears after the last slash.
	name := nameTag
	tag := ""
	lastSlash := strings.LastIndex(nameTag, "/")
	if colon := strings.LastIndex(nameTag, ":"); colon > lastSlash {
		if colon+1 == len(nameTag) {
			return Reference{}, fmt.Errorf("image reference `%s` has an empty tag", input)
		}
		name, tag = nameTag[:colon], nameTag[colon+1:]
	}

	// The registry is present when the first segment looks like a host.
	registryWritten := ""
	repoPart := name
	if first, rest, ok := strings.Cut(name, "/"); ok &&
		(strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost") {
		registryWritten, repoPart = first, rest
	}

	registry := "docker.io"
	if registryWritten != "" {
		registry = NormalizeRegistryHost(registryWritten)
	}

	repository := repoPart
	if registry == "docker.io" && !strings.Contains(repository, "/") {
		repository = "library/" + repository
	}
	if repository == "" {
		return Reference{}, fmt.Errorf("image reference `%s` has no repository", input)
	}

	return Reference{
		Registry:          registry,
		RegistryWritten:   registryWritten,
		Repository:        repository,
		RepositoryWritten: repoPart,
		Tag:               tag,
		Digest:            digest,
	}, nil
}

// TagOrLatest returns the tag, defaulting to "latest".
func (r Reference) TagOrLatest() string {
	if r.Tag == "" {
		return "latest"
	}
	return r.Tag
}

// Render builds a new reference with the given tag and digest, preserving how
// the registry was originally written.
func (r Reference) Render(tag, digest string) string {
	var b strings.Builder
	if r.RegistryWritten != "" {
		b.WriteString(r.RegistryWritten)
		b.WriteByte('/')
	}
	b.WriteString(r.RepositoryWritten)
	b.WriteByte(':')
	b.WriteString(tag)
	b.WriteByte('@')
	b.WriteString(digest)
	return b.String()
}
