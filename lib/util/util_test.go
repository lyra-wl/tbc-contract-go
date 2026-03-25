// 基于 docs/ft.md 的 util 单元测试
package util

import (
	"encoding/hex"
	"testing"

	"github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

// TestGetFtBalanceFromTape 测试从 tape 提取余额（对应 ft.md 中 buildUTXO 等使用的 getFtBalanceFromTape）
func TestGetFtBalanceFromTape(t *testing.T) {
	// 48 字节 amount: 第一个 slot 1000 (LE)
	amountBytes := make([]byte, 48)
	amountBytes[0] = 0xe8
	amountBytes[1] = 0x03
	tapeBytes := append([]byte{0x00, 0x6a, 0x30}, amountBytes...)
	tapeHex := hex.EncodeToString(tapeBytes)

	balance := GetFtBalanceFromTape(tapeHex)
	if balance != "1000" {
		t.Errorf("GetFtBalanceFromTape = %q, want \"1000\"", balance)
	}

	// 无效输入
	if GetFtBalanceFromTape("") != "0" {
		t.Error("empty tape should return 0")
	}
	if GetFtBalanceFromTape("ff") != "0" {
		t.Error("short tape should return 0")
	}
}

// TestFtUTXOToUTXO 测试 FtUTXO 转 UTXO
func TestFtUTXOToUTXO(t *testing.T) {
	txid := "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"
	scriptHex := "76a914e2a623699e81b291c0327f408fea765d534baa2a88ac"
	fu := &bt.FtUTXO{
		TxID:      txid,
		Vout:      0,
		Script:    scriptHex,
		Satoshis:  500,
		FtBalance: "1000",
	}
	u, err := FtUTXOToUTXO(fu)
	if err != nil {
		t.Fatalf("FtUTXOToUTXO: %v", err)
	}
	if u.Vout != 0 || u.Satoshis != 500 {
		t.Errorf("Vout/Satoshis mismatch")
	}
}

// TestFtUTXOsToUTXOs 测试批量转换
func TestFtUTXOsToUTXOs(t *testing.T) {
	scriptHex := "76a914e2a623699e81b291c0327f408fea765d534baa2a88ac"
	list := []*bt.FtUTXO{
		{TxID: "aa", Vout: 0, Script: scriptHex, Satoshis: 500, FtBalance: "100"},
		{TxID: "bb", Vout: 1, Script: scriptHex, Satoshis: 500, FtBalance: "200"},
	}
	result, err := FtUTXOsToUTXOs(list)
	if err != nil {
		t.Fatalf("FtUTXOsToUTXOs: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("len = %d, want 2", len(result))
	}
}

// TestSelectTXFromLocal 测试从本地交易列表中查找
func TestSelectTXFromLocal(t *testing.T) {
	tx := bt.NewTx()
	tx.AddOutput(&bt.Output{Satoshis: 1000, LockingScript: bscript.NewFromBytes([]byte{0x00})})
	txid := tx.TxID()

	txs := []*bt.Tx{bt.NewTx(), tx, bt.NewTx()}
	found := SelectTXFromLocal(txs, txid)
	if found != tx {
		t.Error("SelectTXFromLocal should find the tx")
	}
}

// TestBuildUTXO 测试从交易构建 UTXO
func TestBuildUTXO(t *testing.T) {
	tx := bt.NewTx()
	script, _ := bscript.NewFromHexString("76a914e2a623699e81b291c0327f408fea765d534baa2a88ac")
	tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: script})
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: bscript.NewFromBytes([]byte{0x00, 0x6a, 0x30})})

	// 普通 UTXO (isFT=false)
	u, err := BuildUTXO(tx, 0, false)
	if err != nil {
		t.Fatalf("BuildUTXO: %v", err)
	}
	if u.FtBalance != "0" {
		t.Errorf("non-FT FtBalance = %q, want \"0\"", u.FtBalance)
	}
	if u.Satoshis != 500 {
		t.Errorf("Satoshis = %d, want 500", u.Satoshis)
	}

	// 越界
	_, err = BuildUTXO(tx, 10, false)
	if err != ErrOutputNotExist {
		t.Errorf("expected ErrOutputNotExist, got %v", err)
	}
}

// TestBuildFtPrePreTxData_Errors 测试 BuildFtPrePreTxData 错误情况
func TestBuildFtPrePreTxData_Errors(t *testing.T) {
	tx := bt.NewTx()
	tx.AddOutput(&bt.Output{})
	_, err := BuildFtPrePreTxData(tx, 0, nil)
	if err != ErrOutputNotExist {
		t.Errorf("vout+1 out of range: got %v", err)
	}

	tx2 := bt.NewTx()
	tx2.AddOutput(&bt.Output{})
	tx2.AddOutput(&bt.Output{LockingScript: bscript.NewFromBytes(make([]byte, 30))})
	_, err = BuildFtPrePreTxData(tx2, 0, nil)
	if err != ErrTapeTooShort {
		t.Errorf("tape too short: got %v", err)
	}
}
