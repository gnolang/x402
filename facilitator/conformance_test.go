package facilitator

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	x402 "github.com/x402-foundation/x402/go/v2"
	"github.com/x402-foundation/x402/go/v2/types"
)

// The fixtures below are the literal base64 header values published in the x402
// v2 HTTP transport specification, transcribed verbatim from
// specs/transports-v2/http.md at x402-foundation/x402 commit
// f8f83309706b14cc696962f0a002eeeb66c3dc26 (lines 22, 67, 123 and 147).
//
// They exist so that conformance is checked against the spec rather than against
// an encoder's own output. A round trip through our own types proves only
// that we are self-consistent, which is exactly how this implementation came to
// declare x402Version 2 while speaking v1's transport. Replacing these with
// re-encoded output would make the whole file worthless.
const (
	specPaymentRequiredHeader = "eyJ4NDAyVmVyc2lvbiI6MiwiZXJyb3IiOiJQQVlNRU5ULVNJR05BVFVSRSBoZWFkZXIgaXMgcmVxdWlyZWQiLCJyZXNvdXJjZSI6eyJ1cmwiOiJodHRwczovL2FwaS5leGFtcGxlLmNvbS9wcmVtaXVtLWRhdGEiLCJkZXNjcmlwdGlvbiI6IkFjY2VzcyB0byBwcmVtaXVtIG1hcmtldCBkYXRhIiwibWltZVR5cGUiOiJhcHBsaWNhdGlvbi9qc29uIn0sImFjY2VwdHMiOlt7InNjaGVtZSI6ImV4YWN0IiwibmV0d29yayI6ImVpcDE1NTo4NDUzMiIsImFtb3VudCI6IjEwMDAwIiwiYXNzZXQiOiIweDAzNkNiRDUzODQyYzU0MjY2MzRlNzkyOTU0MWVDMjMxOGYzZENGN2UiLCJwYXlUbyI6IjB4MjA5NjkzQmM2YWZjMEM1MzI4YkEzNkZhRjAzQzUxNEVGMzEyMjg3QyIsIm1heFRpbWVvdXRTZWNvbmRzIjo2MCwiZXh0cmEiOnsibmFtZSI6IlVTREMiLCJ2ZXJzaW9uIjoiMiJ9fV19"

	specPaymentSignatureHeader = "eyJ4NDAyVmVyc2lvbiI6MiwicmVzb3VyY2UiOnsidXJsIjoiaHR0cHM6Ly9hcGkuZXhhbXBsZS5jb20vcHJlbWl1bS1kYXRhIiwiZGVzY3JpcHRpb24iOiJBY2Nlc3MgdG8gcHJlbWl1bSBtYXJrZXQgZGF0YSIsIm1pbWVUeXBlIjoiYXBwbGljYXRpb24vanNvbiJ9LCJhY2NlcHRlZCI6eyJzY2hlbWUiOiJleGFjdCIsIm5ldHdvcmsiOiJlaXAxNTU6ODQ1MzIiLCJhbW91bnQiOiIxMDAwMCIsImFzc2V0IjoiMHgwMzZDYkQ1Mzg0MmM1NDI2NjM0ZTc5Mjk1NDFlQzIzMThmM2RDRjdlIiwicGF5VG8iOiIweDIwOTY5M0JjNmFmYzBDNTMyOGJBMzZGYUYwM0M1MTRFRjMxMjI4N0MiLCJtYXhUaW1lb3V0U2Vjb25kcyI6NjAsImV4dHJhIjp7Im5hbWUiOiJVU0RDIiwidmVyc2lvbiI6IjIifX0sInBheWxvYWQiOnsic2lnbmF0dXJlIjoiMHgyZDZhNzU4OGQ2YWNjYTUwNWNiZjBkOWE0YTIyN2UwYzUyYzZjMzQwMDhjOGU4OTg2YTEyODMyNTk3NjQxNzM2MDhhMmNlNjQ5NjY0MmUzNzdkNmRhOGRiYmY1ODM2ZTliZDE1MDkyZjllY2FiMDVkZWQzZDYyOTNhZjE0OGI1NzFjIiwiYXV0aG9yaXphdGlvbiI6eyJmcm9tIjoiMHg4NTdiMDY1MTlFOTFlM0E1NDUzODc5MWJEYmIwRTIyMzczZTM2YjY2IiwidG8iOiIweDIwOTY5M0JjNmFmYzBDNTMyOGJBMzZGYUYwM0M1MTRFRjMxMjI4N0MiLCJ2YWx1ZSI6IjEwMDAwIiwidmFsaWRBZnRlciI6IjE3NDA2NzIwODkiLCJ2YWxpZEJlZm9yZSI6IjE3NDA2NzIxNTQiLCJub25jZSI6IjB4ZjM3NDY2MTNjMmQ5MjBiNWZkYWJjMDg1NmYyYWViMmQ0Zjg4ZWU2MDM3YjhjYzVkMDRhNzFhNDQ2MmYxMzQ4MCJ9fX0="

	specPaymentResponseSuccessHeader = "eyJzdWNjZXNzIjp0cnVlLCJ0cmFuc2FjdGlvbiI6IjB4MTIzNDU2Nzg5MGFiY2RlZjEyMzQ1Njc4OTBhYmNkZWYxMjM0NTY3ODkwYWJjZGVmMTIzNDU2Nzg5MGFiY2RlZiIsIm5ldHdvcmsiOiJlaXAxNTU6ODQ1MzIiLCJwYXllciI6IjB4ODU3YjA2NTE5RTkxZTNBNTQ1Mzg3OTFiRGJiMEUyMjM3M2UzNmI2NiJ9"

	specPaymentResponseFailureHeader = "eyJzdWNjZXNzIjpmYWxzZSwiZXJyb3JSZWFzb24iOiJpbnN1ZmZpY2llbnRfZnVuZHMiLCJ0cmFuc2FjdGlvbiI6IiIsIm5ldHdvcmsiOiJlaXAxNTU6ODQ1MzIiLCJwYXllciI6IjB4ODU3YjA2NTE5RTkxZTNBNTQ1Mzg3OTFiRGJiMEUyMjM3M2UzNmI2NiJ9"
)

// decodeSpecHeader decodes a spec fixture into both a typed value and a raw
// key map: the typed decode proves our structs absorb it, the raw map proves the
// key set the spec actually published, which a typed decode silently tolerates
// losing.
func decodeSpecHeader(t *testing.T, header string, into any) map[string]json.RawMessage {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(header)
	require.NoError(t, err, "the spec's header value must be valid base64")
	require.NoError(t, json.Unmarshal(data, into), "our types must absorb the spec's fixture")
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	return raw
}

// ---- Envelope level: the spec's own fixtures

// TestConformance_SpecPaymentRequiredHeader checks the accepts[] entry the spec
// publishes against PaymentRequirements, which is half of what a seller relays
// to /verify and /settle. The 402 that carries it is the canonical middleware's
// to emit, so only the entry is decoded here.
func TestConformance_SpecPaymentRequiredHeader(t *testing.T) {
	var got struct {
		Accepts []types.PaymentRequirements `json:"accepts"`
	}
	raw := decodeSpecHeader(t, specPaymentRequiredHeader, &got)

	for _, key := range []string{"x402Version", "error", "resource", "accepts"} {
		assert.Contains(t, raw, key)
	}

	require.Len(t, got.Accepts, 1)
	accepted := got.Accepts[0]
	assert.Equal(t, "exact", accepted.Scheme)
	assert.Equal(t, "eip155:84532", accepted.Network)
	assert.Equal(t, "10000", accepted.Amount)
	assert.Equal(t, "0x036CbD53842c5426634e7929541eC2318f3dCF7e", accepted.Asset)
	assert.Equal(t, "0x209693Bc6afc0C5328bA36FaF03C514EF312287C", accepted.PayTo)
	assert.Equal(t, 60, accepted.MaxTimeoutSeconds)

	// The spec's extra carries EVM scheme keys this implementation knows
	// nothing about. Keeping them is the whole reason Extra is a map: a client
	// has to echo back what it was given.
	assert.Equal(t, map[string]any{"name": "USDC", "version": "2"}, accepted.Extra)

}

func TestConformance_SpecPaymentSignatureHeader(t *testing.T) {
	var got types.PaymentPayload
	raw := decodeSpecHeader(t, specPaymentSignatureHeader, &got)

	for _, key := range []string{"x402Version", "resource", "accepted", "payload"} {
		assert.Contains(t, raw, key)
	}

	assert.Equal(t, 2, got.X402Version)
	require.NotNil(t, got.Resource)
	assert.Equal(t, "https://api.example.com/premium-data", got.Resource.URL)

	assert.Equal(t, "exact", got.Accepted.Scheme)
	assert.Equal(t, "eip155:84532", got.Accepted.Network)
	assert.Equal(t, "10000", got.Accepted.Amount)
	assert.Equal(t, "0x209693Bc6afc0C5328bA36FaF03C514EF312287C", got.Accepted.PayTo)
	assert.Equal(t, 60, got.Accepted.MaxTimeoutSeconds)
	assert.Equal(t, map[string]any{"name": "USDC", "version": "2"}, got.Accepted.Extra)

	// The fixture's payload is the EVM exact scheme's {signature, authorization}.
	// Only its presence is asserted: the envelope is shared across schemes, the
	// payload is not.
	assert.NotEmpty(t, raw["payload"])
	assert.NotContains(t, got.Payload, "transaction", "an EVM payload carries no gno transaction")
}

func TestConformance_SpecPaymentResponseHeaders(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var got x402.SettleResponse
		raw := decodeSpecHeader(t, specPaymentResponseSuccessHeader, &got)
		for _, key := range []string{"success", "transaction", "network", "payer"} {
			assert.Contains(t, raw, key)
		}
		assert.True(t, got.Success)
		assert.Equal(t, "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", got.Transaction)
		assert.Equal(t, x402.Network("eip155:84532"), got.Network)
		assert.Equal(t, "0x857b06519E91e3A54538791bDbb0E22373e36b66", got.Payer)
	})

	t.Run("failure", func(t *testing.T) {
		var got x402.SettleResponse
		raw := decodeSpecHeader(t, specPaymentResponseFailureHeader, &got)
		for _, key := range []string{"success", "errorReason", "transaction", "network", "payer"} {
			assert.Contains(t, raw, key)
		}
		assert.False(t, got.Success)
		assert.Equal(t, "insufficient_funds", got.ErrorReason)
		// The spec publishes transaction as the empty string, not as an absent
		// key, on the failure path.
		assert.Equal(t, `""`, string(raw["transaction"]))
		assert.Empty(t, got.Transaction)
	})
}
