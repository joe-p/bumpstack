// Package registry lists tags and resolves multi-arch manifest digests.
//
// Two listing strategies are implemented:
//
//   - Docker Hub uses its REST API, which (unlike the OCI distribution spec)
//     exposes a push timestamp per tag. Because the API is ordered by
//     `last_updated` descending, the first matching tag is also the most
//     recently pushed one.
//   - Every other registry uses the OCI `/v2/<name>/tags/list` endpoint with the
//     standard bearer-token handshake. Push times are not part of the protocol,
//     so the image config `created` timestamp is used as a best-effort proxy.
//
// The digest written to the compose file always comes from a manifest request
// on the tag, whose `Docker-Content-Digest` header is the digest of the
// multi-arch manifest list/index when one exists.
package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/joe-p/bumpstack/internal/dockerconfig"
	"github.com/joe-p/bumpstack/internal/reference"
)

const manifestAccept = "application/vnd.oci.image.index.v1+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.docker.distribution.manifest.v2+json"

// Version is the version reported in the User-Agent header.
var Version = "0.1.0"

// TagMatch is a tag that matched an image's version pattern.
type TagMatch struct {
	Tag string
	// Pushed is when the tag was pushed, if known.
	Pushed *time.Time
}

// ResolvedDigest is the manifest a tag points at.
type ResolvedDigest struct {
	// Digest is the digest of the manifest the tag resolves to.
	Digest string
	// IsIndex is true when the manifest is a multi-arch index / manifest list.
	IsIndex bool
	// MediaType is the manifest media type, when the registry reported one.
	MediaType string
	// Platforms are the distinct real platforms, e.g. "linux/amd64".
	Platforms []string
}

// IsMultiarch reports whether the manifest covers more than one architecture.
func (r ResolvedDigest) IsMultiarch() bool { return len(r.Platforms) > 1 }

// Client talks to registries.
type Client struct {
	http     *http.Client
	creds    *dockerconfig.Store
	insecure bool
	maxTags  int
}

// New builds a registry client.
func New(creds *dockerconfig.Store, insecure bool) *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &Client{
		http:     &http.Client{Transport: transport},
		creds:    creds,
		insecure: insecure,
		maxTags:  20_000,
	}
}

func (c *Client) scheme() string {
	if c.insecure {
		return "http"
	}
	return "https"
}

// apiHost is the host that serves the OCI distribution API for a reference.
func apiHost(image reference.Reference) string {
	if image.Registry == "docker.io" {
		return "registry-1.docker.io"
	}
	return image.Registry
}

// FindMatchingTags returns the tags matching pattern that the registry knows
// about.
//
// When needAll is false and the registry can order tags by push time, finding
// the first match is enough and paging stops early. Otherwise every match is
// collected so the caller can pick by version.
func (c *Client) FindMatchingTags(image reference.Reference, pattern *regexp.Regexp, needAll bool) ([]TagMatch, error) {
	if image.Registry == "docker.io" {
		if matches, err := c.dockerHubTags(image, pattern, needAll); err == nil && len(matches) > 0 {
			return matches, nil
		}
	}
	return c.ociTags(image, pattern)
}

// ResolveDigest resolves the manifest a tag currently points at, including
// whether it is a multi-arch index or a single-platform manifest.
func (c *Client) ResolveDigest(image reference.Reference, tag string) (ResolvedDigest, error) {
	session := newSession(c, apiHost(image), image.Repository)
	path := fmt.Sprintf("/v2/%s/manifests/%s", image.Repository, tag)

	resp, err := session.request(http.MethodGet, path, manifestAccept)
	if err != nil {
		return ResolvedDigest{}, err
	}
	headerType := headerMediaType(resp)
	headerDigest := resp.header.Get("Docker-Content-Digest")
	var manifest manifest
	_ = json.Unmarshal(resp.body, &manifest)

	digest := headerDigest
	if digest == "" {
		resp, err := session.request(http.MethodHead, path, manifestAccept)
		if err != nil {
			return ResolvedDigest{}, err
		}
		digest = resp.header.Get("Docker-Content-Digest")
		if digest == "" {
			return ResolvedDigest{}, fmt.Errorf("registry did not return a digest for %s:%s", image.Repository, tag)
		}
	}

	mediaType := headerType
	if mediaType == "" {
		mediaType = manifest.MediaType
	}
	isIndex := manifest.Manifests != nil ||
		(manifest.Config == nil && isIndexMediaType(mediaType))

	var platforms []string
	if manifest.Manifests != nil {
		platforms = indexPlatforms(*manifest.Manifests)
	}

	return ResolvedDigest{
		Digest:    digest,
		IsIndex:   isIndex,
		MediaType: mediaType,
		Platforms: platforms,
	}, nil
}

// -- Docker Hub -------------------------------------------------------------

func (c *Client) dockerHubTags(image reference.Reference, pattern *regexp.Regexp, needAll bool) ([]TagMatch, error) {
	namespace, repository := "library", image.Repository
	if ns, repo, ok := strings.Cut(image.Repository, "/"); ok {
		namespace, repository = ns, repo
	}

	url := fmt.Sprintf(
		"https://hub.docker.com/v2/repositories/%s/%s/tags?page_size=100&ordering=last_updated",
		namespace, repository,
	)
	var matches []TagMatch
	scanned, pages := 0, 0

	for {
		var page hubTagsPage
		if err := c.getJSON(url, &page); err != nil {
			return nil, fmt.Errorf("querying Docker Hub for %s/%s: %w", namespace, repository, err)
		}

		for _, entry := range page.Results {
			scanned++
			if !pattern.MatchString(entry.Name) {
				continue
			}
			// Results are ordered by last_updated descending, so unless every
			// match is needed the first one is the newest push.
			pushed := entry.LastUpdated
			if pushed == "" {
				pushed = entry.TagLastPushed
			}
			matches = append(matches, TagMatch{Tag: entry.Name, Pushed: parseTime(pushed)})
			if !needAll {
				return matches, nil
			}
		}

		if page.Next == "" {
			return matches, nil
		}
		url = page.Next
		pages++
		if pages >= 200 || scanned >= c.maxTags {
			return matches, nil
		}
	}
}

// -- OCI distribution -------------------------------------------------------

func (c *Client) ociTags(image reference.Reference, pattern *regexp.Regexp) ([]TagMatch, error) {
	session := newSession(c, apiHost(image), image.Repository)

	path := fmt.Sprintf("/v2/%s/tags/list?n=1000", image.Repository)
	var tags []string
	pages := 0

	for {
		resp, err := session.request(http.MethodGet, path, "")
		if err != nil {
			return nil, err
		}
		next := nextLink(resp.header.Get("Link"))
		var page tagsPage
		if err := json.Unmarshal(resp.body, &page); err != nil {
			return nil, fmt.Errorf("decoding tag list response: %w", err)
		}
		tags = append(tags, page.Tags...)

		if len(tags) >= c.maxTags {
			break
		}
		if next == "" {
			break
		}
		path = next
		pages++
		if pages >= 100 {
			break
		}
	}

	sort.Strings(tags)
	tags = slices.Compact(tags)

	var matches []string
	for _, tag := range tags {
		if pattern.MatchString(tag) {
			matches = append(matches, tag)
		}
	}

	// Probing the push time costs a few requests per tag, so skip it when the
	// pattern is very broad and fall back to natural ordering.
	if len(matches) > 100 {
		out := make([]TagMatch, len(matches))
		for i, tag := range matches {
			out[i] = TagMatch{Tag: tag}
		}
		return out, nil
	}

	result := make([]TagMatch, 0, len(matches))
	for _, tag := range matches {
		created, err := session.tagCreated(tag)
		if err != nil {
			created = nil
		}
		result = append(result, TagMatch{Tag: tag, Pushed: created})
	}
	return result, nil
}

// session is a registry session for one host + repository, caching its auth
// header.
type session struct {
	client     *Client
	host       string
	repository string
	auth       string
}

func newSession(client *Client, host, repository string) *session {
	return &session{client: client, host: host, repository: repository}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (s *session) url(path string) (string, error) {
	base, err := url.Parse(s.client.scheme() + "://" + s.host + "/")
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	next := base.ResolveReference(ref)
	if !strings.EqualFold(next.Scheme, base.Scheme) || !strings.EqualFold(next.Host, base.Host) || next.User != nil {
		return "", fmt.Errorf("refusing registry URL outside origin %s", base)
	}
	return next.String(), nil
}

// request issues a request, transparently answering a 401 auth challenge once.
func (s *session) request(method, path, accept string) (*response, error) {
	url, err := s.url(path)
	if err != nil {
		return nil, err
	}
	resp, err := s.send(method, url, accept)
	if err != nil {
		return nil, fmt.Errorf("%s %s failed: %w", method, url, err)
	}
	if resp.status == http.StatusUnauthorized {
		challenge := resp.header.Get("WWW-Authenticate")
		if challenge == "" {
			return nil, fmt.Errorf("%s requires authentication", s.host)
		}
		if err := s.authorize(challenge); err != nil {
			return nil, err
		}
		resp, err = s.send(method, url, accept)
		if err != nil {
			return nil, fmt.Errorf("%s %s failed: %w", method, url, err)
		}
	}
	if resp.status >= 400 {
		return nil, fmt.Errorf("%s %s failed: HTTP %d (%s)", method, url, resp.status, http.StatusText(resp.status))
	}
	return resp, nil
}

func (s *session) send(method, url, accept string) (*response, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent())
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if s.auth != "" {
		req.Header.Set("Authorization", s.auth)
	}

	httpResp, err := s.client.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	return &response{status: httpResp.StatusCode, header: httpResp.Header, body: body}, nil
}

// authorize performs the bearer/basic token handshake described by challenge.
func (s *session) authorize(challenge string) error {
	scheme, params := parseChallenge(challenge)
	credential, hasCredential := s.client.creds.Get(s.host)

	switch strings.ToLower(scheme) {
	case "bearer":
		realm, ok := params["realm"]
		if !ok {
			return fmt.Errorf("bearer challenge has no realm")
		}
		url := realm
		separator := "?"
		if strings.Contains(url, "?") {
			separator = "&"
		}
		for _, key := range []string{"service", "scope"} {
			if value, ok := params[key]; ok {
				url += separator + key + "=" + percentEncode(value)
				separator = "&"
			}
		}

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", userAgent())
		if hasCredential {
			req.Header.Set("Authorization", credential.BasicHeader())
		}
		resp, err := s.client.http.Do(req)
		if err != nil {
			return fmt.Errorf("requesting token from %s: %w", realm, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("requesting token from %s: HTTP %d (%s)", realm, resp.StatusCode, http.StatusText(resp.StatusCode))
		}
		var token tokenResponse
		if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
			return fmt.Errorf("decoding token response: %w", err)
		}
		value := token.Token
		if value == "" {
			value = token.AccessToken
		}
		if value == "" {
			return fmt.Errorf("token response contained no token")
		}
		s.auth = "Bearer " + value
	case "basic":
		if !hasCredential {
			return fmt.Errorf("%s requires credentials", s.host)
		}
		s.auth = credential.BasicHeader()
	default:
		return fmt.Errorf("unsupported authentication scheme `%s`", scheme)
	}
	return nil
}

// tagCreated is a best-effort push timestamp for a tag, read from the image
// config.
func (s *session) tagCreated(tag string) (*time.Time, error) {
	resp, err := s.request(http.MethodGet, fmt.Sprintf("/v2/%s/manifests/%s", s.repository, tag), manifestAccept)
	if err != nil {
		return nil, err
	}
	var manifest manifest
	if err := json.Unmarshal(resp.body, &manifest); err != nil {
		return nil, fmt.Errorf("decoding manifest: %w", err)
	}

	configDigest := ""
	if manifest.Config != nil {
		configDigest = manifest.Config.Digest
	}
	if configDigest == "" && manifest.Manifests != nil && len(*manifest.Manifests) > 0 {
		first := (*manifest.Manifests)[0]
		resp, err := s.request(http.MethodGet, fmt.Sprintf("/v2/%s/manifests/%s", s.repository, first.Digest), manifestAccept)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(resp.body, &manifest); err != nil {
			return nil, fmt.Errorf("decoding platform manifest: %w", err)
		}
		if manifest.Config != nil {
			configDigest = manifest.Config.Digest
		}
	}

	if configDigest == "" {
		return nil, nil
	}
	resp, err = s.request(http.MethodGet, fmt.Sprintf("/v2/%s/blobs/%s", s.repository, configDigest), "")
	if err != nil {
		return nil, err
	}
	var config imageConfig
	if err := json.Unmarshal(resp.body, &config); err != nil {
		return nil, fmt.Errorf("decoding image config: %w", err)
	}
	return parseTime(config.Created), nil
}

// getJSON issues an unauthenticated GET and decodes the JSON body.
func (c *Client) getJSON(url string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent())

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d (%s)", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func userAgent() string { return "bumpstack/" + Version }

func parseTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &t
}

// isIndexMediaType reports whether mediaType denotes a multi-arch index /
// manifest list.
func isIndexMediaType(mediaType string) bool {
	switch mediaType {
	case "application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json":
		return true
	}
	return false
}

// indexPlatforms extracts the distinct real platforms from an index, ignoring
// the unknown/unknown entries registries use for attestations and SBOMs.
func indexPlatforms(manifests []descriptor) []string {
	var platforms []string
	for _, d := range manifests {
		if d.Platform == nil {
			continue
		}
		p := d.Platform
		if p.Architecture == "" || p.OS == "" || p.OS == "unknown" || p.Architecture == "unknown" {
			continue
		}
		name := p.OS + "/" + p.Architecture
		if p.Variant != "" {
			name += "/" + p.Variant
		}
		if !slices.Contains(platforms, name) {
			platforms = append(platforms, name)
		}
	}
	sort.Strings(platforms)
	return platforms
}

// headerMediaType extracts the bare media type from a Content-Type header,
// dropping any parameters such as `; charset=utf-8`.
func headerMediaType(resp *response) string {
	value := resp.header.Get("Content-Type")
	if value == "" {
		return ""
	}
	if i := strings.IndexByte(value, ';'); i >= 0 {
		value = value[:i]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

var challengeRE = regexp.MustCompile(`(\w+)\s*=\s*(?:"([^"]*)"|([^,\s]+))`)

// parseChallenge parses a WWW-Authenticate header into its scheme and
// parameters.
func parseChallenge(header string) (string, map[string]string) {
	scheme, rest, _ := strings.Cut(header, " ")
	scheme = strings.TrimSpace(scheme)

	params := map[string]string{}
	for _, m := range challengeRE.FindAllStringSubmatch(rest, -1) {
		key := strings.ToLower(m[1])
		value := m[2]
		if value == "" {
			value = m[3]
		}
		params[key] = value
	}
	return scheme, params
}

// nextLink extracts the rel="next" URL from a Link header.
func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		start := strings.IndexByte(part, '<')
		if start < 0 {
			continue
		}
		end := strings.IndexByte(part[start+1:], '>')
		if end < 0 {
			continue
		}
		if strings.Contains(part, `rel="next"`) || strings.Contains(part, "rel=next") {
			return part[start+1 : start+1+end]
		}
	}
	return ""
}

func percentEncode(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

type hubTagsPage struct {
	Results []hubTag `json:"results"`
	Next    string   `json:"next"`
}

type hubTag struct {
	Name          string `json:"name"`
	LastUpdated   string `json:"last_updated"`
	TagLastPushed string `json:"tag_last_pushed"`
}

type tagsPage struct {
	Tags []string `json:"tags"`
}

type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
}

type manifest struct {
	MediaType string        `json:"mediaType"`
	Config    *descriptor   `json:"config"`
	Manifests *[]descriptor `json:"manifests"`
}

type descriptor struct {
	Digest   string    `json:"digest"`
	Platform *platform `json:"platform"`
}

type platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant"`
}

type imageConfig struct {
	Created string `json:"created"`
}
