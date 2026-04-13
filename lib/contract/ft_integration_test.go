//go:build integration
// +build integration

// 基于 docs/ft.md 的真实链上集成测试（含广播）。
// 文档-测试总览见同级 *integration_test.go 文件头注释，并与 ../tbc-contract/docs/runners/chain/README.md「文档功能覆盖对照」表对齐。
//
// 运行（在 tbc-contract-go 目录下；勿使用字面量 /path/to/...，请换成本机实际路径）：
//
//	cd ~/path/to/tbc-contract-go
//	export RUN_REAL_FT_TEST=1
//	export TBC_PRIVATE_KEY=你的WIF
//	export TBC_NETWORK=testnet
//	export FT_TRANSFER_TO=接收地址
//	export FT_TRANSFER_AMOUNT=1000
//	# 可选：export FT_CONTRACT_TXID=已有铸币交易txid（勿在 shell 里写尖括号，否则 zsh 会把 < 当成重定向触发 parse error）
//	# 可选：FT_SKIP_NODE_PARITY=1 …
//	# 可选：FT_SKIP_NODE_INTERPRETER_VERIFY=1 跳过与 Node（tbc-lib-js Interpreter.verify）的脚本结果对照
//	# 可选：FT_NODE_INTERPRETER_FLAGS=minimal|default …（默认 minimal，对齐 Go WithForkID+WithAfterGenesis 所需 opcode）
//	# 可选：FT_SKIP_LOCAL_SCRIPT_VERIFY=1 …
//	# 可选：FT_CONTINUE_BROADCAST_AFTER_JOINT_EQUALVERIFY=1 或 =0 覆盖下方 testnet 默认。当 Go 与 Node 解释器均在同一输入上报 OP_EQUALVERIFY（且 txdata/prepre 已与 Node 对照一致）时，不 Fatalf，仍尝试 api.BroadcastTXRaw（见 tbc-contract/docs/runners/chain/FT_COMPARE.md）。未设置且 TBC_NETWORK 为 testnet 时默认开启；mainnet 等需显式 =1。
//	# 排查 OP_EQUALVERIFY（见下）：FT_DEBUG_TXDATA=1 打印 raw/对照 GetCurrentTxdata·GetPreTxdata·prepre；FT_DEBUG_TXDATA_FULL_RAW=1 打印完整 raw hex
//	# 可选：FT_SKIP_NODE_TXDATA_PARITY=1 跳过与 Node（tbc-contract/lib/util/ftunlock.js）的 txdata 逐字节对照，以及 API.fetchFtPrePreTxData 与 Go FetchFtPrePreTxData 的对照
//	go test -tags=integration -v ./lib/contract -run TestFT_Integration_Transfer_Broadcast -count=1
//
// 与 tbc-contract 相同参数下比对 JS/Go Transfer raw 与各输入解锁脚本：见 ../tbc-contract/scripts/ft-transfer-js-go-compare-broadcast.js 与本仓库 go run ./cmd/ft_transfer_compare
//
// 仅编译集成测试（不跑链上）：
//
//	go test -tags=integration -c -o /dev/null ./lib/contract
//
// 必填环境变量：
// - TBC_PRIVATE_KEY       WIF 私钥（用于签名与支付手续费）
// - TBC_NETWORK           testnet / mainnet / 自定义 API 根 URL
//
// Mint 额外可选（不填使用默认值）：
// - FT_MINT_NAME          默认 "test"
// - FT_MINT_SYMBOL        默认 "test"
// - FT_MINT_DECIMAL       默认 6
// - FT_MINT_AMOUNT        默认 100000000
// - FT_FEE_SAT_PER_KB     每 1000 字节手续费（sat），默认与 tbc-contract（JS feePerKb(80)）一致为 80；若链上报 66 insufficient priority 可显式设为更大值（如 500）
//
// Transfer 额外必填：
// - FT_TRANSFER_TO        Transfer 接收地址
// - FT_TRANSFER_AMOUNT    转账数量（如 1000）
//
// 合约 txid 输入格式：
// - FT_CONTRACT_TXID 可选；若不提供，将自动执行 Mint 并使用生成的 mintTxid 作为后续交易账户。
package contract

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/libsv/go-bk/bec"
	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/bscript/interpreter"
	"github.com/sCrypt-Inc/go-bt/v2/bscript/interpreter/errs"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

const (
	// 集成测试默认参数：勿在仓库中提交真实 WIF；通过环境变量注入 TBC_PRIVATE_KEY。
	// 若同名环境变量存在，环境变量优先。
	defaultRunRealFTTest  = false
	defaultNetwork        = "testnet"
	defaultPrivateKeyWIF  = ""
	defaultTransferTo     = "1JdVc3djVYG7GAMYAd1q9jkpp8gVycTDDq"
	defaultTransferAmount = "1000"
)

var cachedMintContractTxid string
var cachedMintFailed bool
var cachedMintFailReason string
var lastMintSourceRaw string
var lastMintLocalRaw string
var lastMintRebuildRaw string

func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		t.Skipf("缺少环境变量 %s，跳过真实链上测试", key)
	}
	return v
}

func envOrConst(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v != "" {
		return v
	}
	return strings.TrimSpace(def)
}

func mustEnvOrConst(t *testing.T, key, def string) string {
	t.Helper()
	v := envOrConst(key, def)
	if v == "" {
		t.Fatalf("缺少参数 %s，请在环境变量中设置，或在测试文件常量中配置默认值", key)
	}
	return v
}

func envOrDefault(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func parsePositiveInt64(t *testing.T, key string, def int64) int64 {
	t.Helper()
	raw := envOrDefault(key, strconv.FormatInt(def, 10))
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		t.Fatalf("%s 必须是正整数，当前=%q", key, raw)
	}
	return v
}

func parseDecimalRange(t *testing.T, key string, def int) int {
	t.Helper()
	raw := envOrDefault(key, strconv.Itoa(def))
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > 18 {
		t.Fatalf("%s 必须在 [1,18]，当前=%q", key, raw)
	}
	return v
}

func loadPrivKey(t *testing.T) *bec.PrivateKey {
	t.Helper()
	wifStr := strings.TrimSpace(os.Getenv("TBC_PRIVATE_KEY"))
	if wifStr == "" {
		wifStr = strings.TrimSpace(defaultPrivateKeyWIF)
	}
	if wifStr == "" {
		t.Skip("设置 TBC_PRIVATE_KEY（WIF）以运行链上集成测试；勿将真实私钥提交仓库")
	}
	decoded, err := wif.DecodeWIF(wifStr)
	if err != nil {
		t.Fatalf("解析 TBC_PRIVATE_KEY 失败: %v", err)
	}
	return decoded.PrivKey
}

func requireRealRun(t *testing.T) {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("RUN_REAL_FT_TEST"))
	if raw == "" && defaultRunRealFTTest {
		return
	}
	if raw != "1" {
		t.Skip("默认跳过真实链上测试，设置 RUN_REAL_FT_TEST=1 启用")
	}
}

func loadTokenForIntegration(t *testing.T, network, contractTxid string) *FT {
	t.Helper()
	token, err := NewFT(contractTxid)
	if err != nil {
		t.Fatalf("NewFT(contractTxid): %v", err)
	}
	info, err := api.FetchFtInfo(contractTxid, network)
	if err != nil {
		t.Fatalf("FetchFtInfo: %v", err)
	}
	totalSupply, ok := new(big.Int).SetString(info.TotalSupply, 10)
	if !ok {
		t.Fatalf("非法 TotalSupply: %s", info.TotalSupply)
	}
	token.Initialize(&FtInfo{
		Name:        info.Name,
		Symbol:      info.Symbol,
		Decimal:     int(info.Decimal),
		TotalSupply: totalSupply.Int64(),
		CodeScript:  info.CodeScript,
		TapeScript:  info.TapeScript,
	})
	return token
}

// logFtTokenScriptsForDebug 仅记录 code/tape 解码后字节长度（详细调试请用单步或本地脚本）。
func logFtTokenScriptsForDebug(t *testing.T, contractTxid string, codeHexRaw, tapeHexRaw string) {
	t.Helper()
	codeHex := strings.TrimSpace(codeHexRaw)
	tapeHex := strings.TrimSpace(tapeHexRaw)
	codeBytes, errC := hex.DecodeString(codeHex)
	tapeBytes, errT := hex.DecodeString(tapeHex)
	nc, nt := 0, 0
	if errC == nil {
		nc = len(codeBytes)
	}
	if errT == nil {
		nt = len(tapeBytes)
	}
	t.Logf("FT contract_txid=%s code_bytes=%d tape_bytes=%d decode_err=(code:%v tape:%v)",
		contractTxid, nc, nt, errC, errT)
}

// logTransferTxDebugOnBroadcastFailure 广播失败时记录错误与交易摘要；完整 raw 可用节点/钱包另查。
// 若为 OP_EQUALVERIFY：在同一合约与参数下用 tbc-contract（JS）FT.transfer 组装的 raw 长度与失败模式一致时，
// 属链上脚本/节点验证语义问题，而非 Go Transfer 与 JS 的逐字节实现偏差（可用本地脚本对照 raw 长度）。
// verifyTxAllInputsLocalInterpreter 在广播前用 tbc-lib-go 解释器验证每个输入的脚本（与节点 SCRIPT_ERR_* 同源逻辑）。
// testnet 上若 Go 与 tbc-lib-js 均在 OP_EQUALVERIFY 失败且 txdata 已与 Node 对齐，默认不中断、仍尝试广播（continueBroadcastAfterJointEqualVerify）。
// 设置 FT_SKIP_LOCAL_SCRIPT_VERIFY=1 可整段跳过本地验证；FT_CONTINUE_BROADCAST_AFTER_JOINT_EQUALVERIFY=0 可在 testnet 强制遇联合 EQUALVERIFY 仍 Fatalf。
func verifyTxAllInputsLocalInterpreter(t *testing.T, network, txraw string) {
	t.Helper()
	if boolEnv("FT_SKIP_LOCAL_SCRIPT_VERIFY") {
		t.Log("已设置 FT_SKIP_LOCAL_SCRIPT_VERIFY=1，跳过本地解释器验证")
		return
	}
	tx, err := bt.NewTxFromString(txraw)
	if err != nil {
		t.Fatalf("解析 txraw 失败: %v", err)
	}
	n := tx.InputCount()
	skippedJointEqualVerify := 0
	for i := 0; i < n; i++ {
		in := tx.InputIdx(i)
		prevTxid := in.PreviousTxIDStr()
		vout := int(in.PreviousTxOutIndex)
		prevTx, err := api.FetchTXRaw(prevTxid, network)
		if err != nil {
			t.Fatalf("本地验证 FetchTXRaw(%s) input[%d]: %v", prevTxid, i, err)
		}
		if vout < 0 || vout >= len(prevTx.Outputs) {
			t.Fatalf("本地验证 input[%d] vout=%d 越界，父交易 outputs=%d", i, vout, len(prevTx.Outputs))
		}
		prevOut := prevTx.Outputs[vout]
		prevHex := hex.EncodeToString(prevTx.Bytes())
		err = interpreter.NewEngine().Execute(
			interpreter.WithTx(tx, i, prevOut),
			interpreter.WithForkID(),
			interpreter.WithAfterGenesis(),
		)
		nodeOk, nodeErrstr, nodeRan, nodeRunErr := compareGoVsNodeInterpreterVerify(t, txraw, prevHex, i)
		if err != nil {
			if nodeRan && nodeRunErr == nil {
				if nodeOk {
					t.Logf("对照: Go 失败但 Node Interpreter 通过 input[%d]（标志集或实现路径可能不一致）node errstr=%q",
						i, nodeErrstr)
				} else {
					t.Logf("对照: Go 与 Node Interpreter 均未通过 input[%d] node errstr=%q", i, nodeErrstr)
				}
			}
			if errs.IsErrorCode(err, errs.ErrEqualVerify) {
				t.Logf("本地验证 input[%d] 失败: OP_EQUALVERIFY（与节点 SCRIPT_ERR_EQUALVERIFY 同类） prev=%s:%d err=%v",
					i, prevTxid, vout, err)
			} else {
				t.Logf("本地验证 input[%d] 失败: prev=%s:%d err=%v", i, prevTxid, vout, err)
			}
			logInterpreterFailureContext(t, txraw, i, prevTxid, vout, err)
			jointEqualVerify := nodeRan && nodeRunErr == nil && !nodeOk &&
				strings.Contains(strings.ToUpper(nodeErrstr), "EQUALVERIFY")
			if continueBroadcastAfterJointEqualVerify(network) &&
				errs.IsErrorCode(err, errs.ErrEqualVerify) && jointEqualVerify {
				skippedJointEqualVerify++
				t.Logf("联合 OP_EQUALVERIFY：input[%d] Go/Node 均失败，按策略跳过本地致命失败并继续验证其余输入/尝试广播（testnet 默认开启，或 FT_CONTINUE_BROADCAST_AFTER_JOINT_EQUALVERIFY=1；=0 关闭）（见 FT_COMPARE.md）", i)
				continue
			}
			t.Fatalf("广播前本地脚本验证失败 input[%d] (prev %s:%d): %v", i, prevTxid, vout, err)
		}
		if nodeRan && nodeRunErr == nil && !nodeOk {
			t.Fatalf("广播前脚本验证: Go 通过但 Node Interpreter 失败 input[%d] errstr=%q（检查 FT_NODE_INTERPRETER_FLAGS 与 tbc-lib-js 路径）",
				i, nodeErrstr)
		}
		if nodeRunErr != nil {
			t.Logf("Node Interpreter 对照未执行: %v", nodeRunErr)
		}
	}
	if skippedJointEqualVerify > 0 {
		t.Logf("广播前本地解释器: inputs=%d，其中 %d 个输入因 Go/Node 联合 OP_EQUALVERIFY 已跳过致命失败（testnet 默认或 FT_CONTINUE_BROADCAST_AFTER_JOINT_EQUALVERIFY=1）", n, skippedJointEqualVerify)
	} else {
		t.Logf("广播前本地解释器验证通过: inputs=%d", n)
	}
}

// integrationTbcLibJSRootPath 指向 tbc-lib-js 包根目录（含 index.js），与 integration 测试仓库布局一致。
func integrationTbcLibJSRootPath(t *testing.T) string {
	t.Helper()
	rel := filepath.Join("..", "..", "..", "tbc-lib-js-go", "tbc-lib-js")
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Logf("解析 tbc-lib-js 路径失败: %v，使用相对路径 %q", err, rel)
		return rel
	}
	return abs
}

// integrationVerifyInputNodeJSPath 指向 scripts/verify_input_node.js。
func integrationVerifyInputNodeJSPath(t *testing.T) string {
	t.Helper()
	rel := filepath.Join("..", "..", "scripts", "verify_input_node.js")
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Logf("解析 verify_input_node.js 路径失败: %v，使用相对路径 %q", err, rel)
		return rel
	}
	return abs
}

type nodeVerifyOut struct {
	Ok        bool   `json:"ok"`
	Errstr    string `json:"errstr"`
	Error     string `json:"error"`
	FlagsUsed uint32 `json:"flagsUsed"`
}

// nodeVerifyInputInterpreter 调用 Node 脚本对单笔输入做 Interpreter.verify（stdin JSON）。
func nodeVerifyInputInterpreter(libRoot, scriptPath, txHex, prevTxHex string, inputIdx int, flags string) (ok bool, errstr string, err error) {
	payload, err := json.Marshal(map[string]interface{}{
		"txHex":      txHex,
		"prevTxHex":  prevTxHex,
		"inputIndex": inputIdx,
		"flags":      flags,
	})
	if err != nil {
		return false, "", err
	}
	cmd := exec.Command("node", scriptPath, libRoot)
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, "", fmt.Errorf("node verify_input_node: %w out=%s", err, string(out))
	}
	line := strings.TrimSpace(string(out))
	var nv nodeVerifyOut
	if err := json.Unmarshal([]byte(line), &nv); err != nil {
		return false, "", fmt.Errorf("parse node json: %w raw=%q", err, line)
	}
	if nv.Error != "" && !nv.Ok {
		return false, nv.Error, nil
	}
	return nv.Ok, nv.Errstr, nil
}

// compareGoVsNodeInterpreterVerify 在存在 tbc-lib-js 与脚本时运行 Node 对照；跳过条件见 FT_SKIP_NODE_INTERPRETER_VERIFY。
func compareGoVsNodeInterpreterVerify(t *testing.T, txHex, prevTxHex string, inputIdx int) (nodeOk bool, nodeErrstr string, ran bool, runErr error) {
	t.Helper()
	if boolEnv("FT_SKIP_NODE_INTERPRETER_VERIFY") {
		return false, "", false, nil
	}
	lib := integrationTbcLibJSRootPath(t)
	script := integrationVerifyInputNodeJSPath(t)
	if _, err := os.Stat(lib); err != nil {
		return false, "", false, fmt.Errorf("tbc-lib-js 不可用 %s: %w", lib, err)
	}
	if _, err := os.Stat(script); err != nil {
		return false, "", false, fmt.Errorf("verify_input_node.js 不可用 %s: %w", script, err)
	}
	flags := envOrDefault("FT_NODE_INTERPRETER_FLAGS", "minimal")
	ok, errstr, err := nodeVerifyInputInterpreter(lib, script, txHex, prevTxHex, inputIdx, flags)
	if err != nil {
		return false, "", true, err
	}
	if ok {
		t.Logf("Interpreter 对照 input[%d]: Go 与 Node 均通过 (flags=%s)", inputIdx, flags)
	} else {
		t.Logf("Interpreter 对照 input[%d]: Node 未通过 errstr=%q (flags=%s)", inputIdx, errstr, flags)
	}
	return ok, errstr, true, nil
}

func logTransferTxDebugOnBroadcastFailure(t *testing.T, txraw string, broadcastErr error) {
	t.Helper()
	if broadcastErr != nil {
		t.Logf("广播错误: %v", broadcastErr)
		if strings.Contains(broadcastErr.Error(), "OP_EQUALVERIFY") {
			t.Log("提示: 请用同参数在 Node 侧广播 JS 组装的 transfer；若同样失败，则优先排查合约/解锁路径与 testnet 规则，而非仅改 Go。")
		}
	}
	tx, err := bt.NewTxFromString(txraw)
	if err != nil {
		t.Logf("解析失败交易 raw 失败: %v", err)
		return
	}
	t.Logf("失败交易 txid=%s version=%d in=%d out=%d raw_hex_chars=%d",
		tx.TxID(), tx.Version, len(tx.Inputs), len(tx.Outputs), len(txraw))
	for i, o := range tx.Outputs {
		ls := o.LockingScript.Bytes()
		t.Logf("  out[%d] sat=%d script_bytes=%d", i, o.Satoshis, len(ls))
	}
}

// integrationTbcContractFtunlockJSPath 指向 tbc-contract 的 ftunlock.js（getCurrentTxdata / getPreTxdata）。
func integrationTbcContractFtunlockJSPath(t *testing.T) string {
	t.Helper()
	rel := filepath.Join("..", "..", "..", "tbc-contract", "lib", "util", "ftunlock.js")
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Logf("解析 ftunlock.js 路径失败: %v，使用相对路径 %q", err, rel)
		return rel
	}
	return abs
}

// tbcContractRootDir 返回 tbc-contract 仓库根目录（ftunlock.js 的上级的上级目录的上级）。
func tbcContractRootDir(ftunlockJS string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(ftunlockJS)))
}

// nodeGetCurrentTxdata 用 Node + tbc-contract 的 getCurrentTxdata 计算 hex（stdin 传入 transfer raw hex）。
func nodeGetCurrentTxdata(ftunlockJS, moduleRoot, rawHex string, inputIdx int) (string, error) {
	const js = `
const tbc = require('tbc-lib-js');
const { getCurrentTxdata } = require(process.argv[1]);
let b = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', (d) => { b += d; });
process.stdin.on('end', () => {
  try {
    const tx = new tbc.Transaction(b.trim());
    process.stdout.write(getCurrentTxdata(tx, parseInt(process.argv[2], 10)));
  } catch (e) {
    console.error(e);
    process.exit(1);
  }
});
`
	cmd := exec.Command("node", "-e", js, ftunlockJS, fmt.Sprintf("%d", inputIdx))
	cmd.Dir = moduleRoot
	cmd.Stdin = strings.NewReader(rawHex)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("node getCurrentTxdata: %w out=%s", err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// nodeGetPreTxdata 用 Node 计算 getPreTxdata(hex)（stdin 传入父交易 raw hex）。
func nodeGetPreTxdata(ftunlockJS, moduleRoot, preTxHex string, vout int) (string, error) {
	const js = `
const tbc = require('tbc-lib-js');
const { getPreTxdata } = require(process.argv[1]);
let b = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', (d) => { b += d; });
process.stdin.on('end', () => {
  try {
    const tx = new tbc.Transaction(b.trim());
    process.stdout.write(getPreTxdata(tx, parseInt(process.argv[2], 10)));
  } catch (e) {
    console.error(e);
    process.exit(1);
  }
});
`
	cmd := exec.Command("node", "-e", js, ftunlockJS, fmt.Sprintf("%d", vout))
	cmd.Dir = moduleRoot
	cmd.Stdin = strings.NewReader(preTxHex)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("node getPreTxdata: %w out=%s", err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// assertGetCurrentTxdataGoVsNode 对同一 raw 在 Go(bt.GetCurrentTxdata) 与 Node 逐字节对照（FT 输入 0..n-1）。
func assertGetCurrentTxdataGoVsNode(t *testing.T, txraw string, ftInputCount int) {
	t.Helper()
	if ftInputCount <= 0 {
		return
	}
	if strings.TrimSpace(os.Getenv("FT_SKIP_NODE_TXDATA_PARITY")) == "1" {
		t.Log("GetCurrentTxdata 对照: 已设置 FT_SKIP_NODE_TXDATA_PARITY=1，跳过 Node")
		return
	}
	ftunlock := integrationTbcContractFtunlockJSPath(t)
	if _, err := os.Stat(ftunlock); err != nil {
		t.Logf("GetCurrentTxdata 对照: 未找到 %s，跳过 Node: %v", ftunlock, err)
		return
	}
	root := tbcContractRootDir(ftunlock)
	tx, err := bt.NewTxFromString(txraw)
	if err != nil {
		t.Fatalf("assertGetCurrentTxdataGoVsNode 解析 txraw: %v", err)
	}
	for i := 0; i < ftInputCount; i++ {
		goHex, err := bt.GetCurrentTxdata(tx, i)
		if err != nil {
			t.Fatalf("GetCurrentTxdata(go) input=%d: %v", i, err)
		}
		jsHex, err := nodeGetCurrentTxdata(ftunlock, root, txraw, i)
		if err != nil {
			t.Fatalf("GetCurrentTxdata(node) input=%d: %v", i, err)
		}
		if !strings.EqualFold(goHex, jsHex) {
			idx, a, b := hexFirstDiffLower(goHex, jsHex)
			t.Fatalf("GetCurrentTxdata Go vs Node 不一致 input=%d firstDiffByte=%d go=%s js=%s goLen=%d jsLen=%d",
				i, idx, a, b, len(goHex), len(jsHex))
		}
		t.Logf("GetCurrentTxdata 对照 input[%d]: Go 与 Node 一致 (%d 字节 hex)", i, len(goHex)/2)
	}
}

// nodeFetchFtPrePreTxData 调用 tbc-contract API.fetchFtPrePreTxData（与 Go api.FetchFtPrePreTxData 对应），stdin 为 JSON：preTxHex, vout, network。
func nodeFetchFtPrePreTxData(tbcContractRoot, preTxHex string, vout int, network string) (string, error) {
	const js = `
const path = require('path');
const root = process.env.FT_INTEGRATION_TBC_CONTRACT_ROOT;
// api.js 使用 module.exports = API（默认导出），勿用 { API } 解构。
const API = require(path.join(root, 'lib/api/api.js'));
const tbc = require('tbc-lib-js');
let b = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', (d) => { b += d; });
process.stdin.on('end', () => {
  (async () => {
    try {
      const j = JSON.parse(b.trim());
      const preTX = new tbc.Transaction(j.preTxHex);
      const out = await API.fetchFtPrePreTxData(preTX, j.vout, j.network);
      process.stdout.write(out);
    } catch (e) {
      console.error(e);
      process.exit(1);
    }
  })();
});
`
	cmd := exec.Command("node", "-e", js)
	cmd.Dir = tbcContractRoot
	cmd.Env = append(os.Environ(), "FT_INTEGRATION_TBC_CONTRACT_ROOT="+tbcContractRoot)
	payload, err := json.Marshal(map[string]interface{}{
		"preTxHex": preTxHex,
		"vout":     vout,
		"network":  network,
	})
	if err != nil {
		return "", err
	}
	cmd.Stdin = strings.NewReader(string(payload))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("node fetchFtPrePreTxData: %w out=%s", err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// assertFetchFtPrePreTxDataGoVsNode 对照 Go FetchFtPrePreTxData 与 Node API.fetchFtPrePreTxData（逐字节 hex）。
func assertFetchFtPrePreTxDataGoVsNode(t *testing.T, preTX *bt.Tx, vout int, network, goPrepre string) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("FT_SKIP_NODE_TXDATA_PARITY")) == "1" {
		t.Log("FetchFtPrePreTxData 对照: 已设置 FT_SKIP_NODE_TXDATA_PARITY=1，跳过 Node")
		return
	}
	ftunlock := integrationTbcContractFtunlockJSPath(t)
	if _, err := os.Stat(ftunlock); err != nil {
		t.Logf("FetchFtPrePreTxData 对照: 未找到 ftunlock 路径 %s，跳过 Node: %v", ftunlock, err)
		return
	}
	root := tbcContractRootDir(ftunlock)
	apiPath := filepath.Join(root, "lib", "api", "api.js")
	if _, err := os.Stat(apiPath); err != nil {
		t.Logf("FetchFtPrePreTxData 对照: 未找到 %s，跳过 Node: %v", apiPath, err)
		return
	}
	preHex := hex.EncodeToString(preTX.Bytes())
	jsHex, err := nodeFetchFtPrePreTxData(root, preHex, vout, network)
	if err != nil {
		t.Fatalf("FetchFtPrePreTxData(node): %v", err)
	}
	goHex := strings.TrimSpace(goPrepre)
	if !strings.EqualFold(goHex, jsHex) {
		idx, a, b := hexFirstDiffLower(goHex, jsHex)
		t.Fatalf("FetchFtPrePreTxData Go vs Node 不一致 firstDiffByte=%d go=%s js=%s goLen=%d jsLen=%d (vout=%d)",
			idx, a, b, len(goHex), len(jsHex), vout)
	}
	t.Logf("FetchFtPrePreTxData 对照: Go 与 Node 一致 (%d 字节 hex, vout=%d)", len(goHex)/2, vout)
}

// assertGetPreTxdataGoVsNode 对照首笔 FT 父交易的 getPreTxdata（Go vs Node）。
func assertGetPreTxdataGoVsNode(t *testing.T, preTX *bt.Tx, vout int) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("FT_SKIP_NODE_TXDATA_PARITY")) == "1" {
		t.Log("GetPreTxdata 对照: 已设置 FT_SKIP_NODE_TXDATA_PARITY=1，跳过 Node")
		return
	}
	ftunlock := integrationTbcContractFtunlockJSPath(t)
	if _, err := os.Stat(ftunlock); err != nil {
		t.Logf("GetPreTxdata 对照: 未找到 %s，跳过 Node: %v", ftunlock, err)
		return
	}
	root := tbcContractRootDir(ftunlock)
	preHex := hex.EncodeToString(preTX.Bytes())
	goHex, err := bt.GetPreTxdata(preTX, vout)
	if err != nil {
		t.Fatalf("GetPreTxdata(go): %v", err)
	}
	jsHex, err := nodeGetPreTxdata(ftunlock, root, preHex, vout)
	if err != nil {
		t.Fatalf("GetPreTxdata(node): %v", err)
	}
	if !strings.EqualFold(goHex, jsHex) {
		idx, a, b := hexFirstDiffLower(goHex, jsHex)
		t.Fatalf("GetPreTxdata Go vs Node 不一致 firstDiffByte=%d go=%s js=%s goLen=%d jsLen=%d",
			idx, a, b, len(goHex), len(jsHex))
	}
	t.Logf("GetPreTxdata 对照: Go 与 Node 一致 (%d 字节 hex)", len(goHex)/2)
}

// probeFtTransferTxdataDebug 按排查顺序打印：① raw 摘要 ④ prepre 摘要（②③ 由 assertGetCurrentTxdataGoVsNode / assertGetPreTxdataGoVsNode 完成）。需 FT_DEBUG_TXDATA=1。
func probeFtTransferTxdataDebug(t *testing.T, txraw string, prepreTxDatas []string) {
	t.Helper()
	if !boolEnv("FT_DEBUG_TXDATA") {
		return
	}
	tx, err := bt.NewTxFromString(txraw)
	if err != nil {
		t.Logf("FT_DEBUG probe: 解析 txraw 失败: %v", err)
		return
	}
	t.Logf("FT_DEBUG [1] transfer txid=%s version=%d inputs=%d outputs=%d raw_hex_len=%d",
		tx.TxID(), tx.Version, len(tx.Inputs), len(tx.Outputs), len(txraw))
	if boolEnv("FT_DEBUG_TXDATA_FULL_RAW") {
		t.Logf("FT_DEBUG [1b] transfer raw_hex_full=%s", txraw)
	} else {
		snippet := txraw
		if len(snippet) > 512 {
			snippet = snippet[:512] + "…"
		}
		t.Logf("FT_DEBUG [1b] raw_hex_snippet(first512)=%s (设 FT_DEBUG_TXDATA_FULL_RAW=1 打印完整)", snippet)
	}
	for i, p := range prepreTxDatas {
		pre := strings.TrimSpace(p)
		sn := pre
		if len(sn) > 128 {
			sn = sn[:128] + "…"
		}
		t.Logf("FT_DEBUG [4] prepreTxDatas[%d] hex_len=%d snippet=%s", i, len(pre), sn)
	}
}

// logInterpreterFailureContext 本地解释器失败时输出 raw 与排查提示（FT_DEBUG_TXDATA 时输出更长 raw）。
func logInterpreterFailureContext(t *testing.T, txraw string, inputIdx int, prevTxid string, vout int, verifyErr error) {
	t.Helper()
	t.Logf("解释器失败上下文: input=%d prev=%s:%d err=%v", inputIdx, prevTxid, vout, verifyErr)
	tx, err := bt.NewTxFromString(txraw)
	if err == nil {
		t.Logf("  txid=%s version=%d in=%d out=%d raw_len=%d", tx.TxID(), tx.Version, len(tx.Inputs), len(tx.Outputs), len(txraw))
	}
	if boolEnv("FT_DEBUG_TXDATA") || boolEnv("FT_LOG_RAW_ON_VERIFY_FAIL") {
		if boolEnv("FT_DEBUG_TXDATA_FULL_RAW") {
			t.Logf("  raw_hex_full=%s", txraw)
		} else {
			sn := txraw
			if len(sn) > 1024 {
				sn = sn[:1024] + "…"
			}
			t.Logf("  raw_hex_snippet=%s", sn)
		}
	}
	if err == nil && inputIdx >= 0 && inputIdx < len(tx.Inputs) {
		meta6 := bt.CurrentInputOutpointBytes(tx, inputIdx)
		if len(meta6) == 40 {
			t.Logf("  OP_PUSH_META(6) 40字节(hex)= %s（首32字节=prevout txid  wire 序； covenant 里 0x01 0x20 OP_SPLIT 取此32字节与解锁脚本预期对比）", hex.EncodeToString(meta6))
		}
	}
	t.Log("  建议: GetCurrentTxdata/PreTxdata 已与 Node 一致时，OP_EQUALVERIFY 多来自 OP_6 OP_PUSH_META 后首32字节 vs 解锁脚本预期，或更早的 SHA256/CAT 链；partial hash 已由 util/partialsha256 单测覆盖。可设 FT_SKIP_LOCAL_SCRIPT_VERIFY=1 对比链上广播结果。")
}

func hexFirstDiffLower(a, b string) (byteIdx int, aByte, bByte string) {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	minBytes := len(a) / 2
	if lb := len(b) / 2; lb < minBytes {
		minBytes = lb
	}
	for bi := 0; bi < minBytes; bi++ {
		ab := a[bi*2 : bi*2+2]
		bb := b[bi*2 : bi*2+2]
		if ab != bb {
			return bi, ab, bb
		}
	}
	if len(a) != len(b) {
		return minBytes, "len", "mismatch"
	}
	return -1, "", ""
}

// integrationTbcContractFtJSPath 指向 tbc-contract 的 ft.js（含 FT.buildFTtransferCode），用于与 Go BuildFTtransferCode 对照。
func integrationTbcContractFtJSPath(t *testing.T) string {
	t.Helper()
	rel := filepath.Join("..", "..", "..", "tbc-contract", "lib", "contract", "ft.js")
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Logf("解析 ft.js 路径失败: %v，使用相对路径 %q", err, rel)
		return rel
	}
	return abs
}

// assertFTMintOutput0MatchesAPIScript 拉取合约（mint）交易，校验 output[0] 锁定脚本与 FetchFtInfo.code_script 逐字节一致。
func assertFTMintOutput0MatchesAPIScript(t *testing.T, network, contractTxid, apiCodeHex string) {
	t.Helper()
	tx, err := api.FetchTXRaw(contractTxid, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(合约/mint txid=%s): %v", contractTxid, err)
	}
	if len(tx.Outputs) < 1 {
		t.Fatalf("mint/合约交易 %s 无输出", contractTxid)
	}
	chainHex := hex.EncodeToString(tx.Outputs[0].LockingScript.Bytes())
	apiHex := strings.ToLower(strings.TrimSpace(apiCodeHex))
	if strings.ToLower(chainHex) != apiHex {
		idx, _, _ := hexFirstDiffLower(chainHex, apiCodeHex)
		t.Fatalf("链上 mint output[0] 与 API code_script 不一致: firstDiffByte=%d chain_len=%d api_len=%d (合约 txid=%s)",
			idx, len(chainHex)/2, len(apiHex)/2, contractTxid)
	}
	t.Logf("链上校验: 合约 %s output[0] locking_script 与 FetchFtInfo.code_script 一致 (%d 字节, sat=%d)",
		contractTxid, len(chainHex)/2, tx.Outputs[0].Satoshis)
}

// assertBuildFTtransferCodeGoVsNode 对同一 code_script 与地址，对比 Go BuildFTtransferCode 与 JS FT.buildFTtransferCode 的 hex。
// 未找到 tbc-contract/lib/contract/ft.js 或设置 FT_SKIP_NODE_PARITY=1 时跳过（仅记录日志）。
func assertBuildFTtransferCodeGoVsNode(t *testing.T, codeScriptHex, address string) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("FT_SKIP_NODE_PARITY")) == "1" {
		t.Log("BuildFTtransferCode 对照: 已设置 FT_SKIP_NODE_PARITY=1，跳过 Node")
		return
	}
	ftJs := integrationTbcContractFtJSPath(t)
	if _, err := os.Stat(ftJs); err != nil {
		t.Logf("BuildFTtransferCode 对照: 未找到 %s，跳过 Node: %v", ftJs, err)
		return
	}
	goS := BuildFTtransferCode(codeScriptHex, address)
	goHex := hex.EncodeToString(goS.Bytes())
	nodeCode := fmt.Sprintf(`const FT=require(%q);
const code=process.argv[1];
const addr=process.argv[2];
const s=FT.buildFTtransferCode(code, addr);
process.stdout.write(s.toBuffer().toString("hex"));`, ftJs)
	cmd := exec.Command("node", "-e", nodeCode, strings.TrimSpace(codeScriptHex), address)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Node FT.buildFTtransferCode 失败: %v out=%s", err, string(b))
	}
	jsHex := strings.TrimSpace(string(b))
	if !strings.EqualFold(goHex, jsHex) {
		idx, a, b := hexFirstDiffLower(goHex, jsHex)
		t.Fatalf("BuildFTtransferCode Go vs Node 不一致: firstDiffByte=%d go=%s js=%s goLen=%d jsLen=%d",
			idx, a, b, len(goHex), len(jsHex))
	}
	t.Logf("BuildFTtransferCode 对照: Go 与 Node 一致 (%d 字节)", len(goHex)/2)
}

// assertFtUtxoPrevoutMatchesTransferCode 校验 FT UTXO 对应链上输出的锁定脚本等于 BuildFTtransferCode(API code, 来源地址)。
func assertFtUtxoPrevoutMatchesTransferCode(t *testing.T, network, baseCodeHex, fromAddr string, u *bt.FtUTXO) {
	t.Helper()
	if u == nil {
		return
	}
	pre, err := api.FetchTXRaw(u.TxID, network)
	if err != nil {
		t.Fatalf("FetchTXRaw(FT UTXO 父交易 %s): %v", u.TxID, err)
	}
	vout := int(u.Vout)
	if vout < 0 || vout >= len(pre.Outputs) {
		t.Fatalf("FT UTXO vout 越界: txid=%s vout=%d outputs=%d", u.TxID, vout, len(pre.Outputs))
	}
	chainHex := hex.EncodeToString(pre.Outputs[vout].LockingScript.Bytes())
	wantHex := hex.EncodeToString(BuildFTtransferCode(baseCodeHex, fromAddr).Bytes())
	if !strings.EqualFold(chainHex, wantHex) {
		idx, a, b := hexFirstDiffLower(chainHex, wantHex)
		t.Fatalf("FT UTXO 链上锁定脚本与 BuildFTtransferCode(code,from) 不一致: %s:%d firstDiffByte=%d chain=%s want=%s",
			u.TxID, vout, idx, a, b)
	}
	t.Logf("链上校验: FT UTXO %s:%d 锁定脚本与 BuildFTtransferCode(from) 一致 (%d 字节)", u.TxID, vout, len(chainHex)/2)
}

func parseBatchReceivers(t *testing.T, raw string) map[string]float64 {
	t.Helper()
	parts := strings.Split(strings.TrimSpace(raw), ",")
	out := make(map[string]float64, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		kv := strings.SplitN(p, ":", 2)
		if len(kv) != 2 {
			t.Fatalf("FT_BATCH_RECEIVERS 格式错误: %q，示例: addr1:500,addr2:700", p)
		}
		addr := strings.TrimSpace(kv[0])
		amt, err := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64)
		if err != nil || amt <= 0 {
			t.Fatalf("FT_BATCH_RECEIVERS 金额错误: %q", p)
		}
		out[addr] = amt
	}
	if len(out) == 0 {
		t.Fatal("FT_BATCH_RECEIVERS 为空")
	}
	return out
}

func sortedReceiverAddrs(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func retryWithInterval(maxAttempts int, interval time.Duration, fn func() error) error {
	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(interval)
	}
	return lastErr
}

func fetchFtPrePreTxDataWithRetry(preTx *bt.Tx, preTxVout int, network string, maxAttempts int, interval time.Duration) (string, error) {
	var out string
	err := retryWithInterval(maxAttempts, interval, func() error {
		d, err := api.FetchFtPrePreTxData(preTx, preTxVout, network)
		if err != nil {
			return err
		}
		out = d
		return nil
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

func boolEnv(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes" || v == "y"
}

// continueBroadcastAfterJointEqualVerify：Go 与 Node 解释器均在 OP_EQUALVERIFY 失败时，是否跳过本地 Fatalf、继续尝试链上广播。
// 显式 FT_CONTINUE_BROADCAST_AFTER_JOINT_EQUALVERIFY=1/0 优先；未设置时 testnet 默认为 true（见 tbc-contract/docs/runners/chain/FT_COMPARE.md）。
func continueBroadcastAfterJointEqualVerify(network string) bool {
	v := strings.TrimSpace(os.Getenv("FT_CONTINUE_BROADCAST_AFTER_JOINT_EQUALVERIFY"))
	if v == "1" {
		return true
	}
	if v == "0" {
		return false
	}
	low := strings.ToLower(strings.TrimSpace(network))
	return low == "testnet" || strings.Contains(low, "testnet")
}

func logMintTxDebug(t *testing.T, title, txraw string) {
	t.Helper()
	tx, err := bt.NewTxFromString(txraw)
	if err != nil {
		t.Logf("%s 解析失败: %v", title, err)
		return
	}
	t.Logf("%s txid=%s version=%d inputs=%d outputs=%d raw_len=%d", title, tx.TxID(), tx.Version, len(tx.Inputs), len(tx.Outputs), len(txraw))
	for i, in := range tx.Inputs {
		us := ""
		if in.UnlockingScript != nil {
			us = hex.EncodeToString(in.UnlockingScript.Bytes())
		}
		t.Logf("%s input[%d] prevTxid=%s vout=%d unlock_len=%d", title, i, hex.EncodeToString(in.PreviousTxID()), in.PreviousTxOutIndex, len(us))
	}
	for i, out := range tx.Outputs {
		ls := hex.EncodeToString(out.LockingScript.Bytes())
		t.Logf("%s output[%d] sat=%d script_len=%d script=%s", title, i, out.Satoshis, len(ls), ls)
	}
}

func firstHexDiff(a, b string) (idx int, left, right string, ok bool) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i, a[i:minInt(i+32, len(a))], b[i:minInt(i+32, len(b))], true
		}
	}
	if len(a) != len(b) {
		return n, a[minInt(n, len(a)):minInt(n+32, len(a))], b[minInt(n, len(b)):minInt(n+32, len(b))], true
	}
	return -1, "", "", false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func compareMintArtifacts(t *testing.T, leftTitle, leftRaw, rightTitle, rightRaw string) {
	t.Helper()
	li, ls, rs, ok := firstHexDiff(leftRaw, rightRaw)
	if ok {
		t.Logf("mint txraw diff: %s vs %s firstDiffOffset=%d leftSnippet=%s rightSnippet=%s leftLen=%d rightLen=%d",
			leftTitle, rightTitle, li, ls, rs, len(leftRaw), len(rightRaw))
	} else {
		t.Logf("mint txraw equal: %s == %s len=%d", leftTitle, rightTitle, len(leftRaw))
	}

	leftTx, err := bt.NewTxFromString(leftRaw)
	if err != nil {
		t.Logf("解析 %s 失败: %v", leftTitle, err)
		return
	}
	rightTx, err := bt.NewTxFromString(rightRaw)
	if err != nil {
		t.Logf("解析 %s 失败: %v", rightTitle, err)
		return
	}
	if len(leftTx.Outputs) == 0 || len(rightTx.Outputs) == 0 {
		t.Logf("无法比较 output[0]：%s outputs=%d, %s outputs=%d", leftTitle, len(leftTx.Outputs), rightTitle, len(rightTx.Outputs))
		return
	}
	leftCode := hex.EncodeToString(leftTx.Outputs[0].LockingScript.Bytes())
	rightCode := hex.EncodeToString(rightTx.Outputs[0].LockingScript.Bytes())
	ci, cls, crs, cok := firstHexDiff(leftCode, rightCode)
	if cok {
		t.Logf("mint codeScript(output[0]) diff: %s vs %s firstDiffOffset=%d leftSnippet=%s rightSnippet=%s leftLen=%d rightLen=%d",
			leftTitle, rightTitle, ci, cls, crs, len(leftCode), len(rightCode))
	} else {
		t.Logf("mint codeScript(output[0]) equal: %s == %s len=%d", leftTitle, rightTitle, len(leftCode))
	}
}

func waitTxVisible(t *testing.T, network, txid string, maxAttempts int, interval time.Duration) {
	t.Helper()
	for i := 1; i <= maxAttempts; i++ {
		ok, err := api.IsTxOnChain(txid, network)
		if err == nil && ok {
			return
		}
		time.Sleep(interval)
	}
	t.Logf("source 交易在 %d 次轮询内未确认可见，继续尝试广播后续交易", maxAttempts)
}

func waitTxVisibleStrict(network, txid string, maxAttempts int, interval time.Duration) bool {
	for i := 1; i <= maxAttempts; i++ {
		ok, err := api.IsTxOnChain(txid, network)
		if err == nil && ok {
			return true
		}
		time.Sleep(interval)
	}
	return false
}

func broadcastMintWithRetry(network, txraw string, maxAttempts int, interval time.Duration) (string, error) {
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		txid, err := api.BroadcastTXRaw(txraw, network)
		if err == nil {
			return txid, nil
		}
		lastErr = err
		if !strings.Contains(strings.ToLower(err.Error()), "missing inputs") {
			return "", err
		}
		time.Sleep(interval)
	}
	return "", fmt.Errorf("mint 广播重试 %d 次后仍失败: %w", maxAttempts, lastErr)
}

func rebuildMintTxRawWithBroadcastSource(ft *FT, privKey *bec.PrivateKey, addressTo, sourceTxid, sourceRaw string) (string, error) {
	return ft.RebuildMintTxRawWithBroadcastSource(privKey, addressTo, sourceTxid, sourceRaw)
}

func getFTBalanceBN(network, contractTxid, address string) (*big.Int, error) {
	bal, err := api.GetFTBalance(contractTxid, address, network)
	if err != nil {
		return nil, err
	}
	bn, ok := new(big.Int).SetString(bal, 10)
	if !ok {
		return nil, fmt.Errorf("非法 FT 余额: %s", bal)
	}
	return bn, nil
}

// logFTBalanceSnap 查询并记录单地址 FT 余额（用于广播前后对照；索引滞后时「即时」快照可能与最终不一致）。
func logFTBalanceSnap(t *testing.T, phase, network, contractTxid, address string) {
	t.Helper()
	bn, err := getFTBalanceBN(network, contractTxid, address)
	if err != nil {
		t.Logf("%s contractTxid=%s address=%s balance=<err %v>", phase, contractTxid, address, err)
		return
	}
	t.Logf("%s contractTxid=%s address=%s balance=%s", phase, contractTxid, address, bn.String())
}

func summarizeFtUTXOs(utxos []*bt.FtUTXO, limit int) string {
	if len(utxos) == 0 {
		return "[]"
	}
	if limit <= 0 || limit > len(utxos) {
		limit = len(utxos)
	}
	parts := make([]string, 0, limit+1)
	for i := 0; i < limit; i++ {
		u := utxos[i]
		parts = append(parts, fmt.Sprintf("%s:%d(balance=%s)", u.TxID, u.Vout, u.FtBalance))
	}
	if len(utxos) > limit {
		parts = append(parts, fmt.Sprintf("...(+%d)", len(utxos)-limit))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func isFTAccountActive(network, contractTxid, address string) (bool, string) {
	info, err := api.FetchFtInfo(contractTxid, network)
	if err != nil {
		return false, fmt.Sprintf("FetchFtInfo失败: %v", err)
	}
	if strings.TrimSpace(info.CodeScript) == "" || strings.TrimSpace(info.TapeScript) == "" {
		return false, fmt.Sprintf("FT info无效: empty script (name=%q symbol=%q amount=%q decimal=%d codeLen=%d tapeLen=%d)", info.Name, info.Symbol, info.TotalSupply, info.Decimal, len(info.CodeScript), len(info.TapeScript))
	}
	code := BuildFTtransferCode(info.CodeScript, address)
	codeHex := hex.EncodeToString(code.Bytes())

	bal, balErr := getFTBalanceBN(network, contractTxid, address)
	utxos, utxoErr := api.FetchFtUTXOList(contractTxid, address, codeHex, network)
	utxoCount := 0
	if utxoErr == nil {
		utxoCount = len(utxos)
	}
	balStr := "<err>"
	if balErr == nil {
		balStr = bal.String()
	}
	if (balErr == nil && bal.Sign() > 0) || (utxoErr == nil && utxoCount > 0) {
		return true, fmt.Sprintf("balance=%s utxos=%d", balStr, utxoCount)
	}
	return false, fmt.Sprintf("balance=%s(balanceErr=%v) utxos=%d(utxoErr=%v)", balStr, balErr, utxoCount, utxoErr)
}

func resolveWorkingContractTxid(t *testing.T, network, address, sourceTxid, mintTxid string) string {
	t.Helper()
	candidates := []string{mintTxid, sourceTxid}
	for _, txid := range candidates {
		if info, err := api.FetchFtInfo(txid, network); err == nil {
			t.Logf("候选FT info txid=%s name=%q symbol=%q amount=%q decimal=%d codeLen=%d tapeLen=%d", txid, info.Name, info.Symbol, info.TotalSupply, info.Decimal, len(info.CodeScript), len(info.TapeScript))
		} else {
			t.Logf("候选FT info txid=%s 查询失败: %v", txid, err)
		}
		var lastDiag string
		err := retryWithInterval(8, 1*time.Second, func() error {
			ok, diag := isFTAccountActive(network, txid, address)
			lastDiag = diag
			if !ok {
				return fmt.Errorf(diag)
			}
			return nil
		})
		if err == nil {
			t.Logf("选定 FT_CONTRACT_TXID=%s (%s)", txid, lastDiag)
			return txid
		}
		t.Logf("候选 contractTxid=%s 未生效: %s", txid, lastDiag)
	}
	if lastMintSourceRaw != "" {
		logMintTxDebug(t, "mint.source.local.faildump", lastMintSourceRaw)
	}
	if lastMintLocalRaw != "" {
		logMintTxDebug(t, "mint.mint.local.faildump", lastMintLocalRaw)
	}
	if lastMintRebuildRaw != "" {
		logMintTxDebug(t, "mint.mint.rebuild.faildump", lastMintRebuildRaw)
	}
	t.Fatalf("Mint后未找到可用 contractTxid，候选 mintTxid=%s sourceTxid=%s", mintTxid, sourceTxid)
	return ""
}

func assertFTAccountValidByAPI(t *testing.T, network, contractTxid, baseCodeScript, address, label string) {
	t.Helper()
	chainInfo, infoErr := api.FetchFtInfo(contractTxid, network)
	if infoErr == nil {
		if !strings.EqualFold(chainInfo.CodeScript, baseCodeScript) {
			t.Logf("%s: 链上 codeScript 与本地不一致: chain_len=%d local_len=%d", label, len(chainInfo.CodeScript), len(baseCodeScript))
		}
	}

	var finalBal *big.Int
	var lastUTXOCount int
	err := retryWithInterval(12, 2*time.Second, func() error {
		info, err := api.FetchFtInfo(contractTxid, network)
		if err != nil {
			return fmt.Errorf("%s FT info 未就绪: %w", label, err)
		}
		code := BuildFTtransferCode(info.CodeScript, address)
		codeHex := hex.EncodeToString(code.Bytes())
		bn, err := getFTBalanceBN(network, contractTxid, address)
		if err != nil {
			return err
		}
		finalBal = bn
		utxos, err := api.FetchFtUTXOList(contractTxid, address, codeHex, network)
		if err != nil {
			lastUTXOCount = 0
			return fmt.Errorf("%s UTXO 查询未就绪: %w", label, err)
		}
		lastUTXOCount = len(utxos)
		// 账户有效判定：余额>0 或可查询到至少一个 FT UTXO。
		if bn.Sign() > 0 || len(utxos) > 0 {
			return nil
		}
		return fmt.Errorf("%s 余额与UTXO均未生效: balance=%s utxos=%d", label, bn.String(), len(utxos))
	})
	if err != nil {
		finalBalStr := "<nil>"
		if finalBal != nil {
			finalBalStr = finalBal.String()
		}
		if strings.Contains(label, "Mint后FT账户") {
			cachedMintFailed = true
			cachedMintFailReason = fmt.Sprintf("%s API 校验失败: %v (contractTxid=%s address=%s final balance=%s, final utxos=%d)", label, err, contractTxid, address, finalBalStr, lastUTXOCount)
		}
		t.Fatalf("%s API 校验失败: %v (contractTxid=%s address=%s final balance=%s, final utxos=%d)", label, err, contractTxid, address, finalBalStr, lastUTXOCount)
	}
	t.Logf("%s API 校验通过: balance=%s utxos=%d", label, finalBal.String(), lastUTXOCount)
}

func runMintAndGetContractTxid(t *testing.T, network string, privKey *bec.PrivateKey) string {
	t.Helper()
	if cachedMintFailed {
		t.Fatalf("前置 Mint 已失败，停止重复尝试: %s", cachedMintFailReason)
	}
	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成地址失败: %v", err)
	}

	ftName := envOrDefault("FT_MINT_NAME", "test")
	ftSymbol := envOrDefault("FT_MINT_SYMBOL", "test")
	ftDecimal := parseDecimalRange(t, "FT_MINT_DECIMAL", 6)
	ftAmount := parsePositiveInt64(t, "FT_MINT_AMOUNT", 100000000)

	ft, err := NewFT(&FtParams{
		Name:    ftName,
		Symbol:  ftSymbol,
		Amount:  ftAmount,
		Decimal: ftDecimal,
	})
	if err != nil {
		t.Fatalf("NewFT: %v", err)
	}

	utxo, err := api.FetchUTXO(addr.AddressString, 0.02, network)
	if err != nil {
		t.Fatalf("FetchUTXO: %v", err)
	}

	txraws, err := ft.MintFT(privKey, addr.AddressString, utxo)
	if err != nil {
		t.Fatalf("MintFT: %v", err)
	}
	if len(txraws) != 2 {
		t.Fatalf("MintFT 返回交易数=%d，期望=2", len(txraws))
	}
	lastMintSourceRaw = txraws[0]
	lastMintLocalRaw = txraws[1]
	if boolEnv("FT_DEBUG_MINT_DUMP") {
		logMintTxDebug(t, "mint.source.local", txraws[0])
		logMintTxDebug(t, "mint.mint.local", txraws[1])
		if jsMint := strings.TrimSpace(os.Getenv("FT_JS_MINT_RAW")); jsMint != "" {
			compareMintArtifacts(t, "mint.mint.js", jsMint, "mint.mint.local", txraws[1])
		}
		if jsSource := strings.TrimSpace(os.Getenv("FT_JS_SOURCE_RAW")); jsSource != "" {
			compareMintArtifacts(t, "mint.source.js", jsSource, "mint.source.local", txraws[0])
		}
	}

	sourceTxid, err := api.BroadcastTXRaw(txraws[0], network)
	if err != nil {
		t.Fatalf("广播 source 交易失败: %v", err)
	}
	t.Logf("Mint sourceTxid=%s", sourceTxid)
	waitTxVisible(t, network, sourceTxid, 12, 1*time.Second)

	mintRaw, err := rebuildMintTxRawWithBroadcastSource(ft, privKey, addr.AddressString, sourceTxid, txraws[0])
	if err != nil {
		t.Fatalf("基于广播 sourceTxid 重建 mint 交易失败: %v", err)
	}
	lastMintRebuildRaw = mintRaw
	if boolEnv("FT_DEBUG_MINT_DUMP") {
		logMintTxDebug(t, "mint.mint.rebuild", mintRaw)
		compareMintArtifacts(t, "mint.mint.local", txraws[1], "mint.mint.rebuild", mintRaw)
		if jsMint := strings.TrimSpace(os.Getenv("FT_JS_MINT_RAW")); jsMint != "" {
			compareMintArtifacts(t, "mint.mint.js", jsMint, "mint.mint.rebuild", mintRaw)
		}
	}
	mintTxid, err := broadcastMintWithRetry(network, mintRaw, 8, 1*time.Second)
	if err != nil {
		t.Fatalf("广播 mint 交易失败: %v", err)
	}
	t.Logf("Mint mintTxid=%s", mintTxid)
	if !waitTxVisibleStrict(network, mintTxid, 15, 1*time.Second) {
		t.Fatalf("mint tx 未在链上可见，疑似脚本或交易结构无效: mintTxid=%s", mintTxid)
	}
	contractTxid := resolveWorkingContractTxid(t, network, addr.AddressString, sourceTxid, mintTxid)
	assertFTAccountValidByAPI(t, network, contractTxid, ft.CodeScript, addr.AddressString, "Mint后FT账户")
	return contractTxid
}

func resolveContractTxidForIntegration(t *testing.T, network string, privKey *bec.PrivateKey) string {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv("FT_CONTRACT_TXID")); v != "" {
		return v
	}
	if cachedMintFailed {
		t.Fatalf("自动 Mint 已失败，终止后续测试以避免耗时重试: %s", cachedMintFailReason)
	}
	if cachedMintContractTxid != "" {
		return cachedMintContractTxid
	}
	t.Log("未设置 FT_CONTRACT_TXID，自动执行 Mint 生成合约 txid")
	cachedMintContractTxid = runMintAndGetContractTxid(t, network, privKey)
	t.Logf("自动 Mint 成功，FT_CONTRACT_TXID=%s", cachedMintContractTxid)
	return cachedMintContractTxid
}

// TestFT_Integration_Mint_Broadcast 对应 ft.md Mint：组装 + 广播 source/mint 两笔交易。
func TestFT_Integration_Mint_Broadcast(t *testing.T) {
	requireRealRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	mintTxid := runMintAndGetContractTxid(t, network, privKey)
	cachedMintContractTxid = mintTxid
	t.Logf("Mint 广播成功 mintTxid=%s", mintTxid)
}

// TestFT_Integration_Transfer_Broadcast 对应 ft.md Transfer：组装 + 广播单笔转账交易。
//
// 与 cmd/ft_transfer_compare、scripts/ft-transfer-js-go-compare-broadcast.js 的诊断关系：
// 若在同一合约/付款钥/收款地址/金额/网络下，且费率（FT_FEE_SAT_PER_KB，未设则与 ft.go 默认 80 一致）与
// 手续费 UTXO（本测试每次 FetchUTXO(0.01)，对比脚本可用 FT_LOCKSTEP_FEE_* 锁同一 prevout）与 compare 一致，
// 则组出的 raw 应与「Go JSON raw_hex」及 JS transfer 一致。此时广播仍报 OP_EQUALVERIFY 说明该 raw 在当前
// 节点共识下无效，通常 JS 侧 broadcast 亦同败；原因在 FT 合约路径/节点脚本验证与库侧约定之差异，而非
// 集成测试相对 compare 的单独实现错误。
func TestFT_Integration_Transfer_Broadcast(t *testing.T) {
	requireRealRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	// contractTxid := mustEnvOrConst(t, "FT_CONTRACT_TXID", defaultTxid)
	privKey := loadPrivKey(t)
	contractTxid := resolveContractTxidForIntegration(t, network, privKey)
	toAddress := mustEnvOrConst(t, "FT_TRANSFER_TO", defaultTransferTo)

	transferAmountRaw := strings.TrimSpace(envOrConst("FT_TRANSFER_AMOUNT", defaultTransferAmount))

	token := loadTokenForIntegration(t, network, contractTxid)
	amountBN := util.ParseDecimalToBigInt(transferAmountRaw, token.Decimal)
	if amountBN == nil || amountBN.Sign() <= 0 {
		t.Fatalf("FT_TRANSFER_AMOUNT 须为正数，当前=%q", transferAmountRaw)
	}
	logFtTokenScriptsForDebug(t, contractTxid, token.CodeScript, token.TapeScript)
	assertFTMintOutput0MatchesAPIScript(t, network, contractTxid, token.CodeScript)

	fromAddr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成来源地址失败: %v", err)
	}
	fromBefore, err := getFTBalanceBN(network, contractTxid, fromAddr.AddressString)
	if err != nil {
		t.Fatalf("查询转账前来源余额失败: %v", err)
	}
	toBefore, err := getFTBalanceBN(network, contractTxid, toAddress)
	if err != nil {
		t.Fatalf("查询转账前目标余额失败: %v", err)
	}
	t.Logf("Transfer 广播前 FT 余额 contractTxid=%s from=%s balance=%s to=%s balance=%s",
		contractTxid, fromAddr.AddressString, fromBefore.String(), toAddress, toBefore.String())

	assertBuildFTtransferCodeGoVsNode(t, token.CodeScript, fromAddr.AddressString)

	// 与 FT.transfer(JS) / TransferDecimalString(Go) 一致：链上整数单位由 util.ParseDecimalToBigInt(金额原文, decimal) 得到
	ftCodeScript := BuildFTtransferCode(token.CodeScript, fromAddr.AddressString)
	t.Logf("FT 调试 BuildFTtransferCode(来源地址) locking_script_bytes=%d hex_chars=%d",
		len(ftCodeScript.Bytes()), len(hex.EncodeToString(ftCodeScript.Bytes())))
	if asm, err := ftCodeScript.ToASM(); err == nil {
		needle := "OP_6 OP_PUSH_META"
		if idx := strings.Index(asm, needle); idx >= 0 {
			end := idx + 500
			if end > len(asm) {
				end = len(asm)
			}
			t.Logf("FT transfer code ASM around %q:\n%s", needle, asm[idx:end])
		} else {
			t.Logf("FT transfer code ASM: pattern %q not found", needle)
		}
	}
	ftutxos, err := api.FetchFtUTXOs(
		contractTxid,
		fromAddr.AddressString,
		hex.EncodeToString(ftCodeScript.Bytes()),
		network,
		amountBN,
	)
	if err != nil {
		t.Fatalf("FetchFtUTXOs: %v", err)
	}
	if len(ftutxos) == 0 {
		t.Fatal("没有可用 FT UTXO，请先铸造或转入后重试")
	}
	t.Logf("FetchFtUTXOs n=%d", len(ftutxos))
	for i, u := range ftutxos {
		t.Logf("  ftutxos[%d]=%s vout=%d ftBalance=%v", i, u.TxID, u.Vout, u.FtBalance)
	}
	assertFtUtxoPrevoutMatchesTransferCode(t, network, token.CodeScript, fromAddr.AddressString, ftutxos[0])

	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxDatas := make([]string, len(ftutxos))
	for i := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(ftutxos[i].TxID, network)
		if err != nil {
			t.Fatalf("FetchTXRaw(%s): %v", ftutxos[i].TxID, err)
		}
		prepreTxDatas[i], err = fetchFtPrePreTxDataWithRetry(preTXs[i], int(ftutxos[i].Vout), network, 8, 1*time.Second)
		if err != nil {
			t.Fatalf("FetchFtPrePreTxData(vout=%d) 重试后仍失败: %v", ftutxos[i].Vout, err)
		}
	}
	for i := range ftutxos {
		assertFetchFtPrePreTxDataGoVsNode(t, preTXs[i], int(ftutxos[i].Vout), network, prepreTxDatas[i])
	}

	feeUTXO, err := api.FetchUTXO(fromAddr.AddressString, 0.01, network)
	if err != nil {
		t.Fatalf("FetchUTXO(for fee): %v", err)
	}

	txraw, err := token.TransferDecimalString(privKey, toAddress, transferAmountRaw, ftutxos, feeUTXO, preTXs, prepreTxDatas, 0)
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}

	txBuilt, err := bt.NewTxFromString(txraw)
	if err != nil {
		t.Fatalf("解析 Transfer 结果 txraw: %v", err)
	}
	t.Logf("Transfer 已组装 txid=%s raw_hex_len=%d (FT_DEBUG_TXDATA=1 打印 raw 与 prepre 摘要；FT_LOG_RAW_ON_VERIFY_FAIL=1 在解释器失败时打印 raw 片段)", txBuilt.TxID(), len(txraw))

	probeFtTransferTxdataDebug(t, txraw, prepreTxDatas)

	t.Log("GetCurrentTxdata / GetPreTxdata 与 Node 对照（未设置 FT_SKIP_NODE_TXDATA_PARITY 且存在 tbc-contract/lib/util/ftunlock.js 时执行）…")
	assertGetCurrentTxdataGoVsNode(t, txraw, len(ftutxos))
	if len(preTXs) > 0 && len(ftutxos) > 0 {
		assertGetPreTxdataGoVsNode(t, preTXs[0], int(ftutxos[0].Vout))
	}

	verifyTxAllInputsLocalInterpreter(t, network, txraw)

	txid, err := api.BroadcastTXRaw(txraw, network)
	if err != nil {
		logTransferTxDebugOnBroadcastFailure(t, txraw, err)
		t.Fatalf("广播 transfer 交易失败: %v", err)
	}
	t.Logf("Transfer 广播成功 txid=%s", txid)
	t.Logf("Transfer 广播后(即时查询，索引可能尚未更新) txid=%s", txid)
	logFTBalanceSnap(t, "Transfer 广播后(即时)", network, contractTxid, fromAddr.AddressString)
	logFTBalanceSnap(t, "Transfer 广播后(即时)", network, contractTxid, toAddress)
	waitTxVisible(t, network, txid, 12, 1*time.Second)

	var fromAfterFinal *big.Int
	var toAfterFinal *big.Int
	var fromUTXOsFinal []*bt.FtUTXO
	var toUTXOsFinal []*bt.FtUTXO
	err = retryWithInterval(12, 1*time.Second, func() error {
		fromAfter, err := getFTBalanceBN(network, contractTxid, fromAddr.AddressString)
		if err != nil {
			return err
		}
		toAfter, err := getFTBalanceBN(network, contractTxid, toAddress)
		if err != nil {
			return err
		}
		if fromAfter.Cmp(fromBefore) >= 0 {
			return fmt.Errorf("来源余额未下降: before=%s after=%s", fromBefore.String(), fromAfter.String())
		}
		if toAfter.Cmp(toBefore) <= 0 {
			return fmt.Errorf("目标余额未上升: before=%s after=%s", toBefore.String(), toAfter.String())
		}
		fromCodeHex := hex.EncodeToString(BuildFTtransferCode(token.CodeScript, fromAddr.AddressString).Bytes())
		toCodeHex := hex.EncodeToString(BuildFTtransferCode(token.CodeScript, toAddress).Bytes())
		fromUTXOs, err := api.FetchFtUTXOList(contractTxid, fromAddr.AddressString, fromCodeHex, network)
		if err != nil {
			return fmt.Errorf("来源 UTXO 查询失败: %w", err)
		}
		toUTXOs, err := api.FetchFtUTXOList(contractTxid, toAddress, toCodeHex, network)
		if err != nil {
			return fmt.Errorf("目标 UTXO 查询失败: %w", err)
		}
		fromAfterFinal = fromAfter
		toAfterFinal = toAfter
		fromUTXOsFinal = fromUTXOs
		toUTXOsFinal = toUTXOs
		return nil
	})
	if err != nil {
		t.Fatalf("Transfer 后 API 余额/UTXO 校验失败: %v", err)
	}
	t.Logf("Transfer 广播后(索引同步后) 来源 address=%s balance_before=%s balance_after=%s utxos=%d details=%s",
		fromAddr.AddressString, fromBefore.String(), fromAfterFinal.String(), len(fromUTXOsFinal), summarizeFtUTXOs(fromUTXOsFinal, 5))
	t.Logf("Transfer 广播后(索引同步后) 目标 address=%s balance_before=%s balance_after=%s utxos=%d details=%s",
		toAddress, toBefore.String(), toAfterFinal.String(), len(toUTXOsFinal), summarizeFtUTXOs(toUTXOsFinal, 5))
}

// TestFT_Integration_BatchTransfer_Broadcast 对应 ft.md BatchTransfer。
func TestFT_Integration_BatchTransfer_Broadcast(t *testing.T) {
	requireRealRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	contractTxid := resolveContractTxidForIntegration(t, network, privKey)
	receiversRaw := mustEnv(t, "FT_BATCH_RECEIVERS")
	receivers := parseBatchReceivers(t, receiversRaw)

	token := loadTokenForIntegration(t, network, contractTxid)
	fromAddr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成来源地址失败: %v", err)
	}

	var receiversSlice []AddressAmount
	var sum float64
	for addr, amt := range receivers {
		sum += amt
		receiversSlice = append(receiversSlice, AddressAmount{
			Address: addr,
			Amount:  strconv.FormatFloat(amt, 'f', -1, 64),
		})
	}
	sort.Slice(receiversSlice, func(i, j int) bool {
		return receiversSlice[i].Address < receiversSlice[j].Address
	})
	needBN := big.NewInt(int64(sum * math.Pow10(token.Decimal)))

	ftCodeScript := BuildFTtransferCode(token.CodeScript, fromAddr.AddressString)
	ftutxos, err := api.FetchFtUTXOs(contractTxid, fromAddr.AddressString, hex.EncodeToString(ftCodeScript.Bytes()), network, needBN)
	if err != nil {
		t.Fatalf("FetchFtUTXOs: %v", err)
	}
	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxDatas := make([]string, len(ftutxos))
	for i := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(ftutxos[i].TxID, network)
		if err != nil {
			t.Fatalf("FetchTXRaw(%s): %v", ftutxos[i].TxID, err)
		}
		prepreTxDatas[i], err = fetchFtPrePreTxDataWithRetry(preTXs[i], int(ftutxos[i].Vout), network, 8, 1*time.Second)
		if err != nil {
			t.Fatalf("FetchFtPrePreTxData(vout=%d) 重试后仍失败: %v", ftutxos[i].Vout, err)
		}
	}

	feeNeed := 0.005 * float64(len(receiversSlice))
	feeUTXO, err := api.FetchUTXO(fromAddr.AddressString, feeNeed, network)
	if err != nil {
		t.Fatalf("FetchUTXO(for fee): %v", err)
	}

	txraws, err := token.BatchTransfer(privKey, receiversSlice, ftutxos, feeUTXO, preTXs, prepreTxDatas)
	if err != nil {
		t.Fatalf("BatchTransfer: %v", err)
	}
	if len(txraws) == 0 {
		t.Fatal("BatchTransfer 返回空交易列表")
	}

	fromBatchBefore, err := getFTBalanceBN(network, contractTxid, fromAddr.AddressString)
	if err != nil {
		t.Fatalf("BatchTransfer 广播前查询来源余额失败: %v", err)
	}
	t.Logf("BatchTransfer 广播前 from=%s balance=%s receivers=%d", fromAddr.AddressString, fromBatchBefore.String(), len(receivers))
	for _, recvAddr := range sortedReceiverAddrs(receivers) {
		logFTBalanceSnap(t, "BatchTransfer 广播前", network, contractTxid, recvAddr)
	}

	batchItems := make([]api.BroadcastTXsRequestItem, len(txraws))
	for i, r := range txraws {
		batchItems[i] = api.BroadcastTXsRequestItem{TxRaw: r}
	}
	success, failed, err := api.BroadcastTXsRaw(batchItems, network)
	if err != nil {
		t.Fatalf("广播 BatchTransfer 交易失败: %v", err)
	}
	t.Logf("BatchTransfer 广播完成 success=%d failed=%d", success, failed)
	t.Logf("BatchTransfer 广播后(即时查询，索引可能尚未更新)")
	logFTBalanceSnap(t, "BatchTransfer 广播后(即时)", network, contractTxid, fromAddr.AddressString)
	for _, recvAddr := range sortedReceiverAddrs(receivers) {
		logFTBalanceSnap(t, "BatchTransfer 广播后(即时)", network, contractTxid, recvAddr)
	}
}

// TestFT_Integration_Merge_Broadcast 对应 ft.md Merge。
func TestFT_Integration_Merge_Broadcast(t *testing.T) {
	requireRealRun(t)
	network := mustEnvOrConst(t, "TBC_NETWORK", defaultNetwork)
	privKey := loadPrivKey(t)
	contractTxid := resolveContractTxidForIntegration(t, network, privKey)
	mergeInputCountRaw := envOrDefault("FT_MERGE_INPUT_COUNT", "5")
	mergeInputCount, err := strconv.Atoi(mergeInputCountRaw)
	if err != nil || mergeInputCount < 2 {
		t.Fatalf("FT_MERGE_INPUT_COUNT 至少为 2，当前=%q", mergeInputCountRaw)
	}

	token := loadTokenForIntegration(t, network, contractTxid)
	fromAddr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("生成来源地址失败: %v", err)
	}

	ftCodeScript := BuildFTtransferCode(token.CodeScript, fromAddr.AddressString)
	allFTUTXOs, err := api.FetchFtUTXOList(contractTxid, fromAddr.AddressString, hex.EncodeToString(ftCodeScript.Bytes()), network)
	if err != nil {
		t.Fatalf("FetchFtUTXOList: %v", err)
	}
	if len(allFTUTXOs) < 2 {
		t.Fatalf("可用 FT UTXO 少于 2，无法 merge，当前=%d", len(allFTUTXOs))
	}
	sort.Slice(allFTUTXOs, func(i, j int) bool {
		ai, _ := new(big.Int).SetString(allFTUTXOs[i].FtBalance, 10)
		aj, _ := new(big.Int).SetString(allFTUTXOs[j].FtBalance, 10)
		if ai == nil || aj == nil {
			return i < j
		}
		return ai.Cmp(aj) > 0
	})
	if mergeInputCount > len(allFTUTXOs) {
		mergeInputCount = len(allFTUTXOs)
	}
	ftutxos := allFTUTXOs[:mergeInputCount]

	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxDatas := make([]string, len(ftutxos))
	for i := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(ftutxos[i].TxID, network)
		if err != nil {
			t.Fatalf("FetchTXRaw(%s): %v", ftutxos[i].TxID, err)
		}
		prepreTxDatas[i], err = fetchFtPrePreTxDataWithRetry(preTXs[i], int(ftutxos[i].Vout), network, 8, 1*time.Second)
		if err != nil {
			t.Fatalf("FetchFtPrePreTxData(vout=%d) 重试后仍失败: %v", ftutxos[i].Vout, err)
		}
	}

	mergeFee := 0.005 * float64(len(ftutxos))
	feeUTXO, err := api.FetchUTXO(fromAddr.AddressString, mergeFee, network)
	if err != nil {
		t.Fatalf("FetchUTXO(for merge fee): %v", err)
	}

	txraws, err := token.MergeFT(privKey, ftutxos, feeUTXO, preTXs, prepreTxDatas, nil)
	if err != nil {
		t.Fatalf("MergeFT: %v", err)
	}
	if len(txraws) == 0 {
		t.Fatal("MergeFT 返回空交易列表")
	}

	mergeBefore, err := getFTBalanceBN(network, contractTxid, fromAddr.AddressString)
	if err != nil {
		t.Fatalf("MergeFT 广播前查询余额失败: %v", err)
	}
	t.Logf("MergeFT 广播前 from=%s balance=%s merge_inputs=%d", fromAddr.AddressString, mergeBefore.String(), len(ftutxos))

	mergeItems := make([]api.BroadcastTXsRequestItem, len(txraws))
	for i, r := range txraws {
		mergeItems[i] = api.BroadcastTXsRequestItem{TxRaw: r}
	}
	success, failed, err := api.BroadcastTXsRaw(mergeItems, network)
	if err != nil {
		t.Fatalf("广播 MergeFT 交易失败: %v", err)
	}
	t.Logf("MergeFT 广播完成 success=%d failed=%d", success, failed)
	t.Logf("MergeFT 广播后(即时查询，索引可能尚未更新)")
	logFTBalanceSnap(t, "MergeFT 广播后(即时)", network, contractTxid, fromAddr.AddressString)
	if failed > 0 {
		t.Fatalf("MergeFT 存在失败广播: success=%d failed=%d", success, failed)
	}
}
