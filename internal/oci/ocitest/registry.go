// Package ocitest is a fake OCI distribution registry for tests: one
// repository behind a bearer challenge and one manifest per tag with a
// Docker-Content-Digest. It answers as localhost, a host name, as a real
// registry does.
package ocitest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/giantswarm/skill-manager/internal/oci"
)

// Registry is the fake's state; set it before the requests.
type Registry struct {
	// Repo is the repository path the fake serves.
	Repo string
	// Tags are the tags it has.
	Tags []string
	// Realm, when set, is the token realm its challenge names instead of its
	// own token endpoint.
	Realm string
	// FailBody, when set, is the body of a 500 every manifest request gets.
	FailBody string
	// Manifest, when set, is every tag's manifest body, answered without a
	// Docker-Content-Digest.
	Manifest []byte
	// TokenHits counts token-endpoint calls (bearer caching).
	TokenHits atomic.Int32

	server *httptest.Server
}

// New starts a fake registry serving repo. The server stops with the test.
func New(t *testing.T, repo string) *Registry {
	t.Helper()
	f := &Registry{Repo: repo}
	f.server = httptest.NewServer(f.handler())
	t.Cleanup(f.server.Close)
	return f
}

// Host is the registry's localhost:port.
func (f *Registry) Host() string {
	return strings.Replace(strings.TrimPrefix(f.server.URL, "http://"), "127.0.0.1", "localhost", 1)
}

// Reference is the repository as a plain-HTTP oci.Reference.
func (f *Registry) Reference() oci.Reference {
	return oci.Reference{Host: f.Host(), Repository: f.Repo, Insecure: true}
}

// Client is an HTTP client that reaches the fake.
func (f *Registry) Client() *http.Client { return f.server.Client() }

// Digest is the digest the fake reports for a tag's manifest.
func Digest(tag string) string {
	sum := sha256.Sum256([]byte("manifest:" + tag))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (f *Registry) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, _ *http.Request) {
		f.TokenHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "anon", "expires_in": 300})
	})
	mux.HandleFunc("/v2/"+f.Repo+"/manifests/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer anon" {
			realm := f.Realm
			if realm == "" {
				realm = "http://" + r.Host + "/oauth2/token"
			}
			w.Header().Set("Www-Authenticate", `Bearer realm="`+realm+`",service="fake",scope="repository:`+f.Repo+`:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.FailBody != "" {
			http.Error(w, f.FailBody, http.StatusInternalServerError)
			return
		}
		tag := strings.TrimPrefix(r.URL.Path, "/v2/"+f.Repo+"/manifests/")
		for _, t := range f.Tags {
			if t == tag && f.Manifest != nil {
				_, _ = w.Write(f.Manifest)
				return
			}
			if t == tag {
				w.Header().Set("Docker-Content-Digest", Digest(tag))
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		http.NotFound(w, r)
	})
	return mux
}
