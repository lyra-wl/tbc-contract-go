package contract

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/libsv/go-bk/wif"
	bt "github.com/sCrypt-Inc/go-bt/v2"
	"github.com/sCrypt-Inc/go-bt/v2/bscript"
	"github.com/sCrypt-Inc/go-bt/v2/bscript/interpreter"
	"github.com/sCrypt-Inc/go-bt/v2/sighash"
	"github.com/sCrypt-Inc/go-bt/v2/unlocker"
)

func TestUTXO_UnlockWithPrivateKey(t *testing.T) {
	const (
		privateKeyWIF = "KxF1NdXjRxQ2ENESe656kMZ8fo7USBrikfLwUg4PpqXL4qoJF9QM"
		addressTo     = "1FRRnxkUwwGKCdbpaeeuz8B6C3F1vJGBFF"
		utxoTxID      = "5e1e8412cf2948d96508c99cc975bb1a31b3d7a7f7d1e222e47ba4edb583a218"
		utxoVout      = 0
		utxoSatoshis  = 426000000
		utxoScript    = "76a914b770377041443c7eac4a93b721ab7093bdbccaba88ac"
	)

	decoded, err := wif.DecodeWIF(privateKeyWIF)
	if err != nil {
		t.Fatalf("WIF 解码失败: %v", err)
	}
	privKey := decoded.PrivKey

	fromAddr, err := bscript.NewAddressFromPublicKey(privKey.PubKey(), true)
	if err != nil {
		t.Fatalf("从公钥生成地址失败: %v", err)
	}
	t.Logf("WIF 对应地址: %s (pkh=%s)", fromAddr.AddressString, fromAddr.PublicKeyHash)

	utxoPKH := utxoScript[6 : len(utxoScript)-4]
	utxoAddr, err := bscript.NewAddressFromPublicKeyHash(mustDecodeHex(t, utxoPKH), true)
	if err != nil {
		t.Fatalf("从 UTXO pkh 生成地址失败: %v", err)
	}
	t.Logf("UTXO 所属地址: %s (pkh=%s)", utxoAddr.AddressString, utxoPKH)

	expectedPKH := fromAddr.PublicKeyHash
	keyMatchesUTXO := utxoScript == "76a914"+expectedPKH+"88ac"
	if keyMatchesUTXO {
		t.Logf("私钥与 UTXO 匹配，可以正常解锁")
	} else {
		t.Logf("注意: 私钥地址(%s) 与 UTXO 所属地址(%s) 不匹配",
			fromAddr.AddressString, utxoAddr.AddressString)
	}

	txIDBytes, err := hex.DecodeString(utxoTxID)
	if err != nil {
		t.Fatalf("解码 txid 失败: %v", err)
	}
	lockingScript, err := bscript.NewFromHexString(utxoScript)
	if err != nil {
		t.Fatalf("解码 locking script 失败: %v", err)
	}

	utxo := &bt.UTXO{
		TxID:          txIDBytes,
		Vout:          utxoVout,
		LockingScript: lockingScript,
		Satoshis:      utxoSatoshis,
	}

	buildTestTx := func(t *testing.T) *bt.Tx {
		t.Helper()
		tx := bt.NewTx()
		tx.Version = 10
		if err := tx.FromUTXOs(utxo); err != nil {
			t.Fatalf("FromUTXOs 失败: %v", err)
		}
		toAddr, err := bscript.NewAddressFromString(addressTo)
		if err != nil {
			t.Fatalf("解析目标地址失败: %v", err)
		}
		toScript, err := bscript.NewP2PKHFromAddress(toAddr.AddressString)
		if err != nil {
			t.Fatalf("构造 P2PKH 输出脚本失败: %v", err)
		}
		tx.AddOutput(&bt.Output{
			LockingScript: toScript,
			Satoshis:      utxoSatoshis - 1000,
		})
		return tx
	}

	// 验证 signP2PKHInput 在 pkh 不匹配时静默跳过（与 JS tx.sign() 对齐）
	t.Run("signP2PKHInput_skips_on_mismatch", func(t *testing.T) {
		tx := buildTestTx(t)
		if err := signP2PKHInput(tx, privKey, 0); err != nil {
			t.Fatalf("signP2PKHInput 应静默跳过，但返回了错误: %v", err)
		}
		us := tx.Inputs[0].UnlockingScript
		if keyMatchesUTXO {
			if us == nil || us.Len() == 0 {
				t.Fatal("私钥匹配但未生成签名")
			}
			t.Logf("签名成功，unlocking script 长度=%d 字节", us.Len())
		} else {
			if us != nil && us.Len() > 0 {
				t.Fatalf("私钥不匹配但仍生成了签名（长度=%d），应静默跳过", us.Len())
			}
			t.Logf("私钥不匹配，signP2PKHInput 正确地静默跳过了签名（与 JS tx.sign() 行为一致）")
		}
	})

	// 验证 FillAllInputs 在 pkh 不匹配时静默跳过
	t.Run("fillAllInputs_skips_on_mismatch", func(t *testing.T) {
		tx := buildTestTx(t)
		ctx := context.Background()
		if err := tx.FillAllInputs(ctx, &unlocker.Getter{PrivateKey: privKey}); err != nil {
			t.Fatalf("FillAllInputs 应静默跳过，但返回了错误: %v", err)
		}
		us := tx.Inputs[0].UnlockingScript
		if keyMatchesUTXO {
			if us == nil || us.Len() == 0 {
				t.Fatal("私钥匹配但 FillAllInputs 未生成签名")
			}
			t.Logf("FillAllInputs 签名成功，unlocking script 长度=%d 字节", us.Len())
		} else {
			if us != nil && us.Len() > 0 {
				t.Fatalf("私钥不匹配但 FillAllInputs 仍生成了签名（长度=%d），应静默跳过", us.Len())
			}
			t.Logf("私钥不匹配，FillAllInputs 正确地静默跳过了签名（与 JS tx.sign() 行为一致）")
		}
	})

	// 手动 CalcInputSignatureHash 绕过检查，强制签名后验证解释器拒绝
	t.Run("manual_forced_sign_interpreter_rejects", func(t *testing.T) {
		tx := buildTestTx(t)
		sh, err := tx.CalcInputSignatureHash(0, sighash.AllForkID)
		if err != nil {
			t.Fatalf("CalcInputSignatureHash 失败: %v", err)
		}

		sig, err := privKey.Sign(sh)
		if err != nil {
			t.Fatalf("签名失败: %v", err)
		}

		unlockScript, err := bscript.NewP2PKHUnlockingScript(
			privKey.PubKey().SerialiseCompressed(),
			sig.Serialise(),
			sighash.AllForkID,
		)
		if err != nil {
			t.Fatalf("构造 P2PKH 解锁脚本失败: %v", err)
		}
		if err := tx.InsertInputUnlockingScript(0, unlockScript); err != nil {
			t.Fatalf("InsertInputUnlockingScript 失败: %v", err)
		}
		t.Logf("手动强制签名，unlocking script 长度=%d 字节", unlockScript.Len())

		prevOut := &bt.Output{
			LockingScript: lockingScript,
			Satoshis:      utxoSatoshis,
		}
		verifyErr := interpreter.NewEngine().Execute(
			interpreter.WithTx(tx, 0, prevOut),
			interpreter.WithForkID(),
			interpreter.WithAfterGenesis(),
		)
		if keyMatchesUTXO {
			if verifyErr != nil {
				t.Fatalf("私钥匹配但解释器验证失败: %v", verifyErr)
			}
			t.Logf("解释器验证通过 txid=%s", tx.TxID())
		} else {
			if verifyErr == nil {
				t.Fatal("私钥不匹配，预期解释器拒绝，但验证通过了")
			}
			t.Logf("手动强制签名后解释器正确拒绝: %v", verifyErr)
		}
	})
}

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex 解码失败: %v", err)
	}
	return b
}
