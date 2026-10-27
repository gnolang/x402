// Package scheme holds the rules of gno's "exact" x402 mechanism that its payer
// and its facilitator both enforce.
package scheme

import (
	"fmt"
	"regexp"
)

// AssetTransferMethod names how a gno payment moves value: the payer signs a
// bank send the facilitator broadcasts. It is the scheme's default, so an offer
// that omits extra.assetTransferMethod means it.
const AssetTransferMethod = "bank-send"

// MaxMemoBytes caps extra.memo; a longer memo would bloat the signed tx and every
// echo of the requirements. gno's own limit is far higher, so the cap is the
// scheme's rather than the chain's — and the number is the ecosystem's, not one
// picked here: the shipped SVM mechanism caps its own extra.memo at the same 256
// bytes (mechanisms/svm/constants.go), and its spec states the field's purpose in
// the same terms, attaching a payment reference such as an invoice id for
// reconciliation without needing a unique deposit address per payer.
//
// Every role answers for it: the facilitator refuses an over-cap memo on every
// payment, the seller refuses one while the requirements are still its own to
// fix, and the payer refuses to sign one. One name keeps them from drifting
// apart.
const MaxMemoBytes = 256

// chainIDReference is CAIP-2's grammar for the reference half of a network.
var chainIDReference = regexp.MustCompile(`^[-_a-zA-Z0-9]{1,32}$`)

// ValidChainID reports whether a chain id can be published as the reference half
// of a gno network name, "gno:<chain-id>".
//
// x402 names networks in CAIP-2, which limits the reference to
// [-_a-zA-Z0-9]{1,32}, while tm2 accepts any non-empty chain id up to 50 bytes. A
// colon makes the network read as three parts, which upstream's parser and the JS
// buyer refuse, so every payment would fail without naming the configuration.
// Any other chain id outside the grammar, such as one with a dot, both accept
// today, but it names no CAIP-2 network and another implementation may refuse
// it.
func ValidChainID(chainID string) error {
	if !chainIDReference.MatchString(chainID) {
		return fmt.Errorf("chain id %q cannot name a CAIP-2 network: want 1 to 32 of [-_a-zA-Z0-9]", chainID)
	}
	return nil
}
