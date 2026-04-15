package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

func isRetryableHTTPGetErr(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "tls handshake") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "server closed") ||
		strings.Contains(msg, "unexpected eof")
}

// httpGetWithRetry retries GET on transient transport errors (e.g. api.tbcdev.org closing with EOF).
func httpGetWithRetry(url string) (*http.Response, error) {
	const maxAttempts = 4
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(300*attempt) * time.Millisecond)
		}
		resp, err := defaultHTTPClient.Get(url)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryableHTTPGetErr(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

const (
	mainnetAPIURL = "https://api.turingbitchain.io/api/tbc/"
	testnetAPIURL = "https://api.tbcdev.org/api/tbc/"
)

func getBaseURL(network string) string {
	switch network {
	case "testnet":
		return testnetAPIURL
	case "mainnet", "":
		return mainnetAPIURL
	default:
		if network[len(network)-1] == '/' {
			return network
		}
		return network + "/"
	}
}

type balanceResponse struct {
	Data struct {
		Balance uint64 `json:"balance"`
	} `json:"data"`
}

type utxoListResponse struct {
	Data struct {
		UTXOs []struct {
			TxID  string `json:"txid"`
			Index int    `json:"index"`
			Value uint64 `json:"value"`
		} `json:"utxos"`
	} `json:"data"`
}

type txrawResponse struct {
	Data struct {
		TxRaw string `json:"txraw"`
	} `json:"data"`
}

type broadcastResponse struct {
	Code    string `json:"code"`
	Data    struct {
		TxID    string `json:"txid"`
		Error   string `json:"error"`
		Success int    `json:"success"`
		Failed  int    `json:"failed"`
	} `json:"data"`
	Message string `json:"message"`
	Error   string `json:"error"`
}

// FlexStringOrNumber unmarshals a JSON value that may be either a string or a number
// (e.g. recentblocks API returns difficulty as a number on some networks).
type FlexStringOrNumber string

func (f *FlexStringOrNumber) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexStringOrNumber(s)
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(b, &num); err == nil {
		*f = FlexStringOrNumber(num.String())
		return nil
	}
	var v float64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = FlexStringOrNumber(strconv.FormatFloat(v, 'f', -1, 64))
	return nil
}

// String returns the decoded scalar as text.
func (f FlexStringOrNumber) String() string { return string(f) }

type BlockHeaderInfo struct {
	Hash              string             `json:"hash"`
	Confirmations     int                `json:"confirmations"`
	Height            int                `json:"height"`
	Version           int                `json:"version"`
	VersionHex        string             `json:"versionHex"`
	MerkleRoot        string             `json:"merkleroot"`
	Time              int64              `json:"time"`
	Nonce             uint32             `json:"nonce"`
	Bits              string             `json:"bits"`
	Difficulty        FlexStringOrNumber `json:"difficulty"`
	PreviousBlockHash string             `json:"previoushash"`
	NextBlockHash     string             `json:"nexthash"`
}

type blockHeadersResponse struct {
	Data []BlockHeaderInfo `json:"data"`
}

type BroadcastTXsRequestItem struct {
	TxRaw string `json:"txraw"`
}

var txidPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`)

func normalizeTxID(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func isValidTxIDString(txid string) bool {
	txid = normalizeTxID(txid)
	if len(txid) != 64 {
		return false
	}
	_, err := hex.DecodeString(txid)
	return err == nil
}

func findTxIDInText(text string) (string, bool) {
	m := txidPattern.FindString(text)
	if m == "" {
		return "", false
	}
	return normalizeTxID(m), true
}

// isBroadcastAlreadyKnownErr 判定「交易已在 mempool/链上」类响应。重复广播时节点常返回非 200（如 257: txn-already-known），应视为成功并沿用本地 raw 解析出的 txid。
func isBroadcastAlreadyKnownErr(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "already-known") ||
		strings.Contains(m, "already known") ||
		strings.Contains(m, "txn-already") ||
		strings.Contains(m, "rejecting duplicated") ||
		strings.Contains(m, "tx-already-in-mempool")
}

func GetTBCBalance(address, network string) (uint64, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sbalance/address/%s", baseURL, address)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return 0, fmt.Errorf("请求余额接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("余额接口返回状态码 %d: %s", resp.StatusCode, string(body))
	}

	var br balanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		return 0, fmt.Errorf("解析余额响应失败: %w", err)
	}

	return br.Data.Balance, nil
}

func FetchUTXO(address string, amountTBC float64, network string) (*bt.UTXO, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sutxo/address/%s", baseURL, address)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("请求 UTXO 接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("UTXO 接口返回状态码 %d: %s", resp.StatusCode, string(body))
	}

	var ur utxoListResponse
	if err := json.NewDecoder(resp.Body).Decode(&ur); err != nil {
		return nil, fmt.Errorf("解析 UTXO 响应失败: %w", err)
	}

	if len(ur.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("该地址没有可用的 UTXO")
	}

	amountSatoshis := uint64(amountTBC * 1e6)
	selected := &ur.Data.UTXOs[0]
	for i := range ur.Data.UTXOs {
		if ur.Data.UTXOs[i].Value >= amountSatoshis {
			selected = &ur.Data.UTXOs[i]
			break
		}
	}

	txidBytes, err := hex.DecodeString(selected.TxID)
	if err != nil {
		return nil, fmt.Errorf("解码 txid 失败: %w", err)
	}

	chainTx, err := FetchTXRaw(selected.TxID, network)
	if err != nil {
		return nil, fmt.Errorf("拉取 UTXO 父交易以校准 script/金额失败: %w", err)
	}
	if selected.Index < 0 || selected.Index >= len(chainTx.Outputs) {
		return nil, fmt.Errorf("UTXO vout %d 超出父交易输出数 %d", selected.Index, len(chainTx.Outputs))
	}
	out := chainTx.Outputs[selected.Index]
	if out.LockingScript == nil {
		return nil, fmt.Errorf("链上输出 %s:%d 无 locking script", selected.TxID, selected.Index)
	}

	return &bt.UTXO{
		TxID:          txidBytes,
		Vout:          uint32(selected.Index),
		Satoshis:      out.Satoshis,
		LockingScript: out.LockingScript,
	}, nil
}

func BroadcastTXRaw(txraw, network string) (string, error) {
	baseURL := getBaseURL(network)
	url := baseURL + "broadcasttx"

	payload := map[string]string{"txraw": txraw}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("序列化请求体失败: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	var br broadcastResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return "", fmt.Errorf("解析广播响应失败: %w, 内容: %s", err, string(body))
	}

	if br.Code == "200" {
		txid := normalizeTxID(br.Data.TxID)
		if isValidTxIDString(txid) {
			return txid, nil
		}
		if txidFromMsg, ok := findTxIDInText(br.Message); ok {
			return txidFromMsg, nil
		}
		if txidFromBody, ok := findTxIDInText(string(body)); ok {
			return txidFromBody, nil
		}
		return "", fmt.Errorf("广播成功但返回了无效 txid: %q", br.Data.TxID)
	}

	errMsg := br.Message
	if br.Data.Error != "" {
		errMsg = br.Data.Error
	}
	if br.Error != "" {
		errMsg = br.Error
	}
	if errMsg == "" {
		errMsg = fmt.Sprintf("广播失败，code=%s", br.Code)
	}

	combinedErr := errMsg + " " + string(body)
	if isBroadcastAlreadyKnownErr(combinedErr) {
		tx, parseErr := bt.NewTxFromString(txraw)
		if parseErr == nil {
			return tx.TxID(), nil
		}
	}

	return "", fmt.Errorf("%s", errMsg)
}

func FetchTXRaw(txid, network string) (*bt.Tx, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%stxraw/txid/%s", baseURL, txid)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("请求 TXRaw 接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TXRaw 接口返回状态码 %d: %s", resp.StatusCode, string(body))
	}

	var tr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TxRaw string `json:"txraw"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("解析 TXRaw 响应失败: %w", err)
	}
	if tr.Code != "" && tr.Code != "200" {
		msg := tr.Message
		if msg == "" {
			msg = "unknown error"
		}
		return nil, fmt.Errorf("TXRaw 接口业务失败 code=%s: %s", tr.Code, msg)
	}
	raw := strings.TrimSpace(tr.Data.TxRaw)
	if raw == "" {
		return nil, fmt.Errorf("TXRaw 响应缺少 txraw (txid=%s)", normalizeTxID(txid))
	}

	return bt.NewTxFromString(raw)
}

// FetchTXRawHex returns the raw tx hex string from the txraw/txid API.
// Unlike FetchTXRaw, it does not parse the tx into a bt.Tx, so it's useful
// when we need to forward the exact txraw bytes to another implementation.
func FetchTXRawHex(txid, network string) (string, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%stxraw/txid/%s", baseURL, txid)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return "", fmt.Errorf("请求 TXRaw 接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("TXRaw 接口返回状态码 %d: %s", resp.StatusCode, string(body))
	}

	var tr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Data    struct {
			TxRaw string `json:"txraw"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("解析 TXRaw 响应失败: %w", err)
	}
	if tr.Code != "" && tr.Code != "200" {
		msg := tr.Message
		if msg == "" {
			msg = "unknown error"
		}
		return "", fmt.Errorf("TXRaw 接口业务失败 code=%s: %s", tr.Code, msg)
	}

	raw := strings.TrimSpace(tr.Data.TxRaw)
	if raw == "" {
		return "", fmt.Errorf("TXRaw 响应缺少 txraw (txid=%s)", normalizeTxID(txid))
	}
	return raw, nil
}

func IsTxOnChain(txid, network string) (bool, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%stxraw/txid/%s", baseURL, txid)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return false, fmt.Errorf("请求 TXRaw 接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}

	body, _ := io.ReadAll(resp.Body)
	return false, fmt.Errorf("TXRaw 接口返回状态码 %d: %s", resp.StatusCode, string(body))
}

func FetchUTXOs(address, network string) (bt.UTXOs, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sutxo/address/%s", baseURL, address)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("请求 UTXO 接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("UTXO 接口返回状态码 %d: %s", resp.StatusCode, string(body))
	}

	var ur utxoListResponse
	if err := json.NewDecoder(resp.Body).Decode(&ur); err != nil {
		return nil, fmt.Errorf("解析 UTXO 响应失败: %w", err)
	}

	if len(ur.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("The balance in the account is zero.")
	}

	lockingScript, err := bscript.NewP2PKHFromAddress(address)
	if err != nil {
		return nil, fmt.Errorf("创建锁定脚本失败: %w", err)
	}

	result := make(bt.UTXOs, 0, len(ur.Data.UTXOs))
	for i := range ur.Data.UTXOs {
		txidBytes, err := hex.DecodeString(ur.Data.UTXOs[i].TxID)
		if err != nil {
			return nil, fmt.Errorf("解码 txid 失败: %w", err)
		}
		result = append(result, &bt.UTXO{
			TxID:          txidBytes,
			Vout:          uint32(ur.Data.UTXOs[i].Index),
			Satoshis:      ur.Data.UTXOs[i].Value,
			LockingScript: lockingScript,
		})
	}
	return result, nil
}

func GetUTXOs(address string, amountTBC float64, network string) (bt.UTXOs, error) {
	utxos, err := FetchUTXOs(address, network)
	if err != nil {
		return nil, err
	}
	amountSatoshis := uint64(amountTBC * 1e6)
	var total uint64
	for _, u := range utxos {
		total += u.Satoshis
	}
	if total < amountSatoshis {
		return nil, fmt.Errorf("Insufficient tbc balance")
	}
	return utxos, nil
}

func BroadcastTXsRaw(txrawList []BroadcastTXsRequestItem, network string) (success, failed int, err error) {
	baseURL := getBaseURL(network)
	url := baseURL + "broadcasttxs"

	jsonData, err := json.Marshal(txrawList)
	if err != nil {
		return 0, 0, fmt.Errorf("序列化请求体失败: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, 0, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := defaultHTTPClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("HTTP 请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("读取响应失败: %w", err)
	}

	var br broadcastResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return 0, 0, fmt.Errorf("解析广播响应失败: %w, 内容: %s", err, string(body))
	}

	if br.Code == "200" {
		return br.Data.Success, br.Data.Failed, nil
	}
	if br.Code == "400" && (bytes.Contains(body, []byte("partial failure")) || br.Data.Success > 0) {
		return br.Data.Success, br.Data.Failed, nil
	}
	errMsg := br.Message
	if br.Error != "" {
		errMsg = br.Error
	}
	if errMsg == "" {
		errMsg = "Broadcast failed"
	}
	return 0, 0, fmt.Errorf("%s", errMsg)
}

func FetchBlockHeaders(network string) ([]BlockHeaderInfo, error) {
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%srecentblocks/start/0/end/1", baseURL)

	resp, err := httpGetWithRetry(url)
	if err != nil {
		return nil, fmt.Errorf("请求区块头接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Failed to fetch block headers: %s", string(body))
	}

	var r blockHeadersResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("解析区块头响应失败: %w", err)
	}
	return r.Data, nil
}
