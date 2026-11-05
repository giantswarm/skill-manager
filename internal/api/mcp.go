// Package api is skill-manager's two surfaces over the catalog: the MCP
// tools muster and agents call (this file) and the REST API agent-manager and
// the portal call (rest.go), with the same semantics.
package api

import (
	"context"
	"encoding/json"

	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/mark3labs/mcp-go/mcp"
	mcpotel "github.com/mark3labs/mcp-go/otel"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/giantswarm/skill-manager/internal/catalog"
)

const tracerName = "github.com/giantswarm/skill-manager"

// The tools.
const (
	ToolListSkillSources = "list_skill_sources"
	ToolListSkills       = "list_skills"
	ToolResolveSkills    = "resolve_skills"
)

// SourcesResult is list_skill_sources' result and GET /v1/sources' body.
type SourcesResult struct {
	Sources []catalog.Source `json:"sources"`
}

// genAIToolName labels the mcp.tools/call server span with the called tool
// under the GenAI semantic-convention key.
func genAIToolName(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("gen_ai.tool.name", req.Params.Name))
		return next(ctx, req)
	}
}

// readOnly annotates a tool that changes nothing; mcp-go defaults every tool
// to destructive and not idempotent.
func readOnly() mcp.ToolOption {
	return func(t *mcp.Tool) {
		mcp.WithReadOnlyHintAnnotation(true)(t)
		mcp.WithDestructiveHintAnnotation(false)(t)
		mcp.WithIdempotentHintAnnotation(true)(t)
		mcp.WithOpenWorldHintAnnotation(true)(t)
	}
}

// itemSchema is one skill or plugin of resolve_skills.
var itemSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name": map[string]any{"type": "string"},
		"path": map[string]any{"type": "string", "description": "Directory in the repository; empty is its root"},
		"git": map[string]any{
			"type":     "object",
			"required": []string{"url"},
			"properties": map[string]any{
				"url":    map[string]any{"type": "string", "description": "https://github.com/<owner>/<repo>"},
				"ref":    map[string]any{"type": "string", "description": "Branch, tag or commit; empty: the ref of the source holding path, else the default branch"},
				"commit": map[string]any{"type": "string", "description": "Full commit id to keep instead of a ref"},
			},
		},
		"oci": map[string]any{
			"type":       "object",
			"required":   []string{"ref"},
			"properties": map[string]any{"ref": map[string]any{"type": "string", "description": "<registry>/<repository>[:<tag>|@sha256:<digest>]"}},
		},
		"skills":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "A plugin's skill selection, passed through"},
		"claudeFormat": map[string]any{"type": "boolean", "description": "Plugins: true refuses one that is not a Claude Code plugin"},
	},
}

// NewMCPServer builds the MCP server with its tracing and metrics middleware.
func NewMCPServer(svc *catalog.Service, version string) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("skill-manager", version,
		mcpserver.WithToolHandlerMiddleware(genAIToolName),
		mcpserver.WithToolHandlerMiddleware(toolDuration(otel.Meter(tracerName))),
		// The HTTP server span already joined the inbound traceparent, so the
		// MCP spans nest under it instead of extracting it a second time.
		mcpotel.WithServerTracingPropagator(otel.Tracer(tracerName), propagation.NewCompositeTextMapPropagator()),
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithInstructions("The Agent Platform's skills: the configured skill sources, the skills they hold, and the pins agents mount them by. Private repositories are yours to list and pin only when you can read them on GitHub."),
	)

	s.AddTool(mcp.NewTool(ToolListSkillSources,
		tooldesc.Describe("List the configured skill sources, each with the commit its ref resolves to now.",
			"Per source: url, ref (empty: the default branch), path scanned, readOnly, commit; error when it cannot be read or you cannot read it."),
		readOnly(),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		list, err := svc.Sources(ctx)
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(SourcesResult{Sources: list})
	})

	s.AddTool(mcp.NewTool(ToolListSkills,
		tooldesc.Describe("List the skills of the configured skill sources, with each source's commit.",
			"Per repository: the ref read, the commit it resolved to, the path scanned, readOnly, error; per skill its SKILL.md name, description, repository-relative path and tags. Cached briefly; refresh re-reads GitHub."),
		readOnly(),
		mcp.WithString("source", mcp.Description("Only this repository (https://github.com/<owner>/<repo>); default: every configured source")),
		mcp.WithBoolean("refresh", mcp.Description("Bypass the cache")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := svc.List(ctx, req.GetString("source", ""), req.GetBool("refresh", false))
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(res)
	})

	s.AddTool(mcp.NewTool(ToolResolveSkills,
		tooldesc.Describe("Pin skills and plugins: a git ref to its commit, an OCI tag to its digest.",
			"Returns the same items with git.commit set and git.ref dropped, or oci.ref by digest; a git plugin also gets claudeFormat. A git item without ref takes the ref of the configured source holding its path. Refuses a repository you cannot read, a path missing at the commit, and a plugin asking claudeFormat that is not one."),
		readOnly(),
		mcp.WithArray("skills", mcp.Description("Skills: {name, path, git: {url, ref | commit}} or {name, oci: {ref}}"), mcp.Items(itemSchema)),
		mcp.WithArray("plugins", mcp.Description("Plugins: {path, skills, git: {url, ref | commit}} or {skills, oci: {ref}}"), mcp.Items(itemSchema)),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in catalog.Request
		if err := req.BindArguments(&in); err != nil {
			return mcp.NewToolResultError("invalid arguments: " + err.Error()), nil
		}
		res, err := svc.Resolve(ctx, in)
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(res)
	})
	return s
}

// jsonResult is v as the structured result and as its JSON text.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return mcp.NewToolResultStructured(v, string(raw)), nil
}

// toolError is a refusal as a tool error: its kind (forbidden, not found,
// upstream failure, invalid) and the item it names.
func toolError(err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(err.Error())
}
