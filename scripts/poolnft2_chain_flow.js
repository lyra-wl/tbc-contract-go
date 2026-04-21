#!/usr/bin/env node
/**
 * PoolNFT 2.0 线性池链上流程（对齐 tbc-contract/docs/poolNFT2.0.md）：
 * createPoolNFT（两笔广播）→ initPoolNFT → consumeLP → swaptoToken_baseTBC → swaptoTBC_baseToken
 *
 * 依赖：tbc-contract 仓库（含 node_modules/tbc-lib-js）。
 *
 * 环境变量：
 *   TBC_CONTRACT_ROOT     默认：本脚本上两级目录下的 tbc-contract（即与 tbc-contract-go 同级的 tbc-contract）
 *   TBC_NETWORK           testnet | mainnet（默认 testnet）
 *   TBC_POOLNFT2_PRIVATE_KEY_WIF  必填，WIF 私钥
 *   TBC_POOLNFT2_FT_CONTRACT_TXID 底层 FT 合约 txid（64 hex）；testnet 未设且未关闭自铸时见下
 *   TBC_POOLNFT2_MINT_TESTNET_FT  testnet 下是否先自铸新 FT 再建池（默认 1，与 create_poolnft.ts 一致；设 0 则改用 TBC_POOLNFT2_FT_CONTRACT_TXID 或默认 ade89567…）
 *   TBC_POOLNFT2_FT_MINT_NAME / TBC_POOLNFT2_FT_MINT_SYMBOL 自铸 FT 名称与符号（可选）
 *   TBC_POOLNFT2_FEE_TBC  单笔 fetchUTXO 用 TBC 额度（默认 0.01，与 create_poolnft.ts 一致）
 *   TBC_POOLNFT2_INIT_TBC initPoolNFT 注入 TBC（默认 25）
 *   TBC_POOLNFT2_INIT_FT  initPoolNFT 注入 FT 数量（默认 800）
 *   TBC_POOLNFT2_MAIN_PRIVATE_KEY_WIF 可选：主地址私钥；若设置且与 TBC_POOLNFT2_PRIVATE_KEY_WIF 不同，
 *                                     则在 init 前检测「操作地址」TBC，不足时从主地址转入（同 create_poolnft.ts Step 4.5）
 *   TBC_POOLNFT2_CONSUME_LP  consumeLP 数量，与 poolNFT2 一致的小数字符串（默认 "1" 表示 1.000000 LP）
 *   TBC_POOLNFT2_SWAP_TBC   TBC→Token 的 TBC 额（默认 0.05）
 *   TBC_POOLNFT2_SWAP_TOKEN Token→TBC 的 token 额（默认 50）
 *   TBC_POOLNFT2_POST_MINT_WAIT_MS  铸池第二笔广播后等待毫秒（默认 12000）
 *   TBC_POOLNFT2_POOL_INDEX_WAIT_MS 轮询池子索引超时毫秒（默认 120000）
 *   TBC_POOLNFT2_INIT_BROADCAST_RETRIES init_pool 广播失败（如 OP_EQUALVERIFY）时的重试次数（默认 6）
 *   TBC_POOLNFT2_INIT_UTXO_BUFFER_TBC  init 时 fetchUTXO 在 initTbc+fee 之外的缓冲（默认 2）
 *
 * 默认与仓库根目录 create_poolnft.ts 成功路径对齐：LP_PLAN=1、TAG=tenbillion_pool、SERVICE_RATE=25、FEE_TBC=0.01、init 25/800。
 */

const path = require("path");
const fs = require("fs");

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

function defaultContractRoot() {
  const here = __dirname;
  return path.resolve(here, "..", "..", "tbc-contract");
}

function loadDeps() {
  const root = process.env.TBC_CONTRACT_ROOT || defaultContractRoot();
  const indexJs = path.join(root, "index.js");
  const tbcPath = path.join(root, "node_modules", "tbc-lib-js");
  if (!fs.existsSync(indexJs)) {
    throw new Error(
      `找不到 tbc-contract：${indexJs}。请设置 TBC_CONTRACT_ROOT 或把 tbc-contract 放在与 tbc-contract-go 同级目录。`,
    );
  }
  if (!fs.existsSync(tbcPath)) {
    throw new Error(`请在 ${root} 下执行 npm install（需要 tbc-lib-js）。`);
  }
  // eslint-disable-next-line import/no-dynamic-require, global-require
  const tbc = require(tbcPath);
  // eslint-disable-next-line import/no-dynamic-require, global-require
  const { API, poolNFT2, FT } = require(indexJs);
  return { tbc, API, poolNFT2, FT, contractRoot: root };
}

async function broadcastMaybeRetry(API, raw, network, label) {
  try {
    const txid = await API.broadcastTXraw(raw, network);
    console.log(JSON.stringify({ step: label, ok: true, txid }));
    return txid;
  } catch (e) {
    const msg = (e && e.message) || String(e);
    if (/already|duplicate|known/i.test(msg)) {
      console.log(JSON.stringify({ step: label, ok: true, note: "already-known", err: msg }));
      return "";
    }
    throw e;
  }
}

/** 铸池后等待索引器能返回 Pool NFT UTXO（避免 init 脚本与链上不一致导致 OP_EQUALVERIFY） */
/** testnet 自铸 FT（create_poolnft.ts ftMint 同源），返回新 FT 合约 txid（小写 hex） */
async function mintFreshFTForTestnet({
  tbc,
  API,
  FT,
  network,
  privateKey,
  address,
  initFt,
}) {
  const name = (process.env.TBC_POOLNFT2_FT_MINT_NAME || "pool2e2e").trim();
  const symbol = (process.env.TBC_POOLNFT2_FT_MINT_SYMBOL || "p2e").trim();
  const decimal = 6;
  const supply = Math.max(Number(initFt) * 2000, 10_000_000);
  const newToken = new FT({ name, symbol, amount: supply, decimal });
  const utxo = await API.fetchUTXO(privateKey, 0.02, network);
  const mintTX = newToken.MintFT(privateKey, address, utxo);
  await broadcastMaybeRetry(API, mintTX[0], network, "ft_mint_source");
  await sleep(3000);
  const ftTxid = await broadcastMaybeRetry(API, mintTX[1], network, "ft_mint_contract");
  await sleep(8000);
  let txid = (ftTxid || "").trim().toLowerCase();
  if (!txid) {
    txid = new tbc.Transaction(mintTX[1]).hash.toLowerCase();
  }
  console.log(JSON.stringify({ event: "minted_ft_contract", ftContract: txid }));
  return txid;
}

/** 与 API.fetchUTXO 内部 parseDecimalToBigInt(..., 6) 一致：展示 TBC → satoshi */
function displayTbcToSatoshi(amount) {
  const s = Number(amount).toFixed(6);
  const [integerPart, fractionalPart = ""] = s.split(".");
  const paddedFractional = fractionalPart.padEnd(6, "0").slice(0, 6);
  return Number(BigInt(integerPart + paddedFractional));
}

/**
 * create_poolnft.ts 同款：操作地址 TBC 不足 init 时，从主钥地址转入 satoshi。
 * mainWif 与 operatorWif 相同或主地址与操作地址相同时跳过。
 */
async function fundOperatorTbcIfNeeded(
  tbc,
  API,
  network,
  privateKeyMain,
  privateKeyOperator,
  addressOperator,
  initTbc,
  feeTbc,
  initUtxoBuffer,
) {
  const addrMain = privateKeyMain.toAddress().toString();
  if (addrMain === addressOperator) {
    return;
  }
  const needDisplay = Number((initTbc + feeTbc + initUtxoBuffer).toFixed(6));
  const needSat = displayTbcToSatoshi(needDisplay);
  const extraBufferSat = displayTbcToSatoshi(0.5);
  let balA = Number(await API.getTBCbalance(addressOperator, network));
  if (balA >= needSat) {
    return;
  }
  const topUpSat = Math.ceil(needSat - balA + extraBufferSat);
  const mainFetchTbc = topUpSat / 1e6 + feeTbc + 0.05;
  console.log(
    JSON.stringify({
      event: "fund_addressA_for_init",
      from: addrMain,
      to: addressOperator,
      balA_before: balA,
      needSat,
      topUpSat,
    }),
  );
  const utxoMain = await API.fetchUTXO(privateKeyMain, mainFetchTbc, network);
  const tx = new tbc.Transaction()
    .from(utxoMain)
    .to(addressOperator, topUpSat)
    .change(privateKeyMain.toAddress());
  const txSize = tx.getEstimateSize();
  tx.fee(txSize < 1000 ? 80 : Math.ceil((txSize / 1000) * 80));
  tx.sign(privateKeyMain).seal();
  await broadcastMaybeRetry(API, tx.uncheckedSerialize(), network, "topup_tbc_to_operator");
  await sleep(3000);
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
  throw new Error(
    `等待 Pool NFT 索引超时（${maxMs}ms），最后错误: ${lastErr}。合约 txid: ${contractTxid}`,
  );
}

async function main() {
  const { tbc, API, poolNFT2, FT, contractRoot } = loadDeps();
  console.log(JSON.stringify({ event: "deps", TBC_CONTRACT_ROOT: contractRoot }));

  const network = (process.env.TBC_NETWORK || "testnet").trim();
  const wif = (process.env.TBC_POOLNFT2_PRIVATE_KEY_WIF || "").trim();
  if (!wif) {
    throw new Error("请设置环境变量 TBC_POOLNFT2_PRIVATE_KEY_WIF（WIF）");
  }

  const feeTbc = Number(process.env.TBC_POOLNFT2_FEE_TBC || "0.01");
  const initTbc = Number(process.env.TBC_POOLNFT2_INIT_TBC || "25");
  const initFt = Number(process.env.TBC_POOLNFT2_INIT_FT || "800");

  const defaultFtTestnet =
    "ade895678bb988155f4da6a7b1aedd687491bc2b07806c1d4c80a3af8b98dd93";
  const mintTestnetFt =
    network === "testnet" &&
    (process.env.TBC_POOLNFT2_MINT_TESTNET_FT || "1").trim() !== "0";

  const privateKeyA = tbc.PrivateKey.fromString(wif);
  const addressA = tbc.Address.fromPrivateKey(privateKeyA).toString();

  let ftContract = (process.env.TBC_POOLNFT2_FT_CONTRACT_TXID || "").trim();
  if (mintTestnetFt && !ftContract) {
    ftContract = await mintFreshFTForTestnet({
      tbc,
      API,
      FT,
      network,
      privateKey: privateKeyA,
      address: addressA,
      initFt,
    });
  } else if (!ftContract && network === "testnet") {
    ftContract = defaultFtTestnet;
    console.log(JSON.stringify({ event: "default_ft_contract", ftContract }));
  }
  if (!/^[0-9a-fA-F]{64}$/.test(ftContract)) {
    throw new Error("无效的 TBC_POOLNFT2_FT_CONTRACT_TXID（需 64 位 hex）");
  }
  ftContract = ftContract.toLowerCase();
  const consumeLp = String(process.env.TBC_POOLNFT2_CONSUME_LP || "1");
  const swapTbc = Number(process.env.TBC_POOLNFT2_SWAP_TBC || "0.05");
  const swapToken = Number(process.env.TBC_POOLNFT2_SWAP_TOKEN || "50");

  const serviceRate = Number(process.env.TBC_POOLNFT2_SERVICE_RATE || "25");
  const lpPlan = Number(process.env.TBC_POOLNFT2_LP_PLAN || "1");
  const tag = (process.env.TBC_POOLNFT2_TAG || "tenbillion_pool").trim();

  const mainWif = (process.env.TBC_POOLNFT2_MAIN_PRIVATE_KEY_WIF || "").trim();

  const postMintWait = Number(process.env.TBC_POOLNFT2_POST_MINT_WAIT_MS || "12000");
  const poolIndexWait = Number(process.env.TBC_POOLNFT2_POOL_INDEX_WAIT_MS || "120000");
  const initBroadcastRetries = Number(process.env.TBC_POOLNFT2_INIT_BROADCAST_RETRIES || "6");
  const initUtxoBuffer = Number(process.env.TBC_POOLNFT2_INIT_UTXO_BUFFER_TBC || "2");

  let ftBal;
  try {
    ftBal = await API.getFTbalance(ftContract, addressA, network);
  } catch (e) {
    console.log(JSON.stringify({ event: "getFTbalance_error", err: (e && e.message) || String(e) }));
    ftBal = "0";
  }
  const ftBalBn = typeof ftBal === "bigint" ? ftBal : BigInt(String(ftBal));
  console.log(
    JSON.stringify({ event: "ft_balance_before_create", addressA, ftContract, balance: ftBalBn.toString() }),
  );
  if (ftBalBn === 0n) {
    throw new Error(
      `该地址在 FT 合约 ${ftContract} 下余额为 0，无法 initPoolNFT。请先向 ${addressA} 转入该 FT，或设置 TBC_POOLNFT2_FT_CONTRACT_TXID。`,
    );
  }

  const pool = new poolNFT2({ network });
  pool.initCreate(ftContract);

  // --- Step 1: create (source + mint) ---
  const utxoCreate = await API.fetchUTXO(privateKeyA, feeTbc, network);
  const txPair = await pool.createPoolNFT(
    privateKeyA,
    utxoCreate,
    tag,
    serviceRate,
    lpPlan === 2 ? 2 : 1,
    false,
  );
  await broadcastMaybeRetry(API, txPair[0], network, "create_source");
  await sleep(4000);
  const poolMintTxid = await broadcastMaybeRetry(API, txPair[1], network, "create_mint");
  await sleep(postMintWait);

  let contractTxid = (poolMintTxid || "").trim();
  if (!contractTxid) {
    const tx = new tbc.Transaction(txPair[1]);
    contractTxid = tx.hash;
  }
  contractTxid = String(contractTxid).toLowerCase();
  console.log(JSON.stringify({ event: "pool_contract_txid", contractTxid }));

  await waitPoolNftUTXOReady(poolNFT2, contractTxid, network, poolIndexWait);

  // --- Step 2: init pool ---
  const poolUse = new poolNFT2({ txid: contractTxid, network });
  let initOk = false;
  for (let r = 0; r < initBroadcastRetries && !initOk; r++) {
    await poolUse.initfromContractId();
    if (mainWif) {
      const pkMain = tbc.PrivateKey.fromString(mainWif);
      await fundOperatorTbcIfNeeded(
        tbc,
        API,
        network,
        pkMain,
        privateKeyA,
        addressA,
        initTbc,
        feeTbc,
        initUtxoBuffer,
      );
    }
    const utxoInit = await API.fetchUTXO(
      privateKeyA,
      initTbc + feeTbc + initUtxoBuffer,
      network,
    );
    const txInit = await poolUse.initPoolNFT(
      privateKeyA,
      addressA,
      utxoInit,
      initTbc,
      initFt,
    );
    try {
      await broadcastMaybeRetry(API, txInit, network, "init_pool");
      initOk = true;
    } catch (e) {
      const msg = (e && e.message) || String(e);
      console.log(JSON.stringify({ event: "init_pool_broadcast_retry", attempt: r + 1, err: msg }));
      if (r === initBroadcastRetries - 1) {
        throw e;
      }
      await sleep(8000);
      await poolUse.initfromContractId();
    }
  }
  await sleep(5000);

  await poolUse.initfromContractId();

  // --- Step 3: consume LP ---
  const utxoFee1 = await API.fetchUTXO(privateKeyA, feeTbc, network);
  const txConsume = await poolUse.consumeLP(privateKeyA, addressA, utxoFee1, consumeLp);
  await broadcastMaybeRetry(API, txConsume, network, "consume_lp");
  await sleep(5000);

  await poolUse.initfromContractId();

  // --- Step 4: TBC -> token ---
  const utxoSwap1 = await API.fetchUTXO(privateKeyA, swapTbc + feeTbc, network);
  const txSwapTbc = await poolUse.swaptoToken_baseTBC(
    privateKeyA,
    addressA,
    utxoSwap1,
    swapTbc,
    lpPlan === 2 ? 2 : 1,
  );
  await broadcastMaybeRetry(API, txSwapTbc, network, "swap_tbc_to_token");
  await sleep(5000);

  await poolUse.initfromContractId();

  // --- Step 5: token -> TBC ---
  const utxoFee2 = await API.fetchUTXO(privateKeyA, feeTbc, network);
  const txSwapFt = await poolUse.swaptoTBC_baseToken(
    privateKeyA,
    addressA,
    utxoFee2,
    swapToken,
    lpPlan === 2 ? 2 : 1,
  );
  await broadcastMaybeRetry(API, txSwapFt, network, "swap_token_to_tbc");

  console.log(
    JSON.stringify({
      event: "done",
      contractTxid,
      network,
    }),
  );
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
