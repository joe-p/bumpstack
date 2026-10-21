package registry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/joe-p/bumpstack/internal/dockerconfig"
	"github.com/joe-p/bumpstack/internal/reference"
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

// tagServer serves a tag list plus the manifests and config blobs needed to
// probe push times. It counts the requests that probing costs.
func tagServer(t *testing.T, tags ...string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var probes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			body, err := json.Marshal(tagsPage{Tags: tags})
			if err != nil {
				t.Error(err)
			}
			w.Write(body)
		case strings.Contains(r.URL.Path, "/manifests/"):
			probes.Add(1)
			w.Write([]byte(`{"config":{"digest":"sha256:cfg"}}`))
		case strings.Contains(r.URL.Path, "/blobs/"):
			probes.Add(1)
			w.Write([]byte(`{"created":"2024-01-01T00:00:00Z"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &probes
}

func TestVersionOrderingSkipsPushTimeProbing(t *testing.T) {
	server, probes := tagServer(t, "1", "2", "3")
	client := New(dockerconfig.Load(false), true)
	ref := ociReference(t, server.URL)
	pattern := regexp.MustCompile(`^\d+$`)

	matches, err := client.FindMatchingTags(ref, pattern, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Fatalf("matches = %d, want 3", len(matches))
	}
	if got := probes.Load(); got != 0 {
		t.Errorf("version ordering made %d manifest/blob requests, want 0", got)
	}
	for _, match := range matches {
		if match.Pushed != nil {
			t.Errorf("%s has a push time under version ordering", match.Tag)
		}
	}
}

func TestPushedOrderingProbesPushTimes(t *testing.T) {
	server, probes := tagServer(t, "1", "2", "3")
	client := New(dockerconfig.Load(false), true)
	ref := ociReference(t, server.URL)

	matches, err := client.FindMatchingTags(ref, regexp.MustCompile(`^\d+$`), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Fatalf("matches = %d, want 3", len(matches))
	}
	if probes.Load() == 0 {
		t.Error("pushed ordering did not probe any push times")
	}
	for _, match := range matches {
		if match.Pushed == nil {
			t.Errorf("%s has no push time under pushed ordering", match.Tag)
		}
	}
}

func TestBroadPatternsSkipProbing(t *testing.T) {
	tags := make([]string, 0, maxProbeTags+1)
	for i := 0; i <= maxProbeTags; i++ {
		tags = append(tags, strconv.Itoa(i))
	}
	server, probes := tagServer(t, tags...)
	client := New(dockerconfig.Load(false), true)
	ref := ociReference(t, server.URL)

	matches, err := client.FindMatchingTags(ref, regexp.MustCompile(`^\d+$`), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != len(tags) {
		t.Fatalf("matches = %d, want %d", len(matches), len(tags))
	}
	if probes.Load() != 0 {
		t.Errorf("a pattern with %d matches still probed %d tags", len(matches), probes.Load())
	}
}

func TestTagListingFollowsNextLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "last=b") {
			w.Write([]byte(`{"tags":["v2"]}`))
			return
		}
		w.Header().Set("Link", `</v2/app/tags/list?last=b&n=1000>; rel="next"`)
		w.Write([]byte(`{"tags":["v1"]}`))
	}))
	defer server.Close()

	client := New(dockerconfig.Load(false), true)
	ref := ociReference(t, server.URL)
	matches, err := client.FindMatchingTags(ref, regexp.MustCompile(`^v\d+$`), true)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(matches))
	for i, match := range matches {
		got[i] = match.Tag
	}
	if want := []string{"v1", "v2"}; !slices.Equal(got, want) {
		t.Errorf("tags = %v, want %v", got, want)
	}
}

func ociReference(t *testing.T, serverURL string) reference.Reference {
	t.Helper()
	ref, err := reference.Parse(strings.TrimPrefix(serverURL, "http://") + "/app")
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
