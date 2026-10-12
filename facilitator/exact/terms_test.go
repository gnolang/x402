package exact_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	x402 "github.com/x402-foundation/x402/go/v2"

	"github.com/gnoverse/x402/facilitator/exact"
	"github.com/gnoverse/x402/facilitator/exact/exacttest"
)

// TestTheMechanismRefusesTermsItDoesNotImplement pins §6.1's reserved keys:
// extra.assetTransferMethod and extra.paymentFlow mean what the specification
// says, so a payment offered under a transfer method or a flow this mechanism
// does not implement is refused rather than settled under terms it never honored.
// Omitting either key, setting it to null as upstream's SDKs read it, or naming
// the mechanism's own, is the default and verifies.
func TestTheMechanismRefusesTermsItDoesNotImplement(t *testing.T) {
	cases := map[string]struct {
		requirementsExtra map[string]any
		acceptedExtra     map[string]any
		wantReason        string
	}{
		"no transfer method or flow named":    {},
		"the mechanism's own transfer method": {requirementsExtra: map[string]any{"assetTransferMethod": exact.AssetTransferMethod}},
		"the authorization flow named":        {requirementsExtra: map[string]any{"paymentFlow": "authorization"}},
		"a null transfer method":              {requirementsExtra: map[string]any{"assetTransferMethod": nil}},
		"a null flow":                         {requirementsExtra: map[string]any{"paymentFlow": nil}},
		"a payload accepting a null flow":     {acceptedExtra: map[string]any{"paymentFlow": nil}},
		"another transfer method": {
			requirementsExtra: map[string]any{"assetTransferMethod": "eip3009"},
			wantReason:        exact.ReasonInvalidRequirements,
		},
		"a transfer method that is not a string": {
			requirementsExtra: map[string]any{"assetTransferMethod": 1},
			wantReason:        exact.ReasonInvalidRequirements,
		},
		"another flow": {
			requirementsExtra: map[string]any{"paymentFlow": "upfront"},
			wantReason:        exact.ReasonInvalidRequirements,
		},
		"a payload accepting another transfer method": {
			acceptedExtra: map[string]any{"assetTransferMethod": "eip3009"},
			wantReason:    exact.ReasonInvalidPayload,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			txB64, account := exacttest.SignedTx(t, nil)
			requirements := exacttest.Requirements()
			requirements.Extra = tc.requirementsExtra
			payload := exacttest.PaymentPayload(requirements, txB64)
			payload.Accepted.Extra = tc.acceptedExtra

			_, err := exact.NewFacilitator(&exacttest.Node{Account: account}, exacttest.ChainID).
				Verify(context.Background(), payload, requirements, nil)

			if tc.wantReason == "" {
				require.NoError(t, err)
				return
			}
			refusal, ok := errors.AsType[*x402.VerifyError](err)
			require.True(t, ok, "want *x402.VerifyError, got %v", err)
			assert.Equal(t, tc.wantReason, refusal.InvalidReason)
		})
	}
}
