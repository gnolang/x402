package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/gnolang/gno/gno.land/pkg/integration"
	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"
)

// TestTheWeatherGoBuyerPays runs every scenario that drives the weather
// example's buyer with x402-gno, its Go twin, in place of buy.mjs: the same
// commands and the same assertions, on what it prints and on the chain. It
// needs no JS toolchain.
func TestTheWeatherGoBuyerPays(t *testing.T) {
	buyer := goWeatherBuyer(t)

	params := testscript.Params{
		Files: exampleBuyerScenarios(t),
		Setup: func(env *testscript.Env) error {
			env.Setenv("X402_EXAMPLE_BUYER", buyer)
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"x402facilitator": facilitatorCmd,
			"x402seller":      sellerCmd,
		},
	}

	writableGnoRoot(t)
	require.NoError(t, integration.SetupGnolandTestscript(t, &params))
	testscript.Run(t, params)
}

// exampleBuyerScenarios are the scenarios that run $X402_EXAMPLE_BUYER.
func exampleBuyerScenarios(t *testing.T) []string {
	t.Helper()
	scripts, err := filepath.Glob(filepath.Join("testdata", "*.txtar"))
	require.NoError(t, err)

	var named []string
	for _, path := range scripts {
		script, err := os.ReadFile(path)
		require.NoError(t, err)
		if exampleBuyer.Match(script) {
			named = append(named, path)
		}
	}
	require.NotEmpty(t, named)
	return named
}

// exampleBuyer matches a script's use of the example buyer, braced or not.
var exampleBuyer = regexp.MustCompile(`\$\{?X402_EXAMPLE_BUYER\b`)

// goWeatherBuyer builds x402-gno in the repository's own module, against the
// dependencies `go run` and `go install` give its users, and returns the
// binary's path.
func goWeatherBuyer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "x402-gno")
	build := exec.Command("go", "build", "-o", bin, "./examples/weather/x402-gno")
	build.Dir = ".."
	out, err := build.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return bin
}
