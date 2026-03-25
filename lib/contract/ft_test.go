// 基于 docs/ft.md 的 FT 合约单元测试
// 对应 ft.md 中 tbc-lib-js 的调用，此处使用 tbc-lib-go (go-bt) 基础库
package contract

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

// TestNewFT_FromTxid 测试从 txid 创建 FT
func TestNewFT_FromTxid(t *testing.T) {
	txid := "abc123def4567890123456789012345678901234567890123456789012345678"
	ft, err := NewFT(txid)
	if err != nil {
		t.Fatalf("NewFT(txid) failed: %v", err)
	}
	if ft.ContractTxid != txid {
		t.Errorf("ContractTxid = %q, want %q", ft.ContractTxid, txid)
	}
	if ft.Name != "" || ft.Symbol != "" {
		t.Errorf("from txid should have empty name/symbol")
	}
}

// TestNewFT_FromParams 测试从参数创建 FT（对应 ft.md Mint 中的 newToken 参数）
func TestNewFT_FromParams(t *testing.T) {
	params := &FtParams{
		Name:    "test",
		Symbol:  "test",
		Amount:  100000000,
		Decimal: 6,
	}
	ft, err := NewFT(params)
	if err != nil {
		t.Fatalf("NewFT(params) failed: %v", err)
	}
	if ft.Name != params.Name || ft.Symbol != params.Symbol {
		t.Errorf("Name/Symbol mismatch")
	}
	if ft.Decimal != params.Decimal || ft.TotalSupply != params.Amount {
		t.Errorf("Decimal/TotalSupply mismatch")
	}
}

// TestNewFT_InvalidParams 测试非法参数
func TestNewFT_InvalidParams(t *testing.T) {
	tests := []struct {
		name string
		args interface{}
		want string
	}{
		{"nil", nil, "invalid constructor"},
		{"amount zero", &FtParams{Name: "t", Symbol: "t", Amount: 0, Decimal: 6}, "amount must be"},
		{"decimal 0", &FtParams{Name: "t", Symbol: "t", Amount: 1000, Decimal: 0}, "decimal must"},
		{"decimal 19", &FtParams{Name: "t", Symbol: "t", Amount: 1000, Decimal: 19}, "decimal must"},
		{"amount exceeds max", &FtParams{Name: "t", Symbol: "t", Amount: 1e15, Decimal: 6}, "max amount"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewFT(tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if err != nil && tt.want != "" && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.want)
			}
		})
	}
}

// TestInitialize 测试 Initialize（对应 ft.md Transfer 中的 Token.initialize(TokenInfo)）
// TokenInfo 来自 API.fetchFtInfo，对应 bt.FetchFtInfo 返回的 *bt.FtInfo
func TestInitialize(t *testing.T) {
	ft, _ := NewFT("txid123")
	// 模拟 bt.FetchFtInfo 返回的 FtInfo 结构
	apiInfo := &bt.FtInfo{
		Name:        "MyToken",
		Symbol:      "MTK",
		Decimal:     8,
		TotalSupply: "1000000",
		CodeScript:  "00",
		TapeScript:  "00",
	}
	// 转换为 contract.FtInfo 并初始化
	info := &FtInfo{
		Name:        apiInfo.Name,
		Symbol:      apiInfo.Symbol,
		Decimal:     int(apiInfo.Decimal),
		TotalSupply: 1000000,
		CodeScript:  apiInfo.CodeScript,
		TapeScript:  apiInfo.TapeScript,
	}
	ft.Initialize(info)
	if ft.Name != info.Name || ft.Symbol != info.Symbol {
		t.Errorf("Initialize name/symbol mismatch")
	}
	if ft.Decimal != info.Decimal || ft.TotalSupply != info.TotalSupply {
		t.Errorf("Initialize decimal/totalSupply mismatch")
	}
	if ft.CodeScript != info.CodeScript || ft.TapeScript != info.TapeScript {
		t.Errorf("Initialize scripts mismatch")
	}
}

// TestBuildTapeAmount 测试 BuildTapeAmount（对应 ft.md 中的 amount/change 计算）
func TestBuildTapeAmount(t *testing.T) {
	tests := []struct {
		name         string
		amount       *big.Int
		tapeAmountSet []*big.Int
	}{
		{
			"exact match",
			big.NewInt(1000),
			[]*big.Int{big.NewInt(1000)},
		},
		{
			"partial from first slot",
			big.NewInt(300),
			[]*big.Int{big.NewInt(500), big.NewInt(200)},
		},
		{
			"multiple slots",
			big.NewInt(1500),
			[]*big.Int{big.NewInt(1000), big.NewInt(1000)},
		},
		{
			"zero amount",
			big.NewInt(0),
			[]*big.Int{big.NewInt(100)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			amountHex, changeHex := BuildTapeAmount(tt.amount, tt.tapeAmountSet)
			if len(amountHex) != 96 {
				t.Errorf("amountHex length = %d, want 96 (48 bytes hex)", len(amountHex))
			}
			if len(changeHex) != 96 {
				t.Errorf("changeHex length = %d, want 96", len(changeHex))
			}
		})
	}
}

// TestBuildFTtransferCode 测试 BuildFTtransferCode（对应 ft.md 中的 FT.buildFTtransferCode）
// ft.md: ftutxo_codeScript = FT.buildFTtransferCode(Token.codeScript, addressA).toBuffer().toString('hex')
func TestBuildFTtransferCode(t *testing.T) {
	// 构造至少 1558 字节的 code hex（用于替换 hash 位置 1537:1558）
	codeBuf := make([]byte, 1560)
	for i := range codeBuf {
		codeBuf[i] = byte(i % 256)
	}
	codeHex := hex.EncodeToString(codeBuf)

	// 测试地址模式 - 使用 bscript 验证地址（对应 tbc.Address）
	addr := "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq"
	ok, _ := bscript.ValidateAddress(addr)
	if !ok {
		t.Skip("address validation failed, skip")
	}
	script := BuildFTtransferCode(codeHex, addr)
	if script == nil {
		t.Fatal("BuildFTtransferCode returned nil")
	}
	if script.Len() != len(codeBuf) {
		t.Errorf("script length = %d, want %d", script.Len(), len(codeBuf))
	}
	// 对应 ft.md 中 .toBuffer().toString('hex') 用于 fetchFtUTXOs 的 codeScript 参数
	_ = script.String()

	// 测试 40 位 hash 模式（对应 combine script 的 hash 格式）
	hash40 := "e2a623699e81b291c0327f408fea765d534baa2a"
	script2 := BuildFTtransferCode(codeHex, hash40)
	if script2 == nil {
		t.Fatal("BuildFTtransferCode(hash) returned nil")
	}
}

// TestBuildFTtransferTape 测试 BuildFTtransferTape（对应 ft.md 中的 tape 构建）
// 使用 bscript.Script 验证输出
func TestBuildFTtransferTape(t *testing.T) {
	// tape 格式: 3 字节 (OP_FALSE OP_RETURN push) + 48 字节 amount
	// 最小有效 tape: 51 字节 = 102 hex 字符
	amountBytes := make([]byte, 48)
	amountBytes[0] = 0xe8
	amountBytes[1] = 0x03 // 1000 in LE
	tapeBytes := append([]byte{0x00, 0x6a, 0x30}, amountBytes...) // 00 6a 30 + 48 bytes
	tapeHex := hex.EncodeToString(tapeBytes)

	newAmountBytes := make([]byte, 48)
	newAmountBytes[0] = 0xf4
	newAmountBytes[1] = 0x01 // 500 in LE
	newAmountHex := hex.EncodeToString(newAmountBytes)

	script := BuildFTtransferTape(tapeHex, newAmountHex)
	if script == nil {
		t.Fatal("BuildFTtransferTape returned nil")
	}
	if script.Len() != 51 {
		t.Errorf("script length = %d, want 51", script.Len())
	}
	// 验证为有效的 bscript.Script
	if script.Bytes() == nil {
		t.Error("script.Bytes() should not be nil")
	}
}

// TestGetBalanceFromTape 测试 GetBalanceFromTape
// 对应 util.getFtBalanceFromTape / buildUTXO 中从 tape 解析 ftBalance
func TestGetBalanceFromTape(t *testing.T) {
	// 构造 tape: 3 字节 + 48 字节 amount，第一个 slot 放 1000 (0xe803000000000000 LE)
	amountBytes := make([]byte, 48)
	amountBytes[0] = 0xe8
	amountBytes[1] = 0x03
	tapeBytes := append([]byte{0x00, 0x6a, 0x30}, amountBytes...)
	tapeHex := hex.EncodeToString(tapeBytes)

	balance := GetBalanceFromTape(tapeHex)
	if balance == nil {
		t.Fatal("GetBalanceFromTape returned nil")
	}
	if balance.Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("balance = %s, want 1000", balance.String())
	}

	// 空/短 tape 返回 0
	b0 := GetBalanceFromTape("")
	if b0.Cmp(big.NewInt(0)) != 0 {
		t.Errorf("empty tape balance = %s, want 0", b0.String())
	}
}

// TestFtUTXO_WithBuildFTtransferCode 测试 bt.FtUTXO 与 BuildFTtransferCode 配合使用
// 对应 ft.md: ftutxo_codeScript = FT.buildFTtransferCode(Token.codeScript, addressA)
// 以及 API.fetchFtUTXOs(contractTxID, addressA, ftutxo_codeScript, network, amountBN)
func TestFtUTXO_WithBuildFTtransferCode(t *testing.T) {
	codeHex := hex.EncodeToString(make([]byte, 1560))
	addr := "1FhSD1YezTXbdRGWzNbNvUj6qeKQ6gZDMq"
	codeScript := BuildFTtransferCode(codeHex, addr)
	codeScriptHex := codeScript.String()

	// 模拟 bt.FetchFtUTXOs 返回的 FtUTXO
	ftutxo := &bt.FtUTXO{
		TxID:      "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234",
		Vout:      0,
		Script:    codeScriptHex,
		Satoshis:  500,
		FtBalance: "1000000",
	}
	// util.FtUTXOToUTXO 将 FtUTXO 转为 *bt.UTXO 供 tx.FromUTXOs 使用
	_, err := util.FtUTXOToUTXO(ftutxo)
	if err != nil {
		t.Fatalf("FtUTXOToUTXO: %v", err)
	}
}

// TestBuildUTXO_FromTx 测试 util.BuildUTXO（对应 ft.md 中 buildUTXO(tx, vout, true/false)）
func TestBuildUTXO_FromTx(t *testing.T) {
	tx := bt.NewTx()
	script, _ := bscript.NewFromHexString("76a914e2a623699e81b291c0327f408fea765d534baa2a88ac")
	tx.AddOutput(&bt.Output{Satoshis: 500, LockingScript: script})
	tapeScript := bscript.NewFromBytes(append([]byte{0x00, 0x6a, 0x30}, make([]byte, 48)...))
	tx.AddOutput(&bt.Output{Satoshis: 0, LockingScript: tapeScript})

	// buildUTXO(tx, 0, true) - 构建 FT UTXO
	ftu, err := util.BuildUTXO(tx, 0, true)
	if err != nil {
		t.Fatalf("BuildUTXO: %v", err)
	}
	if ftu.FtBalance == "" {
		t.Error("FT UTXO should have FtBalance")
	}

	// buildUTXO(tx, 0, false) - 构建普通 UTXO
	u, err := util.BuildUTXO(tx, 0, false)
	if err != nil {
		t.Fatalf("BuildUTXO: %v", err)
	}
	if u.FtBalance != "0" {
		t.Errorf("non-FT FtBalance = %q, want \"0\"", u.FtBalance)
	}
}
