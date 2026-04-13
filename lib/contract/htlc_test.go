package contract

import (
	"testing"

	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

func TestHTLC_LockingScriptAndDeployShape(t *testing.T) {
	sender := "1JdVc3djVYG7GAMYAd1q9jkpp8gVycTDDq"
	receiver := "1JdVc3djVYG7GAMYAd1q9jkpp8gVycTDDq"
	hashlock := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	timelock := 1774427165
	ls, err := p2pkhLock(sender)
	if err != nil {
		t.Fatal(err)
	}
	utxo := &bt.UTXO{
		TxID:          make([]byte, 32),
		Vout:          0,
		Satoshis:      500000,
		LockingScript: ls,
	}
	raw, err2 := DeployHTLC(sender, receiver, hashlock, timelock, 0.001, utxo)
	if err2 != nil {
		t.Fatal(err2)
	}
	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Outputs) < 1 {
		t.Fatal("expected outputs")
	}
	if len(tx.Inputs) != 1 {
		t.Fatalf("inputs: %d", len(tx.Inputs))
	}
}

func TestHTLC_WithdrawRefundNoChangeOutputs(t *testing.T) {
	sa, _ := bscript.NewAddressFromString("1JdVc3djVYG7GAMYAd1q9jkpp8gVycTDDq")
	sb, _ := bscript.NewAddressFromString("1JdVc3djVYG7GAMYAd1q9jkpp8gVycTDDq")
	htlcScript, err := htlcLockingScript(
		sa.PublicKeyHash,
		sb.PublicKeyHash,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		1000,
	)
	if err != nil {
		t.Fatal(err)
	}
	u := &bt.UTXO{
		TxID:          bytes32(1),
		Vout:          0,
		Satoshis:      100000,
		LockingScript: htlcScript,
	}
	recv := "1JdVc3djVYG7GAMYAd1q9jkpp8gVycTDDq"
	wraw, err := Withdraw(recv, u)
	if err != nil {
		t.Fatal(err)
	}
	wtx, _ := bt.NewTxFromString(wraw)
	if len(wtx.Outputs) != 1 {
		t.Fatalf("withdraw outputs want 1 got %d", len(wtx.Outputs))
	}
	if wtx.Outputs[0].Satoshis != 100000-80 {
		t.Fatalf("withdraw amount")
	}
	sender := "1JdVc3djVYG7GAMYAd1q9jkpp8gVycTDDq"
	rraw, err := Refund(sender, u, 2000)
	if err != nil {
		t.Fatal(err)
	}
	rtx, _ := bt.NewTxFromString(rraw)
	if rtx.LockTime != 2000 {
		t.Fatalf("locktime")
	}
	if rtx.Inputs[0].SequenceNumber != 4294967294 {
		t.Fatalf("sequence")
	}
}

func TestUtil_ParseDecimalToBigInt(t *testing.T) {
	n := util.ParseDecimalToBigInt("0.001", 6)
	if n.Int64() != 1000 {
		t.Fatalf("got %s", n.String())
	}
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	out[0] = b
	return out
}

func p2pkhLock(addr string) (*bscript.Script, error) {
	a, err := bscript.NewAddressFromString(addr)
	if err != nil {
		return nil, err
	}
	return bscript.NewP2PKHFromPubKeyHash(hexDecode(a.PublicKeyHash))
}
