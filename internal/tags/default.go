package tags

import (
	_ "embed"
)

// agentTagsMaxItems is agent.tags maxItems of the agent chart 2.5.0.
const agentTagsMaxItems = 50

// agentTagSchema is a copy of the agent chart's helm/agent/tag.schema.json
// (2.5.0): agent-manager reads it from the chart it embeds, which
// skill-manager does not.
//
//go:embed agent-tag.schema.json
var agentTagSchema []byte

// AgentChartRule is the agent chart's tag rule, which a skill's tags follow
// as an agent's do.
func AgentChartRule() *Rule {
	r, err := NewRule(agentTagSchema, agentTagsMaxItems)
	if err != nil {
		panic(err) // the embedded schema is fixed at build time
	}
	return r
}
