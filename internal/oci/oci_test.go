package oci_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/oci"
	"github.com/giantswarm/skill-manager/internal/oci/ocitest"
)

func TestParseImageReference(t *testing.T) {
	digest := "sha256:5b0bcabd1ed22e9fb1310cf6c2dec7cdef19f0ad69efa1f392e94a4333501270"
	for ref, want := range map[string]oci.ImageReference{
		"ghcr.io/giantswarm/skills/kubectl:1.4.0":       {Reference: oci.Reference{Host: "ghcr.io", Repository: "giantswarm/skills/kubectl"}, Tag: "1.4.0"},
		"ghcr.io/giantswarm/skills/kubectl":             {Reference: oci.Reference{Host: "ghcr.io", Repository: "giantswarm/skills/kubectl"}},
		"ghcr.io/giantswarm/skills/kubectl@" + digest:   {Reference: oci.Reference{Host: "ghcr.io", Repository: "giantswarm/skills/kubectl"}, Digest: digest},
		"localhost:5000/skills/kubectl:v1@" + digest:    {Reference: oci.Reference{Host: "localhost:5000", Repository: "skills/kubectl"}, Tag: "v1", Digest: digest},
		"registry.example.io/skills/runbooks:1.4.0":     {Reference: oci.Reference{Host: "registry.example.io", Repository: "skills/runbooks"}, Tag: "1.4.0"},
		"localhost/skills/runbooks:1.4.0":               {Reference: oci.Reference{Host: "localhost", Repository: "skills/runbooks"}, Tag: "1.4.0"},
		"gsoci.azurecr.io/giantswarm/skills/a:2026.1.0": {Reference: oci.Reference{Host: "gsoci.azurecr.io", Repository: "giantswarm/skills/a"}, Tag: "2026.1.0"},
	} {
		got, err := oci.ParseImageReference(ref)
		require.NoError(t, err, ref)
		assert.Equal(t, want, got, ref)
	}
	pinned, _ := oci.ParseImageReference("ghcr.io/giantswarm/skills/kubectl:1.4.0@" + digest)
	assert.Equal(t, "ghcr.io/giantswarm/skills/kubectl@"+digest, pinned.Pinned(), "the pinned form drops the tag")

	for _, bad := range []string{"", "kubectl:1.4.0", "skills/kubectl:1.4.0", "ghcr.io/", "ghcr.io/x@sha256:short", "ghcr.io/a b:1", "ghcr.io//x",
		"gsoci.azurecr.io/../v2/x:1", "gsoci.azurecr.io/a/./b:1", "gsoci.azurecr.io/a?b=c:1", "gsoci.azurecr.io/a#b:1", "gsoci.azurecr.io/a%2Fb:1", "gsoci.azurecr.io/Upper:1", "gsoci.azurecr.io/a:t?x", "gsoci.azurecr.io/a:.t"} {
		_, err := oci.ParseImageReference(bad)
		assert.Error(t, err, bad)
	}
}

// A tag of an allowed registry resolves to its digest; the bearer is
// fetched once.
func TestRegistryResolvesADigest(t *testing.T) {
	f := ocitest.New(t, "giantswarm/skills/kubectl")
	f.Tags = []string{"1.4.0"}
	r := oci.NewRegistry([]string{f.Host()}, f.Client())
	got, err := r.ManifestDigest(t.Context(), f.Reference(), "1.4.0")
	require.NoError(t, err)
	assert.Equal(t, ocitest.Digest("1.4.0"), got)
	_, err = r.ManifestDigest(t.Context(), f.Reference(), "1.4.0")
	require.NoError(t, err)
	assert.Equal(t, int32(1), f.TokenHits.Load(), "the bearer is cached")

	_, err = r.ManifestDigest(t.Context(), f.Reference(), "9.9.9")
	assert.ErrorIs(t, err, oci.ErrNotFound)
}

// Only allowed registries are asked, and never by an IP address.
func TestRegistryAsksOnlyAllowedHosts(t *testing.T) {
	f := ocitest.New(t, "giantswarm/skills/kubectl")
	f.Tags = []string{"1.4.0"}
	r := oci.NewRegistry([]string{"gsoci.azurecr.io"}, f.Client())
	_, err := r.ManifestDigest(t.Context(), f.Reference(), "1.4.0")
	require.ErrorIs(t, err, oci.ErrHostNotAllowed)
	assert.Zero(t, f.TokenHits.Load(), "nothing was asked")

	ipRef := f.Reference()
	ipRef.Host = "127.0.0.1" + ipRef.Host[len("localhost"):]
	r = oci.NewRegistry([]string{ipRef.Host}, f.Client())
	_, err = r.ManifestDigest(t.Context(), ipRef, "1.4.0")
	assert.ErrorIs(t, err, oci.ErrHostNotAllowed, "an IP literal is refused even when listed")
}

// A token realm elsewhere than the registry or another allowed host is not
// followed.
func TestRegistryFollowsOnlyAllowedRealms(t *testing.T) {
	f := ocitest.New(t, "giantswarm/skills/kubectl")
	f.Tags = []string{"1.4.0"}
	f.Realm = "http://metadata.internal/token"
	r := oci.NewRegistry([]string{f.Host()}, f.Client())
	_, err := r.ManifestDigest(t.Context(), f.Reference(), "1.4.0")
	require.ErrorIs(t, err, oci.ErrHostNotAllowed)
	assert.Contains(t, err.Error(), "realm")
}

// A registry's answer body never reaches the error.
func TestRegistryErrorsCarryNoUpstreamBody(t *testing.T) {
	f := ocitest.New(t, "giantswarm/skills/kubectl")
	f.Tags = []string{"1.4.0"}
	f.FailBody = "internal-secret-detail"
	r := oci.NewRegistry([]string{f.Host()}, f.Client())
	_, err := r.ManifestDigest(t.Context(), f.Reference(), "1.4.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.NotContains(t, err.Error(), "internal-secret-detail")
}

// The default client dials public addresses only: an allowed name that
// resolves to loopback is refused.
func TestRegistryDefaultClientRefusesPrivateAddresses(t *testing.T) {
	f := ocitest.New(t, "giantswarm/skills/kubectl")
	f.Tags = []string{"1.4.0"}
	r := oci.NewRegistry([]string{f.Host()}, nil)
	_, err := r.ManifestDigest(t.Context(), f.Reference(), "1.4.0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a public address")
}

// Without Docker-Content-Digest the manifest body is hashed; one larger than
// the bound is refused, not hashed cut.
func TestRegistryRefusesAnOversizedManifest(t *testing.T) {
	f := ocitest.New(t, "giantswarm/skills/kubectl")
	f.Tags = []string{"1.4.0"}
	f.Manifest = []byte(`{"schemaVersion":2}`)
	r := oci.NewRegistry([]string{f.Host()}, f.Client())
	got, err := r.ManifestDigest(t.Context(), f.Reference(), "1.4.0")
	require.NoError(t, err)
	assert.Equal(t, "sha256:"+fmt.Sprintf("%x", sha256.Sum256(f.Manifest)), got)

	f.Manifest = make([]byte, 4<<20+1)
	_, err = r.ManifestDigest(t.Context(), f.Reference(), "1.4.0")
	assert.ErrorContains(t, err, "larger than")
}

// An empty allowlist asks no registry.
func TestRegistryWithAnEmptyAllowlistAsksNone(t *testing.T) {
	r := oci.NewRegistry([]string{}, nil)
	for _, h := range oci.DefaultHosts {
		assert.ErrorIs(t, r.Allowed(h), oci.ErrHostNotAllowed)
	}
	assert.NoError(t, oci.NewRegistry(nil, nil).Allowed(oci.DefaultHosts[0]), "nil is the defaults")
}
