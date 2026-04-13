// Package fttransfertestts 实现与 tbc-contract/scripts/test.ts 中 Transfer 代码块（约 71–114 行）
// 同序的链上数据拉取与 FT 转账组装：FetchFtInfo → initialize → BuildFTtransferCode(from) →
// FetchFtUTXOs → 各输入 FetchTXRaw + FetchFtPrePreTxData → FetchUTXO(from, tbc_amount+0.01) 或
// LOCKSTEP 手续费 → Transfer（仅 FT、tbc_amount=0）。
//
// 供 cmd/ft_transfer_compare、cmd/ft_transfer_ts_mirror 复用，避免与 JS 比对时两处逻辑漂移。
package fttransfertestts

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/libsv/go-bk/crypto"
	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/api"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/contract"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

// InputReport 单笔输入的解锁脚本与 prevout 信息（与 cmd/ft_transfer_compare JSON 字段一致）。
type InputReport struct {
	Index              int    `json:"index"`
	UnlockingScriptHex string `json:"unlocking_script_hex"`
	PrevTxID           string `json:"prev_txid"`
	Vout               uint32 `json:"vout"`
}

// Result 组装成功或失败时的统一输出结构（供 stdout JSON / raw-only 分支使用）。
type Result struct {
	OK     string                         `json:"ok,omitempty"`
	Error  string                         `json:"error,omitempty"`
	RawHex string                         `json:"raw_hex,omitempty"`
	TxID   string                         `json:"txid,omitempty"`
	Inputs []InputReport                  `json:"inputs,omitempty"`
	Steps  *contract.FTTransferStepReport `json:"steps,omitempty"`
}

// Config 与 tbc-contract/scripts/test.ts Transfer 段参数对应（可在 cmd 里写死常量后传入）。
type Config struct {
	Network           string
	PrivateKeyWIF     string
	FTContractTxid    string
	TransferToAddress string
	// TransferAmount 代币数量十进制字符串，如 "1000"（与 test.ts transferTokenAmount 一致）
	TransferAmount string
	// FeeFetchTBC 对应 test.ts fetchUTXO 第二参 tbc_amount+0.01；仅转 FT 时为 0.01。≤0 时按 0.01。
	FeeFetchTBC float64
	// 指定手续费 prevout 时填写（与 JS FT_LOCKSTEP_* 一致）；Txid 为空则改为 FetchUTXO(from, FeeFetchTBC)
	LockstepFeeTxid string
	LockstepFeeVout int
	RespectFeeEnv   bool // true 时不强制 FT_FEE_SAT_PER_KB=80
	WantStepReport  bool // true 时填充 Result.Steps
}

func envOr(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v != "" {
		return v
	}
	return def
}

// EnvFirst 返回第一个非空环境变量（与 tbc-contract JS 别名一致）。
func EnvFirst(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func fail(msg string) Result {
	return Result{OK: "0", Error: msg}
}

func feeUTXOFromLockstepPrevout(network, txidStr string, vout int) (*bt.UTXO, error) {
	txidStr = strings.TrimSpace(txidStr)
	if txidStr == "" || vout < 0 {
		return nil, fmt.Errorf("invalid lockstep fee prevout")
	}
	raw, err := api.FetchTXRaw(txidStr, network)
	if err != nil {
		return nil, err
	}
	if vout >= len(raw.Outputs) {
		return nil, fmt.Errorf("fee vout %d out of range (outputs=%d)", vout, len(raw.Outputs))
	}
	out := raw.Outputs[vout]
	tid, err := hex.DecodeString(txidStr)
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID:          tid,
		Vout:          uint32(vout),
		LockingScript: out.LockingScript,
		Satoshis:      out.Satoshis,
	}, nil
}

func fetchFtPrePreWithRetry(preTx *bt.Tx, preTxVout int, network string, maxAttempts int, interval time.Duration) (string, error) {
	var last error
	for i := 0; i < maxAttempts; i++ {
		d, err := api.FetchFtPrePreTxData(preTx, preTxVout, network)
		if err == nil {
			return d, nil
		}
		last = err
		time.Sleep(interval)
	}
	return "", last
}

// AssembleFromEnvironment 从当前进程环境变量读取参数并组装转账（与 test.ts Transfer 块等价）。
func AssembleFromEnvironment() Result {
	vv, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("FT_LOCKSTEP_FEE_VOUT")))
	am := strings.TrimSpace(EnvFirst("FT_TRANSFER_AMOUNT", "TBC_FT_TRANSFER_AMOUNT"))
	if am == "" {
		am = "1000"
	}
	return AssembleWithConfig(Config{
		Network:           envOr("TBC_NETWORK", "testnet"),
		PrivateKeyWIF:     EnvFirst("TBC_PRIVATE_KEY", "TBC_PRIVKEY"),
		FTContractTxid:    EnvFirst("FT_CONTRACT_TXID", "TBC_FT_CONTRACT_TXID"),
		TransferToAddress: EnvFirst("FT_TRANSFER_TO", "TBC_FT_TRANSFER_TO"),
		TransferAmount:    am,
		FeeFetchTBC:       0.01,
		LockstepFeeTxid:   strings.TrimSpace(os.Getenv("FT_LOCKSTEP_FEE_TXID")),
		LockstepFeeVout:   vv,
		RespectFeeEnv:     strings.TrimSpace(os.Getenv("FT_COMPARE_RESPECT_FEE_ENV")) == "1",
		WantStepReport:    strings.TrimSpace(os.Getenv("FT_TRANSFER_COMPARE_STEPS")) == "1",
	})
}

// AssembleWithConfig 按给定配置组装（与 test.ts Transfer 块同序）。
// 成功：OK=="1"；失败：OK=="0"，Error 为原因说明。
func AssembleWithConfig(cfg Config) Result {
	if !cfg.RespectFeeEnv {
		_ = os.Setenv("FT_FEE_SAT_PER_KB", "80")
	}

	wifStr := strings.TrimSpace(cfg.PrivateKeyWIF)
	if wifStr == "" {
		return fail("PrivateKeyWIF 为空")
	}
	decoded, err := wif.DecodeWIF(wifStr)
	if err != nil {
		return fail(fmt.Sprintf("解析 WIF: %v", err))
	}
	privKey := decoded.PrivKey

	network := strings.TrimSpace(cfg.Network)
	if network == "" {
		network = "testnet"
	}
	contractTxid := strings.TrimSpace(cfg.FTContractTxid)
	if contractTxid == "" {
		return fail("FTContractTxid 为空")
	}
	toAddr := strings.TrimSpace(cfg.TransferToAddress)
	if toAddr == "" {
		return fail("TransferToAddress 为空")
	}
	amtStr := strings.TrimSpace(cfg.TransferAmount)
	if amtStr == "" {
		amtStr = "1000"
	}
	feeTBC := cfg.FeeFetchTBC
	if feeTBC <= 0 {
		feeTBC = 0.01
	}

	token, err := contract.NewFT(contractTxid)
	if err != nil {
		return fail(fmt.Sprintf("NewFT: %v", err))
	}
	info, err := api.FetchFtInfo(contractTxid, network)
	if err != nil {
		return fail(fmt.Sprintf("FetchFtInfo: %v", err))
	}
	totalSupply, ok := new(big.Int).SetString(info.TotalSupply, 10)
	if !ok {
		return fail("非法 TotalSupply: " + info.TotalSupply)
	}
	token.Initialize(&contract.FtInfo{
		Name:        info.Name,
		Symbol:      info.Symbol,
		Decimal:     int(info.Decimal),
		TotalSupply: totalSupply.Int64(),
		CodeScript:  info.CodeScript,
		TapeScript:  info.TapeScript,
	})

	fromAddr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return fail(fmt.Sprintf("来源地址: %v", err))
	}

	ftCodeScript := contract.BuildFTtransferCode(token.CodeScript, fromAddr.AddressString)
	amountBN := util.ParseDecimalToBigInt(amtStr, token.Decimal)
	if amountBN == nil || amountBN.Sign() <= 0 {
		return fail(fmt.Sprintf("FT_TRANSFER_AMOUNT 须为正数: %q", amtStr))
	}
	maxHuman := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(18-token.Decimal)), nil)
	humanAmt, ok := new(big.Rat).SetString(amtStr)
	if !ok {
		return fail(fmt.Sprintf("FT_TRANSFER_AMOUNT 解析失败: %q", amtStr))
	}
	if humanAmt.Cmp(new(big.Rat).SetInt(maxHuman)) > 0 {
		return fail(fmt.Sprintf("金额超过 decimal=%d 下上限（与 JS 一致：10^%d）", token.Decimal, 18-token.Decimal))
	}

	ftutxos, err := api.FetchFtUTXOs(
		contractTxid,
		fromAddr.AddressString,
		hex.EncodeToString(ftCodeScript.Bytes()),
		network,
		amountBN,
	)
	if err != nil {
		return fail(fmt.Sprintf("FetchFtUTXOs: %v", err))
	}
	if len(ftutxos) == 0 {
		return fail("没有可用 FT UTXO")
	}

	preTXs := make([]*bt.Tx, len(ftutxos))
	prepreTxDatas := make([]string, len(ftutxos))
	for i := range ftutxos {
		preTXs[i], err = api.FetchTXRaw(ftutxos[i].TxID, network)
		if err != nil {
			return fail(fmt.Sprintf("FetchTXRaw(%s): %v", ftutxos[i].TxID, err))
		}
		prepreTxDatas[i], err = fetchFtPrePreWithRetry(preTXs[i], int(ftutxos[i].Vout), network, 8, time.Second)
		if err != nil {
			return fail(fmt.Sprintf("FetchFtPrePreTxData: %v", err))
		}
	}

	// test.ts: tbc_amount=0 → fetchUTXO(privateKeyA, 0.01, network)
	var feeUTXO *bt.UTXO
	if lockTx := strings.TrimSpace(cfg.LockstepFeeTxid); lockTx != "" {
		feeUTXO, err = feeUTXOFromLockstepPrevout(network, lockTx, cfg.LockstepFeeVout)
		if err != nil {
			return fail(fmt.Sprintf("手续费 UTXO(LOCKSTEP): %v", err))
		}
	} else {
		feeUTXO, err = api.FetchUTXO(fromAddr.AddressString, feeTBC, network)
		if err != nil {
			return fail(fmt.Sprintf("FetchUTXO: %v", err))
		}
	}

	var raw string
	var stepRep *contract.FTTransferStepReport
	if cfg.WantStepReport {
		var serr error
		stepRep, raw, serr = token.TransferDecimalStringWithStepReport(privKey, toAddr, amtStr, ftutxos, feeUTXO, preTXs, prepreTxDatas, 0)
		if serr != nil {
			return fail(fmt.Sprintf("Transfer: %v", serr))
		}
	} else {
		var terr error
		raw, terr = token.TransferDecimalString(privKey, toAddr, amtStr, ftutxos, feeUTXO, preTXs, prepreTxDatas, 0)
		if terr != nil {
			return fail(fmt.Sprintf("Transfer: %v", terr))
		}
	}

	tx, err := bt.NewTxFromString(raw)
	if err != nil {
		return fail(fmt.Sprintf("解析 raw: %v", err))
	}

	// 标准 wire txid = SHA256d(实际入网的整笔 raw 字节) 再按字节反转后 hex，与节点 / 浏览器一致。
	// 勿用 tx.TxID()：其哈希的是 go-bt 对内存交易再序列化的 Bytes()，若与组装端原始 hex 有编码差异，
	// 会得到与广播返回值不一致的「假预期」txid。
	rawBytes, derr := hex.DecodeString(strings.TrimSpace(raw))
	if derr != nil {
		return fail(fmt.Sprintf("raw hex decode: %v", derr))
	}
	wireTxid := strings.ToLower(hex.EncodeToString(bt.ReverseBytes(crypto.Sha256d(rawBytes))))

	rep := Result{
		OK:     "1",
		RawHex: strings.ToLower(raw),
		TxID:   wireTxid,
		Inputs: make([]InputReport, 0, len(tx.Inputs)),
		Steps:  stepRep,
	}
	for i, in := range tx.Inputs {
		us := ""
		if in.UnlockingScript != nil {
			us = hex.EncodeToString(in.UnlockingScript.Bytes())
		}
		rep.Inputs = append(rep.Inputs, InputReport{
			Index:              i,
			UnlockingScriptHex: strings.ToLower(us),
			PrevTxID:           in.PreviousTxIDStr(),
			Vout:               in.PreviousTxOutIndex,
		})
	}
	return rep
}
