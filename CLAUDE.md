# orOS Drive: notes for contributors and agents

- Go 1.24, no cgo. `gofmt`, `go vet ./...`, `go test ./...` before every PR.
- The vault format is owned by koulaxizis/oros (`sync.js`, `vault.js`, `OROS_BIBLE.md` "vault.js"). This repo follows it; never invent format changes here. Any change that touches `vault/` must keep `OROS_SRC=<oros checkout> go test ./interop/` green (CI runs it against orOS main).
- Implement the Bible's VD rules exactly (VD-1 absent manifest deletes nothing, VD-4 local wins, VD-5 queue generations, VD-H hash check, FILES-V conflict copies). Each one came from a real data loss.
- `vault/testdata/` vaults were written by the real vault.js via `interop/vaultjs.cjs`; regenerate them with it, never by hand.
- Free forever, MIT, no Google dependencies, each user signs in with their own Dropbox (app key `wnohlxz79ie8w3e`, the orOS sync app).
- Platforms: Windows, Linux, Android first; Mac best effort.
