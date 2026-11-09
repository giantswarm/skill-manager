package skills

import (
	"os"
	"testing"
)

// The tests refresh right after a read; TestRefreshIsThrottled restores the
// bound.
func TestMain(m *testing.M) {
	minRefresh = 0
	os.Exit(m.Run())
}
