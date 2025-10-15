//go:build ((linux && amd64) || (linux && arm64) || (darwin && amd64) || (darwin && arm64) || (windows && amd64)) && !blst_disabled

package blst_test

import (
	"testing"

	"github.com/OffchainLabs/prysm/v6/crypto/bls/blst"
	"github.com/OffchainLabs/prysm/v6/crypto/bls/common"
	"github.com/OffchainLabs/prysm/v6/testing/require"
)

func BenchmarkSignature_Verify(b *testing.B) {
	sk, err := blst.RandKey()
	require.NoError(b, err)

	msg := []byte("Some msg")
	sig := sk.Sign(msg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !sig.Verify(sk.PublicKey(), msg) {
			b.Fatal("could not verify sig")
		}
	}
}

func BenchmarkSignature_AggregateVerify(b *testing.B) {
	sigN := 128 // MAX_ATTESTATIONS per block.

	var pks []common.PublicKey
	var sigs []common.Signature
	var msgs [][32]byte
	for i := 0; i < sigN; i++ {
		msg := [32]byte{'s', 'i', 'g', 'n', 'e', 'd', byte(i)}
		sk, err := blst.RandKey()
		require.NoError(b, err)
		sig := sk.Sign(msg[:])
		pks = append(pks, sk.PublicKey())
		sigs = append(sigs, sig)
		msgs = append(msgs, msg)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.Run("aggregate sign", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			blst.AggregateSignatures(sigs)
		}
	})

	aggregated := blst.AggregateSignatures(sigs)

	b.ResetTimer()
	b.ReportAllocs()
	b.Run("aggregate verify", func(b *testing.B) {

		for i := 0; i < b.N; i++ {
			if !aggregated.AggregateVerify(pks, msgs) {
				b.Fatal("could not verify aggregate sig")
			}
		}
	})

	// now verify each individually to see the performance diff
	b.ResetTimer()
	b.ReportAllocs()
	b.Run("individual verify", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for j := 0; j < sigN; j++ {
				if !sigs[j].Verify(pks[j], msgs[j][:]) {
					b.Fatal("could not verify sig")
				}
			}
		}
	})
}

func BenchmarkSecretKey_Marshal(b *testing.B) {
	key, err := blst.RandKey()
	require.NoError(b, err)
	d := key.Marshal()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := blst.SecretKeyFromBytes(d)
		_ = err
	}
}
