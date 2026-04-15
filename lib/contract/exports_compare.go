package contract

import bt "github.com/sCrypt-Inc/go-bt/v2"

func NewFTTxExported() *bt.Tx                          { return newFTTx() }
func TbcJSEstimateTxBytesExported(tx *bt.Tx) int       { return tbcJSEstimateTxBytes(tx) }
func ObCalcFeeExported(txSize int) int                  { return obCalcFee(txSize) }

func GetOrderBookCurrentTxOutputsDataExported(tx *bt.Tx) (string, error) {
	return getOrderBookCurrentTxOutputsData(tx)
}

func GetOrderBookPreTxdataExported(tx *bt.Tx, vout, contractOutputNumber int) (string, error) {
	return getOrderBookPreTxdata(tx, vout, contractOutputNumber)
}
