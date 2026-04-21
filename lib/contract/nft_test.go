package contract

import (
	"strings"
	"testing"

	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

func TestBuildCodeScriptAndV0(t *testing.T) {
	txid := strings.Repeat("ab", 32)
	s, err := BuildCodeScript(txid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() < 100 {
		t.Fatalf("code script len=%d", s.Len())
	}
	s0, err := BuildCodeScriptV0(txid, 1)
	if err != nil {
		t.Fatal(err)
	}
	if s0.Len() <= s.Len() {
		t.Fatalf("v0 script should be longer than current")
	}
}

func TestBuildNFTHoldMintTape(t *testing.T) {
	addr := "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq"
	ok, _ := bscript.ValidateAddress(addr)
	if !ok {
		t.Skip("address validation")
	}
	m, err := BuildMintScript(addr)
	if err != nil {
		t.Fatal(err)
	}
	h, err := BuildNFTHoldScript(addr)
	if err != nil {
		t.Fatal(err)
	}
	if m.String() == h.String() {
		t.Fatal("mint and hold scripts must differ")
	}
	cd := &CollectionData{CollectionName: "c", Description: "d", Supply: 0}
	tape, err := BuildNFTTapeScript(cd)
	if err != nil {
		t.Fatal(err)
	}
	if tape == nil || !strings.Contains(tape.String(), "4e54617065") {
		t.Fatalf("tape: %s", tape.String())
	}
}

func TestEncodeDecodeNFTData(t *testing.T) {
	h, err := EncodeNFTDataToHex(map[string]string{"a": "b"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeNFTDataFromHex(h)
	if err != nil {
		t.Fatal(err)
	}
	if m["a"] != "b" {
		t.Fatalf("%v", m)
	}
}
