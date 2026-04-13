package util

import (
	"encoding/hex"
	"errors"
	"math/big"
	"regexp"
	"strings"

	"github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

var reSHA256Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var reHex = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// ParseDecimalToBigInt 与 tbc-contract/lib/util parseDecimalToBigInt 一致
func ParseDecimalToBigInt(amount string, decimal int) *big.Int {
	s := strings.TrimSpace(amount)
	parts := strings.SplitN(s, ".", 2)
	intPart := parts[0]
	if intPart == "" {
		intPart = "0"
	}
	frac := ""
	if len(parts) > 1 {
		frac = parts[1]
	}
	for len(frac) < decimal {
		frac += "0"
	}
	if len(frac) > decimal {
		frac = frac[:decimal]
	}
	combined := intPart + frac
	n := new(big.Int)
	if _, ok := n.SetString(combined, 10); !ok {
		return big.NewInt(0)
	}
	return n
}

// IsValidSHA256Hash 对应 util._isValidSHA256Hash
func IsValidSHA256Hash(s string) bool {
	return reSHA256Hex.MatchString(s)
}

// IsValidHexString 对应 util._isValidHexString
func IsValidHexString(s string) bool {
	if s == "" {
		return false
	}
	return reHex.MatchString(s)
}

var (
	ErrOutputNotExist       = errors.New("output at index does not exist")
	ErrTapeTooShort         = errors.New("tape script too short")
	ErrInputIndexOutOfRange = errors.New("input index out of range")
)

// FtUnspentOutput 扩展 UTXO，包含 FT 余额，用于 transfer 等操作
type FtUnspentOutput struct {
	*bt.UTXO
	FtBalance string
}

// BuildUTXO 从交易构建 UTXO，用于 FT 或普通
// 对应 JS util.buildUTXO
func BuildUTXO(tx *bt.Tx, vout int, isFT bool) (*FtUnspentOutput, error) {
	if vout >= len(tx.Outputs) {
		return nil, ErrOutputNotExist
	}
	output := tx.Outputs[vout]
	var ftBalance string
	if isFT && vout+1 < len(tx.Outputs) {
		ftBalance = GetFtBalanceFromTape(tx.Outputs[vout+1].LockingScript.String())
	} else {
		ftBalance = "0"
	}
	txID, err := hex.DecodeString(tx.TxID())
	if err != nil {
		return nil, err
	}
	return &FtUnspentOutput{
		UTXO: &bt.UTXO{
			TxID:          txID,
			Vout:          uint32(vout),
			LockingScript: output.LockingScript,
			Satoshis:      output.Satoshis,
		},
		FtBalance: ftBalance,
	}, nil
}

// BuildFtPrePreTxData 构建 FT 的 pre-pre 交易数据
// 对应 JS util.buildFtPrePreTxData
func BuildFtPrePreTxData(preTX *bt.Tx, preTxVout int, localTXs []*bt.Tx) (string, error) {
	if preTxVout+1 >= len(preTX.Outputs) {
		return "", ErrOutputNotExist
	}
	tapeScript := preTX.Outputs[preTxVout+1].LockingScript.Bytes()
	if len(tapeScript) < 51 {
		return "", ErrTapeTooShort
	}
	tapeSlice := tapeScript[3:51]
	tapeHex := hex.EncodeToString(tapeSlice)

	var prepretxdata string
	for i := len(tapeHex) - 16; i >= 0; i -= 16 {
		chunk := tapeHex[i : i+16]
		if chunk != "0000000000000000" {
			inputIndex := i / 16
			if inputIndex >= len(preTX.Inputs) {
				return "", ErrInputIndexOutOfRange
			}
			// 与 Tx.TxID() / 浏览器 txid 一致，与 api.FetchFtPrePreTxData 中 FetchTXRaw 所用格式一致
			prevTxID := hex.EncodeToString(preTX.Inputs[inputIndex].PreviousTxID())
			prepreTX := SelectTXFromLocal(localTXs, prevTxID)
			data, err := bt.GetPrePreTxdata(prepreTX, int(preTX.Inputs[inputIndex].PreviousTxOutIndex))
			if err != nil {
				return "", err
			}
			// 与 tbc-contract/lib/util/util.ts buildFtPrePreTxData 及 api.fetchFtPrePreTxData 一致：append 拼接（勿 prepend）。
			prepretxdata += data
		}
	}
	return "57" + prepretxdata, nil
}

// SelectTXFromLocal 从本地交易列表中找到指定 txid 的交易
// 对应 JS util.selectTXfromLocal
func SelectTXFromLocal(txs []*bt.Tx, txid string) *bt.Tx {
	for _, tx := range txs {
		if tx.TxID() == txid {
			return tx
		}
	}
	panic("transaction not found: " + txid)
}

// GetFtBalanceFromTape 从 tape 脚本中提取 FT 余额（支持大数）
// 对应 JS util.getFtBalanceFromTape
func GetFtBalanceFromTape(tapeHex string) string {
	data, err := hex.DecodeString(tapeHex)
	if err != nil || len(data) < 51 {
		return "0"
	}
	tapeSlice := data[3 : 3+48]
	balance := new(big.Int)
	for i := 0; i < 6; i++ {
		if i*8+8 <= len(tapeSlice) {
			var v uint64
			for j := 0; j < 8; j++ {
				v |= uint64(tapeSlice[i*8+j]) << (j * 8)
			}
			balance.Add(balance, new(big.Int).SetUint64(v))
		}
	}
	return balance.String()
}

// FtUTXOToUTXO 将 FtUTXO 转为 bt.UTXO 用于交易输入
func FtUTXOToUTXO(f *bt.FtUTXO) (*bt.UTXO, error) {
	txID, err := hex.DecodeString(f.TxID)
	if err != nil {
		return nil, err
	}
	script, err := bscript.NewFromHexString(f.Script)
	if err != nil {
		return nil, err
	}
	return &bt.UTXO{
		TxID:          txID,
		Vout:          f.Vout,
		LockingScript: script,
		Satoshis:      f.Satoshis,
	}, nil
}

// FtUTXOsToUTXOs 批量转换
func FtUTXOsToUTXOs(list []*bt.FtUTXO) ([]*bt.UTXO, error) {
	result := make([]*bt.UTXO, len(list))
	for i, f := range list {
		u, err := FtUTXOToUTXO(f)
		if err != nil {
			return nil, err
		}
		result[i] = u
	}
	return result, nil
}
