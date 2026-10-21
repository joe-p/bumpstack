package registry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/joe-p/bumpstack/internal/dockerconfig"
)

func TestSessionRejectsCrossOriginURLs(t *testing.T) {
	requests := 0
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer other.Close()
	client := New(dockerconfig.Load(false), true)
	s := newSession(client, "registry.example", "app")
	s.auth = "Basic secret"
	for _, path := range []string{other.URL + "/tags", "//" + strings.TrimPrefix(other.URL, "http://") + "/tags", "https://registry.example/tags"} {
		if _, err := s.request(http.MethodGet, path, ""); err == nil {
			t.Errorf("request(%q) should reject an origin change", path)
		}
	}
	if requests != 0 {
		t.Errorf("cross-origin server received %d requests", requests)
	}
}

func TestSessionAllowsSameOriginPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing session authorization")
		}
		w.Write([]byte(`{"tags":[]}`))
	}))
	defer server.Close()
	s := newSession(New(dockerconfig.Load(false), true), strings.TrimPrefix(server.URL, "http://"), "app")
	s.auth = "Bearer token"
	for _, path := range []string{"/v2/app/tags/list?last=1", server.URL + "/v2/app/tags/list?last=1"} {
		if _, err := s.request(http.MethodGet, path, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParsesBearerChallenge(t *testing.T) {
	scheme, params := parseChallenge(
		`Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/redis:pull"`,
	)
	if scheme != "Bearer" {
		t.Errorf("scheme = %q", scheme)
	}
	if params["realm"] != "https://auth.docker.io/token" {
		t.Errorf("realm = %q", params["realm"])
	}
	if params["service"] != "registry.docker.io" {
		t.Errorf("service = %q", params["service"])
	}
	if params["scope"] != "repository:library/redis:pull" {
		t.Errorf("scope = %q", params["scope"])
	}
}

func TestParsesNextLink(t *testing.T) {
	link := `</v2/foo/tags/list?last=v1&n=1000>; rel="next", </v2/foo/tags/list?last=v9>; rel="prev"`
	if got := nextLink(link); got != "/v2/foo/tags/list?last=v1&n=1000" {
		t.Errorf("nextLink = %q", got)
	}
	if got := nextLink(`</x>; rel="prev"`); got != "" {
		t.Errorf("nextLink = %q, want empty", got)
	}
}

func TestPercentEncodesScope(t *testing.T) {
	if got := percentEncode("repository:library/redis:pull"); got != "repository%3Alibrary%2Fredis%3Apull" {
		t.Errorf("percentEncode = %q", got)
	}
}

func TestClassifiesIndexMediaTypes(t *testing.T) {
	if !isIndexMediaType("application/vnd.oci.image.index.v1+json") {
		t.Error("oci index should be an index")
	}
	if !isIndexMediaType("application/vnd.docker.distribution.manifest.list.v2+json") {
		t.Error("docker list should be an index")
	}
	if isIndexMediaType("application/vnd.oci.image.manifest.v1+json") {
		t.Error("oci manifest should not be an index")
	}
	if isIndexMediaType("application/vnd.docker.distribution.manifest.v2+json") {
		t.Error("docker manifest should not be an index")
	}
}

func TestExtractsRealPlatformsIgnoringAttestations(t *testing.T) {
	index := parseManifest(t, `{
		"manifests": [
			{"digest": "a", "platform": {"architecture": "amd64", "os": "linux"}},
			{"digest": "b", "platform": {"architecture": "arm64", "os": "linux"}},
			{"digest": "c", "platform": {"architecture": "unknown", "os": "unknown"}}
		]
	}`)
	if index.Manifests == nil {
		t.Fatal("manifests missing")
	}
	got := indexPlatforms(*index.Manifests)
	if want := []string{"linux/amd64", "linux/arm64"}; !slices.Equal(got, want) {
		t.Errorf("platforms = %v, want %v", got, want)
	}

	// An arch-specific tag is an index too, but only covers one platform.
	single := parseManifest(t, `{
		"manifests": [
			{"digest": "a", "platform": {"architecture": "arm64", "os": "linux"}},
			{"digest": "c", "platform": {"architecture": "unknown", "os": "unknown"}}
		]
	}`)
	if single.Manifests == nil {
		t.Fatal("manifests missing")
	}
	got = indexPlatforms(*single.Manifests)
	if want := []string{"linux/arm64"}; !slices.Equal(got, want) {
		t.Errorf("platforms = %v, want %v", got, want)
	}
}

func parseManifest(t *testing.T, text string) manifest {
	t.Helper()
	var m manifest
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	return m
}
