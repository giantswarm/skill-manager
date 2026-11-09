package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/api"
	"github.com/giantswarm/skill-manager/internal/catalog"
	"github.com/giantswarm/skill-manager/internal/catalog/catalogtest"
)

// Without OAuth nothing guards the API: the deployment in front of it is the
// trust boundary.
func TestServerMountsHealthRESTAndMCP(t *testing.T) {
	svc := catalog.New(catalogtest.Lister{}, &catalogtest.Pinner{})
	srv, err := New(Config{Addr: "127.0.0.1:0"}, api.NewREST(svc, quiet()), api.NewMCPServer(svc, "test"), quiet())
	require.NoError(t, err)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	get := func(path string) int {
		resp, err := http.Get(ts.URL + path)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusOK, get("/healthz"))
	assert.Equal(t, http.StatusOK, get("/readyz"))
	assert.Equal(t, http.StatusOK, get("/v1/sources"))
	assert.Equal(t, http.StatusOK, get("/v1/skills"))
	assert.Equal(t, http.StatusNotFound, get("/v1/nope"))
	resp405, err := http.Post(ts.URL+"/v1/sources", "application/json", nil)
	require.NoError(t, err)
	_ = resp405.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp405.StatusCode)

	// The MCP endpoint answers an initialize.
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// A body beyond the bound is refused, and the endpoint keeps answering.
	big := `{"jsonrpc":"2.0","id":2,"method":"ping","params":{"pad":"` + strings.Repeat("x", maxMCPBodyBytes) + `"}}`
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/mcp", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	tooBig, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = tooBig.Body.Close()
	assert.GreaterOrEqual(t, tooBig.StatusCode, 400)
	assert.Equal(t, http.StatusOK, get("/healthz"))
}

// A client holding the MCP listening stream does not hold up a shutdown.
func TestShutdownClosesOpenMCPStreams(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	svc := catalog.New(catalogtest.Lister{}, &catalogtest.Pinner{})
	srv, err := New(Config{Addr: addr}, api.NewREST(svc, quiet()), api.NewMCPServer(svc, "test"), quiet())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	base := "http://" + addr + "/mcp"
	var session string
	require.Eventually(t, func() bool {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
		req, _ := http.NewRequest(http.MethodPost, base, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		session = resp.Header.Get("Mcp-Session-Id")
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond)

	req, _ := http.NewRequest(http.MethodGet, base, nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", session)
	req.Header.Set("Mcp-Protocol-Version", "2025-03-26")
	stream, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = stream.Body.Close() }()
	require.Equal(t, http.StatusOK, stream.StatusCode)

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
		assert.Less(t, time.Since(start), 5*time.Second)
	case <-time.After(14 * time.Second):
		t.Fatal("Run waited for the open stream")
	}
}
