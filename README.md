# orOS Drive

Mirror chosen folders of your computer or phone into the encrypted [orOS](https://useoros.online) Vault, and later see the orOS disk as a drive. The Vault lives in your own Dropbox, end-to-end encrypted: Dropbox sees neither file names nor contents. Windows, Linux and Android. MIT, free forever.

**Status: wave 1 of 7.** The core speaks the Vault format and is proven against the real orOS code; the command line can sign in, list and copy files out of the Vault. It never writes to your Vault yet. Mirroring comes in wave 3.

## Command line (wave 1)

```
orosdrive login [--paste]     sign in to the Dropbox orOS syncs with
orosdrive status              account and vault summary
orosdrive ls [-l] [path]      list the orOS disk, e.g. `orosdrive ls -l Docs`
orosdrive pull <path> <dir>   copy a file or folder out of the Vault
orosdrive logout
```

- `login` opens Dropbox in your browser and waits on `http://localhost:47800/` (the address orOS Portable also uses, so close Portable first). `--paste` shows a link instead and you paste the code back, for machines without a browser.
- The orOS sync passphrase is asked for when needed (or taken from `OROS_PASSPHRASE`). It never leaves the computer and is not stored.
- The Dropbox sign-in is kept in your config folder (`%AppData%\orOS Drive` / `~/.config/orOS Drive`), readable by you only. It gives access to the orOS app folder only.

Builds for Windows and Linux (amd64, arm64) are attached to every CI run as the `orosdrive` artifact.

## How it works

orOS keeps your disk in `/vault` of its Dropbox app folder (the format is described in `OROS_BIBLE.md` of [koulaxizis/oros](https://github.com/koulaxizis/oros), section "vault.js"):

- `key.json`: one random 32-byte vault key, wrapped with your sync passphrase (PBKDF2-SHA-256, 600,000 rounds, AES-GCM). A passphrase change re-wraps it; the key itself stays.
- `manifest.json`: AES-GCM sealed list of every file (path, SHA-256, size, date).
- `objects/<HMAC>`: each file's content, sealed with the vault key, named by an HMAC of its hash so nobody can test whether a known file is stored.

orOS Drive implements the same format and the same rules (conditional writes on the manifest revision, a downloaded file must match its hash, an absent manifest deletes nothing), in Go:

| Package | |
|---|---|
| `vault/` | crypto, key file, manifest, download/upload, `DirStore` (a vault in a folder) |
| `dropbox/` | Dropbox API v2: PKCE sign-in, refresh, conditional uploads, upload sessions, retries |
| `cmd/orosdrive/` | the command line |
| `interop/` | runs the real `sync.js` + `vault.js` in Node to hand vaults back and forth |

## Tests

```
go test ./...                               # unit tests, golden vaults written by vault.js
OROS_SRC=../oros go test ./interop/         # against the live orOS code (needs Node)
```

CI runs both on every push, the interop job with `sync.js` and `vault.js` from orOS `main`, so a format change in orOS that this code does not follow turns CI red.

## Plan

1. Core + compatibility proof (this)
2. orOS: Drive folders load on demand in the browser, so phones don't fill up
3. Desktop mirror for Windows and Linux (tray app, two-way, conflict copies)
4. Android mirror inside the orOS Android app
5. Virtual drive on Linux (FUSE) and Android (Files app)
6. Virtual drive on Windows (Explorer, Cloud Files API)
7. Clean-up of unused objects, very large files

## Licence

MIT, see [LICENSE](LICENSE). Support orOS on [Ko-fi](https://ko-fi.com/koulaxizis).
