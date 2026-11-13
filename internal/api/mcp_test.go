package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/giantswarm/mcp-toolkit/brand"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/skill-manager/internal/catalog"
	"github.com/giantswarm/skill-manager/internal/catalog/catalogtest"
	"github.com/giantswarm/skill-manager/internal/skills"
)

// callTool calls a tool through the MCP server and returns its text and
// whether it is an error result.
func callTool(t *testing.T, svc *catalog.Service, name string, args map[string]any) (string, bool) {
	t.Helper()
	srv := NewMCPServer(svc, "test")
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	require.NoError(t, err)
	out, err := json.Marshal(srv.HandleMessage(t.Context(), raw))
	require.NoError(t, err)
	var parsed struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	if parsed.Error != nil {
		return parsed.Error.Message, true
	}
	require.NotEmpty(t, parsed.Result.Content, string(out))
	return parsed.Result.Content[0].Text, parsed.Result.IsError
}

func TestMCPResolveSkillsPinsTheItems(t *testing.T) {
	text, isErr := callTool(t, newCatalog(), ToolResolveSkills, map[string]any{
		"skills":  []any{map[string]any{"name": "x", "path": "skills/x", "git": map[string]any{"url": catalogtest.SkillsRepo}}},
		"plugins": []any{map[string]any{"oci": map[string]any{"ref": "ghcr.io/giantswarm/skills/kubectl:1.4.0"}}},
	})
	require.False(t, isErr, text)
	var got catalog.Request
	require.NoError(t, json.Unmarshal([]byte(text), &got))
	assert.Equal(t, catalog.Request{
		Skills:  []catalog.Item{{Name: "x", Path: "skills/x", Git: &catalog.Git{URL: catalogtest.SkillsRepo, Commit: catalogtest.TagHead}}},
		Plugins: []catalog.Item{{OCI: &catalog.OCI{Ref: "ghcr.io/giantswarm/skills/kubectl@" + catalogtest.Digest}}},
	}, got)
}

func TestMCPResolveSkillsNamesTheRefusedItem(t *testing.T) {
	text, isErr := callTool(t, newCatalog(), ToolResolveSkills, map[string]any{
		"skills": []any{map[string]any{"name": "x", "git": map[string]any{"url": catalogtest.PrivateRepo}}},
	})
	require.True(t, isErr)
	assert.Contains(t, text, "skills[0]")
	assert.Contains(t, text, "forbidden")
}

func TestMCPListsSourcesAndSkills(t *testing.T) {
	var gotSource string
	var gotRefresh bool
	svc := catalog.New(catalogtest.Lister{Repos: []skills.Repository{
		{RepoURL: catalogtest.SkillsRepo, Ref: "v1.2.0", Path: "skills", ReadOnly: true, Commit: catalogtest.TagHead, Skills: []skills.Skill{{Name: "overview", Path: "skills/overview"}}},
	}, Observe: func(source string, refresh bool) { gotSource, gotRefresh = source, refresh }}, &catalogtest.Pinner{})

	text, isErr := callTool(t, svc, ToolListSkillSources, nil)
	require.False(t, isErr, text)
	assert.JSONEq(t, `{"sources":[{"url":"`+catalogtest.SkillsRepo+`","ref":"v1.2.0","path":"skills","readOnly":true,"commit":"`+catalogtest.TagHead+`"}]}`, text)

	text, isErr = callTool(t, svc, ToolListSkills, map[string]any{"source": catalogtest.SkillsRepo, "refresh": true})
	require.False(t, isErr, text)
	assert.Equal(t, catalogtest.SkillsRepo, gotSource)
	assert.True(t, gotRefresh)
	var res skills.Result
	require.NoError(t, json.Unmarshal([]byte(text), &res))
	require.Len(t, res.Repositories, 1)
	assert.Equal(t, "overview", res.Repositories[0].Skills[0].Name)
}

// resolve_skills refuses an unknown field as POST /v1/resolve does.
func TestMCPResolveSkillsRefusesUnknownFields(t *testing.T) {
	text, isErr := callTool(t, newCatalog(), ToolResolveSkills, map[string]any{
		"skills": []any{map[string]any{"name": "x", "url": catalogtest.SkillsRepo}},
	})
	assert.True(t, isErr)
	assert.Contains(t, text, `unknown field "url"`)
}

// The server advertises the Giant Swarm logo as its icon, which muster shows
// as the connector's icon.
func TestMCPServerAdvertisesGiantSwarmIcon(t *testing.T) {
	srv := NewMCPServer(newCatalog(), "test")
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": mcp.LATEST_PROTOCOL_VERSION,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "t", "version": "0"},
	}})
	require.NoError(t, err)
	out, err := json.Marshal(srv.HandleMessage(t.Context(), raw))
	require.NoError(t, err)
	var parsed struct {
		Result mcp.InitializeResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed), string(out))
	assert.Equal(t, []mcp.Icon{brand.GiantSwarmIcon()}, parsed.Result.ServerInfo.Icons)
}

func TestToolErrorNamesTheMissingGrant(t *testing.T) {
	res := toolError(fmt.Errorf("resolve: %w", &catalog.ErrGrantRequired{App: "github", Reason: "grant it"}))
	require.True(t, res.IsError)
	require.Len(t, res.Content, 2)
	require.Equal(t, "grant_required: grant it", res.Content[0].(mcp.TextContent).Text)
	want := map[string]any{"status": "grant_required", "apps": []any{"github"}, "message": "grant it"}
	var serialized map[string]any
	require.NoError(t, json.Unmarshal([]byte(res.Content[1].(mcp.TextContent).Text), &serialized), "the second text block is the structured content")
	require.Equal(t, want, serialized)
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var wire struct {
		StructuredContent map[string]any `json:"structuredContent"`
	}
	require.NoError(t, json.Unmarshal(raw, &wire))
	require.Equal(t, want, wire.StructuredContent)
}

// A missing grant is 403: the bearer is valid, so a client keeps its token.
func TestRESTMissingGrantIsForbiddenAndNamesTheApp(t *testing.T) {
	require.Equal(t, http.StatusForbidden, statusOf(&catalog.ErrGrantRequired{App: "github", Reason: "grant it"}))
	recorder := httptest.NewRecorder()
	(&REST{log: slog.New(slog.DiscardHandler)}).writeError(recorder, fmt.Errorf("resolve: %w", &catalog.ErrGrantRequired{App: "skill-manager-app", Reason: "grant it"}))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Empty(t, recorder.Header().Get("WWW-Authenticate"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, "grant_required", body["code"])
	require.Equal(t, []any{"skill-manager-app"}, body["apps"])
	require.Equal(t, "grant it", body["message"])
	require.NotContains(t, body, "grantRequired", "grantRequired is the listings' shape")
}
