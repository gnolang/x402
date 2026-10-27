package exact_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheClientLinksNeitherTheFacilitatorNorTheVM: a buyer importing this
// package must not link the facilitator, nor gno.land's node and its VM.
func TestTheClientLinksNeitherTheFacilitatorNorTheVM(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	require.NoError(t, err, "%s", out)
	deps := strings.Fields(string(out))
	require.Contains(t, deps, "github.com/gnolang/gno/tm2/pkg/std", "the listing names the package's real dependencies")
	for _, dep := range deps {
		assert.NotEqual(t, "github.com/gnolang/x402/facilitator/exact", dep)
		assert.NotContains(t, dep, "/gnovm/")
	}
}
