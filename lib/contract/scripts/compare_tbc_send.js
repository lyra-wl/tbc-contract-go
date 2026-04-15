/**
 * TBC 普通 P2PKH 转账 — 与 main.go tbc_test 共用 testdata/tbc_test.json
 *
 * 用法（在仓库任意目录）:
 *   node tbc-contract-go/lib/contract/scripts/compare_tbc_send.js
 *
 * 依赖: 工作区 tbc-contract/node_modules/tbc-lib-js
 */
const fs = require("fs");
const path = require("path");

const fixturePath = path.join(__dirname, "..", "testdata", "tbc_test.json");
const tbcLibPath = path.join(
  __dirname,
  "..",
  "..",
  "..",
  "..",
  "tbc-contract",
  "node_modules",
  "tbc-lib-js"
);
const tbc = require(tbcLibPath);
const Hash = tbc.crypto.Hash;

function transferOutputsTxIDHex(tx) {
  const h = Hash.sha256sha256(tx.toBuffer());
  return Buffer.from(h).reverse().toString("hex").toLowerCase();
}

function buildOutputsReport(txraw) {
  const tx = new tbc.Transaction(txraw);
  const outs = tx.outputs.map((o, index) => ({
    index,
    satoshis: o.satoshis,
    script_hex: o.script.toBuffer().toString("hex").toLowerCase(),
  }));
  return {
    tx_raw: String(txraw).trim().toLowerCase(),
    tx_id: transferOutputsTxIDHex(tx),
    tx_outputs: outs,
  };
}

function main() {
  const raw = fs.readFileSync(fixturePath, "utf8");
  const data = JSON.parse(raw);
  if (data.disabled) {
    console.log("compare_tbc_send: skipped (disabled=true)");
    return;
  }
  if (data.operation !== "tbc_send") {
    console.error("expected operation=tbc_send, got", data.operation);
    process.exit(1);
  }
  const ins = data.inputs;
  const priv = tbc.PrivateKey.fromWIF(ins.private_key_wif);
  const addressFrom = tbc.Address.fromPrivateKey(priv).toString();
  const u = ins.utxos[0];
  const utxo = {
    txId: u.txid,
    outputIndex: u.vout,
    script: u.script,
    satoshis: u.satoshis,
  };

  const satPerKb = (() => {
    const v = String(process.env.FT_FEE_SAT_PER_KB || "").trim();
    const n = v === "" ? 80 : parseInt(v, 10);
    return Number.isFinite(n) && n > 0 ? n : 80;
  })();

  const tx = new tbc.Transaction()
    .from(utxo)
    .to(ins.address_to, ins.send_satoshis)
    .feePerKb(satPerKb)
    .change(addressFrom);
  tx.sign(priv);
  tx.seal();
  const txraw = tx.uncheckedSerialize();
  const outputs = buildOutputsReport(txraw);

  data.results = data.results || {};
  data.results.js = {
    tx_raw_hex_len: txraw.length,
    tx_id: outputs.tx_id,
    outputs: {
      tx_raw: outputs.tx_raw,
      tx_id: outputs.tx_id,
      tx_outputs: outputs.tx_outputs,
    },
    fee_sat_per_kb: satPerKb,
    generator: "compare_tbc_send.js",
  };

  const goRaw =
    data.results.go &&
    data.results.go.outputs &&
    data.results.go.outputs.tx_raw;
  const jsRaw = outputs.tx_raw;
  if (goRaw) {
    data.results.match = goRaw === jsRaw;
    data.results.analysis =
      data.results.match === true
        ? "tx_raw 与 Go 逐字节一致；wire tx_id 一致。"
        : "tx_raw 与 Go 不一致，请核对费率与库版本。";
  } else {
    data.results.analysis = "尚未运行 Go（main.go tbc_test），无法比对。";
  }

  fs.writeFileSync(fixturePath, JSON.stringify(data, null, 2) + "\n", "utf8");
  console.log("updated:", fixturePath);
  console.log("test_id:", data.test_id);
  console.log("tx_id (wire):", outputs.tx_id);
  console.log("tx_raw len (hex chars):", txraw.length);
}

main();
