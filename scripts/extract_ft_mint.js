const fs = require("fs");
const p = require("path").join(__dirname, "../../tbc-contract/lib/contract/ft.js");
const s = fs.readFileSync(p, "utf8");
const start = s.indexOf("getFTmintCode(txid, vout, address, tapeSize)");
if (start < 0) process.exit(1);
const sub = s.slice(start);
const marker = "const codeScript = new tbc.Script(`";
const idx = sub.indexOf(marker);
if (idx < 0) process.exit(2);
let i = idx + marker.length;
let out = "";
for (; i < sub.length; i++) {
  const c = sub[i];
  if (c === "`") break;
  if (c === "\\" && sub[i + 1] === "`") {
    out += "`";
    i++;
    continue;
  }
  out += c;
}
const outPath = require("path").join(__dirname, "../lib/contract/ft_mint_template.asm");
fs.writeFileSync(outPath, out);
console.log("written", out.length);
