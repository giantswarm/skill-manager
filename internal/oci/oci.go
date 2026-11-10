// Package oci is the minimal OCI distribution client skill-manager needs,
// moved with the skills from agent-manager: the digest a tag resolves to, the
// pin kagent requires for a skill image. A request names its registry, so the
// client asks only allowed registries, never by IP address, dials public
// addresses only, and follows a token realm or a redirect only to an allowed
// host.
package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/giantswarm/skill-manager/internal/lru"
)

// manifestAccept lists every manifest media type a HEAD may resolve: a skill
// image can be a single-platform manifest or an index.
const manifestAccept = "application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.docker.distribution.manifest.list.v2+json"

// DefaultHosts are the registries asked when none are configured.
var DefaultHosts = []string{"gsoci.azurecr.io", "gsociprivate.azurecr.io"}

var (
	// ErrNotFound is a registry's 404: the repository or tag is not there.
	ErrNotFound = errors.New("not found")
	// ErrHostNotAllowed is a registry, or a token realm, that is not on the
	// allowlist or is an IP address.
	ErrHostNotAllowed = errors.New("registry host not allowed")
)

// digestPattern is a sha256 digest as the registry reports it.
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// repositoryPattern and tagPattern are the OCI distribution spec's grammar
// for a repository name and a tag: both go into the manifest URL, which must
// not carry a query, a fragment, an escape or a dot segment.
var (
	repositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagPattern        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)

// Reference is a parsed registry repository.
type Reference struct {
	// Host is the registry host, e.g. gsoci.azurecr.io.
	Host string
	// Repository is the path within the registry.
	Repository string
	// Insecure selects plain HTTP (tests).
	Insecure bool
}

// ImageReference is a parsed image reference: <host>/<repository>[:<tag>][@<digest>].
type ImageReference struct {
	Reference
	Tag    string
	Digest string
}

// Pinned renders the reference by digest, the immutable form.
func (r ImageReference) Pinned() string {
	return r.Host + "/" + r.Repository + "@" + r.Digest
}

// ParseImageReference parses <host>/<repository>[:<tag>][@sha256:<digest>].
// The registry host is required (no Docker Hub defaulting): a skill reference
// names where it comes from.
func ParseImageReference(ref string) (ImageReference, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.ContainsAny(ref, " \t\n") {
		return ImageReference{}, fmt.Errorf("OCI reference %q is empty or contains whitespace", ref)
	}
	out := ImageReference{}
	rest := ref
	if at := strings.Index(rest, "@"); at >= 0 {
		out.Digest = rest[at+1:]
		rest = rest[:at]
		if !digestPattern.MatchString(out.Digest) {
			return ImageReference{}, fmt.Errorf("OCI reference %q: digest must be sha256:<64 hex digits>", ref)
		}
	}
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return ImageReference{}, fmt.Errorf("OCI reference %q must be <registry>/<repository>[:<tag>][@sha256:<digest>]", ref)
	}
	out.Host, rest = rest[:slash], rest[slash+1:]
	if !strings.ContainsAny(out.Host, ".:") && out.Host != "localhost" {
		return ImageReference{}, fmt.Errorf("OCI reference %q must start with the registry host (e.g. registry.example.io/skills/runbooks:1.4.0)", ref)
	}
	if colon := strings.LastIndex(rest, ":"); colon >= 0 {
		out.Tag, rest = rest[colon+1:], rest[:colon]
	}
	if rest == "" || strings.HasPrefix(rest, "/") || strings.HasSuffix(rest, "/") {
		return ImageReference{}, fmt.Errorf("OCI reference %q has no repository", ref)
	}
	if !repositoryPattern.MatchString(rest) {
		return ImageReference{}, fmt.Errorf("OCI reference %q: repository %q is not a valid OCI repository name", ref, rest)
	}
	if out.Tag != "" && !tagPattern.MatchString(out.Tag) {
		return ImageReference{}, fmt.Errorf("OCI reference %q: tag %q is not a valid OCI tag", ref, out.Tag)
	}
	out.Repository = rest
	return out, nil
}

func (r Reference) baseURL() string {
	scheme := "https"
	if r.Insecure {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// maxTokens bounds the bearer cache.
const maxTokens = 256

// maxManifestBytes bounds a manifest hashed for its digest: the registries'
// own manifest size limit.
const maxManifestBytes = 4 << 20

// Registry talks to the allowed OCI registries. It is safe for concurrent
// use.
type Registry struct {
	http  *http.Client
	hosts map[string]bool

	tokens *lru.Cache[string, token]
}

type token struct {
	value   string
	expires time.Time
}

// NewRegistry builds a client of the registries hosts (host or host:port;
// nil: DefaultHosts, empty: none). A nil client is one that dials public
// addresses only.
func NewRegistry(hosts []string, client *http.Client) *Registry {
	if hosts == nil {
		hosts = DefaultHosts
	}
	allowed := map[string]bool{}
	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			allowed[h] = true
		}
	}
	if client == nil {
		client = PublicClient(60 * time.Second)
	}
	r := &Registry{hosts: allowed, tokens: lru.New[string, token](maxTokens)}
	// A redirect is a request too: it goes to an allowed host only.
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return r.Allowed(req.URL.Host)
	}
	r.http = &c
	return r
}

// Allowed refuses a host that is not on the allowlist or is an IP address.
func (c *Registry) Allowed(host string) error {
	h := strings.ToLower(host)
	name := h
	if sh, _, err := net.SplitHostPort(h); err == nil {
		name = sh
	}
	if net.ParseIP(strings.Trim(name, "[]")) != nil {
		return fmt.Errorf("%w: %s is an IP address", ErrHostNotAllowed, host)
	}
	if !c.hosts[h] {
		return fmt.Errorf("%w: %s is not an allowed registry", ErrHostNotAllowed, host)
	}
	return nil
}

// PublicClient is an HTTP client that dials public addresses only: no
// loopback, private, link-local, unspecified or multicast address, whatever
// a name resolves to.
func PublicClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || !public(ip) {
			return fmt.Errorf("%s is not a public address", host)
		}
		return nil
	}}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	return &http.Client{Timeout: timeout, Transport: transport}
}

// nonPublic are the special-purpose ranges (RFC 6890 and successors) the
// net/netip predicates do not cover: shared address space (CGNAT, used for
// pod and service networks), IETF protocol assignments, benchmarking,
// documentation, reserved, and their IPv6 counterparts.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// public reports whether ip is a public unicast address.
func public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// ManifestDigest resolves a tag to the digest of its manifest: the registry's
// Docker-Content-Digest on a HEAD, else the sha256 of the manifest body.
func (c *Registry) ManifestDigest(ctx context.Context, ref Reference, tag string) (string, error) {
	if err := c.Allowed(ref.Host); err != nil {
		return "", err
	}
	manifestURL := fmt.Sprintf("%s/v2/%s/manifests/%s", ref.baseURL(), ref.Repository, url.PathEscape(tag))
	resp, err := c.do(ctx, ref, http.MethodHead, manifestURL)
	if err != nil {
		return "", err
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	_ = resp.Body.Close()
	if digestPattern.MatchString(digest) {
		return digest, nil
	}
	resp, err = c.do(ctx, ref, http.MethodGet, manifestURL)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		return "", fmt.Errorf("read manifest %s:%s: %w", ref.Repository, tag, err)
	}
	if len(body) > maxManifestBytes {
		// A cut body hashes to a digest no manifest has.
		return "", fmt.Errorf("manifest %s/%s:%s is larger than %d bytes and the registry sent no Docker-Content-Digest", ref.Host, ref.Repository, tag, maxManifestBytes)
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// do performs one request, answering a bearer challenge with an anonymous
// token from the registry's token endpoint (cached per scope). An error
// names the status, never the answer's body.
func (c *Registry) do(ctx context.Context, ref Reference, method, target string) (*http.Response, error) {
	scope := "repository:" + ref.Repository + ":pull"
	resp, err := c.request(ctx, method, target, c.cachedToken(ref.Host, scope))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("Www-Authenticate")
		_ = resp.Body.Close()
		tok, err := c.fetchToken(ctx, ref, challenge, scope)
		if err != nil {
			return nil, err
		}
		if resp, err = c.request(ctx, method, target, tok); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = resp.Body.Close()
		err := fmt.Errorf("%s %s/%s: registry answered %d", method, ref.Host, ref.Repository, resp.StatusCode)
		if resp.StatusCode == http.StatusNotFound {
			err = fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return nil, err
	}
	return resp, nil
}

func (c *Registry) request(ctx context.Context, method, target, bearer string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", manifestAccept)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, req.URL.Host, err)
	}
	return resp, nil
}

func (c *Registry) cachedToken(host, scope string) string {
	if t, ok := c.tokens.Get(host + " " + scope); ok && time.Now().Before(t.expires) {
		return t.value
	}
	return ""
}

// fetchToken follows a `Bearer realm="…",service="…",scope="…"` challenge
// anonymously. The realm must be on the registry's own host or another
// allowed one, over https unless the reference is insecure.
func (c *Registry) fetchToken(ctx context.Context, ref Reference, challenge, fallbackScope string) (string, error) {
	params := parseChallenge(challenge)
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" {
		return "", fmt.Errorf("registry %s answered 401 without a usable bearer realm", ref.Host)
	}
	wantScheme := "https"
	if ref.Insecure {
		wantScheme = "http"
	}
	if realm.Scheme != wantScheme {
		return "", fmt.Errorf("%w: registry %s names a token realm over %s", ErrHostNotAllowed, ref.Host, realm.Scheme)
	}
	if !strings.EqualFold(realm.Host, ref.Host) {
		if err := c.Allowed(realm.Host); err != nil {
			return "", fmt.Errorf("registry %s token realm: %w", ref.Host, err)
		}
	}
	q := url.Values{}
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	scope := params["scope"]
	if scope == "" {
		scope = fallbackScope
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("token endpoint of %s: %w", ref.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint of %s answered %d", ref.Host, resp.StatusCode)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("token endpoint of %s: no JSON answer", ref.Host)
	}
	tok := body.AccessToken
	if tok == "" {
		tok = body.Token
	}
	if tok == "" {
		return "", fmt.Errorf("token endpoint of %s returned no token", ref.Host)
	}
	ttl := time.Duration(body.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	c.tokens.Add(ref.Host+" "+fallbackScope, token{value: tok, expires: time.Now().Add(ttl - 10*time.Second)})
	return tok, nil
}

// parseChallenge splits `Bearer k="v",k2="v2"` into its parameters.
func parseChallenge(header string) map[string]string {
	out := map[string]string{}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(header), "Bearer"))
	for _, part := range strings.Split(rest, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		out[strings.TrimSpace(kv[0])] = strings.Trim(strings.TrimSpace(kv[1]), `"`)
	}
	return out
}
