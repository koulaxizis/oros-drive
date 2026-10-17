// Runs the REAL orOS sync.js + vault.js (from a koulaxizis/oros
// checkout) in Node against a vault kept in a folder, so the Go code
// and the browser code can hand a vault back and forth.
//
//   node interop/vaultjs.cjs sync   <oros> <store> <disk> <state> <pass> [--touch] [--fetch-drive]
//   node interop/vaultjs.cjs legacy <oros> <store> <disk> <pass>
//
// <store>  vault keys as files (key.json, manifest.json, objects/…),
//          the same layout as Go's vault.DirStore.
// <disk>   the orOS disk: <disk>/a/b.txt is /internal/a/b.txt.
// <state>  JSON file with the device's localStorage (kept between runs,
//          like a browser profile).
// sync     loads <disk> into an in-memory orosFS, runs one
//          orosVault.sync(), writes <disk>, <store> and <state> back and
//          prints the stats as JSON. --touch first queues every file
//          (what fs.js does when the user edits files). --fetch-drive
//          then opens every on-demand Drive file (VD-OD) so it lands on
//          <disk>; with an orOS from before VD-OD it changes nothing.
// legacy   writes <disk> as a vault from before VD-KEY (passphrase-
//          sealed manifest, objects/<sha256>), as an old orOS did.

"use strict";

const fs = require("fs");
const path = require("path");
const vm = require("vm");
const { webcrypto } = require("crypto");

const [cmd, orosDir, storeDir, diskDir, ...rest] = process.argv.slice(2);
if (!cmd || !orosDir || !storeDir || !diskDir) {
  console.error("usage: see the header of this file");
  process.exit(2);
}

// ---------- the vault folder as a fake Dropbox ----------
const files = new Map();          // "/vault/<key>" -> { data: Buffer, rev }
let revN = 0;
function walk(dir, base, fn) {
  if (!fs.existsSync(dir)) return;
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    const rel = base ? base + "/" + e.name : e.name;
    if (e.isDirectory()) walk(p, rel, fn); else fn(rel, p);
  }
}
walk(storeDir, "", (rel, p) => files.set("/vault/" + rel, { data: fs.readFileSync(p), rev: "js" + (++revN) }));

function response(status, body, headers) {
  const h = new Map(Object.entries(headers || {}).map(([k, v]) => [k.toLowerCase(), v]));
  const buf = Buffer.isBuffer(body) ? body : Buffer.from(typeof body === "string" ? body : JSON.stringify(body));
  return {
    status, ok: status >= 200 && status < 300,
    headers: { get: (k) => (h.has(k.toLowerCase()) ? h.get(k.toLowerCase()) : null) },
    text: async () => buf.toString("utf8"),
    json: async () => JSON.parse(buf.toString("utf8")),
    arrayBuffer: async () => buf.buffer.slice(buf.byteOffset, buf.byteOffset + buf.length)
  };
}
async function bodyBytes(b) {
  if (b == null) return Buffer.alloc(0);
  if (typeof b === "string") return Buffer.from(b);
  if (b instanceof ArrayBuffer) return Buffer.from(b);
  if (ArrayBuffer.isView(b)) return Buffer.from(b.buffer, b.byteOffset, b.byteLength);
  if (typeof b.arrayBuffer === "function") return Buffer.from(await b.arrayBuffer());
  throw new Error("unknown body " + typeof b);
}
async function fakeFetch(url, opts) {
  opts = opts || {};
  const headers = opts.headers || {};
  const ep = String(url).replace(/^https:\/\/[^/]+\/2\//, "");
  const arg = headers["Dropbox-API-Arg"] ? JSON.parse(headers["Dropbox-API-Arg"]) : null;
  if (ep === "files/download") {
    const f = files.get(arg.path);
    if (!f) return response(409, { error_summary: "path/not_found/" });
    return response(200, f.data, { "dropbox-api-result": JSON.stringify({ rev: f.rev }) });
  }
  if (ep === "files/upload") {
    const cur = files.get(arg.path);
    const mode = arg.mode;
    let conflict = false;
    if (mode === "add") conflict = !!cur;
    else if (mode && mode[".tag"] === "update") conflict = !cur || cur.rev !== mode.update;
    if (conflict) return response(409, { error_summary: "path/conflict/file/.." });
    const rev = "js" + (++revN);
    files.set(arg.path, { data: await bodyBytes(opts.body), rev });
    return response(200, { rev });
  }
  const args = opts.body ? JSON.parse(opts.body) : {};
  if (ep === "files/get_metadata") {
    const f = files.get(args.path);
    if (!f) return response(409, { error_summary: "path/not_found/" });
    return response(200, { ".tag": "file", rev: f.rev });
  }
  if (ep === "files/delete_v2") { files.delete(args.path); return response(200, {}); }
  if (ep === "files/list_folder") {
    const entries = [...files.keys()].map((p) => ({ ".tag": "file", path_lower: p.toLowerCase() }));
    return response(200, { entries, has_more: false });
  }
  throw new Error("fake Dropbox: unhandled " + ep);
}

// ---------- the orOS disk as an in-memory orosFS ----------
const disk = new Map();           // "/internal/a/b" -> Blob
walk(diskDir, "", (rel, p) => disk.set("/internal/" + rel, new Blob([fs.readFileSync(p)])));
function enoent(p) { const e = new Error("ENOENT " + p); e.code = "ENOENT"; return e; }
function isDir(p) { const pre = p + "/"; for (const k of disk.keys()) if (k.startsWith(pre)) return true; return false; }
const orosFS = {
  ready: async () => {},
  stat: async (p) => {
    if (disk.has(p)) return { dir: false, size: disk.get(p).size };
    if (p === "/internal" || isDir(p)) return { dir: true, size: 0 };
    throw enoent(p);
  },
  ls: async (p) => {
    const pre = p + "/", seen = new Map();
    for (const k of disk.keys()) {
      if (!k.startsWith(pre)) continue;
      const rest = k.slice(pre.length), i = rest.indexOf("/");
      seen.set(i < 0 ? rest : rest.slice(0, i), i >= 0);
    }
    return [...seen].map(([name, dir]) => ({ name, dir }));
  },
  read: async (p) => { if (!disk.has(p)) { if (isDir(p)) { const e = new Error("EISDIR"); e.code = "EISDIR"; throw e; } throw enoent(p); } return disk.get(p); },
  write: async (p, blob) => { disk.set(p, blob); },
  rm: async (p) => { if (!disk.delete(p)) throw enoent(p); }
};

// ---------- a booted device ----------
class MemoryStorage {
  constructor(init) { this.map = new Map(Object.entries(init || {})); }
  getItem(k) { return this.map.has(k) ? this.map.get(k) : null; }
  setItem(k, v) { this.map.set(k, String(v)); }
  removeItem(k) { this.map.delete(k); }
  key(i) { return [...this.map.keys()][i] || null; }
  get length() { return this.map.size; }
}

async function device(pass, stateFile) {
  let init = {};
  if (stateFile && fs.existsSync(stateFile)) init = JSON.parse(fs.readFileSync(stateFile, "utf8"));
  const localStorage = new MemoryStorage(init);
  localStorage.setItem("oros-db-access", "token");
  localStorage.setItem("oros-db-refresh", "refresh");
  localStorage.setItem("oros-db-expiry", String(Date.now() + 864e5));
  localStorage.setItem("oros-sync-interval", "0");
  const noop = () => {};
  const ctx = {
    localStorage, sessionStorage: new MemoryStorage(),
    document: { getElementById: () => null, addEventListener: noop, visibilityState: "visible" },
    navigator: { onLine: true, userAgent: "oros-drive-interop" },
    location: { search: "", origin: "https://interop.invalid", href: "" },
    history: { replaceState: noop },
    crypto: webcrypto, fetch: fakeFetch,
    console: { log: noop, info: noop, warn: noop, error: (...a) => console.error(...a) },
    setTimeout: () => 0, clearTimeout: noop, setInterval: () => 0, clearInterval: noop,
    addEventListener: noop,
    TextEncoder, TextDecoder, URLSearchParams, Uint8Array, ArrayBuffer, Blob,
    btoa, atob, Promise, JSON, Date, Math, Error, TypeError, Object, Array
  };
  ctx.window = ctx;
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(orosDir, "sync.js"), "utf8"), ctx, { filename: "sync.js" });
  await ctx.orosSync.vaultUnlocked;
  ctx.orosSync.setPassphrase(pass, false);
  ctx.orosFS = orosFS;
  vm.runInContext(fs.readFileSync(path.join(orosDir, "vault.js"), "utf8"), ctx, { filename: "vault.js" });
  return ctx;
}

function rmrf(d) { fs.rmSync(d, { recursive: true, force: true }); }
async function dumpDisk() {
  rmrf(diskDir);
  fs.mkdirSync(diskDir, { recursive: true });
  for (const [p, blob] of disk) {
    const out = path.join(diskDir, ...p.slice("/internal/".length).split("/"));
    fs.mkdirSync(path.dirname(out), { recursive: true });
    fs.writeFileSync(out, Buffer.from(await blob.arrayBuffer()));
  }
}
function dumpStore() {
  rmrf(storeDir);
  fs.mkdirSync(storeDir, { recursive: true });
  for (const [p, f] of files) {
    if (!p.startsWith("/vault/")) continue;
    const out = path.join(storeDir, ...p.slice("/vault/".length).split("/"));
    fs.mkdirSync(path.dirname(out), { recursive: true });
    fs.writeFileSync(out, f.data);
  }
}

async function main() {
  if (cmd === "sync") {
    const [stateFile, pass, ...flags] = rest;
    const ctx = await device(pass, stateFile);
    if (flags.includes("--touch")) await ctx.orosVault.touchTree("/internal");
    const res = await ctx.orosVault.sync("interop");
    if (flags.includes("--fetch-drive") && typeof ctx.orosVault.remoteOnly === "function") {
      for (const e of ctx.orosVault.remoteOnly()) await ctx.orosVault.fetch(e.path);
    }
    await dumpDisk();
    dumpStore();
    const keep = {};
    for (const [k, v] of ctx.localStorage.map) if (k.startsWith("oros-vault-")) keep[k] = v;
    fs.writeFileSync(stateFile, JSON.stringify(keep));
    process.stdout.write(JSON.stringify(res.stats || res) + "\n");
    return;
  }
  if (cmd === "legacy") {
    const [pass] = rest;
    const ctx = await device(pass, null);
    const vc = ctx.orosSync.vaultCrypto;
    const manifest = { ver: 1, files: {} };
    for (const [p, blob] of disk) {
      const buf = await blob.arrayBuffer();
      const h = await vc.sha256Hex(buf);
      manifest.files[p] = { h, s: blob.size, m: new Date().toISOString(), mime: "application/octet-stream" };
      const env = await vc.encryptBytes(buf);
      files.set("/vault/objects/" + h, { data: Buffer.from(env), rev: "js" + (++revN) });
    }
    files.set("/vault/manifest.json", { data: Buffer.from(await vc.encryptJson(manifest)), rev: "js" + (++revN) });
    dumpStore();
    return;
  }
  throw new Error("unknown command " + cmd);
}

main().catch((e) => { console.error(e && e.stack || e); process.exit(1); });
