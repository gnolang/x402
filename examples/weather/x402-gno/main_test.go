package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gnolang/gno/tm2/pkg/crypto/bip39"
	"github.com/gnolang/gno/tm2/pkg/crypto/hd"
	"github.com/gnolang/gno/tm2/pkg/crypto/secp256k1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	x402 "github.com/x402-foundation/x402/go/v2"
)

// buy runs the buyer with args and env, the whole environment it reads, and
// returns what it printed and its exit code.
func buy(t *testing.T, env map[string]string, args ...string) (string, int) {
	t.Helper()
	var out strings.Builder
	code := run(args, func(name string) string { return env[name] }, &out)
	return out.String(), code
}

// ---- --new-key

// newKey is what one run of --new-key printed, and the parts a reader copies.
type newKey struct {
	stdout, mnemonic, address, pay string
}

var (
	mnemonicLine = regexp.MustCompile(`(?m)^mnemonic: (.+)$`)
	addressLine  = regexp.MustCompile(`(?m)^address: +(\S+)$`)
	payLine      = regexp.MustCompile(`(?m)^ +(X402_MNEMONIC=.+)$`)
)

func makeKey(t *testing.T, env map[string]string, args ...string) newKey {
	t.Helper()
	stdout, code := buy(t, env, append([]string{"--new-key"}, args...)...)
	require.Equal(t, 0, code, stdout)
	key := newKey{stdout: stdout}
	if m := mnemonicLine.FindStringSubmatch(stdout); m != nil {
		key.mnemonic = m[1]
	}
	if m := addressLine.FindStringSubmatch(stdout); m != nil {
		key.address = m[1]
	}
	if m := payLine.FindStringSubmatch(stdout); m != nil {
		key.pay = m[1]
	}
	return key
}

// addressOf derives a mnemonic's account the way gnokey and Adena do, along
// gno's BIP44 path, without the keybase the buyer derives it with.
func addressOf(t *testing.T, mnemonic string) string {
	t.Helper()
	seed, err := bip39.NewSeedWithErrorChecking(mnemonic, "")
	require.NoError(t, err)
	master, chain := hd.ComputeMastersFromSeed(seed)
	key, err := hd.DerivePrivateKeyForPath(master, chain, "44'/118'/0'/0/0")
	require.NoError(t, err)
	return secp256k1.PrivKeySecp256k1(key).PubKey().Address().String()
}

func TestAddressOfDerivesTest1(t *testing.T) {
	assert.Equal(t, "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5", addressOf(t, test1Mnemonic))
}

func TestNewKeyPrintsA24WordMnemonicAndTheAddressItPaysFrom(t *testing.T) {
	key := makeKey(t, nil)

	require.Len(t, strings.Fields(key.mnemonic), 24)
	assert.Equal(t, addressOf(t, key.mnemonic), key.address)
}

func TestNewKeyMakesADifferentKeyEveryTime(t *testing.T) {
	assert.NotEqual(t, makeKey(t, nil).mnemonic, makeKey(t, nil).mnemonic)
}

func TestNewKeySaysTheKeyIsATestnetThrowawayKeptNowhere(t *testing.T) {
	key := makeKey(t, nil)

	assert.Contains(t, key.stdout, "testnet")
	assert.Contains(t, key.stdout, "throwaway")
	assert.Contains(t, key.stdout, "saved nowhere")
	assert.Contains(t, key.stdout, "https://faucet.gno.land")
}

// A test binary carries no module version, as a buyer run from a clone does
// not, so the command is the one that runs it inside the clone.
func TestNewKeyPrintsTheCommandThatPaysTheSellerWithTheKey(t *testing.T) {
	key := makeKey(t, nil, "https://weather.example/weather")

	assert.Equal(t, "X402_MNEMONIC='"+key.mnemonic+"' go run github.com/gnolang/x402/examples/weather/x402-gno https://weather.example/weather", key.pay)
}

func TestNewKeyLeavesTheSellerToFillInWhenGivenNone(t *testing.T) {
	key := makeKey(t, nil)

	assert.Equal(t, "X402_MNEMONIC='"+key.mnemonic+"' go run github.com/gnolang/x402/examples/weather/x402-gno <seller URL>", key.pay)
}

// The weather page shows the pay command this prints.
func TestNewKeyWritesTheSellerURLAsTheWeatherPageDoes(t *testing.T) {
	raw, err := os.ReadFile("../testdata/shell-words.json")
	require.NoError(t, err)
	var words []struct{ URL, Word string }
	require.NoError(t, json.Unmarshal(raw, &words))
	require.NotEmpty(t, words)

	for _, w := range words {
		key := makeKey(t, nil, w.URL)
		assert.True(t, strings.HasSuffix(key.pay, " "+w.Word), "%s printed %q", w.URL, key.pay)
	}
}

func TestNewKeyTouchesNeitherTheNetworkNorTheDisk(t *testing.T) {
	var requests atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer node.Close()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)

	makeKey(t, map[string]string{"X402_GNO_RPC": node.URL, "X402_SELLER_URL": node.URL + "/weather"}, node.URL+"/weather")

	assert.Zero(t, requests.Load())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestPayCommandPinsTheVersionItWasFetchedAt(t *testing.T) {
	cases := map[string]struct{ version, want string }{
		"a release":        {"v0.5.0", "go run github.com/gnolang/x402/examples/weather/x402-gno@v0.5.0"},
		"a commit":         {"v0.5.1-0.20261025120000-0123456789ab", "go run github.com/gnolang/x402/examples/weather/x402-gno@v0.5.1-0.20261025120000-0123456789ab"},
		"built in a clone": {"", "go run github.com/gnolang/x402/examples/weather/x402-gno"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, payCommand(tc.version))
		})
	}
}

// A binary built in a clone carries the commit it was built at, which go run
// cannot fetch when it was never pushed, whatever version go stamped from it.
func TestFetchedVersionIsEmptyForABinaryBuiltInAClone(t *testing.T) {
	inClone := []debug.BuildSetting{{Key: "vcs.revision", Value: "27af85afbc5900000000000000000000000000ff"}}
	cases := map[string]struct {
		info *debug.BuildInfo
		want string
	}{
		"go run <package>@<version>":    {&debug.BuildInfo{Main: debug.Module{Version: "v0.5.0"}}, "v0.5.0"},
		"go run in a clone":             {&debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, ""},
		"go build in a clone, at a tag": {&debug.BuildInfo{Main: debug.Module{Version: "v0.5.0"}, Settings: inClone}, ""},
		"go build in a clone, past it":  {&debug.BuildInfo{Main: debug.Module{Version: "v0.5.1-0.20261025220344-27af85afbc59"}, Settings: inClone}, ""},
		"go build, uncommitted":         {&debug.BuildInfo{Main: debug.Module{Version: "v0.5.0+dirty"}, Settings: inClone}, ""},
		"built through a replace directive": {&debug.BuildInfo{
			Main: debug.Module{Version: "v0.0.0", Replace: &debug.Module{Path: "..", Version: "(devel)"}},
		}, ""},
		"no build information": {nil, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, fetchedVersion(tc.info))
		})
	}
}

// ---- paying

// onyxTerms is an offer for a chain whose public RPC the buyer knows, so a buyer
// that reaches the node at X402_GNO_RPC did not fall back to its own table.
const onyxTerms = `{
  "x402Version": 2,
  "error": "Payment required",
  "resource": {"url": "http://seller.test/weather", "description": "Weather data", "mimeType": "application/json"},
  "accepts": [{
    "scheme": "exact",
    "network": "gno:onyx-1",
    "asset": "ugnot",
    "amount": "250000",
    "payTo": "g1nmzz8jc66mgfswfqhuze5ulmyde3ql5hsyffud",
    "maxTimeoutSeconds": 300,
    "extra": {"areFeesSponsored": false}
  }]
}`

// sellerAndNode stands up a seller answering every request with terms and a node
// answering nothing a payment can use, and records which of the two was asked,
// in order.
func sellerAndNode(t *testing.T, terms string) (seller, node *httptest.Server, seen func() []string) {
	t.Helper()
	var (
		mu  sync.Mutex
		log []string
	)
	record := func(who string) {
		mu.Lock()
		defer mu.Unlock()
		log = append(log, who)
	}
	seller = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		record("seller")
		w.Header().Set("PAYMENT-REQUIRED", base64.StdEncoding.EncodeToString([]byte(terms)))
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(seller.Close)
	node = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		record("node")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(node.Close)
	return seller, node, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(log)
	}
}

// The offer names the chain, and so the node the payment's account state must
// come from.
func TestPayingReadsTheOfferBeforeItAsksANode(t *testing.T) {
	seller, node, seen := sellerAndNode(t, onyxTerms)

	stdout, code := buy(t, map[string]string{"X402_GNO_RPC": node.URL}, seller.URL+"/weather")

	assert.Equal(t, 1, code)
	assert.Contains(t, stdout, "not paid: ")
	require.NotEmpty(t, seen())
	assert.Equal(t, "seller", seen()[0])
	assert.Contains(t, seen(), "node", "X402_GNO_RPC names the node, whatever chain the offer names")
}

func TestPayingRefusesAnOfferWithNoGnoNetwork(t *testing.T) {
	seller, node, seen := sellerAndNode(t, strings.Replace(onyxTerms, `"gno:onyx-1"`, `"eip155:8453"`, 1))

	stdout, code := buy(t, map[string]string{"X402_GNO_RPC": node.URL}, seller.URL+"/weather")

	assert.Equal(t, 1, code)
	assert.Contains(t, stdout, "not paid: the seller's offer names no ugnot payment on a gno network")
	assert.NotContains(t, seen(), "node")
}

// listing is onyxTerms offering one copy of its offer per edit, each copy
// changed by its edit.
func listing(t *testing.T, edits ...func(*x402.PaymentRequirements)) string {
	t.Helper()
	var terms x402.PaymentRequired
	require.NoError(t, json.Unmarshal([]byte(onyxTerms), &terms))
	require.Len(t, terms.Accepts, 1)
	offers := make([]x402.PaymentRequirements, 0, len(edits))
	for _, edit := range edits {
		offer := terms.Accepts[0]
		edit(&offer)
		offers = append(offers, offer)
	}
	terms.Accepts = offers
	listed, err := json.Marshal(terms)
	require.NoError(t, err)
	return string(listed)
}

// A gno seller may price a route in any bank coin, and this buyer pays in ugnot
// alone, so it pays the ugnot offer wherever the list puts it.
func TestPayingPicksTheUgnotOffer(t *testing.T) {
	seller, node, seen := sellerAndNode(t, listing(t,
		func(offer *x402.PaymentRequirements) { offer.Network, offer.Asset = "gno:other-1", "uother" },
		func(*x402.PaymentRequirements) {},
	))

	buy(t, map[string]string{"X402_GNO_RPC": node.URL}, seller.URL+"/weather")

	assert.Contains(t, seen(), "node", "the buyer stopped before signing the ugnot offer")
}

// An offer above the cap is passed over for the next one on another gno chain,
// rather than ending the purchase.
func TestPayingPassesOverAnOfferAboveTheCap(t *testing.T) {
	seller, node, seen := sellerAndNode(t, listing(t,
		func(offer *x402.PaymentRequirements) { offer.Amount = "2000000" },
		func(offer *x402.PaymentRequirements) { offer.Network = "gno:dev" },
	))

	buy(t, map[string]string{"X402_GNO_RPC": node.URL}, seller.URL+"/weather")

	assert.Contains(t, seen(), "node", "the buyer stopped before signing the offer within the cap")
}

// The receipt rides in the answer's head, so a payment that settled is named
// even when the body cannot be read whole; the buyer still fails, since the
// resource did not arrive.
func TestPayingNamesASettledPaymentWhoseAnswerIsCutShort(t *testing.T) {
	const tx = "fa01716b171e58dfdc84b5075017a9d1ac20b0d745ed75627f3f80effa14317e"
	const payer = "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5"
	receipt := encoded(t, map[string]any{"success": true, "transaction": tx, "payer": payer, "network": "gno:dev"})
	seller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("PAYMENT-RESPONSE", receipt)
		_, _ = w.Write(bytes.Repeat([]byte("a"), maxAnswer+1))
	}))
	defer seller.Close()

	stdout, code := buy(t, nil, seller.URL+"/weather")

	assert.Equal(t, 1, code)
	assert.True(t, strings.Contains(stdout, "paid: tx "+tx+" by "+payer+"\n"), "the receipt line is missing")
	assert.True(t, strings.HasSuffix(stdout, "\nthe answer was cut short: it runs past 10485760 bytes\n"), "the cut is not reported last")
	assert.False(t, strings.Contains(stdout, "not paid"), "a settled payment reported as not paid")
}

func TestPayingReadsAnEmptySellerArgumentAsNoneGiven(t *testing.T) {
	seller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "sunny")
	}))
	defer seller.Close()

	stdout, code := buy(t, map[string]string{"X402_SELLER_URL": seller.URL}, "")

	assert.Contains(t, stdout, "served without payment\nsunny\n")
	assert.Equal(t, 0, code)
}

func TestRPCForPicksTheNodeOfTheOffersChain(t *testing.T) {
	cases := map[string]struct {
		network, env, want string
	}{
		"a chain with a public RPC": {"gno:onyx-1", "", "https://rpc.onyx.testnets.gno.land:443"},
		"any other chain":           {"gno:dev", "", "http://127.0.0.1:26657"},
		"X402_GNO_RPC set":          {"gno:onyx-1", "http://node.test:26657", "http://node.test:26657"},
		"a node in tm2's own form":  {"gno:dev", "tcp://127.0.0.1:26657", "http://127.0.0.1:26657"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{"X402_GNO_RPC": tc.env}
			assert.Equal(t, tc.want, rpcFor(tc.network, func(name string) string { return env[name] }))
		})
	}
}

// ---- the answer

// encoded is v as JSON in base64, the way x402 v2 carries a message in a header.
func encoded(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(raw)
}

// sellerAnswer is one case of testdata/answers.json: what a seller answers and
// what the buyer prints after its "buying as" line, and its exit code. A 402
// would have the buyer pay, so refusals arrive under another status; the buyer
// reads the receipt and the challenge whatever the status.
type sellerAnswer struct {
	Name       string
	Status     int
	Receipt    map[string]any // sent in PAYMENT-RESPONSE
	RawReceipt string         // sent in PAYMENT-RESPONSE as it is
	Challenge  map[string]any // sent in PAYMENT-REQUIRED
	Body       string
	Stdout     string
	Exit       int
}

func TestTheBuyerSaysWhatTheSellersAnswerMeans(t *testing.T) {
	raw, err := os.ReadFile("../testdata/answers.json")
	require.NoError(t, err)
	var answers []sellerAnswer
	require.NoError(t, json.Unmarshal(raw, &answers))
	require.NotEmpty(t, answers)

	for _, a := range answers {
		t.Run(a.Name, func(t *testing.T) {
			header := http.Header{}
			if a.Receipt != nil {
				header.Set("PAYMENT-RESPONSE", encoded(t, a.Receipt))
			}
			if a.RawReceipt != "" {
				header.Set("PAYMENT-RESPONSE", a.RawReceipt)
			}
			if a.Challenge != nil {
				header.Set("PAYMENT-REQUIRED", encoded(t, a.Challenge))
			}
			seller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				maps.Copy(w.Header(), header)
				w.WriteHeader(a.Status)
				_, _ = io.WriteString(w, a.Body)
			}))
			defer seller.Close()

			stdout, code := buy(t, nil, seller.URL+"/weather")

			_, printed, _ := strings.Cut(stdout, "\n")
			assert.Equal(t, a.Stdout, printed)
			assert.Equal(t, a.Exit, code)
		})
	}
}
