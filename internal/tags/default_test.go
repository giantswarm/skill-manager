package tags

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The agent chart's rule is built in: skill tags follow what an agent's tags
// may be, lowercase kebab-case.
func TestAgentChartRule(t *testing.T) {
	assert.Equal(t, []string{"triage", "kubernetes"}, AgentChartRule().Parse("Triage, kubernetes, bad_tag"))
}
