package contract

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"regexp"

	"github.com/libsv/go-bk/bec"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/sighash"
	"github.com/sCrypt-Inc/go-bt/v2/unlocker"
	"github.com/sCrypt-Inc/go-bt/v2/util/partialsha256"

	"golang.org/x/crypto/ripemd160"
)

const (
	orderDataEncodedLen = 114

	sellOrderTemplateHex = "765187637556ba01207f77547f75817654958f01289351947901157f597f7701217f597f597f517f7701207f756b517f77816b517f77816b517f776b517f776b7654958f01289379816b7654958f0128935394796b54958f0127935294796b006b7600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575686ca87e6b007e7e7e7e7e7e7e7e7e7ea86c7e7eaa56ba01207f7588006b7600879163a86c7e7e6bbb6c7e7e6bbb6c7e7e6c6c75756b676d6d6d760087916378787e6c6c6c7e7b7c886c55798194547901157f597f5879527a517f77886c76537a517f77887c01217f6c76537a517f77887c597f6c76537a517f7781887c597f6c76537a517f7781887c517f7701207f756c7c886b6b6b6b6b6bbb6c7e7e6b676d6d6c6c6c75756b6868760119885279537f7701147f756c6c6c76547a8700886b6b6bbb6c7e7e6b760119885279537f7701147f756c6c6c76547a8700886b766b557981946b6bbb6c7e7e6b760119885279537f7701147f756c6c6c6c76557a8700886b6b5579819400886bbb6c7e7e6b5279025c0788768255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a935979025c078857798255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a936c6c6c6c765a79885f7988765a79517f7701147f758700885f79517f7701147f75886c6c527a950340420f9676527a950340420f96547988537a947b886ba86c7e7e6bbb6c7e7e6ba86c7e7e6bbb6c7e7ea857ba8867528876a9"
	buyOrderTemplateHex  = "765187637556ba01207f77547f75817654958f01289351947901157f597f7701217f597f597f517f7701207f756b517f77816b517f77816b517f776b517f776b7654958f0128935394796b54958f0127935294796b006b7600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575687600879163bb7e6c7e6b6775757575686ca87e6b007e7e7e7e7e7e7e7e7e7ea86c7e7eaa56ba01207f7588006b760087636d6d6d7600879163bb6c7e7e676d6d6c686c6c75756b67577957797e6c6c6c7e7b7c885379025c0788788255947f054654617065886c6c765879886b6b537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a935679517f7701147f756b6b6ba86c7e7e6bbb6c7e7e6b527901157f597f6c6c6c6c76577a517f7788547a01217f6c76537a517f77887c597f6c76537a517f7781887c597f6c76537a517f7781767c88527a517f7701207f756c7c88587a517f7781517a950340420f96567a7c886b6b6b6b6b6bbb6c6c5279a97c887e7e6b68760119885279537f7701147f756c6c76537a8700886b6bbb6c7e7e6b760119885279537f7701147f756c6c76537a8700886b5479816b6bbb6c7e7e6b760119885279537f7701147f756c6c6c76547a8878577981936c6c5279950340420f96547a886c527a950340420f967c6b7c6b6b6bbb6c7e7e6b5279025c0788768255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a935979025c078857798255947f05465461706588537f7701307f7500517a587f587f587f587f587f81567a937c81517a937c81517a937c81517a937c81517a937c81517a936c6c6c6c765a79885f7988765a79517f7701147f758700885f79517f7701147f75870088537a94527a9400886ba86c7e7e6bbb6c7e7e6ba86c7e7e6bbb6c7e7ea857ba8867528876a9"
)

var sha256HexPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

type OrderBook struct {
	Type               string
	HoldAddress        string
	SaleVolume         uint64
	FeeRate            uint64
	UnitPrice          uint64
	FtAContractPartial string
	FtAContractID      string
	ContractVersion    int
	BuyCodeDust        uint64
}

type OrderData struct {
	HoldAddress   string
	SaleVolume    uint64
	FtPartialHash string
	FeeRate       uint64
	UnitPrice     uint64
	FtID          string
}

func NewOrderBook() *OrderBook {
	return &OrderBook{
		ContractVersion: 1,
		BuyCodeDust:     300,
	}
}

func (o *OrderBook) BuildSellOrderTX(
	holdAddress string,
	saleVolume uint64,
	unitPrice uint64,
	feeRate uint64,
	ftID string,
	ftPartialHash string,
	utxos []*bt.UTXO,
) (string, error) {
	if saleVolume == 0 || unitPrice == 0 {
		return "", fmt.Errorf("saleVolume and unitPrice must be positive")
	}
	if len(utxos) == 0 {
		return "", fmt.Errorf("utxos cannot be empty")
	}
	if !isValidAddress(holdAddress) {
		return "", fmt.Errorf("invalid holdAddress")
	}
	if !isValidSHA256Hex(ftID) || !isValidSHA256Hex(ftPartialHash) {
		return "", fmt.Errorf("ftID and ftPartialHash must be valid SHA256 hash strings")
	}

	o.Type = "sell"
	o.HoldAddress = holdAddress
	o.SaleVolume = saleVolume
	o.UnitPrice = unitPrice
	o.FeeRate = feeRate
	o.FtAContractID = ftID
	o.FtAContractPartial = ftPartialHash

	sellOrderCode, err := o.GetSellOrderCode()
	if err != nil {
		return "", err
	}

	tx := bt.NewTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{
		LockingScript: sellOrderCode,
		Satoshis:      saleVolume,
	})
	if err := tx.ChangeToAddress(holdAddress, newFeeQuote80()); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

func (o *OrderBook) BuildCancelSellOrderTX(sellUTXO *bt.UTXO, utxos []*bt.UTXO) (string, error) {
	if sellUTXO == nil {
		return "", fmt.Errorf("sellUTXO cannot be nil")
	}
	if len(utxos) == 0 {
		return "", fmt.Errorf("utxos cannot be empty")
	}
	sellData, err := GetOrderData(sellUTXO.LockingScript.String(), true)
	if err != nil {
		return "", err
	}

	tx := bt.NewTx()
	if err := tx.FromUTXOs(sellUTXO); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.To(sellData.HoldAddress, sellUTXO.Satoshis)
	if err := tx.ChangeToAddress(sellData.HoldAddress, newFeeQuote80()); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

func (o *OrderBook) GetSellOrderCode() (*bscript.Script, error) {
	dataHex, err := o.buildOrderDataHex()
	if err != nil {
		return nil, err
	}
	return bscript.NewFromHexString(sellOrderTemplateHex + dataHex)
}

func (o *OrderBook) GetBuyOrderCode() (*bscript.Script, error) {
	dataHex, err := o.buildOrderDataHex()
	if err != nil {
		return nil, err
	}
	return bscript.NewFromHexString(buyOrderTemplateHex + dataHex)
}

func UpdateSaleVolume(codeScriptHex string, newSaleVolume uint64) (string, error) {
	buf, err := hex.DecodeString(codeScriptHex)
	if err != nil {
		return "", err
	}
	if len(buf) < orderDataEncodedLen {
		return "", fmt.Errorf("script too short")
	}
	data := buf[len(buf)-orderDataEncodedLen:]
	if data[0] != 0x14 || data[21] != 0x08 || data[30] != 0x20 || data[63] != 0x08 || data[72] != 0x08 || data[81] != 0x20 {
		return "", fmt.Errorf("invalid order data layout")
	}
	binary.LittleEndian.PutUint64(data[22:30], newSaleVolume)
	return hex.EncodeToString(buf), nil
}

func GetOrderData(codeScriptHex string, mainnet bool) (*OrderData, error) {
	buf, err := hex.DecodeString(codeScriptHex)
	if err != nil {
		return nil, err
	}
	if len(buf) < orderDataEncodedLen {
		return nil, fmt.Errorf("script too short")
	}
	data := buf[len(buf)-orderDataEncodedLen:]
	if data[0] != 0x14 || data[21] != 0x08 || data[30] != 0x20 || data[63] != 0x08 || data[72] != 0x08 || data[81] != 0x20 {
		return nil, fmt.Errorf("invalid order data layout")
	}

	addr, err := bscript.NewAddressFromPublicKeyHash(data[1:21], mainnet)
	if err != nil {
		return nil, err
	}
	return &OrderData{
		HoldAddress:   addr.AddressString,
		SaleVolume:    binary.LittleEndian.Uint64(data[22:30]),
		FtPartialHash: hex.EncodeToString(data[31:63]),
		FeeRate:       binary.LittleEndian.Uint64(data[64:72]),
		UnitPrice:     binary.LittleEndian.Uint64(data[73:81]),
		FtID:          hex.EncodeToString(data[82:114]),
	}, nil
}

func PlaceHolderP2PKHOutput() (*bscript.Script, error) {
	return bscript.NewFromASM("OP_FALSE OP_RETURN ffffffffffffffffffffffffffffffffffffffffffff")
}

// FillSigsSellOrder 填充卖单签名，对齐 orderBook.ts fillSigsSellOrder。
// orderType: "make" 或 "cancel"
func (o *OrderBook) FillSigsSellOrder(sellOrderTxRaw string, sigs []string, publicKey string, orderType string) (string, error) {
	rawBytes, err := hex.DecodeString(sellOrderTxRaw)
	if err != nil {
		return "", fmt.Errorf("invalid tx raw hex")
	}
	tx, err := bt.NewTxFromBytes(rawBytes)
	if err != nil {
		return "", err
	}
	for i, sig := range sigs {
		var asm string
		if orderType == "cancel" && i == 0 {
			asm = fmt.Sprintf("%s %s OP_2", sig, publicKey)
		} else {
			asm = fmt.Sprintf("%s %s", sig, publicKey)
		}
		script, err := bscript.NewFromASM(asm)
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), script); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// MakeSellOrderWithSign 带私钥签名的卖单构建，对齐 orderBook.ts makeSellOrder_privateKey。
func (o *OrderBook) MakeSellOrderWithSign(
	privKey *bec.PrivateKey,
	saleVolume, unitPrice, feeRate uint64,
	ftID, ftPartialHash string,
	utxos []*bt.UTXO,
) (string, error) {
	addr, _ := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	holdAddress := addr.AddressString

	o.Type = "sell"
	o.HoldAddress = holdAddress
	o.SaleVolume = saleVolume
	o.UnitPrice = unitPrice
	o.FeeRate = feeRate
	o.FtAContractID = ftID
	o.FtAContractPartial = ftPartialHash

	sellOrderCode, err := o.GetSellOrderCode()
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.AddOutput(&bt.Output{LockingScript: sellOrderCode, Satoshis: saleVolume})
	if err := tx.ChangeToAddress(holdAddress, newFeeQuote80()); err != nil {
		return "", err
	}

	ctx := context.Background()
	if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
		return "", err
	}
	return hex.EncodeToString(tx.Bytes()), nil
}

// CancelSellOrderWithSign 带私钥签名的撤销卖单，对齐 orderBook.ts cancelSellOrder_privateKey。
func (o *OrderBook) CancelSellOrderWithSign(
	privKey *bec.PrivateKey,
	sellUTXO *bt.UTXO,
	utxos []*bt.UTXO,
) (string, error) {
	sellData, err := GetOrderData(sellUTXO.LockingScript.String(), true)
	if err != nil {
		return "", err
	}

	tx := newFTTx()
	if err := tx.FromUTXOs(sellUTXO); err != nil {
		return "", err
	}
	if err := tx.FromUTXOs(utxos...); err != nil {
		return "", err
	}
	tx.To(sellData.HoldAddress, sellUTXO.Satoshis)
	if err := tx.ChangeToAddress(sellData.HoldAddress, newFeeQuote80()); err != nil {
		return "", err
	}

	// Input 0: sell order unlock with OP_2
	sh, err := tx.CalcInputSignatureHash(0, sighash.AllForkID)
	if err != nil {
		return "", err
	}
	sig, err := privKey.Sign(sh)
	if err != nil {
		return "", err
	}
	pubKeyHex := hex.EncodeToString(privKey.PubKey().SerialiseCompressed())
	sigHex := hex.EncodeToString(append(sig.Serialise(), byte(sighash.AllForkID)))
	cancelASM := fmt.Sprintf("%s %s OP_2", sigHex, pubKeyHex)
	cancelScript, err := bscript.NewFromASM(cancelASM)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, cancelScript); err != nil {
		return "", err
	}

	// Sign remaining P2PKH inputs
	ctx := context.Background()
	for i := 1; i < len(tx.Inputs); i++ {
		su := &unlocker.Simple{PrivateKey: privKey}
		us, err := su.UnlockingScript(ctx, tx, bt.UnlockerParams{
			InputIdx:     uint32(i),
			SigHashFlags: sighash.AllForkID,
		})
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return "", err
		}
	}

	return hex.EncodeToString(tx.Bytes()), nil
}

// MatchOrder 撮合交易，对齐 orderBook.ts matchOrder。
func (o *OrderBook) MatchOrder(
	privKey *bec.PrivateKey,
	buyUTXO *bt.UTXO, buyPreTX *bt.Tx,
	ftUTXO *bt.UTXO, ftPreTX *bt.Tx, ftPrePreTxData string,
	sellUTXO *bt.UTXO, sellPreTX *bt.Tx,
	utxos []*bt.UTXO,
	ftFeeAddress, tbcFeeAddress string,
	ftCodeHex, ftTapeHex string,
	ftBalance uint64,
	ftID string,
) (string, error) {
	precision := uint64(1000000)
	buyData, err := GetOrderData(buyUTXO.LockingScript.String(), true)
	if err != nil {
		return "", fmt.Errorf("parse buy order: %w", err)
	}
	sellData, err := GetOrderData(sellUTXO.LockingScript.String(), true)
	if err != nil {
		return "", fmt.Errorf("parse sell order: %w", err)
	}

	// 计算撮合数量
	matchedTBC := buyData.SaleVolume
	if sellData.SaleVolume < matchedTBC {
		matchedTBC = sellData.SaleVolume
	}
	tbcTax := matchedTBC * buyData.FeeRate / precision
	tbcBuyer := matchedTBC - tbcTax
	newSellVolume := sellData.SaleVolume - matchedTBC

	ftPay := matchedTBC * sellData.UnitPrice / precision
	ftTax := ftPay * sellData.FeeRate / precision
	ftSeller := ftPay - ftTax
	newBuyVolume := buyData.SaleVolume - matchedTBC

	// 构建交易
	tx := newFTTx()
	_ = tx.FromUTXOs(buyUTXO)
	_ = tx.FromUTXOs(ftUTXO)
	_ = tx.FromUTXOs(sellUTXO)
	_ = tx.FromUTXOs(utxos...)

	// FT Seller 输出
	ftSellerAmountSet := []*big.Int{big.NewInt(int64(ftBalance))}
	ftSellerAmountHex, _ := BuildTapeAmountWithFtInputIndex(big.NewInt(int64(ftSeller)), ftSellerAmountSet, 1)

	ftSellerCode := BuildFTtransferCode(ftCodeHex, sellData.HoldAddress)
	tx.AddOutput(&bt.Output{LockingScript: ftSellerCode, Satoshis: ftUTXO.Satoshis})
	ftSellerTape := BuildFTtransferTape(ftTapeHex, ftSellerAmountHex)
	tx.AddOutput(&bt.Output{LockingScript: ftSellerTape, Satoshis: 0})

	// FT Tax 输出
	remainingFtSet := []*big.Int{big.NewInt(int64(ftBalance - ftSeller))}
	ftTaxAmountHex, changeHex := BuildTapeAmountWithFtInputIndex(big.NewInt(int64(ftTax)), remainingFtSet, 1)

	ftTaxCode := BuildFTtransferCode(ftCodeHex, ftFeeAddress)
	tx.AddOutput(&bt.Output{LockingScript: ftTaxCode, Satoshis: ftUTXO.Satoshis})
	ftTaxTape := BuildFTtransferTape(ftTapeHex, ftTaxAmountHex)
	tx.AddOutput(&bt.Output{LockingScript: ftTaxTape, Satoshis: 0})

	// TBC Buyer 输出
	tx.To(buyData.HoldAddress, tbcBuyer)

	// TBC Tax 输出
	if buyData.FeeRate == 0 && tbcTax == 0 {
		placeholder, _ := PlaceHolderP2PKHOutput()
		tx.AddOutput(&bt.Output{LockingScript: placeholder, Satoshis: 0})
	} else if tbcTax < 10 {
		return "", fmt.Errorf("TBC tax amount is less than dust limit")
	} else {
		tx.To(tbcFeeAddress, tbcTax)
	}

	// 交易手续费找零
	var inputsFee uint64
	for _, u := range utxos {
		inputsFee += u.Satoshis
	}
	txSize := tbcJSEstimateTxBytes(tx) + 2*1000 + 2000
	fee := 80
	if txSize >= 1000 {
		fee = int(math.Ceil(float64(txSize) / 1000.0 * 80.0))
	}
	changeAddr := ""
	if len(utxos) > 0 && utxos[0].LockingScript != nil && utxos[0].LockingScript.IsP2PKH() {
		addr, _ := bscript.NewAddressFromPublicKeyHash(utxos[0].LockingScript.Bytes()[3:23], true)
		if addr != nil {
			changeAddr = addr.AddressString
		}
	}
	if changeAddr != "" && int(inputsFee) > fee+1300 {
		tx.To(changeAddr, uint64(int(inputsFee)-fee-1300))
	}

	// 部分成交处理
	if newSellVolume > 0 {
		newSellCode, err := UpdateSaleVolume(sellUTXO.LockingScript.String(), newSellVolume)
		if err != nil {
			return "", err
		}
		newSellScript, _ := bscript.NewFromHexString(newSellCode)
		tx.AddOutput(&bt.Output{LockingScript: newSellScript, Satoshis: newSellVolume})
	} else if newBuyVolume > 0 && ftBalance-ftPay > 0 {
		newBuyCode, err := UpdateSaleVolume(buyUTXO.LockingScript.String(), newBuyVolume)
		if err != nil {
			return "", err
		}
		newBuyScript, _ := bscript.NewFromHexString(newBuyCode)
		tx.AddOutput(&bt.Output{LockingScript: newBuyScript, Satoshis: o.BuyCodeDust})

		buyHash160 := sha256ripemd160(sha256sum(newBuyScript.Bytes()))
		ftChangeCode := BuildFTtransferCode(ftCodeHex, hex.EncodeToString(buyHash160))
		tx.AddOutput(&bt.Output{LockingScript: ftChangeCode, Satoshis: ftUTXO.Satoshis})
		ftChangeTape := BuildFTtransferTape(ftTapeHex, changeHex)
		tx.AddOutput(&bt.Output{LockingScript: ftChangeTape, Satoshis: 0})
	}

	// 设置 order unlock 脚本 (input 0 - buy, input 2 - sell)
	buyUnlock, err := o.getOrderUnlock(tx, buyPreTX, int(buyUTXO.Vout))
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(0, buyUnlock); err != nil {
		return "", err
	}

	sellUnlock, err := o.getOrderUnlock(tx, sellPreTX, int(sellUTXO.Vout))
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(2, sellUnlock); err != nil {
		return "", err
	}

	// FT unlock for input 1
	ftInstance := &FT{ContractTxid: ftID}
	ftSwapUnlock, err := ftInstance.getFTunlockSwap(privKey, tx, ftPreTX, ftPrePreTxData, buyPreTX, 1, int(ftUTXO.Vout), 2)
	if err != nil {
		return "", err
	}
	if err := tx.InsertInputUnlockingScript(1, ftSwapUnlock); err != nil {
		return "", err
	}

	// Sign remaining P2PKH inputs
	ctx := context.Background()
	for i := 3; i < len(tx.Inputs); i++ {
		su := &unlocker.Simple{PrivateKey: privKey}
		us, err := su.UnlockingScript(ctx, tx, bt.UnlockerParams{
			InputIdx:     uint32(i),
			SigHashFlags: sighash.AllForkID,
		})
		if err != nil {
			return "", err
		}
		if err := tx.InsertInputUnlockingScript(uint32(i), us); err != nil {
			return "", err
		}
	}

	return hex.EncodeToString(tx.Bytes()), nil
}

// getOrderUnlock 构建 order 解锁脚本，对齐 orderBook.ts getOrderUnlock。
// 内部使用 orderbookunlock 中的 getPreTxdata 和 getCurrentTxOutputsData。
func (o *OrderBook) getOrderUnlock(currentTX *bt.Tx, preTX *bt.Tx, preTxVout int) (*bscript.Script, error) {
	preTxData, err := getOrderBookPreTxdata(preTX, preTxVout, 1)
	if err != nil {
		return nil, err
	}
	currentTxData, err := getOrderBookCurrentTxOutputsData(currentTX)
	if err != nil {
		return nil, err
	}
	optionHex := "51"
	return bscript.NewFromHexString(currentTxData + preTxData + optionHex)
}

// 以下为 orderbookunlock 功能的 Go 实现，对齐 tbc-contract/lib/util/orderbookunlock.ts

const (
	obVersion         = 10
	obFtCodeLength    = 1884
	obBuyCodeLength   = 896 + 114
	obSellCodeLength  = 832 + 114
	obFtPartialOffset = 1856
	obBuyPartialOffset  = 896
	obSellPartialOffset = 832
)

func getOrderBookPreTxdata(tx *bt.Tx, vout int, contractOutputNumber int) (string, error) {
	var buf []byte
	// Version, LockTime, InputCount, OutputCount (16 bytes header)
	buf = append(buf, 0x10) // vliolength
	buf = appendUint32LE(buf, obVersion)
	buf = appendUint32LE(buf, tx.LockTime)
	buf = appendUint32LE(buf, uint32(len(tx.Inputs)))
	buf = appendUint32LE(buf, uint32(len(tx.Outputs)))

	// Inputs
	var inputBuf, inputHashBuf []byte
	for _, in := range tx.Inputs {
		inputBuf = append(inputBuf, 0x28) // inputslength
		prevID := in.PreviousTxID()
		reversed := make([]byte, 32)
		for i := 0; i < 32; i++ {
			reversed[i] = prevID[31-i]
		}
		inputBuf = append(inputBuf, reversed...)
		inputBuf = appendUint32LE(inputBuf, in.PreviousTxOutIndex)
		inputBuf = appendUint32LE(inputBuf, in.SequenceNumber)
		h := sha256sum(in.UnlockingScript.Bytes())
		inputHashBuf = append(inputHashBuf, h...)
	}
	for i := len(tx.Inputs); i < 10; i++ {
		inputBuf = append(inputBuf, 0x00)
	}
	buf = append(buf, inputBuf...)

	// UnlockingScriptHash
	buf = append(buf, 0x20) // hashlength
	buf = append(buf, sha256sum(inputHashBuf)...)

	// Outputs
	for i := 0; i < len(tx.Outputs); i++ {
		out := tx.Outputs[i]
		lockScript := out.LockingScript.Bytes()
		scriptLen := len(lockScript)
		sizeBytes := obGetSize(scriptLen)
		isCurrentContract := (i == vout)

		var partialHash string
		var suffixData []byte

		if isCurrentContract {
			partialOffset := 0
			if scriptLen == obBuyCodeLength {
				partialOffset = obBuyPartialOffset
			} else if scriptLen == obSellCodeLength {
				partialOffset = obSellPartialOffset
			}
			partialHash = partialsha256.CalculatePartialHash(lockScript[:partialOffset])
			suffixData = lockScript[partialOffset:]
		} else {
			if scriptLen < 64 {
				partialHash = "00"
				suffixData = lockScript
			} else {
				maxOff := (scriptLen / 64) * 64
				partialHash = partialsha256.CalculatePartialHash(lockScript[:maxOff])
				suffixData = lockScript[maxOff:]
			}
		}

		// amount
		buf = append(buf, 0x08)
		buf = appendUint64LESlice(buf, out.Satoshis)
		buf = append(buf, obGetLengthHex(len(suffixData))...)
		buf = append(buf, suffixData...)

		if len(partialHash) > 2 || isCurrentContract {
			buf = append(buf, 0x20)
		}
		phBytes, _ := hex.DecodeString(partialHash)
		buf = append(buf, phBytes...)
		buf = append(buf, obGetLengthHex(len(sizeBytes))...)
		buf = append(buf, sizeBytes...)

		if isCurrentContract {
			for j := 1; j < contractOutputNumber; j++ {
				if i+j >= len(tx.Outputs) {
					break
				}
				nextOut := tx.Outputs[i+j]
				nextScript := nextOut.LockingScript.Bytes()
				nextSize := obGetSize(len(nextScript))
				buf = append(buf, 0x08)
				buf = appendUint64LESlice(buf, nextOut.Satoshis)
				buf = append(buf, obGetLengthHex(len(nextScript))...)
				buf = append(buf, nextScript...)
				buf = append(buf, 0x00)
				buf = append(buf, obGetLengthHex(len(nextSize))...)
				buf = append(buf, nextSize...)
			}
			i += contractOutputNumber - 1
		}
	}
	for i := len(tx.Outputs); i < 10; i++ {
		buf = append(buf, 0x00, 0x00, 0x00, 0x00)
	}
	return hex.EncodeToString(buf), nil
}

func getOrderBookCurrentTxOutputsData(tx *bt.Tx) (string, error) {
	var buf []byte
	for i := 0; i < len(tx.Outputs); i++ {
		out := tx.Outputs[i]
		lockScript := out.LockingScript.Bytes()
		scriptLen := len(lockScript)
		sizeBytes := obGetSize(scriptLen)

		partialOffset := 0
		if scriptLen == obFtCodeLength {
			partialOffset = obFtPartialOffset
		} else if scriptLen == obBuyCodeLength {
			partialOffset = obBuyPartialOffset
		} else if scriptLen == obSellCodeLength {
			partialOffset = obSellPartialOffset
		}

		isSpecial := partialOffset > 0
		var partialHash string
		var suffixData []byte

		if isSpecial {
			partialHash = partialsha256.CalculatePartialHash(lockScript[:partialOffset])
			suffixData = lockScript[partialOffset:]
		} else {
			if scriptLen < 64 {
				partialHash = "00"
				suffixData = lockScript
			} else {
				maxOff := (scriptLen / 64) * 64
				partialHash = partialsha256.CalculatePartialHash(lockScript[:maxOff])
				suffixData = lockScript[maxOff:]
			}
		}

		buf = append(buf, 0x08)
		buf = appendUint64LESlice(buf, out.Satoshis)
		buf = append(buf, obGetLengthHex(len(suffixData))...)
		buf = append(buf, suffixData...)

		if isSpecial {
			buf = append(buf, 0x20)
		}
		phBytes, _ := hex.DecodeString(partialHash)
		buf = append(buf, phBytes...)
		buf = append(buf, obGetLengthHex(len(sizeBytes))...)
		buf = append(buf, sizeBytes...)

		if scriptLen == obFtCodeLength {
			if i+1 < len(tx.Outputs) {
				nextOut := tx.Outputs[i+1]
				nextScript := nextOut.LockingScript.Bytes()
				buf = append(buf, 0x08)
				buf = appendUint64LESlice(buf, nextOut.Satoshis)
				buf = append(buf, obGetLengthHex(len(nextScript))...)
				buf = append(buf, nextScript...)
				i++
			}
		}
	}

	paddingCount := 0
	if len(tx.Outputs) == 7 {
		paddingCount = 10
	} else if len(tx.Outputs) == 8 {
		paddingCount = 6
	}
	for i := 0; i < paddingCount; i++ {
		buf = append(buf, 0x00)
	}
	return hex.EncodeToString(buf), nil
}

func appendUint32LE(buf []byte, v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return append(buf, b...)
}

func appendUint64LESlice(buf []byte, v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return append(buf, b...)
}

func obGetSize(length int) []byte {
	if length < 256 {
		return []byte{byte(length)}
	}
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(length))
	return b
}

func obGetLengthHex(length int) []byte {
	if length < 76 {
		return []byte{byte(length)}
	} else if length < 256 {
		return []byte{0x4c, byte(length)}
	} else {
		b := make([]byte, 3)
		b[0] = 0x4d
		binary.LittleEndian.PutUint16(b[1:], uint16(length))
		return b
	}
}

func (o *OrderBook) buildOrderDataHex() (string, error) {
	if !isValidAddress(o.HoldAddress) {
		return "", fmt.Errorf("invalid hold address")
	}
	if !isValidSHA256Hex(o.FtAContractID) || !isValidSHA256Hex(o.FtAContractPartial) {
		return "", fmt.Errorf("invalid ft hash")
	}
	addr, err := bscript.NewAddressFromString(o.HoldAddress)
	if err != nil {
		return "", err
	}
	addressHash, err := hex.DecodeString(addr.PublicKeyHash)
	if err != nil {
		return "", err
	}
	ftPartial, err := hex.DecodeString(o.FtAContractPartial)
	if err != nil {
		return "", err
	}
	ftID, err := hex.DecodeString(o.FtAContractID)
	if err != nil {
		return "", err
	}

	data := make([]byte, 0, orderDataEncodedLen)
	data = append(data, 0x14)
	data = append(data, addressHash...)
	data = append(data, 0x08)
	data = appendUint64LE(data, o.SaleVolume)
	data = append(data, 0x20)
	data = append(data, ftPartial...)
	data = append(data, 0x08)
	data = appendUint64LE(data, o.FeeRate)
	data = append(data, 0x08)
	data = appendUint64LE(data, o.UnitPrice)
	data = append(data, 0x20)
	data = append(data, ftID...)

	return hex.EncodeToString(data), nil
}

func appendUint64LE(dst []byte, n uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, n)
	return append(dst, b...)
}

func isValidSHA256Hex(s string) bool {
	return sha256HexPattern.MatchString(s)
}

func isValidAddress(addr string) bool {
	ok, _ := bscript.ValidateAddress(addr)
	return ok
}

func sha256sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func sha256ripemd160(data []byte) []byte {
	r := ripemd160.New()
	r.Write(data)
	return r.Sum(nil)
}
