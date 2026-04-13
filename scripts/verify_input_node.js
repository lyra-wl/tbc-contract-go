#!/usr/bin/env node
'use strict'

/**
 * stdin: JSON 一行或多行拼接，解析为对象：
 *   txHex        当前交易 raw hex
 *   prevTxHex    所花费 UTXO 所在父交易 raw hex
 *   inputIndex   输入下标
 *   flags        可选："minimal"|"go"（默认，对齐 go-bt WithForkID+WithAfterGenesis 所需 opcode）、"default"（Interpreter.DEFAULT_FLAGS）
 *
 * argv[2]: tbc-lib-js 根目录绝对路径（含 index.js）
 *
 * stdout: 一行 JSON { ok, errstr?, error? }
 */
const fs = require('fs')
const path = require('path')

const libRoot = process.argv[2]
if (!libRoot) {
  console.log(JSON.stringify({ ok: false, error: 'missing argv[2] tbc-lib-js root' }))
  process.exit(1)
}

const tbc = require(path.join(libRoot, 'index.js'))
const Interpreter = tbc.Script.Interpreter

function readStdin () {
  return new Promise((resolve, reject) => {
    let b = ''
    process.stdin.setEncoding('utf8')
    process.stdin.on('data', (d) => { b += d })
    process.stdin.on('end', () => resolve(b))
    process.stdin.on('error', reject)
  })
}

function flagsFromSpec (spec) {
  if (spec == null || spec === '' || spec === 'minimal' || spec === 'go') {
    return Interpreter.SCRIPT_ENABLE_SIGHASH_FORKID |
      Interpreter.SCRIPT_ENABLE_MONOLITH_OPCODES |
      Interpreter.SCRIPT_ENABLE_MAGNETIC_OPCODES
  }
  if (spec === 'default') {
    return Interpreter.DEFAULT_FLAGS
  }
  if (typeof spec === 'number' && !isNaN(spec)) {
    return spec >>> 0
  }
  const n = parseInt(String(spec), 10)
  if (!isNaN(n)) {
    return n >>> 0
  }
  return Interpreter.SCRIPT_ENABLE_SIGHASH_FORKID |
    Interpreter.SCRIPT_ENABLE_MONOLITH_OPCODES |
    Interpreter.SCRIPT_ENABLE_MAGNETIC_OPCODES
}

;(async () => {
  const raw = (await readStdin()).trim()
  const j = JSON.parse(raw)
  const txHex = String(j.txHex || '').trim()
  const prevTxHex = String(j.prevTxHex || '').trim()
  const inputIndex = parseInt(j.inputIndex, 10)
  const flagsSpec = j.flags

  if (!txHex || !prevTxHex || isNaN(inputIndex)) {
    console.log(JSON.stringify({ ok: false, error: 'need txHex, prevTxHex, inputIndex' }))
    process.exit(0)
  }

  const tx = new tbc.Transaction(txHex)
  const prevTx = new tbc.Transaction(prevTxHex)
  if (inputIndex < 0 || inputIndex >= tx.inputs.length) {
    console.log(JSON.stringify({ ok: false, error: 'inputIndex out of range', nin: tx.inputs.length }))
    process.exit(0)
  }

  const input = tx.inputs[inputIndex]
  const vout = input.outputIndex
  if (vout < 0 || vout >= prevTx.outputs.length) {
    console.log(JSON.stringify({
      ok: false,
      error: 'vout out of range',
      vout,
      outs: prevTx.outputs.length
    }))
    process.exit(0)
  }

  const prevOut = prevTx.outputs[vout]
  const satoshisBN = prevOut.satoshisBN
  const flags = flagsFromSpec(flagsSpec)

  const interp = new Interpreter()
  const ok = interp.verify(
    input.script,
    prevOut.script,
    tx,
    inputIndex,
    flags,
    satoshisBN,
    undefined
  )

  console.log(JSON.stringify({
    ok: !!ok,
    errstr: interp.errstr || '',
    flagsUsed: flags
  }))
})().catch((e) => {
  console.log(JSON.stringify({ ok: false, error: String(e.message || e) }))
  process.exit(1)
})
