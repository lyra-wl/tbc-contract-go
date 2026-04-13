package api

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/sCrypt-Inc/go-bt/v2/bscript"
)

type SimpleUTXO struct {
	TxID     string
	Vout     uint32
	Script   string
	Satoshis uint64
}

type utxoByScriptHashResponse struct {
	Data struct {
		UTXOs []struct {
			TxID  string `json:"txid"`
			Index int    `json:"index"`
			Value uint64 `json:"value"`
		} `json:"utxos"`
	} `json:"data"`
}

func FetchUMTXO(scriptASM string, tbcAmount float64, network string) (*SimpleUTXO, error) {
	script, err := bscript.NewFromASM(scriptASM)
	if err != nil {
		return nil, err
	}
	multiScript := hex.EncodeToString(script.Bytes())
	hash, err := scriptHashFromHex(multiScript)
	if err != nil {
		return nil, err
	}

	amountSatoshis := uint64(tbcAmount * 1e6)
	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sutxo/scriptpubkeyhash/%s", baseURL, hash)

	resp, err := defaultHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Failed to fetch UTXO: %s", string(body))
	}

	var r utxoByScriptHashResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if len(r.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("The balance in the account is zero.")
	}

	selected := &r.Data.UTXOs[0]
	for i := range r.Data.UTXOs {
		v := r.Data.UTXOs[i].Value
		if v > amountSatoshis && v < 3200000000 {
			selected = &r.Data.UTXOs[i]
			break
		}
	}

	if selected.Value < amountSatoshis {
		var total uint64
		for i := range r.Data.UTXOs {
			total += r.Data.UTXOs[i].Value
		}
		if total < amountSatoshis {
			return nil, fmt.Errorf("Insufficient tbc balance")
		}
		return nil, fmt.Errorf("Please mergeUTXO")
	}

	return &SimpleUTXO{
		TxID:     selected.TxID,
		Vout:     uint32(selected.Index),
		Script:   multiScript,
		Satoshis: selected.Value,
	}, nil
}

func FetchUMTXOs(scriptASM string, network string) ([]*SimpleUTXO, error) {
	script, err := bscript.NewFromASM(scriptASM)
	if err != nil {
		return nil, err
	}
	multiScript := hex.EncodeToString(script.Bytes())
	hash, err := scriptHashFromHex(multiScript)
	if err != nil {
		return nil, err
	}

	baseURL := getBaseURL(network)
	url := fmt.Sprintf("%sutxo/scriptpubkeyhash/%s", baseURL, hash)

	resp, err := defaultHTTPClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Failed to fetch UTXO: %s", string(body))
	}

	var r utxoByScriptHashResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if len(r.Data.UTXOs) == 0 {
		return nil, fmt.Errorf("The balance in the account is zero.")
	}

	result := make([]*SimpleUTXO, 0, len(r.Data.UTXOs))
	for i := range r.Data.UTXOs {
		u := &r.Data.UTXOs[i]
		result = append(result, &SimpleUTXO{
			TxID:     u.TxID,
			Vout:     uint32(u.Index),
			Script:   multiScript,
			Satoshis: u.Value,
		})
	}
	return result, nil
}

func GetUMTXOs(scriptASM string, amountTBC float64, network string) ([]*SimpleUTXO, error) {
	utxos, err := FetchUMTXOs(scriptASM, network)
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
