#!/usr/bin/env node
/**
 * 与 poolnft2_chain_flow.js / Go 集成测试 **相同环境变量与默认参数**，单独跑 init_pool 对照：
 * - 未设置 TBC_POOLNFT2_POOL_CONTRACT_TXID：先 createPoolNFT（两笔广播）→ 等待索引 → initPoolNFT → 广播
 * - 已设置 TBC_POOLNFT2_POOL_CONTRACT_TXID：跳过建池，只对已有池做 initPoolNFT → 广播
 *
 * 用于判断「纯 Node + tbc-contract」路径下 init 是否成功；若成功可与 Go 侧日志/raw 比对。
 *
 * 依赖：TBC_CONTRACT_ROOT 或 仓库布局 working-project/tbc-contract（本脚本位于 tbc-contract-go/scripts）
 *
 * 示例：
 *   export TBC_POOLNFT2_PRIVATE_KEY_WIF='...'
 *   export TBC_NETWORK=testnet
 *   export TBC_POOLNFT2_FT_CONTRACT_TXID=ade895678bb988155f4da6a7b1aedd687491bc2b07806c1d4c80a3af8b98dd93
 *   # 可选：仅对已铸池合约做 init
 *   # export TBC_POOLNFT2_POOL_CONTRACT_TXID=db453c2961bc71fd9a6b9bd1b05586307452f67ee3327be39cc7d1f94f154277
 *   node tbc-contract-go/scripts/poolnft2_init_pool_compare.js
 *
 * 可选：
 *   TBC_POOLNFT2_DUMP_INIT_RAW=1       打印 init 完整 raw hex（便于与 Go 或其它工具比对）
 *   TBC_POOLNFT2_SKIP_BROADCAST=1      只构造 init raw、不调用 broadcast（需已设置 TBC_POOLNFT2_POOL_CONTRACT_TXID，
 *                                     否则无链上池时 initfromContractId 无法完成）
 */

const path = require("path");
const fs = require("fs");

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

function defaultContractRoot() {
  return path.resolve(__dirname, "..", "..", "tbc-contract");
}

function loadDeps() {
  const root = process.env.TBC_CONTRACT_ROOT || defaultContractRoot();
  const indexJs = path.join(root, "index.js");
  const tbcPath = path.join(root, "node_modules", "tbc-lib-js");
  if (!fs.existsSync(indexJs)) {
    throw new Error(`找不到 tbc-contract：${indexJs}。请设置 TBC_CONTRACT_ROOT。`);
  }
  if (!fs.existsSync(tbcPath)) {
    throw new Error(`请在 ${root} 下执行 npm install。`);
  }
  const tbc = require(tbcPath);
  const { API, poolNFT2 } = require(indexJs);
  return { tbc, API, poolNFT2, contractRoot: root };
}

async function waitPoolNftUTXOReady(poolNFT2Cls, contractTxid, network, maxMs) {
  const deadline = Date.now() + maxMs;
  let attempt = 0;
  let lastErr = "";
  while (Date.now() < deadline) {
    attempt += 1;
    const probe = new poolNFT2Cls({ txid: contractTxid, network });
    try {
      await probe.initfromContractId();
      await probe.fetchPoolNftUTXO(contractTxid);
      console.log(JSON.stringify({ event: "pool_utxo_ready", contractTxid, attempt }));
      return;
    } catch (e) {
      lastErr = (e && e.message) || String(e);
    }
    await sleep(5000);
  }
  throw new Error(`等待 Pool NFT 索引超时: ${lastErr}`);
}

function logParams() {
  const snap = {
    event: "params_snapshot",
    TBC_NETWORK: process.env.TBC_NETWORK || "testnet",
    TBC_POOLNFT2_FT_CONTRACT_TXID:
      process.env.TBC_POOLNFT2_FT_CONTRACT_TXID || "(testnet default in script)",
    TBC_POOLNFT2_POOL_CONTRACT_TXID: process.env.TBC_POOLNFT2_POOL_CONTRACT_TXID || "",
    TBC_POOLNFT2_FEE_TBC: process.env.TBC_POOLNFT2_FEE_TBC || "0.15",
    TBC_POOLNFT2_INIT_TBC: process.env.TBC_POOLNFT2_INIT_TBC || "25",
    TBC_POOLNFT2_INIT_FT: process.env.TBC_POOLNFT2_INIT_FT || "800",
    TBC_POOLNFT2_SERVICE_RATE: process.env.TBC_POOLNFT2_SERVICE_RATE || "25",
    TBC_POOLNFT2_LP_PLAN: process.env.TBC_POOLNFT2_LP_PLAN || "2",
    TBC_POOLNFT2_TAG: process.env.TBC_POOLNFT2_TAG || "tbc",
    TBC_POOLNFT2_POST_MINT_WAIT_MS: process.env.TBC_POOLNFT2_POST_MINT_WAIT_MS || "12000",
    TBC_POOLNFT2_POOL_INDEX_WAIT_MS: process.env.TBC_POOLNFT2_POOL_INDEX_WAIT_MS || "120000",
    TBC_POOLNFT2_INIT_UTXO_BUFFER_TBC: process.env.TBC_POOLNFT2_INIT_UTXO_BUFFER_TBC || "10",
  };
  console.log(JSON.stringify(snap));
}

async function main() {
  const { tbc, API, poolNFT2, contractRoot } = loadDeps();
  console.log(JSON.stringify({ event: "deps", TBC_CONTRACT_ROOT: contractRoot }));
  logParams();

  const network = (process.env.TBC_NETWORK || "testnet").trim();
  const wif = (process.env.TBC_POOLNFT2_PRIVATE_KEY_WIF || "").trim();
  if (!wif) {
    throw new Error("请设置 TBC_POOLNFT2_PRIVATE_KEY_WIF");
  }

  const defaultFt =
    "ade895678bb988155f4da6a7b1aedd687491bc2b07806c1d4c80a3af8b98dd93";
  let ftContract = (process.env.TBC_POOLNFT2_FT_CONTRACT_TXID || "").trim();
  if (!ftContract && network === "testnet") {
    ftContract = defaultFt;
  }
  if (!/^[0-9a-fA-F]{64}$/.test(ftContract)) {
    throw new Error("无效的 TBC_POOLNFT2_FT_CONTRACT_TXID");
  }
  ftContract = ftContract.toLowerCase();

  const feeTbc = Number(process.env.TBC_POOLNFT2_FEE_TBC || "0.15");
  const initTbc = Number(process.env.TBC_POOLNFT2_INIT_TBC || "25");
  const initFt = Number(process.env.TBC_POOLNFT2_INIT_FT || "800");
  const serviceRate = Number(process.env.TBC_POOLNFT2_SERVICE_RATE || "25");
  const lpPlan = Number(process.env.TBC_POOLNFT2_LP_PLAN || "2");
  const tag = (process.env.TBC_POOLNFT2_TAG || "tbc").trim();
  const postMintWait = Number(process.env.TBC_POOLNFT2_POST_MINT_WAIT_MS || "12000");
  const poolIndexWait = Number(process.env.TBC_POOLNFT2_POOL_INDEX_WAIT_MS || "120000");
  const initUtxoBuffer = Number(process.env.TBC_POOLNFT2_INIT_UTXO_BUFFER_TBC || "10");

  const privateKeyA = tbc.PrivateKey.fromString(wif);
  const addressA = tbc.Address.fromPrivateKey(privateKeyA).toString();

  let contractTxid = (process.env.TBC_POOLNFT2_POOL_CONTRACT_TXID || "").trim().toLowerCase();

  const skipBroadcast = process.env.TBC_POOLNFT2_SKIP_BROADCAST === "1";

  if (!contractTxid) {
    const pool = new poolNFT2({ network });
    pool.initCreate(ftContract);
    const utxoCreate = await API.fetchUTXO(privateKeyA, feeTbc, network);
    const txPair = await pool.createPoolNFT(
      privateKeyA,
      utxoCreate,
      tag,
      serviceRate,
      lpPlan === 2 ? 2 : 1,
      false,
    );
    if (skipBroadcast) {
      contractTxid = new tbc.Transaction(txPair[1]).hash.toLowerCase();
      console.log(
        JSON.stringify({
          step: "create_skipped_broadcast",
          note: "TBC_POOLNFT2_SKIP_BROADCAST=1，链上无此池；后续 initfromContractId 会失败，仅用于本地 raw 结构",
          mint_txid_local: contractTxid,
          source_raw_len: txPair[0].length,
          mint_raw_len: txPair[1].length,
        }),
      );
    } else {
      const txid0 = await API.broadcastTXraw(txPair[0], network);
      console.log(JSON.stringify({ step: "create_source", txid: txid0 }));
      await sleep(4000);
      const txid1 = await API.broadcastTXraw(txPair[1], network);
      console.log(JSON.stringify({ step: "create_mint", txid: txid1 }));
      contractTxid = (txid1 || "").trim().toLowerCase();
      if (!contractTxid) {
        contractTxid = new tbc.Transaction(txPair[1]).hash;
      }
      await sleep(postMintWait);
      await waitPoolNftUTXOReady(poolNFT2, contractTxid, network, poolIndexWait);
    }
  } else {
    if (!/^[0-9a-fA-F]{64}$/.test(contractTxid)) {
      throw new Error("无效的 TBC_POOLNFT2_POOL_CONTRACT_TXID");
    }
    await waitPoolNftUTXOReady(poolNFT2, contractTxid, network, poolIndexWait);
  }

  console.log(JSON.stringify({ event: "using_pool_contract", contractTxid }));

  let ftBal;
  try {
    ftBal = await API.getFTbalance(ftContract, addressA, network);
  } catch (e) {
    ftBal = "0";
  }
  const ftBalBn = typeof ftBal === "bigint" ? ftBal : BigInt(String(ftBal));
  console.log(
    JSON.stringify({
      event: "ft_balance",
      addressA,
      ftContract,
      balance: ftBalBn.toString(),
    }),
  );
  if (ftBalBn === 0n) {
    throw new Error("FT 余额为 0，无法 init");
  }

  const poolUse = new poolNFT2({ txid: contractTxid, network });
  if (!skipBroadcast || process.env.TBC_POOLNFT2_POOL_CONTRACT_TXID) {
    await poolUse.initfromContractId();
  } else {
    console.log(
      JSON.stringify({
        event: "skip_initfromContractId",
        reason: "SKIP_BROADCAST 且无链上池，无法拉 poolinfo；下面仅解析 init raw（需自行改脚本注入链上 pool）",
      }),
    );
    process.exit(0);
  }

  const extra = await poolUse.getPoolNftExtraInfo();
  console.log(
    JSON.stringify({
      event: "pool_state_after_initfromContractId",
      contractTxid,
      with_lock: poolUse.with_lock,
      with_lock_time: poolUse.with_lock_time,
      lp_plan: poolUse.lp_plan,
      service_fee_rate: poolUse.service_fee_rate,
      tape_extra: extra,
      poolnft_code_hex_len: (poolUse.poolnft_code || "").length,
    }),
  );

  const utxoInit = await API.fetchUTXO(
    privateKeyA,
    initTbc + feeTbc + initUtxoBuffer,
    network,
  );
  const txInitRaw = await poolUse.initPoolNFT(
    privateKeyA,
    addressA,
    utxoInit,
    initTbc,
    initFt,
  );

  console.log(
    JSON.stringify({
      event: "init_tx_built",
      init_raw_length: txInitRaw.length,
      init_input_count: new tbc.Transaction(txInitRaw).inputs.length,
      init_output_count: new tbc.Transaction(txInitRaw).outputs.length,
    }),
  );

  if (process.env.TBC_POOLNFT2_DUMP_INIT_RAW === "1") {
    console.log(JSON.stringify({ event: "init_raw_hex", raw: txInitRaw }));
  }

  if (process.env.TBC_POOLNFT2_SKIP_BROADCAST === "1") {
    console.log(JSON.stringify({ event: "skip_broadcast", TBC_POOLNFT2_SKIP_BROADCAST: "1" }));
    return;
  }

  try {
    const initTxid = await API.broadcastTXraw(txInitRaw, network);
    console.log(
      JSON.stringify({
        event: "init_broadcast_ok",
        init_txid: initTxid,
        contractTxid,
      }),
    );
  } catch (e) {
    console.log(
      JSON.stringify({
        event: "init_broadcast_fail",
        err: (e && e.message) || String(e),
        contractTxid,
      }),
    );
    process.exitCode = 1;
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
