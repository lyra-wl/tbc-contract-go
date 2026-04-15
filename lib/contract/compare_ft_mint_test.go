package contract

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

func TestCompareFTMint(t *testing.T) {
	const (
		privKeyWIF = "L1dzpqTvtKKYn2dYkMoBJSPYeBcYUpWNaXMWyME7gmJVCrifLW8x"
		addressTo  = "1Hiw63nWTTgAkjRU5SQyz6ASGKQuyHYaQP"
		txid       = "5e1e8412cf2948d96508c99cc975bb1a31b3d7a7f7d1e222e47ba4edb583a218"
		vout       = 0
		satoshis   = uint64(426000000)
		script     = "76a914b770377041443c7eac4a93b721ab7093bdbccaba88ac"
	)

	sep := func(title string) {
		t.Logf("\n%s\n[Go] %s\n%s", "======================================================================", title, "======================================================================")
	}

	sep("1. 解析私钥与地址")
	w, err := wif.DecodeWIF(privKeyWIF)
	if err != nil {
		t.Fatalf("decode WIF: %v", err)
	}
	privKey := w.PrivKey

	addr, _ := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	t.Logf("address: %s", addr.AddressString)
	t.Logf("pubKeyHash: %s", addr.PublicKeyHash)

	sep("2. 创建 FT 实例")
	ft, err := NewFT(&FtParams{
		Name:    "TestToken",
		Symbol:  "TTK",
		Amount:  1000000,
		Decimal: 8,
	})
	if err != nil {
		t.Fatalf("NewFT: %v", err)
	}
	t.Logf("name: %s symbol: %s decimal: %d totalSupply: %d", ft.Name, ft.Symbol, ft.Decimal, ft.TotalSupply)

	sep("3. 构造 UTXO")
	txidBytes, _ := hex.DecodeString(txid)
	lockScript, _ := bscript.NewFromHexString(script)
	utxo := &bt.UTXO{
		TxID:          txidBytes,
		Vout:          vout,
		Satoshis:      satoshis,
		LockingScript: lockScript,
	}
	t.Logf("utxo: txid=%s vout=%d satoshis=%d script=%s", txid, utxo.Vout, utxo.Satoshis, script)

	sep("4. 执行 MintFT")
	raws, err := ft.MintFT(privKey, addressTo, utxo)
	if err != nil {
		t.Fatalf("MintFT: %v", err)
	}

	sep("5. txSource 分析")
	txSource, err := bt.NewTxFromString(raws[0])
	if err != nil {
		t.Fatalf("parse txSource: %v", err)
	}
	t.Logf("txSource.txid: %s", txSource.TxID())
	t.Logf("txSource.version: %d", txSource.Version)
	t.Logf("txSource.inputs.length: %d", len(txSource.Inputs))
	t.Logf("txSource.outputs.length: %d", len(txSource.Outputs))
	for i, o := range txSource.Outputs {
		sh := hex.EncodeToString(o.LockingScript.Bytes())
		prefix := sh
		if len(prefix) > 80 {
			prefix = prefix[:80]
		}
		t.Logf("  output[%d]: satoshis=%d, script_len=%d, script_hex=%s...", i, o.Satoshis, o.LockingScript.Len(), prefix)
	}
	t.Logf("txSource.raw_hex_len: %d", len(raws[0]))
	t.Logf("txSource.raw_byte_len: %d", len(raws[0])/2)
	goSourceFee := int(satoshis - txSource.TotalOutputSatoshis())
	t.Logf("txSource.fee: %d", goSourceFee)

	sep("6. txMint 分析")
	txMint, err := bt.NewTxFromString(raws[1])
	if err != nil {
		t.Fatalf("parse txMint: %v", err)
	}
	t.Logf("txMint.txid: %s", txMint.TxID())
	t.Logf("txMint.version: %d", txMint.Version)
	t.Logf("txMint.inputs.length: %d", len(txMint.Inputs))
	t.Logf("txMint.outputs.length: %d", len(txMint.Outputs))
	var mintOutputTotal uint64
	for i, o := range txMint.Outputs {
		mintOutputTotal += o.Satoshis
		sh := hex.EncodeToString(o.LockingScript.Bytes())
		prefix := sh
		if len(prefix) > 80 {
			prefix = prefix[:80]
		}
		t.Logf("  output[%d]: satoshis=%d, script_len=%d, script_hex=%s...", i, o.Satoshis, o.LockingScript.Len(), prefix)
	}
	t.Logf("txMint.fee: %d", 9900-mintOutputTotal)
	t.Logf("txMint.raw_hex_len: %d", len(raws[1]))
	t.Logf("txMint.raw_byte_len: %d", len(raws[1])/2)

	sep("7. codeScript / tapeScript")
	t.Logf("ft.codeScript hex_len: %d", len(ft.CodeScript))
	first80 := ft.CodeScript
	if len(first80) > 80 {
		first80 = first80[:80]
	}
	last80 := ft.CodeScript
	if len(last80) > 80 {
		last80 = last80[len(last80)-80:]
	}
	t.Logf("ft.codeScript first80: %s", first80)
	t.Logf("ft.codeScript last80: %s", last80)
	t.Logf("ft.tapeScript hex_len: %d", len(ft.TapeScript))
	t.Logf("ft.tapeScript: %s", ft.TapeScript)

	sep("8. RAW HEX 输出")
	t.Logf("GO_SOURCE_RAW=%s", raws[0])
	t.Logf("GO_MINT_RAW=%s", raws[1])

	sep("9. 与 JS 对比")
	jsSourceTxid := "0e1eddd1d9d5ec9f1a075eb621a73189f8effb87f342bd3a9acf6e05d849cf82"
	jsMintTxid := "c003208e73efa0b6af8d98a06ffeb5923a535b7ff223a5888068754c8f2589cd"
	jsSourceFee := 80
	jsMintFee := 174
	jsCodeScriptLen := 3768
	jsTapeScriptLen := 146
	jsSourceRawLen := 640
	jsMintRawLen := 4336

	goSourceTxid := txSource.TxID()
	goMintTxid := txMint.TxID()
	goMintFee := int(9900 - mintOutputTotal)

	compare := func(label, jsVal, goVal string) {
		match := "MATCH"
		if jsVal != goVal {
			match = "DIFF !!!"
		}
		t.Logf("  %-25s JS=%-20s Go=%-20s %s", label, jsVal, goVal, match)
	}

	compare("source.txid", jsSourceTxid, goSourceTxid)
	compare("source.fee", fmt.Sprintf("%d", jsSourceFee), fmt.Sprintf("%d", goSourceFee))
	compare("source.raw_hex_len", fmt.Sprintf("%d", jsSourceRawLen), fmt.Sprintf("%d", len(raws[0])))
	compare("mint.txid", jsMintTxid, goMintTxid)
	compare("mint.fee", fmt.Sprintf("%d", jsMintFee), fmt.Sprintf("%d", goMintFee))
	compare("mint.raw_hex_len", fmt.Sprintf("%d", jsMintRawLen), fmt.Sprintf("%d", len(raws[1])))
	compare("codeScript.hex_len", fmt.Sprintf("%d", jsCodeScriptLen), fmt.Sprintf("%d", len(ft.CodeScript)))
	compare("tapeScript.hex_len", fmt.Sprintf("%d", jsTapeScriptLen), fmt.Sprintf("%d", len(ft.TapeScript)))

	jsCodeFirst80 := "59796b51798255947f05465461706588537f587f587f587f587f587f587f7581760087646c765501"
	jsCodeLast80 := "ffffffffffffffffffff756a15b770377041443c7eac4a93b721ab7093bdbccaba000532436f6465"
	jsTape := "006a3000407a10f35a00000000000000000000000000000000000000000000000000000000000000000000000000000000000001080954657374546f6b656e0354544b054654617065"

	compare("codeScript.first80", jsCodeFirst80, first80)
	compare("codeScript.last80", jsCodeLast80, last80)
	compare("tapeScript", jsTape, ft.TapeScript)

	if txSource.TxID() != jsSourceTxid {
		t.Logf("\n=== SOURCE RAW DIFF ===")
		jsRaw := "0a0000000118a283b5eda47be422e2d1f7a7d7b3311abb75c99cc90865d94829cf12841e5e000000006a473044022074fb36245950b20a6ef2fa5db105df36afafe795a5d74eef1208f87df554b7e4022038a4e9e5b5b66765d26fc60d4079269839aba42dba2c538f7920a80d0a71c689412102da5f120a4328469bc41f5dd5e45d16212ab84640c1ab2a2daab649db84b97646ffffffff03ac260000000000002676a914b770377041443c7eac4a93b721ab7093bdbccaba88ac6a0b666f72206674206d696e74000000000000000049006a3000407a10f35a00000000000000000000000000000000000000000000000000000000000000000000000000000000000001080954657374546f6b656e0354544b05465461706584176419000000001976a914b770377041443c7eac4a93b721ab7093bdbccaba88ac00000000"
		goRaw := raws[0]
		minLen := len(jsRaw)
		if len(goRaw) < minLen {
			minLen = len(goRaw)
		}
		firstDiff := -1
		for i := 0; i < minLen; i++ {
			if jsRaw[i] != goRaw[i] {
				firstDiff = i
				break
			}
		}
		if firstDiff >= 0 {
			start := firstDiff - 20
			if start < 0 {
				start = 0
			}
			end := firstDiff + 40
			if end > minLen {
				end = minLen
			}
			t.Logf("first diff at hex pos %d (byte %d)", firstDiff, firstDiff/2)
			t.Logf("JS  [%d:%d]: %s", start, end, jsRaw[start:end])
			t.Logf("Go  [%d:%d]: %s", start, end, goRaw[start:end])
		} else if len(jsRaw) != len(goRaw) {
			t.Logf("raw lengths differ: JS=%d Go=%d", len(jsRaw), len(goRaw))
		}
	}
}

