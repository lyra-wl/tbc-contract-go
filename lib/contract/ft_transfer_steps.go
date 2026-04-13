package contract

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/libsv/go-bk/bec"
	"github.com/libsv/go-bk/crypto"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/sighash"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

// FTTransferStepReport 与 tbc-contract JS 侧对照用的四步诊断（BSV 语境下对应常见「账户模型」排查顺序）：
//
//	1 — 字段：version / locktime / 各输入 prevout+sequence+上一笔 sat 与 locking script / 各输出 sat+locking script
//	2 — 线格式（全部输入 unlocking script 为空）的 SHA256d(wire)，类比「仅结构序列化」的哈希
//	3 — 各输入 BIP143+ForkID 的签名 digest（与 CalcInputSignatureHash 一致，即 SHA256d(preimage)）
//	4 — 最终 txid 与 raw hex
type FTTransferStepReport struct {
	Step1 *FTStep1Fields `json:"step1_fields"`
	// Step2WireEmptyScriptsSHA256dHex 全输入 scriptSig 为空时的整笔交易线格式 double-SHA256（非 txid；txid 还含反转等）
	Step2WireEmptyScriptsSHA256dHex string   `json:"step2_wire_empty_scripts_sha256d_hex"`
	Step3SighashDigestHex           []string `json:"step3_sighash_digest_sha256d_per_input"`
	Step4Txid                       string   `json:"step4_txid"`
	Step4RawHex                     string   `json:"step4_raw_hex"`
}

// FTStep1Fields 不含 unlocking script（签名产物），便于与对端「构造是否一致」对比。
type FTStep1Fields struct {
	Version  uint32          `json:"version"`
	Locktime uint32          `json:"locktime"`
	Inputs   []FTStep1Input  `json:"inputs"`
	Outputs  []FTStep1Output `json:"outputs"`
	FeeSats  int64           `json:"fee_satoshis"`
	InTotal  uint64          `json:"input_satoshis_total"`
	OutTotal uint64          `json:"output_satoshis_total"`
}

type FTStep1Input struct {
	PrevTxID             string `json:"prev_txid"`
	Vout                 uint32 `json:"vout"`
	Sequence             uint32 `json:"sequence"`
	PrevSatoshis         uint64 `json:"prev_satoshis"`
	PrevLockingScriptHex string `json:"prev_locking_script_hex"`
}

type FTStep1Output struct {
	Satoshis         uint64 `json:"satoshis"`
	LockingScriptHex string `json:"locking_script_hex"`
}

func summarizeFTStep1(tx *bt.Tx) *FTStep1Fields {
	s := &FTStep1Fields{
		Version:  tx.Version,
		Locktime: tx.LockTime,
		Inputs:   make([]FTStep1Input, len(tx.Inputs)),
		Outputs:  make([]FTStep1Output, len(tx.Outputs)),
	}
	var inSum, outSum uint64
	for i, in := range tx.Inputs {
		lsHex := ""
		if in.PreviousTxScript != nil {
			lsHex = strings.ToLower(hex.EncodeToString(in.PreviousTxScript.Bytes()))
		}
		s.Inputs[i] = FTStep1Input{
			PrevTxID:             in.PreviousTxIDStr(),
			Vout:                 in.PreviousTxOutIndex,
			Sequence:             in.SequenceNumber,
			PrevSatoshis:         in.PreviousTxSatoshis,
			PrevLockingScriptHex: lsHex,
		}
		inSum += in.PreviousTxSatoshis
	}
	for i, out := range tx.Outputs {
		lsHex := ""
		if out.LockingScript != nil {
			lsHex = strings.ToLower(hex.EncodeToString(out.LockingScript.Bytes()))
		}
		s.Outputs[i] = FTStep1Output{Satoshis: out.Satoshis, LockingScriptHex: lsHex}
		outSum += out.Satoshis
	}
	s.InTotal = inSum
	s.OutTotal = outSum
	s.FeeSats = int64(inSum) - int64(outSum)
	return s
}

func wireEmptyUnlockingScriptsSHA256dHex(tx *bt.Tx) (string, error) {
	c := tx.Clone()
	for _, in := range c.Inputs {
		in.UnlockingScript = nil
	}
	d := crypto.Sha256d(c.Bytes())
	return strings.ToLower(hex.EncodeToString(d)), nil
}

func allInputSighashDigestsHex(tx *bt.Tx) ([]string, error) {
	out := make([]string, len(tx.Inputs))
	for i := range tx.Inputs {
		sh, err := tx.CalcInputSignatureHash(uint32(i), sighash.AllForkID)
		if err != nil {
			return nil, fmt.Errorf("input %d sighash: %w", i, err)
		}
		out[i] = strings.ToLower(hex.EncodeToString(sh))
	}
	return out, nil
}

func fillFTTransferStepReport(tx *bt.Tx, rep *FTTransferStepReport) error {
	if rep == nil {
		return nil
	}
	rep.Step1 = summarizeFTStep1(tx)
	var err error
	rep.Step2WireEmptyScriptsSHA256dHex, err = wireEmptyUnlockingScriptsSHA256dHex(tx)
	if err != nil {
		return err
	}
	rep.Step3SighashDigestHex, err = allInputSighashDigestsHex(tx)
	return err
}

// TransferDecimalStringWithStepReport 与 TransferDecimalString 相同，并填充 FTTransferStepReport（签名前填 1～3，完成后填 4）。
func (f *FT) TransferDecimalStringWithStepReport(privKey *bec.PrivateKey, addressTo, amountDecimal string,
	ftutxos []*bt.FtUTXO, utxo *bt.UTXO, preTX []*bt.Tx, prepreTxData []string, tbcAmountSat uint64) (*FTTransferStepReport, string, error) {

	s := strings.TrimSpace(amountDecimal)
	if s == "" {
		return nil, "", fmt.Errorf("empty transfer amount")
	}
	if strings.HasPrefix(s, "-") {
		return nil, "", fmt.Errorf("invalid amount input")
	}
	amountBNInt := util.ParseDecimalToBigInt(s, f.Decimal)
	ftAmtFloat, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, "", fmt.Errorf("invalid amount: %w", err)
	}
	rep := &FTTransferStepReport{}
	raw, err := f.transferWithAmountBN(privKey, addressTo, amountBNInt, s, ftAmtFloat, ftutxos, utxo, preTX, prepreTxData, tbcAmountSat, rep)
	if err != nil {
		return nil, "", err
	}
	return rep, raw, nil
}
