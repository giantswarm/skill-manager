// Package api is skill-manager's two surfaces over the catalog: the MCP
// tools muster and agents call (this file) and the REST API agent-manager and
// the portal call (rest.go), with the same semantics.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

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
	ToolGetSkill         = "get_skill"
	ToolGetSkillFile     = "get_skill_file"
)

// SourcesResult is list_skill_sources' result and GET /v1/sources' body.
type SourcesResult struct {
	Sources []catalog.Source `json:"sources"`
	catalog.Withheld
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

// prop is a JSON Schema property of type typ, described when desc is set.
func prop(typ, desc string) map[string]any {
	p := map[string]any{"type": typ}
	if desc != "" {
		p["description"] = desc
	}
	return p
}

// itemSchema is one skill or plugin of resolve_skills.
var itemSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"name": prop("string", ""),
		"path": prop("string", "Directory in the repository; empty is its root"),
		"git": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"url"},
			"properties": map[string]any{
				"url":    prop("string", "https://github.com/<owner>/<repo>"),
				"ref":    prop("string", "Branch, tag or commit; empty: the ref of the source holding path, else the default branch"),
				"commit": prop("string", "Full commit id to keep instead of a ref"),
			},
		},
		"oci": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"ref"},
			"properties":           map[string]any{"ref": prop("string", "<registry>/<repository>[:<tag>|@sha256:<digest>]")},
		},
		"skills":       map[string]any{"type": "array", "items": prop("string", ""), "description": "A plugin's skill selection, passed through"},
		"claudeFormat": prop("boolean", "Plugins: true refuses one that is not a Claude Code plugin"),
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
		tooldesc.Describe("List the configured skill sources, each with the commit its ref resolves to.",
			"Per source: url, ref (empty: the default branch), path scanned, readOnly, commit (cached up to the cache TTL); error when it cannot be read. Private sources you cannot read are left out, counted in partial, withheld and grantRequired.app."),
		readOnly(),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		list, withheld, err := svc.Sources(ctx)
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(SourcesResult{Sources: list, Withheld: withheld})
	})

	s.AddTool(mcp.NewTool(ToolListSkills,
		tooldesc.Describe("List the skills of the configured skill sources, with each source's commit.",
			"Per repository: the ref read, the commit it resolved to, the path scanned, readOnly, error, stale; per skill its SKILL.md name, description, repository-relative path and tags; per plugin its name, path and format (agent-plugins: root plugin.json, select its skills by name; claude-code: .claude-plugin/plugin.json, loaded whole). Cached up to the cache TTL; refresh re-reads a listing older than 10 seconds. Private sources you cannot read are left out, counted in partial, withheld and grantRequired.app."),
		readOnly(),
		mcp.WithString("source", mcp.Description("Only this repository, any https://github.com/<owner>/<repo>; a configured one keeps its ref and path. Default: every source")),
		mcp.WithBoolean("refresh", mcp.Description("Re-read a listing older than 10 seconds instead of serving it from the cache")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := svc.List(ctx, req.GetString("source", ""), req.GetBool("refresh", false))
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(res)
	})

	s.AddTool(mcp.NewTool(ToolResolveSkills,
		tooldesc.Describe("Pin skills and plugins: a git ref to its commit, an OCI tag to its digest.",
			"Returns the items with git.commit set and git.ref dropped, or oci.ref by digest; a git plugin also gets claudeFormat. No ref: the ref of the source holding the path. The first refused item fails the call, named skills[i] or plugins[i]: a repository you cannot read, a missing path or commit, a non-Claude plugin asking claudeFormat."),
		readOnly(),
		mcp.WithArray("skills", mcp.Description("Skills: {name, path, git: {url, ref | commit}} or {name, oci: {ref}}"), mcp.Items(itemSchema)),
		mcp.WithArray("plugins", mcp.Description("Plugins: {path, skills, git: {url, ref | commit}} or {skills, oci: {ref}}"), mcp.Items(itemSchema)),
		mcp.WithSchemaAdditionalProperties(false),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in, err := decodeRequest(req.GetArguments())
		if err != nil {
			return mcp.NewToolResultError("invalid arguments: " + err.Error()), nil
		}
		res, err := svc.Resolve(ctx, in)
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(res)
	})

	s.AddTool(mcp.NewTool(ToolGetSkill,
		tooldesc.Describe("Read one skill of the catalogue at a commit: its SKILL.md and its files.",
			"Returns name, description, tags, instructions (the body below the frontmatter) and files (path, size, sha; SKILL.md first, at most 200, filesTruncated past that). A file below a nested SKILL.md belongs to that skill. Only a full commit id, only below a configured source, and a private repository only for a caller who can read it."),
		readOnly(),
		mcp.WithString("url", mcp.Required(), mcp.Description("https://github.com/<owner>/<repo>, a configured source")),
		mcp.WithString("commit", mcp.Required(), mcp.Description("Full commit id, as list_skills reports it")),
		mcp.WithString("path", mcp.Description("The skill directory; empty is the repository root")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := svc.Skill(ctx, req.GetString("url", ""), req.GetString("commit", ""), req.GetString("path", ""))
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(res)
	})

	s.AddTool(mcp.NewTool(ToolGetSkillFile,
		tooldesc.Describe("Read one file of a skill at a commit.",
			"Returns path, size, sha, binary, truncated, and content for a text file of at most 512 KiB. The file must be one get_skill lists; the same checks as get_skill apply."),
		readOnly(),
		mcp.WithString("url", mcp.Required(), mcp.Description("https://github.com/<owner>/<repo>, a configured source")),
		mcp.WithString("commit", mcp.Required(), mcp.Description("Full commit id")),
		mcp.WithString("path", mcp.Description("The skill directory; empty is the repository root")),
		mcp.WithString("file", mcp.Required(), mcp.Description("Path relative to the skill directory, e.g. references/style.md")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := svc.SkillFile(ctx, req.GetString("url", ""), req.GetString("commit", ""), req.GetString("path", ""), req.GetString("file", ""))
		if err != nil {
			return toolError(err), nil
		}
		return jsonResult(res)
	})
	return s
}

// decodeRequest reads resolve_skills' arguments as POST /v1/resolve reads its
// body: an unknown field is refused, not dropped.
func decodeRequest(args map[string]any) (catalog.Request, error) {
	var in catalog.Request
	raw, err := json.Marshal(args)
	if err != nil {
		return in, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	err = dec.Decode(&in)
	return in, err
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
// statusGrantRequired is muster's provider App contract: a tool result whose
// structured status it is names, under apps, the Apps the call needs.
const statusGrantRequired = "grant_required"

// toolError is the tool's refusal. A missing provider App grant also carries
// {"status": "grant_required", "apps": [<app>]} as structured content, which
// muster answers with the App's sign-in.
func toolError(err error) *mcp.CallToolResult {
	if grant, ok := errors.AsType[*catalog.ErrGrantRequired](err); ok {
		res := mcp.NewToolResultError(statusGrantRequired + ": " + grant.Reason)
		res.StructuredContent = map[string]any{"status": statusGrantRequired, "apps": []string{grant.App}, "message": grant.Reason}
		return res
	}
	return mcp.NewToolResultError(err.Error())
}
