package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
