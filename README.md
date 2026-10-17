# orOS Drive

Mirror chosen folders of your computer or phone into the encrypted [orOS](https://useoros.online) Vault, and later see the orOS disk as a drive. The Vault lives in your own Dropbox, end-to-end encrypted: Dropbox sees neither file names nor contents. Windows, Linux and Android. MIT, free forever.

**Status: wave 5 of 7.** The desktop app mirrors folders both ways between Windows or Linux and the Vault, and the orOS Android app mirrors phone folders (wave 4). orOS shows them in Files › orOS Drive and downloads a file only when you open it. On Linux the whole orOS disk can also appear as a folder, and on Android as "orOS" in the Files app (wave 5).

## The app

Start `orOS Drive.exe` (Windows) or `orosdrive` (Linux; the .deb adds it to the menu). The settings open in your browser:

1. **Sign in to Dropbox**, the account orOS syncs with. Sign-in comes back to `http://localhost:47800/` (the address orOS Portable also uses, so close Portable for that moment).
2. **Unlock the vault** with your orOS sync passphrase. It unwraps the vault key, which goes into the system keychain (Windows Credential Manager, the Secret Service on Linux). The passphrase itself is never stored. Without a keychain (a Linux server) the key is kept in a file only you can read, and the page says so.
3. **Add folders.** Each one appears in orOS under `Drive/<this computer>/<folder>`.

The app then lives in the tray (on GNOME the tray needs the AppIndicator extension; without it the settings page is the way in), starts at login if you tick it, and syncs when a mirrored folder changes, when the Vault changes (checked every 30 seconds) and every 10 minutes.

### What happens to your files

- Changes go both ways: new files, edits, renames (as delete + add) and deletions.
- **The same file changed on both sides:** the computer's version keeps the name, orOS's version is kept beside it as `name (conflict YYYYMMDD-HHMMSS).ext`, on both sides. Deleted on one side but edited on the other: the edit wins.
- **The first sync of a folder only adds.** Nothing is deleted on either side until both sides have seen a file.
- **Many deletions at once** (more than 50, adjustable) wait for your yes in the settings page.
- **A missing folder** (an unplugged disk) pauses that folder; it is never read as "everything was deleted". An empty Vault after a reset stops the mirror instead of rebuilding it.
- **Files deleted here because they were deleted in orOS** go to the orOS Drive trash (Settings › Open the trash) for 30 days.
- Files bigger than 500 MB (adjustable) stay on this computer and are listed. Lock and temp files (`~$*.docx`, `*.part`, `*.tmp`, `Thumbs.db`, `.DS_Store`) are never mirrored. Names Windows cannot hold (`a:b.txt`) stay in orOS and are listed.
- A file that changed in the last 3 seconds waits for the next pass, and a local change made while syncing always wins.

### The orOS disk as a folder (Linux)

Settings › "The orOS disk as a folder" (or `orosdrive mount`) shows the whole orOS disk at `~/orOS` while the app runs. Files download when a program opens them and upload when it saves them; what you opened stays in a cache (2 GB, the oldest copies go first). Saving works like anywhere else: programs that write a temp file and rename it over the original (office suites, editors) are fine, and their lock and temp files stay on this computer. A file changed in orOS while you had it open is kept beside yours as a conflict copy; a folder you delete keeps any file someone else changed meanwhile. Needs FUSE (`fuse3`, present on most desktops). Empty folders you make here show on this computer until a file goes in them (the vault keeps files only).

## Command line

```
orosdrive [run] [--no-tray] [--background]   the app (no tray: servers)
orosdrive open                    open the settings page of the running app
orosdrive login [--paste]         sign in to the Dropbox orOS syncs with
orosdrive unlock                  unlock with the orOS sync passphrase
orosdrive add [--name N] <folder> mirror a folder
orosdrive folders | remove <name> | sync
orosdrive status                  account and vault summary
orosdrive ls [-l] [path]          list the orOS disk, e.g. `orosdrive ls -l Docs`
orosdrive pull <path> <dir>       copy a file or folder out of the Vault
orosdrive mount [<folder> | off]  show the whole orOS disk as a folder (Linux)
orosdrive logout
```

When the app is running, `add`, `remove`, `unlock` and `sync` go through it. `--paste` signs in on machines without a browser.

## Downloads

Releases (tag `v*`) carry Windows zips (`orOS Drive.exe` without a console window + `orosdrive.exe` for the terminal), Linux tarballs and `.deb` packages for amd64 and arm64. Every CI run attaches the same files as the `orosdrive` artifact. The Windows exe is not signed yet, so SmartScreen may warn the first time ("More info › Run anyway").

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
| `mirror/` | the two-way mirror: scan, three-way merge per file, conflict copies, trash, safety limits |
| `vdisk/` | the whole disk as a file system: on-demand downloads, cache, conflict-safe writes; `vdisk/fusefs` mounts it on Linux |
| `mobile/` | the API the orOS Android app binds with gomobile (phone folders, the Files-app provider) |
| `app/` | the desktop app: background loop, folder watcher, settings page (`app/web`), tray, autostart |
| `internal/secret/` | keychain storage for the Dropbox sign-in and the vault key |
| `internal/config/` | settings (`app.json`) and per-folder state |
| `cmd/orosdrive/` | the program: the app and the command line |
| `packaging/` | release files (`package.sh`) and the smoke test CI runs on Windows and Linux |
| `interop/` | runs the real `sync.js` + `vault.js` in Node to hand vaults back and forth |

## Tests

```
go test ./...                               # unit tests, golden vaults written by vault.js
OROS_SRC=../oros go test ./interop/         # against the live orOS code (needs Node)
```

CI runs both on every push, the interop job with `sync.js` and `vault.js` from orOS `main`, so a format change in orOS that this code does not follow turns CI red.

## Plan

1. Core + compatibility proof (done)
2. orOS: Drive folders load on demand in the browser, so phones don't fill up (done)
3. Desktop mirror for Windows and Linux (done)
4. Android mirror inside the orOS Android app (done)
5. Virtual drive on Linux (FUSE) and Android (Files app) (this)
6. Virtual drive on Windows (Explorer, Cloud Files API)
7. Clean-up of unused objects, very large files

## Licence

MIT, see [LICENSE](LICENSE). Support orOS on [Ko-fi](https://ko-fi.com/koulaxizis).
