// Package contract 提供 TBC 合约的 Go 实现
// 对应 tbc-contract 的 lib/contract
package contract

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/libsv/go-bk/bec"
	"github.com/libsv/go-bk/crypto"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/sighash"
	"github.com/sCrypt-Inc/go-bt/v2/unlocker"
	"github.com/sCrypt-Inc/tbc-contract-go/lib/util"
)

//go:embed ft_mint_template.asm
var ftMintTemplateASM string

// FtParams 新建 FT 时的参数
type FtParams struct {
	Name    string
	Symbol  string
	Amount  int64
	Decimal int
}

// FtInfo FT 代币信息，用于 Initialize
type FtInfo struct {
	Name        string
	Symbol      string
	Decimal     int
	TotalSupply int64
	CodeScript  string
	TapeScript  string
}

// FT 同质化代币合约
type FT struct {
	Name         string
	Symbol       string
	Decimal      int
	TotalSupply  int64
	CodeScript   string
	TapeScript   string
	ContractTxid string
}

// NewFT 创建 FT 实例，支持从 txid 或参数创建
func NewFT(txidOrParams interface{}) (*FT, error) {
	ft := &FT{}
	switch v := txidOrParams.(type) {
	case string:
		ft.ContractTxid = v
	case *FtParams:
		if v.Amount <= 0 {
			return nil, fmt.Errorf("amount must be a natural number")
		}
		if v.Decimal <= 0 || v.Decimal > 18 {
			return nil, fmt.Errorf("decimal must be 1-18")
		}
		maxAmount := int64(math.Floor(21 * math.Pow(10, float64(14-v.Decimal))))
		if v.Amount > maxAmount {
			return nil, fmt.Errorf("when decimal is %d, max amount cannot exceed %d", v.Decimal, maxAmount)
		}
		ft.Name = v.Name
		ft.Symbol = v.Symbol
		ft.Decimal = v.Decimal
		ft.TotalSupply = v.Amount
	default:
		return nil, fmt.Errorf("invalid constructor arguments")
	}
	return ft, nil
}

// Initialize 用 FtInfo 初始化
func (f *FT) Initialize(info *FtInfo) {
	f.Name = info.Name
	f.Symbol = info.Symbol
	f.Decimal = info.Decimal
	f.TotalSupply = info.TotalSupply
	f.CodeScript = info.CodeScript
	f.TapeScript = info.TapeScript
}

// MintFT 铸造新 FT，返回 [txSourceRaw, txMintRaw]
func (f *FT) MintFT(privKey *bec.PrivateKey, addressTo string, utxo *bt.UTXO) ([]string, error) {
	totalSupply := new(big.Int).Mul(
		big.NewInt(f.TotalSupply),
		new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(f.Decimal)), nil),
	)
	tapeAmount := writeTapeAmount(totalSupply)

	nameHex := hex.EncodeToString([]byte(f.Name))
	symbolHex := hex.EncodeToString([]byte(f.Symbol))
	decimalHex := fmt.Sprintf("%02x", f.Decimal)

	tapeASM := fmt.Sprintf("OP_FALSE OP_RETURN %s %s %s %s 4654617065", tapeAmount, decimalHex, nameHex, symbolHex)
	tapeScript, err := bscript.NewFromASM(tapeASM)
	if err != nil {
		return nil, err
	}
	tapeSize := len(tapeScript.Bytes())

	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return nil, err
	}
	pubKeyHash := addr.PublicKeyHash
	flagHex := hex.EncodeToString([]byte("for ft mint"))

	sourceOutputScript, _ := bscript.NewFromASM(fmt.Sprintf("OP_DUP OP_HASH160 %s OP_EQUALVERIFY OP_CHECKSIG OP_RETURN %s", pubKeyHash, flagHex))

	txSource := newFTTx()
	_ = txSource.FromUTXOs(utxo)
	txSource.AddOutput(&bt.Output{LockingScript: sourceOutputScript, Satoshis: 9900})
	txSource.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	feeQuote := newFeeQuote80()
	_ = txSource.ChangeToAddress(addr.AddressString, feeQuote)
	satPerKB := feeSatPerKBFromEnv()
	estSource := txSource.JSEstimateSize()
	if err := txSource.AdjustImplicitFeeToTarget(mintSourceTargetFeeSat(satPerKB, estSource)); err != nil {
		return nil, err
	}

	ctx := context.Background()
	if err := txSource.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return nil, err
	}

	txSourceRaw := hex.EncodeToString(txSource.Bytes())

	// 与 ft.ts getFTmintCode(txSource.hash, 0, …) 对齐：JS 的 hash 即 Transaction 展示用 txid
	//（v10：sha256d(newTxHeader) 后字节反序再 hex），与 (*bt.Tx).TxID() 相同，勿与「未反序的哈希」混淆。
	sourceTxHash := mintSourceTxHash(txSource)
	codeScript, err := getFTmintCode(sourceTxHash, 0, addressTo, tapeSize)
	if err != nil {
		return nil, err
	}
	f.CodeScript = hex.EncodeToString(codeScript.Bytes())
	f.TapeScript = hex.EncodeToString(tapeScript.Bytes())

	txMint := newFTTx()
	_ = txMint.From(sourceTxHash, 0, sourceOutputScript.String(), 9900)
	txMint.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	txMint.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	changeScript, err := bscript.NewP2PKHFromAddress(addr.AddressString)
	if err != nil {
		return nil, err
	}
	txMint.AddOutput(&bt.Output{LockingScript: changeScript, Satoshis: 0})

	if err := signP2PKHInput(txMint, privKey, 0); err != nil {
		return nil, err
	}

	mintActualBytes := len(txMint.Bytes())
	mintTargetFee := int(math.Ceil(float64(mintActualBytes) * float64(satPerKB) / 1000.0))
	mintInputSat := int(9900)
	mintNonChangeSat := int(txMint.Outputs[0].Satoshis + txMint.Outputs[1].Satoshis)
	mintChangeSat := mintInputSat - mintNonChangeSat - mintTargetFee
	if mintChangeSat < 0 {
		mintChangeSat = 0
	}
	txMint.Outputs[len(txMint.Outputs)-1].Satoshis = uint64(mintChangeSat)

	if err := signP2PKHInput(txMint, privKey, 0); err != nil {
		return nil, err
	}

	f.ContractTxid = txMint.TxID()
	return []string{txSourceRaw, hex.EncodeToString(txMint.Bytes())}, nil
}

// RebuildMintTxRawWithBroadcastSource 在 source 已上链且已知 sourceTxid 后重建 mint 原始 hex（须已成功调用 MintFT 以填充 TapeScript）。
func (f *FT) RebuildMintTxRawWithBroadcastSource(privKey *bec.PrivateKey, addressTo, sourceTxid, sourceTxRaw string) (string, error) {
	addr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		return "", err
	}
	tapeScript, err := bscript.NewFromHexString(f.TapeScript)
	if err != nil {
		return "", err
	}
	codeScript, err := getFTmintCode(sourceTxid, 0, addressTo, len(tapeScript.Bytes()))
	if err != nil {
		return "", err
	}

	sourceTx, err := bt.NewTxFromString(sourceTxRaw)
	if err != nil {
		return "", err
	}
	if len(sourceTx.Outputs) == 0 {
		return "", fmt.Errorf("source tx outputs is empty")
	}
	sourceOutputScript := sourceTx.Outputs[0].LockingScript
	sourceOutputSats := sourceTx.Outputs[0].Satoshis

	txMint := newFTTx()
	_ = txMint.From(sourceTxid, 0, sourceOutputScript.String(), sourceOutputSats)
	txMint.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	txMint.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	changeScript, err := bscript.NewP2PKHFromAddress(addr.AddressString)
	if err != nil {
		return "", err
	}
	txMint.AddOutput(&bt.Output{LockingScript: changeScript, Satoshis: 0})

	if err := signP2PKHInput(txMint, privKey, 0); err != nil {
		return "", err
	}

	satPerKB := feeSatPerKBFromEnv()
	mintActualBytes := len(txMint.Bytes())
	mintTargetFee := int(math.Ceil(float64(mintActualBytes) * float64(satPerKB) / 1000.0))
	mintInputSat := int(sourceOutputSats)
	mintNonChangeSat := int(txMint.Outputs[0].Satoshis + txMint.Outputs[1].Satoshis)
	mintChangeSat := mintInputSat - mintNonChangeSat - mintTargetFee
	if mintChangeSat < 0 {
		mintChangeSat = 0
	}
	txMint.Outputs[len(txMint.Outputs)-1].Satoshis = uint64(mintChangeSat)

	if err := signP2PKHInput(txMint, privKey, 0); err != nil {
		return "", err
	}

	f.CodeScript = hex.EncodeToString(codeScript.Bytes())
	f.ContractTxid = txMint.TxID()
	return hex.EncodeToString(txMint.Bytes()), nil
}

// Transfer 转移 FT（金额经 float64；与 JS 完全一致请优先用 TransferDecimalString + 环境变量原始字符串）。
func (f *FT) Transfer(privKey *bec.PrivateKey, addressTo string, ftAmount float64,
	ftutxos []*bt.FtUTXO, utxo *bt.UTXO, preTX []*bt.Tx, prepreTxData []string, tbcAmountSat uint64) (string, error) {

	if ftAmount < 0 {
		return "", fmt.Errorf("invalid amount input")
	}
	amountBNInt := util.ParseDecimalToBigInt(strconv.FormatFloat(ftAmount, 'f', -1, 64), f.Decimal)
	return f.transferWithAmountBN(privKey, addressTo, amountBNInt, "", ftAmount, ftutxos, utxo, preTX, prepreTxData, tbcAmountSat, nil)
}

// TransferDecimalString 与 tbc-contract FT.transfer 中 parseDecimalToBigInt(ft_amount, decimal) 同源：
// 使用十进制字符串（如环境变量 FT_TRANSFER_AMOUNT 原文），不经 float64 中间值，避免与 JS 在多位小数或极大整数上分歧。
func (f *FT) TransferDecimalString(privKey *bec.PrivateKey, addressTo, amountDecimal string,
	ftutxos []*bt.FtUTXO, utxo *bt.UTXO, preTX []*bt.Tx, prepreTxData []string, tbcAmountSat uint64) (string, error) {

	s := strings.TrimSpace(amountDecimal)
	if s == "" {
		return "", fmt.Errorf("empty transfer amount")
	}
	if strings.HasPrefix(s, "-") {
		return "", fmt.Errorf("invalid amount input")
	}
	amountBNInt := util.ParseDecimalToBigInt(s, f.Decimal)
	ftAmtFloat, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("invalid amount: %w", err)
	}
	return f.transferWithAmountBN(privKey, addressTo, amountBNInt, s, ftAmtFloat, ftutxos, utxo, preTX, prepreTxData, tbcAmountSat, nil)
}

// checkFTAmountHumanJSMax 与 ft.ts：Number(ft_amount) > Number(parseDecimalToBigInt(1, 18-decimal))。
func checkFTAmountHumanJSMax(decimal int, humanAmount string) error {
	maxHuman := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(18-decimal)), nil)
	human, ok := new(big.Rat).SetString(strings.TrimSpace(humanAmount))
	if !ok {
		return fmt.Errorf("invalid amount for max check")
	}
	if human.Cmp(new(big.Rat).SetInt(maxHuman)) > 0 {
		return fmt.Errorf("when decimal is %d, max amount cannot exceed 10^%d (JS FT.transfer)", decimal, 18-decimal)
	}
	return nil
}

func (f *FT) transferWithAmountBN(privKey *bec.PrivateKey, addressTo string, amountBNInt *big.Int, amountHumanStr string, ftAmountForMax float64,
	ftutxos []*bt.FtUTXO, utxo *bt.UTXO, preTX []*bt.Tx, prepreTxData []string, tbcAmountSat uint64, stepReport *FTTransferStepReport) (string, error) {

	addr, _ := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	addressFrom := addr.AddressString

	tapeAmountSet := make([]*big.Int, len(ftutxos))
	tapeAmountSum := new(big.Int)
	for i, fu := range ftutxos {
		b, ok := new(big.Int).SetString(strings.TrimSpace(fu.FtBalance), 10)
		if !ok {
			return "", fmt.Errorf("invalid FtBalance %q for utxo %s vout %d", fu.FtBalance, fu.TxID, fu.Vout)
		}
		tapeAmountSet[i] = b
		tapeAmountSum.Add(tapeAmountSum, b)
	}
	if amountBNInt.Cmp(tapeAmountSum) > 0 {
		return "", fmt.Errorf("insufficient balance, please add more FT UTXOs")
	}
	if f.Decimal > 18 {
		return "", fmt.Errorf("decimal cannot exceed 18")
	}
	humanStr := strings.TrimSpace(amountHumanStr)
	if humanStr == "" {
		humanStr = strconv.FormatFloat(ftAmountForMax, 'f', -1, 64)
	}
	if err := checkFTAmountHumanJSMax(f.Decimal, humanStr); err != nil {
		return "", err
	}

	amountHex, changeHex := BuildTapeAmount(amountBNInt, tapeAmountSet)

	ftUTXOs, _ := util.FtUTXOsToUTXOs(ftutxos)
	tx := newFTTx()
	_ = tx.FromUTXOs(ftUTXOs...)
	_ = tx.FromUTXOs(utxo)

	codeScript := BuildFTtransferCode(f.CodeScript, addressTo)
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tapeScript := BuildFTtransferTape(f.TapeScript, amountHex)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	if tbcAmountSat > 0 {
		tx.To(addressTo, tbcAmountSat)
	}
	if amountBNInt.Cmp(tapeAmountSum) < 0 {
		changeCode := BuildFTtransferCode(f.CodeScript, addressFrom)
		tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 500})
		changeTape := BuildFTtransferTape(f.TapeScript, changeHex)
		tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
	}

	changeScript, err := bscript.NewP2PKHFromAddress(addressFrom)
	if err != nil {
		return "", fmt.Errorf("NewP2PKHFromAddress for change: %w", err)
	}
	inputTotal := tx.TotalInputSatoshis()
	outputTotal := tx.TotalOutputSatoshis()
	if inputTotal <= outputTotal {
		return "", fmt.Errorf("insufficient input satoshis: in=%d out=%d", inputTotal, outputTotal)
	}
	tx.AddOutput(&bt.Output{LockingScript: changeScript, Satoshis: inputTotal - outputTotal})

	satPerKB := feeSatPerKBFromEnv()
	nFt := len(ftutxos)
	ftUnlockLens := make([]int, nFt)
	for i := 0; i < nFt; i++ {
		us, err := f.getFTunlock(privKey, tx, preTX[i], prepreTxData[i], i, int(ftutxos[i].Vout))
		if err != nil {
			return "", fmt.Errorf("probe FT unlock length input %d: %w", i, err)
		}
		ftUnlockLens[i] = us.Len()
	}
	if err := adjustFTTransferChangeFee(tx, satPerKB, nFt, ftUnlockLens); err != nil {
		return "", err
	}

	if stepReport != nil {
		if err := fillFTTransferStepReport(tx, stepReport); err != nil {
			return "", err
		}
	}

	// 与 tbc-contract lib/contract/ft.js transfer 一致：先 tx.sign（费输入 P2PKH），再 seal 里用回调
	// 重建 FT 解锁（getFTunlock 内 getSignature 所见为「费已签」后的交易）。BIP143 下 FT 输入的
	// sighash 虽不依赖其它输入的 unlocking script，仍先签费再算 FT，避免与节点/JS 实现偏差。
	ctx := context.Background()
	for ii := nFt; ii < len(tx.Inputs); ii++ {
		su := &unlocker.Simple{PrivateKey: privKey}
		us, err := su.UnlockingScript(ctx, tx, bt.UnlockerParams{
			InputIdx:     uint32(ii),
			SigHashFlags: sighash.AllForkID,
		})
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(ii), us); err != nil {
			return "", err
		}
	}
	for i := range ftutxos {
		us, err := f.getFTunlock(privKey, tx, preTX[i], prepreTxData[i], i, int(ftutxos[i].Vout))
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return "", err
		}
	}

	rawHex := hex.EncodeToString(tx.Bytes())
	if stepReport != nil {
		stepReport.Step4Txid = tx.TxID()
		stepReport.Step4RawHex = strings.ToLower(rawHex)
	}

	return rawHex, nil
}

// BatchTransfer 批量转账，对齐 tbc-contract ft.ts batchTransfer。
// receiveAddressAmount 为有序列表 []{Address, Amount}，Amount 为十进制字符串或 float64。
// 返回值为每笔交易的 hex raw 列表（链式花费，需顺序广播）。
func (f *FT) BatchTransfer(privKey *bec.PrivateKey, receiveAddressAmount []AddressAmount,
	ftutxos []*bt.FtUTXO, feeUTXO *bt.UTXO, preTXs []*bt.Tx, prepreTxDatas []string) ([]string, error) {

	if len(receiveAddressAmount) == 0 {
		return nil, fmt.Errorf("no receivers specified")
	}

	addr, _ := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	addressFrom := addr.AddressString

	var txsraw []string
	var ftutxoBalance big.Int
	for _, fu := range ftutxos {
		b, ok := new(big.Int).SetString(strings.TrimSpace(fu.FtBalance), 10)
		if !ok {
			return nil, fmt.Errorf("invalid FtBalance %q", fu.FtBalance)
		}
		ftutxoBalance.Add(&ftutxoBalance, b)
	}

	currentPreTXs := preTXs
	currentPrepreTxDatas := prepreTxDatas
	currentFtutxos := ftutxos
	currentFeeUTXO := feeUTXO
	balance := new(big.Int).Set(&ftutxoBalance)

	for i, ra := range receiveAddressAmount {
		amountBN := util.ParseDecimalToBigInt(ra.Amount, f.Decimal)
		if amountBN.Sign() <= 0 {
			return nil, fmt.Errorf("invalid amount %q for address %s", ra.Amount, ra.Address)
		}

		tapeAmountSet := make([]*big.Int, 0)
		if i == 0 {
			for _, fu := range currentFtutxos {
				b, _ := new(big.Int).SetString(strings.TrimSpace(fu.FtBalance), 10)
				tapeAmountSet = append(tapeAmountSet, b)
			}
		} else {
			tapeAmountSet = append(tapeAmountSet, new(big.Int).Set(balance))
		}

		amountHex, changeHex := BuildTapeAmount(amountBN, tapeAmountSet)

		tx := newFTTx()
		if i == 0 {
			ftUTXOs, _ := util.FtUTXOsToUTXOs(currentFtutxos)
			_ = tx.FromUTXOs(ftUTXOs...)
			_ = tx.FromUTXOs(currentFeeUTXO)
		} else {
			prevTx := currentPreTXs[0]
			if err := addInputFromPrevTxOutput(tx, prevTx, 2); err != nil {
				return nil, fmt.Errorf("add input from prev tx vout 2: %w", err)
			}
			if err := addInputFromPrevTxOutput(tx, prevTx, 4); err != nil {
				return nil, fmt.Errorf("add input from prev tx vout 4: %w", err)
			}
		}

		codeScript := BuildFTtransferCode(f.CodeScript, ra.Address)
		tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
		tapeScript := BuildFTtransferTape(f.TapeScript, amountHex)
		tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

		if amountBN.Cmp(balance) < 0 {
			changeCode := BuildFTtransferCode(f.CodeScript, addressFrom)
			tx.AddOutput(&bt.Output{LockingScript: changeCode, Satoshis: 500})
			changeTape := BuildFTtransferTape(f.TapeScript, changeHex)
			tx.AddOutput(&bt.Output{LockingScript: changeTape, Satoshis: 0})
		}

		changeScript, err := bscript.NewP2PKHFromAddress(addressFrom)
		if err != nil {
			return nil, fmt.Errorf("NewP2PKHFromAddress for batch change: %w", err)
		}
		inputTotal := tx.TotalInputSatoshis()
		outputTotal := tx.TotalOutputSatoshis()
		if inputTotal <= outputTotal {
			return nil, fmt.Errorf("insufficient input satoshis in batch: in=%d out=%d", inputTotal, outputTotal)
		}
		tx.AddOutput(&bt.Output{LockingScript: changeScript, Satoshis: inputTotal - outputTotal})

		satPerKB := feeSatPerKBFromEnv()
		nFt := 0
		if i == 0 {
			nFt = len(currentFtutxos)
		} else {
			nFt = 1
		}

		ftUnlockLens := make([]int, nFt)
		if i == 0 {
			for j := 0; j < nFt; j++ {
				us, err := f.getFTunlock(privKey, tx, currentPreTXs[j], currentPrepreTxDatas[j], j, int(currentFtutxos[j].Vout))
				if err != nil {
					return nil, fmt.Errorf("probe batch FT unlock length input %d: %w", j, err)
				}
				ftUnlockLens[j] = us.Len()
			}
		} else {
			us, err := f.getFTunlock(privKey, tx, currentPreTXs[0], currentPrepreTxDatas[0], 0, 2)
			if err != nil {
				return nil, fmt.Errorf("probe batch FT unlock length: %w", err)
			}
			ftUnlockLens[0] = us.Len()
		}
		if err := adjustFTTransferChangeFee(tx, satPerKB, nFt, ftUnlockLens); err != nil {
			return nil, fmt.Errorf("adjustFTTransferChangeFee batch iter %d: %w", i, err)
		}

		// Sign fee inputs (P2PKH) first
		ctx := context.Background()
		for ii := nFt; ii < len(tx.Inputs); ii++ {
			su := &unlocker.Simple{PrivateKey: privKey}
			us, err := su.UnlockingScript(ctx, tx, bt.UnlockerParams{
				InputIdx:     uint32(ii),
				SigHashFlags: sighash.AllForkID,
			})
			if err != nil {
				return nil, err
			}
			if err := tx.InsertInputUnlockingScript(uint32(ii), us); err != nil {
				return nil, err
			}
		}

		// Sign FT inputs
		if i == 0 {
			for j := range currentFtutxos {
				us, err := f.getFTunlock(privKey, tx, currentPreTXs[j], currentPrepreTxDatas[j], j, int(currentFtutxos[j].Vout))
				if err != nil {
					return nil, fmt.Errorf("getFTunlock input %d: %w", j, err)
				}
				if err := tx.InsertInputUnlockingScript(uint32(j), us); err != nil {
					return nil, err
				}
			}
		} else {
			us, err := f.getFTunlock(privKey, tx, currentPreTXs[0], currentPrepreTxDatas[0], 0, 2)
			if err != nil {
				return nil, fmt.Errorf("getFTunlock batch input 0: %w", err)
			}
			if err := tx.InsertInputUnlockingScript(0, us); err != nil {
				return nil, err
			}
		}

		txsraw = append(txsraw, hex.EncodeToString(tx.Bytes()))

		// Rebuild prepreTxData for the next iteration
		if i == 0 {
			var prepretxdata string
			for j := 0; j < len(currentPreTXs); j++ {
				d, err := bt.GetPrePreTxdata(currentPreTXs[j], int(tx.Inputs[j].PreviousTxOutIndex))
				if err != nil {
					return nil, err
				}
				prepretxdata = d + prepretxdata
			}
			currentPrepreTxDatas = []string{"57" + prepretxdata}
		} else {
			d, err := bt.GetPrePreTxdata(currentPreTXs[0], int(tx.Inputs[0].PreviousTxOutIndex))
			if err != nil {
				return nil, err
			}
			currentPrepreTxDatas = []string{"57" + d}
		}
		currentPreTXs = []*bt.Tx{tx}
		balance.Sub(balance, amountBN)
	}
	return txsraw, nil
}

// AddressAmount 用于 BatchTransfer 的接收地址和金额对
type AddressAmount struct {
	Address string
	Amount  string
}

// MergeFT 合并 FT UTXO，对齐 tbc-contract ft.ts mergeFT。
// 每批最多 5 个 FT UTXO 合并为 1 个，递归合并直到剩下一个。
// localTXs 用于 BuildFtPrePreTxData 的本地交易查找。
func (f *FT) MergeFT(privKey *bec.PrivateKey, ftutxos []*bt.FtUTXO, feeUTXO *bt.UTXO,
	preTXs []*bt.Tx, prepreTxDatas []string, localTXs []*bt.Tx) ([]string, error) {

	preTXsCopy := make([]*bt.Tx, len(preTXs))
	copy(preTXsCopy, preTXs)

	maxBatch := 5
	endIdx := maxBatch
	if endIdx > len(ftutxos) {
		endIdx = len(ftutxos)
	}
	currentFtutxos := ftutxos[:endIdx]
	currentPreTXs := preTXs[:endIdx]
	currentPrepreTxDatas := prepreTxDatas[:endIdx]

	var txsraw []string
	var lastTx *bt.Tx

	for iteration := 0; len(currentFtutxos) > 1; iteration++ {
		var tx *bt.Tx
		var err error
		if iteration == 0 {
			tx, err = f.mergeFTSingle(privKey, currentFtutxos, currentPreTXs, currentPrepreTxDatas, feeUTXO)
		} else {
			tx, err = f.mergeFTSingle(privKey, currentFtutxos, currentPreTXs, currentPrepreTxDatas, nil)
		}
		if err != nil {
			return nil, err
		}
		txsraw = append(txsraw, hex.EncodeToString(tx.Bytes()))
		lastTx = tx

		idx := (iteration + 1) * maxBatch
		endIdx = idx + maxBatch
		if endIdx > len(ftutxos) {
			endIdx = len(ftutxos)
		}
		currentPreTXs = make([]*bt.Tx, 0)
		if idx < len(preTXs) {
			currentPreTXs = append(currentPreTXs, preTXs[idx:endIdx]...)
		}
		currentPreTXs = append(currentPreTXs, tx)

		if idx < len(prepreTxDatas) {
			currentPrepreTxDatas = prepreTxDatas[idx:endIdx]
		} else {
			currentPrepreTxDatas = nil
		}

		if idx < len(ftutxos) {
			currentFtutxos = ftutxos[idx:endIdx]
		} else {
			currentFtutxos = nil
		}
	}

	if len(txsraw) <= 1 && len(currentFtutxos) < 1 {
		return txsraw, nil
	}

	// Recursive merge phase: rebuild ftutxos from prior merge results
	utxoTX := currentPreTXs[len(currentPreTXs)-1]
	_ = lastTx
	nonEmpty := len(currentPreTXs) - 1
	newFeeUTXO, err := buildUTXOFromTx(utxoTX, 2)
	if err != nil {
		return nil, err
	}

	newFtutxos := make([]*bt.FtUTXO, 0)
	if currentFtutxos != nil {
		newFtutxos = append(newFtutxos, currentFtutxos...)
	}
	newPreTXs := currentPreTXs[:nonEmpty]

	for _, rawHex := range txsraw {
		txBytes, _ := hex.DecodeString(rawHex)
		tx, err := bt.NewTxFromBytes(txBytes)
		if err != nil {
			return nil, err
		}
		newPreTXs = append(newPreTXs, tx)
		ftBalance := util.GetFtBalanceFromTape(hex.EncodeToString(tx.Outputs[1].LockingScript.Bytes()))
		newFtutxos = append(newFtutxos, &bt.FtUTXO{
			TxID:      tx.TxID(),
			Vout:      0,
			Script:    hex.EncodeToString(tx.Outputs[0].LockingScript.Bytes()),
			Satoshis:  tx.Outputs[0].Satoshis,
			FtBalance: ftBalance,
		})
	}

	if len(localTXs) == 0 {
		localTXs = preTXsCopy
	}

	newPrepreTxDatas := make([]string, 0)
	if nonEmpty < len(currentPrepreTxDatas) {
		newPrepreTxDatas = append(newPrepreTxDatas, currentPrepreTxDatas...)
	}
	for i := nonEmpty; i < len(newPreTXs); i++ {
		ppd, err := util.BuildFtPrePreTxData(newPreTXs[i], 0, localTXs)
		if err != nil {
			return nil, fmt.Errorf("buildFtPrePreTxData for merge round: %w", err)
		}
		newPrepreTxDatas = append(newPrepreTxDatas, ppd)
	}
	localTXs = newPreTXs

	recursiveResults, err := f.MergeFT(privKey, newFtutxos, newFeeUTXO, newPreTXs, newPrepreTxDatas, localTXs)
	if err != nil {
		return nil, err
	}
	txsraw = append(txsraw, recursiveResults...)
	return txsraw, nil
}

// mergeFTSingle 单轮合并（最多 5 个 FT UTXO → 1 个），对齐 ft.ts _mergeFT。
func (f *FT) mergeFTSingle(privKey *bec.PrivateKey, ftutxos []*bt.FtUTXO,
	preTXs []*bt.Tx, prepreTxDatas []string, feeUTXO *bt.UTXO) (*bt.Tx, error) {

	if len(ftutxos) == 0 {
		return nil, fmt.Errorf("no FT UTXOs available for merge")
	}
	if len(ftutxos) == 1 {
		return nil, fmt.Errorf("single UTXO does not need merge")
	}

	addr, _ := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	addressFrom := addr.AddressString

	tapeAmountSet := make([]*big.Int, len(ftutxos))
	tapeAmountSum := new(big.Int)
	for i, fu := range ftutxos {
		b, ok := new(big.Int).SetString(strings.TrimSpace(fu.FtBalance), 10)
		if !ok {
			return nil, fmt.Errorf("invalid FtBalance %q", fu.FtBalance)
		}
		tapeAmountSet[i] = b
		tapeAmountSum.Add(tapeAmountSum, b)
	}

	amountHex, changeHex := BuildTapeAmount(tapeAmountSum, tapeAmountSet)
	zeroChange := "000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	if changeHex != zeroChange {
		return nil, fmt.Errorf("change amount is not zero during merge")
	}

	ftUTXOs, _ := util.FtUTXOsToUTXOs(ftutxos)
	tx := newFTTx()
	_ = tx.FromUTXOs(ftUTXOs...)
	if feeUTXO != nil {
		_ = tx.FromUTXOs(feeUTXO)
	} else {
		if err := addInputFromPrevTxOutput(tx, preTXs[len(preTXs)-1], 2); err != nil {
			return nil, err
		}
	}

	codeScript := BuildFTtransferCode(f.CodeScript, addressFrom)
	tx.AddOutput(&bt.Output{LockingScript: codeScript, Satoshis: 500})
	tapeScript := BuildFTtransferTape(f.TapeScript, amountHex)
	tx.AddOutput(&bt.Output{LockingScript: tapeScript, Satoshis: 0})

	changeScript, err := bscript.NewP2PKHFromAddress(addressFrom)
	if err != nil {
		return nil, fmt.Errorf("NewP2PKHFromAddress for merge change: %w", err)
	}
	inputTotal := tx.TotalInputSatoshis()
	outputTotal := tx.TotalOutputSatoshis()
	if inputTotal <= outputTotal {
		return nil, fmt.Errorf("insufficient input satoshis in merge: in=%d out=%d", inputTotal, outputTotal)
	}
	tx.AddOutput(&bt.Output{LockingScript: changeScript, Satoshis: inputTotal - outputTotal})

	nFt := len(ftutxos)
	if nFt > 5 {
		nFt = 5
	}

	satPerKB := feeSatPerKBFromEnv()
	ftUnlockLens := make([]int, nFt)
	for j := 0; j < nFt; j++ {
		us, probeErr := f.getFTunlock(privKey, tx, preTXs[j], prepreTxDatas[j], j, int(ftutxos[j].Vout))
		if probeErr != nil {
			return nil, fmt.Errorf("probe merge FT unlock length input %d: %w", j, probeErr)
		}
		ftUnlockLens[j] = us.Len()
	}
	if err := adjustFTTransferChangeFee(tx, satPerKB, nFt, ftUnlockLens); err != nil {
		return nil, fmt.Errorf("adjustFTTransferChangeFee merge: %w", err)
	}

	ctx := context.Background()
	for ii := nFt; ii < len(tx.Inputs); ii++ {
		su := &unlocker.Simple{PrivateKey: privKey}
		us, err := su.UnlockingScript(ctx, tx, bt.UnlockerParams{
			InputIdx:     uint32(ii),
			SigHashFlags: sighash.AllForkID,
		})
		if err != nil {
			return nil, err
		}
		if err := tx.InsertInputUnlockingScript(uint32(ii), us); err != nil {
			return nil, err
		}
	}

	for i := 0; i < nFt; i++ {
		us, err := f.getFTunlock(privKey, tx, preTXs[i], prepreTxDatas[i], i, int(ftutxos[i].Vout))
		if err != nil {
			return nil, fmt.Errorf("getFTunlock merge input %d: %w", i, err)
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return nil, err
		}
	}

	return tx, nil
}

// addInputFromPrevTxOutput 对齐 JS tx.addInputFromPrevTx(prevTx, vout)，
// 从上一笔交易的指定输出创建输入。
func addInputFromPrevTxOutput(tx *bt.Tx, prevTx *bt.Tx, vout int) error {
	if vout >= len(prevTx.Outputs) {
		return fmt.Errorf("vout %d out of range for tx with %d outputs", vout, len(prevTx.Outputs))
	}
	out := prevTx.Outputs[vout]
	return tx.From(prevTx.TxID(), uint32(vout), out.LockingScript.String(), out.Satoshis)
}

// buildUTXOFromTx 从交易的指定输出构建 UTXO
func buildUTXOFromTx(tx *bt.Tx, vout int) (*bt.UTXO, error) {
	if vout >= len(tx.Outputs) {
		return nil, fmt.Errorf("output index %d out of range", vout)
	}
	txID, _ := hex.DecodeString(tx.TxID())
	return &bt.UTXO{
		TxID:          txID,
		Vout:          uint32(vout),
		LockingScript: tx.Outputs[vout].LockingScript,
		Satoshis:      tx.Outputs[vout].Satoshis,
	}, nil
}

// ftTransferUnlockerGetter 用于 FT 转移，混合 FT 自定义解锁和 P2PKH 标准解锁
type ftTransferUnlockerGetter struct {
	ftScripts []*bscript.Script
	privKey   *bec.PrivateKey
	callIdx   int
}

func newFTTransferUnlockerGetter(ftScripts []*bscript.Script, privKey *bec.PrivateKey) *ftTransferUnlockerGetter {
	return &ftTransferUnlockerGetter{ftScripts: ftScripts, privKey: privKey}
}

func (g *ftTransferUnlockerGetter) Unlocker(ctx context.Context, lockingScript *bscript.Script) (bt.Unlocker, error) {
	idx := g.callIdx
	g.callIdx++
	if idx < len(g.ftScripts) {
		return &fixedScriptUnlocker{script: g.ftScripts[idx]}, nil
	}
	return &unlocker.Simple{PrivateKey: g.privKey}, nil
}

type fixedScriptUnlocker struct {
	script *bscript.Script
}

func (u *fixedScriptUnlocker) UnlockingScript(ctx context.Context, tx *bt.Tx, params bt.UnlockerParams) (*bscript.Script, error) {
	return u.script, nil
}

// tbcJSEstimateTxBytes 委托 tbc-lib-go 的 (*bt.Tx).JSEstimateSize()（与 tbc-lib-js _estimateSize 一致）。
func tbcJSEstimateTxBytes(tx *bt.Tx) int {
	return tx.JSEstimateSize()
}

// ftInputUnlockWireDeltaFromEmpty 将「空 scriptSig」输入（36+varint(0)+0+4=41）换成「长 L 的 scriptSig」时，
// 线大小增加量 = L + varint(L) − 1（多出来的 varint 字节：空脚本为 1 字节长度前缀，L≥253 时为 3 字节）。
// 与 tbc-lib-js Input.toBufferWriter 一致；误用 (L−41) 会少算 2 字节（L=960 时少 43B 计费 → 与 JS 找零仍差数十 sat）。
func ftInputUnlockWireDeltaFromEmpty(unlockLen int) int {
	if unlockLen <= 0 {
		return 0
	}
	vi := bt.VarInt(uint64(unlockLen)).Length()
	return unlockLen + vi - 1
}

// adjustFTTransferChangeFee：在写入 TBC 找零输出之后、签名之前，把隐式手续费调到 targetFee（与 tbc-lib-js 一致）。
// 体积估算用 (*bt.Tx).JSEstimateSize()：FT 输入 scriptSig 仍空，Input._estimateSize≈41B。
// JS 里 setInputScript(fn) 会立刻执行回调并 setScript，故 seal() 里 _updateChangeOutput → getFee 时
// FT 输入已是真实线长；此处用 ftUnlockLens（每笔 getFTunlock 的 Len）按 ftInputUnlockWireDeltaFromEmpty 补足。
// 若 ftUnlockLens 长度与 nFt 不一致则回退到 FT_SIGNED_UNLOCK_BYTES（默认 960）。
// 若仍遇节点 66 insufficient priority：FT_RELAY_FEE_SIGNED_ESTIMATE=1 且 FT_RELAY_SIGNED_UNLOCK_BYTES。
func adjustFTTransferChangeFee(tx *bt.Tx, satPerKB, nFt int, ftUnlockLens []int) error {
	if len(tx.Outputs) == 0 {
		return nil
	}
	sealedBytes := tx.JSEstimateSize()
	useMeasured := nFt > 0 && len(ftUnlockLens) == nFt
	ftUnlockDefault := 960
	if v := strings.TrimSpace(os.Getenv("FT_SIGNED_UNLOCK_BYTES")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ftUnlockDefault = n
		}
	}
	baseBump := ftInputUnlockWireDeltaFromEmpty(ftUnlockDefault)
	if useMeasured {
		for _, L := range ftUnlockLens {
			sealedBytes += ftInputUnlockWireDeltaFromEmpty(L)
		}
	} else if nFt > 0 && baseBump > 0 {
		sealedBytes += nFt * baseBump
	}
	targetFee := int(math.Ceil(float64(sealedBytes) * float64(satPerKB) / 1000.0))

	if strings.TrimSpace(os.Getenv("FT_RELAY_FEE_SIGNED_ESTIMATE")) == "1" {
		relayUnlock := 0
		if v := strings.TrimSpace(os.Getenv("FT_RELAY_SIGNED_UNLOCK_BYTES")); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				relayUnlock = n
			}
		}
		relayBump := ftInputUnlockWireDeltaFromEmpty(relayUnlock)
		if nFt > 0 && relayBump > 0 {
			var extra int
			if useMeasured {
				for _, L := range ftUnlockLens {
					bi := ftInputUnlockWireDeltaFromEmpty(L)
					if relayBump > bi {
						extra += relayBump - bi
					}
				}
			} else if relayBump > baseBump {
				extra = nFt * (relayBump - baseBump)
			}
			if extra > 0 {
				relayFee := int(math.Ceil(float64(sealedBytes+extra) * float64(satPerKB) / 1000.0))
				if relayFee > targetFee {
					targetFee = relayFee
				}
			}
		}
	}

	return tx.AdjustImplicitFeeToTarget(targetFee)
}

// mintSourceTargetFeeSat 与 tbc-contract/lib/contract/ft.ts Mint 中 source 手续费一致：
// txSize < 1000 为 satPerKB（默认 80）；否则 ceil(txSize/1000*satPerKB)。
func mintSourceTargetFeeSat(satPerKB, estBytes int) int {
	if estBytes < 1000 {
		return satPerKB
	}
	return int(math.Ceil(float64(estBytes) * float64(satPerKB) / 1000.0))
}

func (f *FT) getFTunlock(privKey *bec.PrivateKey, tx *bt.Tx, preTX *bt.Tx, prepreTxData string, inputIdx, preTxVout int) (*bscript.Script, error) {
	pretxdata, err := bt.GetPreTxdata(preTX, preTxVout)
	if err != nil {
		return nil, err
	}
	currenttxdata, err := bt.GetCurrentTxdata(tx, inputIdx)
	if err != nil {
		return nil, err
	}
	sh, err := tx.CalcInputSignatureHash(uint32(inputIdx), sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return nil, err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(sighash.AllForkID))
	sigHex := fmt.Sprintf("%02x%s", len(sigBytes), hex.EncodeToString(sigBytes))
	pubKey := privKey.PubKey().SerialiseCompressed()
	pubKeyHex := fmt.Sprintf("%02x%s", len(pubKey), hex.EncodeToString(pubKey))

	unlockHex := currenttxdata + prepreTxData + sigHex + pubKeyHex + pretxdata
	unlockScript, err := bscript.NewFromHexString(unlockHex)
	if err != nil {
		return nil, err
	}
	return unlockScript, nil
}

func (f *FT) getFTunlockSwap(privKey *bec.PrivateKey, currentTX *bt.Tx, preTX *bt.Tx, prepreTxData string, contractTX *bt.Tx, currentUnlockIndex int, preTxVout int, ftVersion int, isCoin ...bool) (*bscript.Script, error) {
	pretxdata, err := bt.GetPreTxdata(preTX, preTxVout)
	if err != nil {
		return nil, err
	}
	var contractTxData string
	if ftVersion == 2 {
		contractTxData, err = bt.GetContractTxdata(contractTX, -1)
	} else {
		if len(currentTX.Inputs) == 0 {
			return nil, fmt.Errorf("no inputs in current tx")
		}
		contractTxData, err = bt.GetContractTxdata(contractTX, int(currentTX.Inputs[0].PreviousTxOutIndex))
	}
	if err != nil {
		return nil, err
	}
	currentinputsdata := bt.GetCurrentInputsdata(currentTX)
	currenttxdata, err := bt.GetCurrentTxdata(currentTX, currentUnlockIndex)
	if err != nil {
		return nil, err
	}
	sh, err := currentTX.CalcInputSignatureHash(uint32(currentUnlockIndex), sighash.AllForkID)
	if err != nil {
		return nil, err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return nil, err
	}
	sigBytes := sig.Serialise()
	sigBytes = append(sigBytes, byte(sighash.AllForkID))
	sigHex := fmt.Sprintf("%02x%s", len(sigBytes), hex.EncodeToString(sigBytes))
	pubKey := privKey.PubKey().SerialiseCompressed()
	pubKeyHex := fmt.Sprintf("%02x%s", len(pubKey), hex.EncodeToString(pubKey))

	coinFlag := ""
	if len(isCoin) > 0 && isCoin[0] {
		coinFlag = "51"
	}
	unlockHex := currenttxdata + prepreTxData + sigHex + pubKeyHex + currentinputsdata + contractTxData + coinFlag + pretxdata
	return bscript.NewFromHexString(unlockHex)
}

// BuildTapeAmount 与 JS FT.buildTapeAmount(amountBN, tapeAmountSet) 一致。
func BuildTapeAmount(amountBN *big.Int, tapeAmountSet []*big.Int) (amountHex, changeHex string) {
	return BuildTapeAmountWithFtInputIndex(amountBN, tapeAmountSet, 0)
}

// BuildTapeAmountWithFtInputIndex 与 JS FT.buildTapeAmount(amountBN, tapeAmountSet, ftInputIndex) 一致。
func BuildTapeAmountWithFtInputIndex(amountBN *big.Int, tapeAmountSet []*big.Int, ftInputIndex int) (amountHex, changeHex string) {
	aw := &bytes.Buffer{}
	cw := &bytes.Buffer{}
	writeU64 := func(w *bytes.Buffer, v *big.Int) {
		u := uint64(0)
		if v != nil && v.Sign() > 0 {
			u = v.Uint64()
		}
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, u)
		w.Write(b)
	}
	j := 0
	if ftInputIndex > 0 {
		for j = 0; j < ftInputIndex; j++ {
			writeU64(aw, big.NewInt(0))
			writeU64(cw, big.NewInt(0))
		}
	}
	remain := new(big.Int).Set(amountBN)
	i := 0
	for i = 0; i < 6; i++ {
		if remain.Sign() <= 0 {
			break
		}
		var slot *big.Int
		if i < len(tapeAmountSet) {
			slot = tapeAmountSet[i]
		}
		if slot == nil {
			slot = big.NewInt(0)
		}
		if slot.Cmp(remain) < 0 {
			writeU64(aw, slot)
			writeU64(cw, big.NewInt(0))
			remain.Sub(remain, slot)
		} else {
			writeU64(aw, remain)
			writeU64(cw, new(big.Int).Sub(slot, remain))
			remain = big.NewInt(0)
		}
	}
	for j += i; i < 6 && j < 6; i, j = i+1, j+1 {
		var slot *big.Int
		if i < len(tapeAmountSet) {
			slot = tapeAmountSet[i]
		}
		if slot != nil && slot.Sign() != 0 {
			writeU64(aw, big.NewInt(0))
			writeU64(cw, slot)
		} else {
			writeU64(aw, big.NewInt(0))
			writeU64(cw, big.NewInt(0))
		}
	}
	return hex.EncodeToString(aw.Bytes()), hex.EncodeToString(cw.Bytes())
}

// BuildFTtransferCode 构建转移用的 code script（对齐 tbc-contract FT.buildFTtransferCode）。
// JS 实现为替换解析后 chunks[length-2] 的 push 数据；固定偏移 1537:1558 仅适用于旧版脚本布局，
// FT v2（更长 code）须走 chunk 路径，否则与链上 mint 输出不一致（如首差异在字节 1537 附近）。
func BuildFTtransferCode(codeHex, addressOrHash string) *bscript.Script {
	codeHex = strings.TrimSpace(codeHex)
	codeBuf, err := hex.DecodeString(codeHex)
	if err != nil || len(codeBuf) == 0 {
		panic("BuildFTtransferCode: invalid code hex")
	}
	var hashBuf []byte
	ok, _ := bscript.ValidateAddress(addressOrHash)
	if ok {
		addr, _ := bscript.NewAddressFromString(addressOrHash)
		pkhBytes := hexDecode(addr.PublicKeyHash)
		hashBuf = make([]byte, 21)
		copy(hashBuf[:20], pkhBytes)
		hashBuf[20] = 0x00
	} else {
		if len(addressOrHash) != 40 {
			panic("invalid address or hash")
		}
		hashBuf = hexDecode(addressOrHash + "01")
	}
	s := bscript.NewFromBytes(codeBuf)
	chunks := s.Chunks()
	if len(chunks) >= 2 {
		idx := len(chunks) - 2
		c := &chunks[idx]
		if c.Buf != nil {
			if len(c.Buf) == len(hashBuf) {
				copy(c.Buf, hashBuf)
			} else {
				c.Buf = append([]byte(nil), hashBuf...)
				c.Len = len(c.Buf)
			}
			out, err := bscript.FromChunks(chunks)
			if err == nil && out != nil {
				return out
			}
		}
	}
	// 旧版固定布局回退（单测等合成 buffer）
	if len(codeBuf) >= 1558 {
		out := append([]byte(nil), codeBuf...)
		copy(out[1537:1558], hashBuf)
		return bscript.NewFromBytes(out)
	}
	panic("BuildFTtransferCode: unsupported code script layout")
}

// BuildFTtransferTape 构建转移用的 tape script
func BuildFTtransferTape(tapeHex, amountHex string) *bscript.Script {
	amountBuf, _ := hex.DecodeString(amountHex)
	tapeBuf, _ := hex.DecodeString(tapeHex)
	copy(tapeBuf[3:51], amountBuf[:48])
	return bscript.NewFromBytes(tapeBuf)
}

// GetBalanceFromTape 从 tape 提取余额
func GetBalanceFromTape(tapeHex string) *big.Int {
	s := util.GetFtBalanceFromTape(tapeHex)
	b, _ := new(big.Int).SetString(s, 10)
	return b
}

func writeTapeAmount(totalSupply *big.Int) string {
	buf := make([]byte, 48)
	for i := 0; i < 6; i++ {
		word := new(big.Int).Rsh(totalSupply, uint(i*64))
		word = word.And(word, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1)))
		binary.LittleEndian.PutUint64(buf[i*8:], word.Uint64())
	}
	return hex.EncodeToString(buf)
}

// strip0xHexPushesInASM removes 0x from hex push literals so bscript.NewFromASM can
// hex.DecodeString each token (Go hex does not accept 0x). Applies to all even-length
// hex bodies (0x01, 0x24, 0x24 + utxo, etc.) — opcode names are never 0x-prefixed.
// collapseTbcMintASM matches tbc.Script ASM: a length token `0xNN` followed by `0x<data>`
// is a single push of len(data)=NN bytes (not two independent hex tokens). The special case
// `0x01 0x<hex>` is one push whose body is the full <hex> (2, 4, … digits), not only 2 digits.
func collapseTbcMintASM(asm string) string {
	re01 := regexp.MustCompile(`0x01\s+0x([0-9a-fA-F]+)`)
	asm = re01.ReplaceAllStringFunc(asm, func(m string) string {
		sub := re01.FindStringSubmatch(m)
		if len(sub) != 2 {
			return m
		}
		h := sub[1]
		if len(h)%2 != 0 || len(h) < 2 {
			return m
		}
		return h
	})
	rePush := regexp.MustCompile(`0x([0-9a-fA-F]{2})\s+0x([0-9a-fA-F]+)`)
	asm = rePush.ReplaceAllStringFunc(asm, func(m string) string {
		sub := rePush.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		b, err := hex.DecodeString(sub[1])
		if err != nil || len(b) != 1 {
			return m
		}
		n := int(b[0])
		if n < 1 || n > 75 {
			return m
		}
		data := sub[2]
		if len(data) != 2*n {
			return m
		}
		return data
	})
	return asm
}

func strip0xHexPushesInASM(asm string) string {
	parts := strings.Fields(asm)
	for i, p := range parts {
		if !strings.HasPrefix(p, "0x") || len(p) <= 2 {
			continue
		}
		rest := p[2:]
		if len(rest) < 2 || len(rest)%2 != 0 {
			continue
		}
		if _, err := hex.DecodeString(rest); err == nil {
			parts[i] = rest
		}
	}
	return strings.Join(parts, " ")
}

// mintSourceTxHash 返回与 tbc-contract/lib/contract/ft.ts 中 MintFT 使用的 txSource.hash 相同的字符串。
//
// tbc-lib-js：Transaction.prototype.hash 对 _getHash()（v10 为 Hash.sha256sha256(newTxHeader)）做 readReverse 后转 hex。
// go-bt/v2：(*Tx).TxID() 对 crypto.Sha256d(newTxHeader()) 做 ReverseBytes 后转 hex。
// 二者均为 Bitcoin 约定下的「展示用 txid」，与区块浏览器 / RPC 的 txid 字段一致，可直接传入 getFTmintCode。
func mintSourceTxHash(tx *bt.Tx) string {
	return tx.TxID()
}

// getFTmintCode 构建铸造用 code 脚本。txid 必须为上述展示用 txid（与 JS getFTmintCode 第一个参数一致）。
func getFTmintCode(txid string, vout int, address string, tapeSize int) (*bscript.Script, error) {
	txidBytes, err := hex.DecodeString(txid)
	if err != nil || len(txidBytes) != 32 {
		return nil, fmt.Errorf("invalid txid")
	}
	utxoBuf := make([]byte, 36)
	for i := 0; i < 32; i++ {
		utxoBuf[i] = txidBytes[31-i]
	}
	binary.LittleEndian.PutUint32(utxoBuf[32:], uint32(vout))
	utxoHex := hex.EncodeToString(utxoBuf)

	addr, err := bscript.NewAddressFromString(address)
	if err != nil {
		return nil, err
	}
	hash := addr.PublicKeyHash + "00"
	tapeSizeHex := bt.GetSizeHex(tapeSize)

	asm := ftMintTemplateASM
	asm = strings.ReplaceAll(asm, "${utxoHex}", utxoHex)
	asm = strings.ReplaceAll(asm, "${tapeSizeHex}", tapeSizeHex)
	asm = strings.ReplaceAll(asm, "${hash}", hash)
	asm = collapseTbcMintASM(asm)
	asm = strip0xHexPushesInASM(asm)
	return bscript.NewFromASM(asm)
}

// feeSatPerKBFromEnv 默认 80，与 JS feePerKb(80) 一致；FT_FEE_SAT_PER_KB 覆盖。
func feeSatPerKBFromEnv() int {
	satPerKB := 80
	if v := strings.TrimSpace(os.Getenv("FT_FEE_SAT_PER_KB")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			satPerKB = n
		}
	}
	return satPerKB
}

// nftFeeSatPerKBFromEnv NFT 合约路径（合集 OP_RETURN 较大）：NFT_FEE_SAT_PER_KB 优先，否则与 FT 共用 feeSatPerKBFromEnv。
// 测试网广播若报 66 insufficient priority，可设 NFT_FEE_SAT_PER_KB=500（与 ft.md 中可调 fee 思路一致）。
func nftFeeSatPerKBFromEnv() int {
	if v := strings.TrimSpace(os.Getenv("NFT_FEE_SAT_PER_KB")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return feeSatPerKBFromEnv()
}

func newFeeQuoteWithSatPerKB(satPerKB int) *bt.FeeQuote {
	fq := bt.NewFeeQuote()
	fq.AddQuote(bt.FeeTypeStandard, &bt.Fee{
		FeeType:   bt.FeeTypeStandard,
		MiningFee: bt.FeeUnit{Satoshis: satPerKB, Bytes: 1000},
		RelayFee:  bt.FeeUnit{Satoshis: satPerKB, Bytes: 1000},
	})
	fq.AddQuote(bt.FeeTypeData, &bt.Fee{
		FeeType:   bt.FeeTypeData,
		MiningFee: bt.FeeUnit{Satoshis: satPerKB, Bytes: 1000},
		RelayFee:  bt.FeeUnit{Satoshis: satPerKB, Bytes: 1000},
	})
	return fq
}

// newFeeQuote80 默认每 KB 80 sat，与 tbc-contract/lib/contract/ft.js 中 feePerKb(80) 一致；可通过 FT_FEE_SAT_PER_KB 覆盖。
func newFeeQuote80() *bt.FeeQuote {
	return newFeeQuoteWithSatPerKB(feeSatPerKBFromEnv())
}

// newFeeQuoteNFT 用于 NFT.createCollection / createNFT / transfer 等；费率见 nftFeeSatPerKBFromEnv。
func newFeeQuoteNFT() *bt.FeeQuote {
	return newFeeQuoteWithSatPerKB(nftFeeSatPerKBFromEnv())
}

func hexDecode(s string) []byte {
	b, _ := hex.DecodeString(s)
	return b
}

// P2PKHToP2PKHSendTBC 普通 P2PKH 向普通 P2PKH 转 TBC（单笔输出 + 找零），与 multiSIg 等一致使用 version=10。
func P2PKHToP2PKHSendTBC(addressFrom, addressTo string, tbcAmount float64, utxos []*bt.UTXO, privKey *bec.PrivateKey) (string, error) {
	if err := validateP2PKHAddress(addressFrom); err != nil {
		return "", err
	}
	if err := validateP2PKHAddress(addressTo); err != nil {
		return "", err
	}
	amt := util.ParseDecimalToBigInt(fmt.Sprintf("%g", tbcAmount), 6)
	if amt.Sign() <= 0 {
		return "", fmt.Errorf("invalid amount")
	}
	addrTo, err := bscript.NewAddressFromString(addressTo)
	if err != nil {
		return "", err
	}
	ls, err := bscript.NewP2PKHFromPubKeyHash(hexDecode(addrTo.PublicKeyHash))
	if err != nil {
		return "", err
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: ls, Satoshis: amt.Uint64()})
	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// P2PKHOutputTBC 描述一笔 P2PKH 转账输出。
type P2PKHOutputTBC struct {
	Address string
	TBC     float64
}

// P2PKHToManyP2PKHSendTBC 一笔交易内向多个 P2PKH 各转指定 TBC，最后向 addressFrom 找零（费率同 newFeeQuote80）。
func P2PKHToManyP2PKHSendTBC(addressFrom string, outputs []P2PKHOutputTBC, utxos []*bt.UTXO, privKey *bec.PrivateKey) (string, error) {
	if err := validateP2PKHAddress(addressFrom); err != nil {
		return "", err
	}
	if len(outputs) == 0 {
		return "", fmt.Errorf("no outputs")
	}
	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	for _, o := range outputs {
		if err := validateP2PKHAddress(o.Address); err != nil {
			return "", err
		}
		amt := util.ParseDecimalToBigInt(fmt.Sprintf("%g", o.TBC), 6)
		if amt.Sign() <= 0 {
			return "", fmt.Errorf("invalid amount for %s", o.Address)
		}
		addrTo, err := bscript.NewAddressFromString(o.Address)
		if err != nil {
			return "", err
		}
		ls, err := bscript.NewP2PKHFromPubKeyHash(hexDecode(addrTo.PublicKeyHash))
		if err != nil {
			return "", err
		}
		tx.AddOutput(&bt.Output{LockingScript: ls, Satoshis: amt.Uint64()})
	}
	if err := tx.ChangeToAddress(addressFrom, newFeeQuote80()); err != nil {
		return "", err
	}
	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// newFTTx 创建 HTLC/MultiSig/FT 等需在 TBC 节点广播的交易（version=10；默认 version=1 可能被拒）。
func newFTTx() *bt.Tx {
	tx := bt.NewTx()
	tx.Version = 10
	return tx
}

// signP2PKHInput 为指定输入生成 P2PKH 解锁脚本（ALL|FORKID）。
// 与 tbc-lib-js tx.sign() 对齐：若输入为 P2PKH 且私钥的 pkh 与锁定脚本不匹配，
// 则静默跳过该输入（不签名、不报错），避免产生无效的解锁脚本。
func signP2PKHInput(tx *bt.Tx, privKey *bec.PrivateKey, inputIdx uint32) error {
	in := tx.Inputs[inputIdx]
	if in.PreviousTxScript != nil && in.PreviousTxScript.IsP2PKH() {
		scriptPKH, err := in.PreviousTxScript.PublicKeyHash()
		if err == nil {
			keyPKH := crypto.Hash160(privKey.PubKey().SerialiseCompressed())
			if !bytes.Equal(scriptPKH, keyPKH) {
				return nil
			}
		}
	}

	sh, err := tx.CalcInputSignatureHash(inputIdx, sighash.AllForkID)
	if err != nil {
		return err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return err
	}
	us, err := bscript.NewP2PKHUnlockingScript(
		privKey.PubKey().SerialiseCompressed(),
		sig.Serialise(),
		sighash.AllForkID,
	)
	if err != nil {
		return err
	}
	return tx.InsertInputUnlockingScript(inputIdx, us)
}
