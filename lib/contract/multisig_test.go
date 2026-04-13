package contract

import (
	"encoding/hex"
	"testing"

	"github.com/libsv/go-bk/bec"
)

func TestMultiSig_GetMultiSigAddressAndLockScript(t *testing.T) {
	privs := make([]*bec.PrivateKey, 3)
	var pks []string
	for i := 0; i < 3; i++ {
		pk, err := bec.NewPrivateKey(bec.S256())
		if err != nil {
			t.Fatal(err)
		}
		privs[i] = pk
		pks = append(pks, hex.EncodeToString(pk.PubKey().SerialiseCompressed()))
	}
	addr, err := GetMultiSigAddress(pks, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidateMultiSigAddress(addr) {
		t.Fatal("expected valid multisig address")
	}
	ok, err := VerifyMultiSigAddress(pks, addr)
	if err != nil || !ok {
		t.Fatalf("verify: %v ok=%v", err, ok)
	}
	asm, err := GetMultiSigLockScript(addr)
	if err != nil || asm == "" {
		t.Fatalf("lock script: %v %q", err, asm)
	}
}
