// Package exacttest provides fixtures for testing the gno exact facilitator and
// what is built on it: a scripted chain, the requirements the fixtures price, and
// payments signed for real against the sign doc gno's auth ante would use.
package exacttest

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/gnolang/gno/tm2/pkg/amino"
	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/crypto/ed25519"
	"github.com/gnolang/gno/tm2/pkg/sdk/bank"
	"github.com/gnolang/gno/tm2/pkg/std"
	"github.com/stretchr/testify/require"
	"github.com/x402-foundation/x402/go/v2/types"

	"github.com/gnolang/x402/facilitator/exact"
)

const (
	// ChainID is the chain the fixtures are signed for. The sign doc covers it,
	// so verifying against another chain id must fail.
	ChainID = "dev"

	// Payer is the address the fixture payments pay from: gno's well-known test1.
	Payer = "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5"

	// PayTo is the address the fixture requirements pay.
	PayTo = "g1u7y667z64x2h7vc6fmpcprgey4ck233jaww9zq"
)

// Requirements is the offer the fixture payments answer: 250000ugnot to PayTo on
// gno:dev.
func Requirements() types.PaymentRequirements {
	return types.PaymentRequirements{
		Scheme:  "exact",
		Network: "gno:" + ChainID,
		Amount:  "250000",
		Asset:   "ugnot",
		PayTo:   PayTo,
	}
}

// PaymentPayload is the envelope a buyer sends for requirements, carrying
// transaction as its scheme payload.
func PaymentPayload(requirements types.PaymentRequirements, transaction string) types.PaymentPayload {
	return types.PaymentPayload{
		X402Version: 2,
		Accepted:    requirements,
		Payload:     map[string]any{"transaction": transaction},
	}
}

// Node is a scripted chain. Each field sets what the matching call answers, and
// the counters record the calls that reached it.
type Node struct {
	Account      exact.SignerAccount
	AccountErr   error
	OnAccount    func() // runs during the account read, when set
	SimulateErr  error
	Hash         string
	BroadcastErr error
	OnBroadcast  func() // runs during the broadcast, when set

	// Delivery answers AwaitDelivery when set; otherwise every transaction is
	// delivered. NeverDelivered is the answer for one no block executes.
	Delivery func(ctx context.Context) error

	Simulates   int
	Broadcasts  int
	Broadcasted []byte // the bytes the last broadcast carried
	Awaits      int
	Awaited     string // the hash the last AwaitDelivery asked about
}

// NeverDelivered is a Delivery for a transaction no block executes: it waits
// out the context it is given, as a node polled for one would.
func NeverDelivered(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

var _ exact.Node = (*Node)(nil)

func (n *Node) SignerAccount(context.Context, *std.Tx) (exact.SignerAccount, error) {
	if n.OnAccount != nil {
		n.OnAccount()
	}
	return n.Account, n.AccountErr
}

func (n *Node) Simulate(*std.Tx) error {
	n.Simulates++
	return n.SimulateErr
}

func (n *Node) Broadcast(tx []byte) (string, error) {
	if n.OnBroadcast != nil {
		n.OnBroadcast()
	}
	n.Broadcasts++
	n.Broadcasted = tx
	return n.Hash, n.BroadcastErr
}

func (n *Node) AwaitDelivery(ctx context.Context, hash string) error {
	n.Awaits++
	n.Awaited = hash
	if n.Delivery == nil {
		return nil
	}
	return n.Delivery(ctx)
}

// Payment describes the payment Requirements prices, signed for real against
// the sign-doc inputs the auth ante would use. Account checks exercise crypto,
// so a placeholder signature proves nothing there.
type Payment struct {
	Key         crypto.PrivKey
	ChainID     string
	Number      uint64
	Sequence    uint64
	SessionAddr crypto.Address // zero for a master-key signature
	From        crypto.Address // zero defaults to the signing key's own address
}

// Tx is the payment, signed.
func (p Payment) Tx(t *testing.T) *std.Tx {
	t.Helper()
	from := p.From
	if from.IsZero() {
		from = p.Key.PubKey().Address()
	}
	req := Requirements()
	tx := &std.Tx{
		Msgs: []std.Msg{bank.MsgSend{
			FromAddress: from,
			ToAddress:   crypto.MustAddressFromString(req.PayTo),
			Amount:      std.MustParseCoins(req.Amount + req.Asset),
		}},
		Fee: std.NewFee(100000, std.MustParseCoin("1000000ugnot")),
	}
	signBytes, err := tx.GetSignBytes(p.ChainID, p.Number, p.Sequence)
	require.NoError(t, err)
	signature, err := p.Key.Sign(signBytes)
	require.NoError(t, err)
	tx.Signatures = []std.Signature{{
		PubKey:      p.Key.PubKey(),
		Signature:   signature,
		SessionAddr: p.SessionAddr,
	}}
	return tx
}

// MasterKey is the key the fixture payments are signed with.
func MasterKey(t *testing.T) crypto.PrivKey {
	t.Helper()
	return ed25519.GenPrivKeyFromSecret([]byte("x402-account-master"))
}

// AccountAt is the on-chain account a signature keyed by key verifies against
// at the given account number and sequence.
func AccountAt(key crypto.PrivKey, number, sequence uint64) exact.SignerAccount {
	return AccountWithKey(key.PubKey(), number, sequence)
}

// AccountWithKey is the same for an explicit stored key, including none — the
// shape of an account that has never signed.
func AccountWithKey(pub crypto.PubKey, number, sequence uint64) exact.SignerAccount {
	return exact.SignerAccount{Account: &std.BaseAccount{
		PubKey:        pub,
		AccountNumber: number,
		Sequence:      sequence,
	}}
}

// SignedTx is the payment Requirements prices, signed for real and encoded the
// way a payload carries it, together with the on-chain account its signature
// verifies against. mutate, when non-nil, alters the transaction after it is
// signed.
//
// A payment that must reach simulation needs this rather than PlaceholderTx: the
// facilitator verifies the signature against the signer's account, which a
// placeholder cannot satisfy. The payer stays Payer, since the account a payment
// is checked against is whichever one the node answers with.
func SignedTx(t *testing.T, mutate func(*std.Tx)) (txB64 string, account exact.SignerAccount) {
	t.Helper()
	const (
		number   = 7
		sequence = 3
	)
	key := MasterKey(t)
	tx := Payment{
		Key:      key,
		ChainID:  ChainID,
		Number:   number,
		Sequence: sequence,
		From:     crypto.MustAddressFromString(Payer),
	}.Tx(t)
	if mutate != nil {
		mutate(tx)
	}
	return Encode(t, *tx), AccountAt(key, number, sequence)
}

// PlaceholderTx is the payment Requirements prices with a placeholder signature,
// encoded the way a payload carries it. Static verification reads no signature
// content, so it passes that and nothing after. mutate, when non-nil, alters the
// transaction before it is encoded.
func PlaceholderTx(t *testing.T, mutate func(*std.Tx)) string {
	t.Helper()
	tx := std.Tx{
		Msgs: []std.Msg{bank.MsgSend{
			FromAddress: crypto.MustAddressFromString(Payer),
			ToAddress:   crypto.MustAddressFromString(PayTo),
			Amount:      std.MustParseCoins("250000ugnot"),
		}},
		Fee:        std.NewFee(100000, std.MustParseCoin("1000000ugnot")),
		Signatures: []std.Signature{{Signature: []byte("sig")}},
	}
	if mutate != nil {
		mutate(&tx)
	}
	return Encode(t, tx)
}

// Encode is the base64 amino encoding a payload's transaction field carries.
func Encode(t *testing.T, tx std.Tx) string {
	t.Helper()
	raw, err := amino.Marshal(tx)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(raw)
}
