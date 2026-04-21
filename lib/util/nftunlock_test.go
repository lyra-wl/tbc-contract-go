package util

import (
	"encoding/hex"
	"testing"

	"github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

func TestGetNFTCurrentVsV0(t *testing.T) {
	tx := bt.NewTx()
	tx.Version = 10
	script, _ := bscript.NewFromHexString("76a914" + "11" + "88ac")
	tx.AddOutput(&bt.Output{Satoshis: 100, LockingScript: script})
	tx.AddOutput(&bt.Output{Satoshis: 200, LockingScript: script})

	cur, err := GetNFTCurrentTxdata(tx)
	if err != nil {
		t.Fatal(err)
	}
	curV0, err := GetNFTCurrentTxdataV0(tx)
	if err != nil {
		t.Fatal(err)
	}
	if cur == curV0 {
		t.Fatal("current and v0 txdata should differ")
	}
	if _, err := hex.DecodeString(cur); err != nil {
		t.Fatal(err)
	}
}

func TestNftAppendOutputsDataThroughCurrent(t *testing.T) {
	tx := bt.NewTx()
	tx.AddOutput(&bt.Output{Satoshis: 1, LockingScript: bscript.NewFromBytes([]byte{0x01})})
	h, err := GetNFTCurrentTxdata(tx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hex.DecodeString(h); err != nil {
		t.Fatal(err)
	}
}

func TestNftEncodeMinimalPushData(t *testing.T) {
	// SCRIPT_VERIFY_MINIMALDATA: 单字节 0x01..0x10 须为 OP_1..OP_16，不得为 0x01 <byte>
	if got := hex.EncodeToString(NftEncodeMinimalPushData([]byte{0x05})); got != "55" {
		t.Fatalf("push single 0x05: got %s want 55 (OP_5)", got)
	}
	if got := hex.EncodeToString(NftEncodeMinimalPushData([]byte{0x42})); got != "0142" {
		t.Fatalf("push single non-special byte: got %s want 0142", got)
	}
	if got := hex.EncodeToString(NftEncodeMinimalPushData([]byte{})); got != "00" {
		t.Fatalf("empty push: got %s want 00", got)
	}
}
