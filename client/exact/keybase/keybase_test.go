package keybase_test

import (
	"sync"
	"testing"
	"time"

	"github.com/gnolang/gno/tm2/pkg/crypto"
	"github.com/gnolang/gno/tm2/pkg/crypto/keys"
	"github.com/gnolang/gno/tm2/pkg/crypto/multisig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gnolang/x402/client/exact/keybase"
)

const (
	// test1Mnemonic is gno's well-known test1 key. Nothing here is broadcast.
	test1Mnemonic = "source bonus chronic canvas draft south burst lottery vacant surface solve popular case indicate oppose farm nothing bullet exhibit title speed wink action roast"

	// test1Address is the address test1Mnemonic derives.
	test1Address = "g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5"

	// sessionMnemonic is a second public key, never funded.
	sessionMnemonic = "dance carry grunt uphold cloth wire usage usage clock reduce surprise page destroy reopen multiply away defense promote person web cigar immense armed enough"
)

// TestTheSignerSignsWithTheKeyItNames: the signer's public key is the
// keybase's, found by name or by address, and what it signs verifies against
// that key.
func TestTheSignerSignsWithTheKeyItNames(t *testing.T) {
	kb := keys.NewInMemory()
	info, err := kb.CreateAccount("buyer", test1Mnemonic, "", "", 0, 0)
	require.NoError(t, err)

	for _, name := range []string{"buyer", test1Address} {
		t.Run(name, func(t *testing.T) {
			signer, err := keybase.Signer(kb, name, "")
			require.NoError(t, err)

			signature, err := signer.Sign([]byte("sign bytes"))
			require.NoError(t, err)
			assert.Equal(t, info.GetPubKey(), signer.PubKey())
			assert.True(t, signer.PubKey().VerifyBytes([]byte("sign bytes"), signature))
		})
	}
}

// TestTheSignerRefusesAKeyTheKeybaseDoesNotHold: a missing key is an
// error when the signer is made, before any payment is attempted with it.
func TestTheSignerRefusesAKeyTheKeybaseDoesNotHold(t *testing.T) {
	_, err := keybase.Signer(keys.NewInMemory(), "buyer", "")

	require.Error(t, err)
}

// TestTheSignerSignsNothingWithTheWrongPassword: a key kept encrypted
// signs only with its password, so the first payment made with the wrong one
// fails rather than producing a signature.
func TestTheSignerSignsNothingWithTheWrongPassword(t *testing.T) {
	kb := keys.NewInMemory()
	_, err := kb.CreateAccount("buyer", test1Mnemonic, "", "right", 0, 0)
	require.NoError(t, err)

	wrong, err := keybase.Signer(kb, "buyer", "wrong")
	require.NoError(t, err)
	_, err = wrong.Sign([]byte("sign bytes"))
	require.Error(t, err)

	right, err := keybase.Signer(kb, "buyer", "right")
	require.NoError(t, err)
	_, err = right.Sign([]byte("sign bytes"))
	require.NoError(t, err)
}

// TestTheSignerRefusesAKeyThatCannotSign: a keybase may hold only a key's
// public half, an offline key or a multisig key. Neither can ever sign, so the
// signer refuses it when it is made, rather than let a payment read the chain
// and fail after, or tell the buyer to fund an address it cannot pay from.
func TestTheSignerRefusesAKeyThatCannotSign(t *testing.T) {
	kb := keys.NewInMemory()
	info, err := keys.NewInMemory().CreateAccount("buyer", test1Mnemonic, "", "", 0, 0)
	require.NoError(t, err)
	pub := info.GetPubKey()
	_, err = kb.CreateOffline("watch-only", pub)
	require.NoError(t, err)
	_, err = kb.CreateMulti("shared", multisig.NewPubKeyMultisigThreshold(1, []crypto.PubKey{pub}))
	require.NoError(t, err)

	_, err = keybase.Signer(kb, "watch-only", "")
	require.ErrorContains(t, err, "offline key, which cannot sign")
	_, err = keybase.Signer(kb, "shared", "")
	require.ErrorContains(t, err, "multi key, which cannot sign")
}

// TestTheSignerSignsConcurrentlyFromAKeybaseOnDisk: gnokey's keybase on
// disk locks its database for each signature, so payments signing at once
// take their turns rather than failing all but one.
func TestTheSignerSignsConcurrentlyFromAKeybaseOnDisk(t *testing.T) {
	kb, err := keys.NewKeyBaseFromDir(t.TempDir())
	require.NoError(t, err)
	_, err = kb.CreateAccount("buyer", test1Mnemonic, "", "pw", 0, 0)
	require.NoError(t, err)
	signer, err := keybase.Signer(kb, "buyer", "pw")
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() { _, errs[i] = signer.Sign([]byte("sign bytes")) })
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
}

// TestASignerMadeDuringASignatureWaitsForIt: finding a key opens
// gnokey's keybase on disk too, so a signer made while another key of the same
// home signs waits its turn rather than fail with the database locked.
func TestASignerMadeDuringASignatureWaitsForIt(t *testing.T) {
	disk, err := keys.NewKeyBaseFromDir(t.TempDir())
	require.NoError(t, err)
	_, err = disk.CreateAccount("buyer", test1Mnemonic, "", "pw", 0, 0)
	require.NoError(t, err)
	_, err = disk.CreateAccount("session", sessionMnemonic, "", "pw", 0, 0)
	require.NoError(t, err)
	kb := &pausedKeybase{Keybase: disk, signing: make(chan struct{}), resume: make(chan struct{})}
	buyer, err := keybase.Signer(kb, "buyer", "pw")
	require.NoError(t, err)

	signed, made := make(chan struct{}), make(chan struct{})
	var signErr, makeErr error
	go func() {
		defer close(signed)
		_, signErr = buyer.Sign([]byte("sign bytes"))
	}()
	<-kb.signing
	go func() {
		defer close(made)
		_, makeErr = keybase.Signer(kb, "session", "pw")
	}()

	assert.Never(t, closed(made), 200*time.Millisecond, 10*time.Millisecond)
	close(kb.resume)
	require.Eventually(t, closed(signed), 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, closed(made), 5*time.Second, 10*time.Millisecond)
	require.NoError(t, signErr)
	require.NoError(t, makeErr)
}

// pausedKeybase holds a signature in progress until resume closes, and closes
// signing once it has begun.
type pausedKeybase struct {
	keys.Keybase
	signing, resume chan struct{}
}

func (kb *pausedKeybase) Sign(name, passphrase string, msg []byte) ([]byte, crypto.PubKey, error) {
	close(kb.signing)
	<-kb.resume
	return kb.Keybase.Sign(name, passphrase, msg)
}

// closed reports whether ch has been closed, without waiting.
func closed(ch <-chan struct{}) func() bool {
	return func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
}
