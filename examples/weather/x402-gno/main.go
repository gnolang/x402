// Command x402-gno buys the weather from the seller one directory up: one paid
// GET, printed for a human. It is buy.mjs in Go, with the same arguments,
// environment and output. The x402 flow is the five numbered steps in pay: ask,
// read the terms from the 402, sign, ask again with the payment, read the
// receipt. Upstream's Go client encodes and decodes each message; the gno-aware
// lines are the signer, the node and the Register call.
//
//	go run github.com/gnolang/x402/examples/weather/x402-gno@<version> [seller URL]
//	go run github.com/gnolang/x402/examples/weather/x402-gno@<version> --new-key [seller URL]
//
// The buyer signs with the mnemonic in X402_MNEMONIC, a funded account on the
// chain the seller's offer names, and reads its account sequence from a node on
// that chain: X402_GNO_RPC when set, else the chain's public RPC in publicRPC,
// else a local node. With X402_SESSION_MASTER set to an account's address,
// X402_MNEMONIC is instead a session key that account registered, and payments
// move that account's coins. --new-key prints a fresh mnemonic and the command
// that pays the seller with it, and pays nothing.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	rpcclient "github.com/gnolang/gno/tm2/pkg/bft/rpc/client"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/crypto/bip39"
	"github.com/gnolang/gno/tm2/pkg/crypto/keys"
	"github.com/gnolang/gno/tm2/pkg/std"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402http "github.com/x402-foundation/x402/go/v2/http"

	clientexact "github.com/gnolang/x402/client/exact"
)

// gno's well-known test1 mnemonic, the default for a local test chain.
const test1Mnemonic = "source bonus chronic canvas draft south burst lottery vacant surface solve popular case indicate oppose farm nothing bullet exhibit title speed wink action roast"

// maxPrice is the most this buyer pays for one request, in ugnot. The network
// fee is paid on top of it.
const maxPrice = "1000000"

const defaultSellerURL = "http://localhost:8401/weather"

const packagePath = "github.com/gnolang/x402/examples/weather/x402-gno"

// requestTimeout bounds each request; a paid one waits while the seller has
// the payment settled on chain.
const requestTimeout = 2 * time.Minute

// maxAnswer bounds the body read from a seller, which this buyer does not
// trust, in bytes.
const maxAnswer = 10 << 20

// The public RPC of each chain a buyer reads its account from when X402_GNO_RPC
// is unset. Any other chain, such as a local dev chain, falls back to localRPC.
var publicRPC = map[string]string{"onyx-1": "https://rpc.onyx.testnets.gno.land:443"}

const localRPC = "http://127.0.0.1:26657"

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout))
}

// run buys once, or makes a key with --new-key, and returns the exit code.
func run(args []string, getenv func(string) string, out io.Writer) int {
	newKey := len(args) > 0 && args[0] == "--new-key"
	if newKey {
		args = args[1:]
	}
	var seller string
	if len(args) > 0 {
		seller = args[0]
	}

	if newKey {
		if err := printNewKey(out, seller); err != nil {
			fmt.Fprintf(out, "no key made: %v\n", err)
			return 1
		}
		return 0
	}
	served, err := pay(out, cmp.Or(seller, getenv("X402_SELLER_URL"), defaultSellerURL), getenv)
	if err != nil {
		fmt.Fprintf(out, "not paid: %v\n", err)
		return 1
	}
	if !served {
		return 1
	}
	return 0
}

// rpcFor returns the node to read the account from for a payment on network,
// "gno:<chain id>". A node reports its address in tm2's own form, which names
// the transport rather than the scheme an HTTP client needs.
func rpcFor(network string, getenv func(string) string) string {
	chain, _ := strings.CutPrefix(network, "gno:")
	rpc := cmp.Or(getenv("X402_GNO_RPC"), publicRPC[chain], localRPC)
	if addr, ok := strings.CutPrefix(rpc, "tcp://"); ok {
		return "http://" + addr
	}
	return rpc
}

// ---- --new-key

// printNewKey writes no file and sends no request: the mnemonic it prints is
// the key's only copy.
func printNewKey(out io.Writer, seller string) error {
	entropy, err := bip39.NewEntropy(256)
	if err != nil {
		return err
	}
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		return err
	}
	_, address, err := keyOf(mnemonic)
	if err != nil {
		return err
	}
	target := "<seller URL>"
	if seller != "" {
		target = shellWord(seller)
	}
	_, err = fmt.Fprintf(out, `a new key for the testnet, saved nowhere: this is the only copy of its mnemonic
use it as a throwaway, and never send it anything you would mind losing

mnemonic: %s
address:  %s

fund the address at https://faucet.gno.land, then pay with it:
  X402_MNEMONIC='%s' %s %s
`, mnemonic, address, mnemonic, payCommand(moduleVersion()), target)
	return err
}

// keyOf derives mnemonic's key into a keybase held in memory alone, under the
// name "buyer", and returns the address a payment it signs comes from.
func keyOf(mnemonic string) (keys.Keybase, crypto.Address, error) {
	kb := keys.NewInMemory()
	info, err := kb.CreateAccount("buyer", mnemonic, "", "", 0, 0)
	if err != nil {
		return nil, crypto.Address{}, err
	}
	return kb, info.GetAddress(), nil
}

func moduleVersion() string {
	info, _ := debug.ReadBuildInfo()
	return fetchedVersion(info)
}

// fetchedVersion is the module version go fetched info's binary at, a release
// or a commit, or "" for a binary built from a checkout. A checkout's build
// records its commit, which may never have been pushed, or replaces the module
// with a directory, under a version that names nothing.
func fetchedVersion(info *debug.BuildInfo) string {
	if info == nil || info.Main.Version == "(devel)" || info.Main.Replace != nil {
		return ""
	}
	if slices.ContainsFunc(info.Settings, func(setting debug.BuildSetting) bool { return setting.Key == "vcs.revision" }) {
		return ""
	}
	return info.Main.Version
}

// payCommand is the command that runs this buyer again: pinned to version, or,
// without one, the clone's own buyer, which runs from inside the clone.
func payCommand(version string) string {
	if version == "" {
		return "go run " + packagePath
	}
	return "go run " + packagePath + "@" + version
}

var plainWord = regexp.MustCompile(`^[\w@%+=:,./-]+$`)

// shellWord writes s as one shell word: as it is when no shell reads any of its
// characters, single-quoted otherwise, such as for a ? (a glob in zsh) or an &.
func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ---- paying

// pay buys sellerURL once, prints the outcome, and reports whether the seller
// served it. An error means no answer came back to report.
func pay(out io.Writer, sellerURL string, getenv func(string) string) (bool, error) {
	mnemonic := cmp.Or(getenv("X402_MNEMONIC"), test1Mnemonic)
	kb, address, err := keyOf(mnemonic)
	if err != nil {
		return false, err
	}
	signer, err := clientexact.KeybaseSigner(kb, "buyer", "")
	if err != nil {
		return false, err
	}

	var opts []clientexact.Option
	payer := address
	switch master := getenv("X402_SESSION_MASTER"); {
	case master != "":
		owner, err := crypto.AddressFromBech32(master)
		if err != nil {
			return false, fmt.Errorf("X402_SESSION_MASTER: %w", err)
		}
		opts = append(opts, clientexact.WithMaster(owner))
		payer = owner
		fmt.Fprintf(out, "buying as %s, signed by its session key %s\n", owner, address)
	case mnemonic == test1Mnemonic:
		fmt.Fprintf(out, "buying as %s, gno's public test1 key; set X402_MNEMONIC to pay from your own account\n", address)
	default:
		fmt.Fprintf(out, "buying as %s\n", address)
	}

	ctx := context.Background()
	// The scheme is registered once the offer names its chains, each with a node
	// on that chain. A stock client pays only in USD-pegged assets until told
	// otherwise, and ugnot is not one.
	client := x402.Newx402Client()
	messages := x402http.Newx402HTTPClient(client)
	// Each request opens its own connection. The transport sends a request
	// again when the seller closed a reused one before answering, and a paid
	// request sent again is the same payment sent twice.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	web := &http.Client{Timeout: requestTimeout, Transport: transport}

	// 1. Ask without paying. A priced resource answers 402; any other answer is
	// the final one.
	got, err := get(ctx, web, sellerURL, nil)
	if err != nil {
		return false, err
	}
	if got.status == http.StatusPaymentRequired {
		// 2. Read the terms, which the 402 carries in its PAYMENT-REQUIRED header.
		terms, err := messages.GetPaymentRequiredResponse(headersOf(got.header), got.body)
		if err != nil {
			return false, err
		}
		networks := ugnotNetworks(terms.Accepts)
		if len(networks) == 0 {
			return false, errors.New("the seller's offer names no ugnot payment on a gno network")
		}
		allowed := make([]x402.SpendControlAsset, 0, len(networks))
		for _, network := range networks {
			rpc, err := rpcclient.NewHTTPClient(rpcFor(network, getenv))
			if err != nil {
				return false, err
			}
			client.Register(x402.Network(network), clientexact.NewExactGnoScheme(signer,
				clientexact.NewRPCNode(rpc), opts...))
			allowed = append(allowed, x402.SpendControlAsset{Network: x402.Network(network), Asset: "ugnot", MaxAmountPerPayment: maxPrice})
		}
		client.SetSpendControls(x402.SpendControls{AllowedAssets: allowed})

		// 3. Sign a payment that meets them: a bank send of the price to the
		// payee, within maxPrice, over the account state the offer's chain
		// reports. Nothing is broadcast here.
		selected, err := client.SelectPaymentRequirements(terms.Accepts)
		if err != nil {
			return false, err
		}
		payment, err := client.CreatePaymentPayload(x402.WithPaymentRequired(ctx, terms), selected, terms.Resource, terms.Extensions)
		if err != nil {
			if _, unknown := errors.AsType[std.UnknownAddressError](err); unknown {
				return false, fmt.Errorf("%s has no account at %s; fund it, then run this again (https://faucet.gno.land funds the public testnets)", address, rpcFor(selected.Network, getenv))
			}
			return false, err
		}
		signed, err := json.Marshal(payment)
		if err != nil {
			return false, err
		}
		headers, err := messages.EncodePaymentSignatureHeader(signed)
		if err != nil {
			return false, err
		}

		// 4. Ask again with the signed payment in PAYMENT-SIGNATURE. The seller
		// has its facilitator check it and settle it on chain before answering.
		got, err = get(ctx, web, sellerURL, headers)
		if err != nil {
			// The seller may have had the payment settled before its answer was
			// lost, so paying again could pay twice.
			fmt.Fprintf(out, "not confirmed: the answer to the payment was lost (%v); it may have gone through, so check %s before paying again\n", err, payer)
			return false, nil
		}
	}

	// 5. Read the answer: the weather once the payment settled, and the receipt
	// in PAYMENT-RESPONSE.
	return report(out, got), nil
}

// ugnotNetworks lists, in the offer's order and once each, the gno chains its
// exact offers price in ugnot, the only coin the spend controls admit.
func ugnotNetworks(accepts []x402.PaymentRequirements) []string {
	var networks []string
	for _, offer := range accepts {
		if offer.Scheme == "exact" && strings.HasPrefix(offer.Network, "gno:") && offer.Asset == "ugnot" &&
			!slices.Contains(networks, offer.Network) {
			networks = append(networks, offer.Network)
		}
	}
	return networks
}

// answer is what a seller sent back: its status and head, and as much of its
// body as was read, up to maxAnswer bytes. cut says why the body ends early,
// and is nil when it was read whole.
type answer struct {
	status int
	header http.Header
	body   []byte
	cut    error
}

// get asks for url with headers added. An error means no answer came back: a
// body that cannot be read whole still leaves the head, which carries the
// receipt of a payment that settled.
func get(ctx context.Context, web *http.Client, url string, headers map[string]string) (answer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return answer{}, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res, err := web.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer res.Body.Close()
	got := answer{status: res.StatusCode, header: res.Header}
	got.body, got.cut = io.ReadAll(io.LimitReader(res.Body, maxAnswer+1))
	if got.cut == nil && len(got.body) > maxAnswer {
		got.body, got.cut = got.body[:maxAnswer], fmt.Errorf("it runs past %d bytes", maxAnswer)
	}
	return got, nil
}

// headersOf flattens header to the first value of each name.
func headersOf(header http.Header) map[string]string {
	flat := make(map[string]string, len(header))
	for name := range header {
		flat[name] = header.Get(name)
	}
	return flat
}

// report prints what the seller's answer means, and whether it served the
// resource whole.
func report(out io.Writer, got answer) bool {
	messages := x402http.Newx402HTTPClient(x402.Newx402Client())
	headers := headersOf(got.header)
	receipt, receiptErr := messages.GetPaymentSettleResponse(headers)

	if got.status < 200 || got.status > 299 {
		// A verify refusal carries its reason in the challenge, a settle failure
		// in the receipt.
		var reason, tx string
		if receiptErr == nil {
			reason, tx = receipt.ErrorReason, receipt.Transaction
		}
		if reason == "" {
			if challenge, err := messages.GetPaymentRequiredResponse(headers, got.body); err == nil {
				reason = challenge.Error
			}
		}
		reason = strings.TrimSpace(reason)
		switch {
		case receiptErr == nil && receipt.ErrorReason == "settlement_pending":
			// The chain admitted the payment and has not executed it yet: the
			// weather is withheld, but the payment may still go through, and the
			// hash is how to find out.
			fmt.Fprintf(out, "not confirmed yet: tx %s (%s); the weather was withheld, and the payment may still go through\n", tx, reason)
		case receiptErr == nil && receipt.ErrorReason == "duplicate_settlement":
			// A copy of a payment the facilitator is settling or has settled: the
			// copy charges nothing, but the payment it copies may already have
			// gone through.
			named := ""
			if tx != "" {
				named = "tx " + tx + " "
			}
			fmt.Fprintf(out, "already settling: %s(%s); the weather was withheld, and the payment may already have gone through\n", named, reason)
		default:
			// A refusal may name a transaction as well, such as one a block
			// executed and aborted, which still charged the network fee.
			line := fmt.Sprintf("not paid (status %d)", got.status)
			if reason != "" {
				line += ": " + reason
			}
			if tx != "" {
				line += "; tx " + tx
			}
			fmt.Fprintln(out, line)
		}
		return false
	}

	switch {
	case got.header.Get("PAYMENT-RESPONSE") == "":
		fmt.Fprintln(out, "served without payment")
	case receiptErr != nil:
		fmt.Fprintln(out, "served, with a receipt this buyer cannot read")
	default:
		fmt.Fprintf(out, "paid: tx %s by %s\n", receipt.Transaction, receipt.Payer)
	}
	fmt.Fprintln(out, strings.TrimSpace(string(got.body)))
	if got.cut != nil {
		fmt.Fprintf(out, "the answer was cut short: %v\n", got.cut)
		return false
	}
	return true
}
