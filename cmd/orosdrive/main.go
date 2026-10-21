// orosdrive is orOS Drive: with no command (or "run") it is the
// desktop app that mirrors folders into the orOS Vault (tray icon and
// a settings page in the browser); the commands below drive it from a
// terminal, which also makes it usable on a Linux server.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/koulaxizis/oros-drive/app"
	"github.com/koulaxizis/oros-drive/dropbox"
	"github.com/koulaxizis/oros-drive/internal/secret"
	"github.com/koulaxizis/oros-drive/vault"
	"github.com/koulaxizis/oros-drive/webdav"
)

var version = "dev"

const usage = `orOS Drive %s: your orOS Vault from this computer.

Usage:
  orosdrive [run] [--no-tray] [--background]
                                 the app: mirrors your folders, tray icon, settings page
  orosdrive open                 open the settings page of the running app
  orosdrive login [--paste]      sign in to your Dropbox (the one orOS syncs with)
  orosdrive login --nextcloud <server> [--dir /orOS]
                                 sign in to your Nextcloud (approve in the browser)
  orosdrive login --webdav <url> --user <name> [--dir /orOS]
                                 use any WebDAV folder (app password asked for,
                                 or read from OROS_DAV_PASSWORD)
  orosdrive logout               forget the sign-in and the vault key
  orosdrive unlock               open the vault key with the orOS sync passphrase
                                 (kept in the system keychain; the passphrase is not)
  orosdrive add [--name N] <folder>
                                 mirror a folder into orOS (Files › orOS Drive)
  orosdrive folders              list mirrored folders
  orosdrive remove <name>        stop mirroring a folder (files stay on both sides)
  orosdrive sync                 one sync now (without the app running)
  orosdrive status               account and vault summary
  orosdrive ls [-l] [path]       list files in the orOS disk (path like Docs/Work)
  orosdrive pull [--force] <path> <folder>
                                 copy a file or folder of the orOS disk into <folder>
  orosdrive usage [--clean]      space used in the cloud; --clean removes content no
                                 file has used for 30 days
  orosdrive mount [<folder> | off]
                                 show the whole orOS disk as <folder> while the app
                                 runs (Linux, Windows; default ~/orOS)
  orosdrive version

The orOS sync passphrase is asked for when needed (or read from
OROS_PASSPHRASE). It never leaves this computer and is not stored.
`

func main() {
	var err error
	cmd, args := "run", []string{}
	if len(os.Args) >= 2 {
		cmd, args = os.Args[1], os.Args[2:]
	}
	if strings.HasPrefix(cmd, "--") && cmd != "--help" && cmd != "--version" {
		cmd, args = "run", os.Args[1:]
	}
	switch cmd {
	case "run":
		err = cmdRun(args)
	case "open":
		err = cmdOpen()
	case "unlock":
		err = cmdUnlock()
	case "add":
		err = cmdAdd(args)
	case "folders":
		err = cmdFolders()
	case "remove":
		err = cmdRemove(args)
	case "sync":
		err = cmdSync()
	case "login":
		err = cmdLogin(args)
	case "logout":
		err = cmdLogout()
	case "status":
		err = cmdStatus()
	case "ls":
		err = cmdLs(args)
	case "pull":
		err = cmdPull(args)
	case "mount":
		err = cmdMount(args)
	case "usage":
		err = cmdUsage(args)
	case "version", "--version", "-v":
		fmt.Println("orosdrive", version)
	case "help", "--help", "-h":
		fmt.Printf(usage, version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n"+usage, cmd, version)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "orosdrive:", friendly(err))
		os.Exit(1)
	}
}

func friendly(err error) string {
	switch {
	case errors.Is(err, dropbox.ErrNotConnected):
		return "not signed in: run `orosdrive login` first"
	case webdav.Code(err) != "":
		return davText[webdav.Code(err)]
	case errors.Is(err, vault.ErrLocked):
		return "this passphrase does not open the vault (use the orOS sync passphrase)"
	case errors.Is(err, vault.ErrMismatch):
		return err.Error() + " (the stored object was changed or swapped; nothing was written)"
	}
	return err.Error()
}

// davText says what a refused WebDAV connection means.
var davText = map[string]string{
	"dav-url":           "use the full https:// address of your server (plain http only for this computer), with no password in it",
	"dav-missing":       "fill in the user name (--user) and the app password",
	"dav-unreachable":   "the server did not answer: check the address and the connection",
	"dav-auth":          "the server refused this user name or password",
	"dav-notdav":        "this address is not a WebDAV folder",
	"dav-readonly":      "cannot write to this folder on the server",
	"dav-etag":          "the server does not report file versions (ETag), so it cannot sync safely",
	"dav-conditions":    "this server does not stop two devices from overwriting each other (If-Match), so orOS Drive will not sync through it",
	"dav-notnextcloud":  "this address is not a Nextcloud server",
	"dav-login-expired": "the sign-in was not approved in time; try again",
}

func client() (*dropbox.Client, error) {
	t, err := secret.LoadTokens()
	if err != nil {
		return nil, err
	}
	c := dropbox.New(t)
	c.OnTokens = func(t dropbox.Tokens) { _ = secret.SaveTokens(t) }
	return c, nil
}

// davClient is the WebDAV connection, when this computer uses one.
func davClient() (*webdav.Client, error) {
	c, pass, err := secret.LoadWebDAV()
	if err != nil {
		return nil, err
	}
	return webdav.New(c, pass), nil
}

func passphrase() (*vault.Passphrase, error) {
	p, err := readPassphrase()
	if err != nil {
		return nil, err
	}
	return vault.NewPassphrase(p), nil
}

func readPassphrase() (string, error) {
	if p := os.Getenv("OROS_PASSPHRASE"); p != "" {
		return p, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("no passphrase: set OROS_PASSPHRASE or run in a terminal")
	}
	fmt.Fprint(os.Stderr, "orOS sync passphrase: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New("empty passphrase")
	}
	return string(b), nil
}

// storeForVault is the user's Dropbox, or a local folder when
// OROS_DRIVE_STORE_DIR is set (tests and offline checks of a copied vault).
func storeForVault() (vault.Store, error) {
	if d := os.Getenv("OROS_DRIVE_STORE_DIR"); d != "" {
		return vault.NewDirStore(d), nil
	}
	if c, err := davClient(); err == nil {
		return c, nil
	}
	return client()
}

func openVault() (*vault.Vault, *vault.Remote, error) {
	c, err := storeForVault()
	if err != nil {
		return nil, nil, err
	}
	p, err := passphrase()
	if err != nil {
		return nil, nil, err
	}
	v := vault.New(c, p)
	r, err := v.FetchManifest()
	if err != nil {
		return nil, nil, err
	}
	if !r.Exists {
		// An empty manifest proves nothing: check the passphrase on the key file.
		if _, err := v.LoadKeys(false); err != nil {
			return nil, nil, err
		}
	}
	return v, r, nil
}

func openURL(u string) error { return app.Open(u) }

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	paste := fs.Bool("paste", false, "show a link and paste the code back (no browser on this machine)")
	nc := fs.String("nextcloud", "", "your Nextcloud server's address")
	dav := fs.String("webdav", "", "the WebDAV address of your files")
	user := fs.String("user", "", "WebDAV user name")
	dir := fs.String("dir", "", "folder on the server (default /orOS)")
	fs.Parse(args)
	if *nc != "" || *dav != "" {
		return loginWebDAV(*nc, *dav, *user, *dir)
	}
	c := dropbox.New(dropbox.Tokens{})
	p := dropbox.NewPKCE()
	var code string
	var redirect string
	if *paste {
		fmt.Println("Open this link, allow orOS, then paste the code here:")
		fmt.Println(c.AuthorizeURL(p, ""))
		fmt.Print("Code: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		code = strings.TrimSpace(line)
	} else {
		redirect = dropbox.LoopbackRedirect
		u := c.AuthorizeURL(p, redirect)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		got := make(chan struct{})
		var werr error
		go func() { code, werr = dropbox.WaitForCode(ctx, p); close(got) }()
		time.Sleep(200 * time.Millisecond)
		fmt.Println("Opening Dropbox in your browser. If nothing opens, visit:")
		fmt.Println(u)
		_ = openURL(u)
		<-got
		if werr != nil {
			return werr
		}
	}
	if code == "" {
		return errors.New("no code")
	}
	t, err := c.Exchange(code, p, redirect)
	if err != nil {
		return err
	}
	if err := secret.SaveTokens(t); err != nil {
		return err
	}
	_ = secret.DeleteWebDAV()
	if a, err := c.CurrentAccount(); err == nil {
		fmt.Printf("Signed in as %s (%s).\n", a.Name, a.Email)
	} else {
		fmt.Println("Signed in.")
	}
	return nil
}

// loginWebDAV connects a Nextcloud (Login Flow v2) or a WebDAV folder.
// The server is checked first (webdav.Probe); nothing is saved when it
// fails.
func loginWebDAV(nc, dav, user, dir string) error {
	var c *webdav.Client
	if nc != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		l, err := webdav.StartNextcloud(ctx, nc, dir)
		if err != nil {
			return err
		}
		fmt.Println("Approve orOS Drive on your Nextcloud page. If nothing opens, visit:")
		fmt.Println(l.URL)
		_ = openURL(l.URL)
		if c, err = l.Wait(ctx); err != nil {
			return err
		}
	} else {
		conf, err := webdav.NormConf(dav, user, dir)
		if err != nil {
			return err
		}
		pass := os.Getenv("OROS_DAV_PASSWORD")
		if pass == "" && term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprint(os.Stderr, "App password: ")
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return err
			}
			pass = string(b)
		}
		if c, err = webdav.Connect(conf, pass); err != nil {
			return err
		}
	}
	if err := secret.SaveWebDAV(c.Conf, c.Pass); err != nil {
		return err
	}
	_ = secret.DeleteTokens()
	fmt.Printf("Connected to %s.\n", c.Conf.Account())
	return nil
}

func cmdLogout() error {
	if c, err := client(); err == nil {
		_ = c.Revoke()
	}
	if c, err := davClient(); err == nil {
		_ = c.Revoke()
	}
	_ = secret.DeleteWebDAV()
	_ = secret.DeleteVaultKey()
	if err := secret.DeleteTokens(); err != nil {
		return err
	}
	fmt.Println("Signed out.")
	return nil
}

func cmdStatus() error {
	if d, err := davClient(); err == nil && os.Getenv("OROS_DRIVE_STORE_DIR") == "" {
		fmt.Printf("WebDAV:   %s\n", d.Conf.Account())
	} else if os.Getenv("OROS_DRIVE_STORE_DIR") == "" {
		c, err := client()
		if err != nil {
			return err
		}
		a, err := c.CurrentAccount()
		if err != nil {
			return err
		}
		fmt.Printf("Dropbox:  %s (%s)\n", a.Name, a.Email)
	}
	_, r, err := openVault()
	if err != nil {
		return err
	}
	if !r.Exists {
		fmt.Println("Vault:    empty (no files synced from orOS yet)")
		return nil
	}
	var total int64
	legacy := 0
	for _, e := range r.Files {
		if s, ok := e.Size(); ok {
			total += s
		}
		if e.V == 1 {
			legacy++
		}
	}
	fmt.Printf("Vault:    %d files, %s\n", len(r.Files), human(total))
	if r.Legacy || legacy > 0 {
		fmt.Printf("          %d files still in the old format (orOS converts them on its next sync)\n", legacy)
	}
	return nil
}

func cmdUsage(args []string) error {
	fs := flag.NewFlagSet("usage", flag.ExitOnError)
	clean := fs.Bool("clean", false, "remove content no file has used for 30 days")
	fs.Parse(args)
	v, _, err := openVault()
	if err != nil {
		return err
	}
	u, err := v.Usage(time.Now())
	if err != nil {
		return err
	}
	fmt.Printf("Files:    %d in orOS, %s\n", u.Files, human(u.FileBytes))
	fmt.Printf("Stored:   %s in %d pieces (encrypted)\n", human(u.Stored), u.Objects)
	fmt.Printf("Unused:   %s; removable now (unused for 30 days): %s\n", human(u.UnusedB), human(u.FreeB))
	if u.Missing > 0 {
		fmt.Printf("Missing:  %d files have no stored content\n", u.Missing)
	}
	switch u.Hold {
	case "no-manifest":
		fmt.Println("Nothing is synced yet, so nothing is removed.")
		return nil
	case "mismatch":
		fmt.Println("The stored content does not match the list of files, so nothing is removed.")
		return nil
	}
	if !*clean || u.Free == 0 {
		return nil
	}
	n, b, err := v.CleanUp(time.Now())
	fmt.Printf("Removed:  %d pieces, %s\n", n, human(b))
	return err
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// under returns the manifest paths at or under a disk path.
func under(r *vault.Remote, disk string) []string {
	var out []string
	for _, p := range r.Paths() {
		if disk == vault.RootPath || p == disk || strings.HasPrefix(p, disk+"/") {
			out = append(out, p)
		}
	}
	return out
}

func cmdLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	long := fs.Bool("l", false, "show size and date")
	fs.Parse(args)
	_, r, err := openVault()
	if err != nil {
		return err
	}
	disk := vault.DiskPath(fs.Arg(0))
	for _, p := range under(r, disk) {
		if *long {
			e := r.Files[p]
			size := "?"
			if s, ok := e.Size(); ok {
				size = human(s)
			}
			when := ""
			if t := e.ModTime(); !t.IsZero() {
				when = t.Local().Format("2006-01-02 15:04")
			}
			fmt.Printf("%10s  %16s  %s\n", size, when, vault.UserPath(p))
		} else {
			fmt.Println(vault.UserPath(p))
		}
	}
	return nil
}

// localTarget maps a manifest path under base to a file in dest, or
// refuses a name this system cannot hold safely.
func localTarget(dest, base, p string) (string, error) {
	rel := strings.TrimPrefix(p, base)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = p[strings.LastIndex(p, "/")+1:]
	}
	local := filepath.FromSlash(rel)
	if !filepath.IsLocal(local) || strings.ContainsAny(rel, "\\\x00") {
		return "", fmt.Errorf("unsafe name %q", rel)
	}
	return filepath.Join(dest, local), nil
}

func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite files that already exist")
	fs.Parse(args)
	if fs.NArg() != 2 {
		return errors.New("usage: orosdrive pull [--force] <path> <folder>")
	}
	v, r, err := openVault()
	if err != nil {
		return err
	}
	disk := vault.DiskPath(fs.Arg(0))
	paths := under(r, disk)
	if len(paths) == 0 {
		return fmt.Errorf("nothing at %s in the vault", fs.Arg(0))
	}
	base := disk
	if len(paths) == 1 && paths[0] == disk { // a single file: put it in the folder by name
		base = disk[:strings.LastIndex(disk, "/")]
	}
	failed := 0
	for _, p := range paths {
		target, err := localTarget(fs.Arg(1), base, p)
		if err == nil && !*force {
			if _, serr := os.Stat(target); serr == nil {
				err = errors.New("exists (use --force)")
			}
		}
		if err == nil {
			err = pullOne(v, r.Files[p], target)
		}
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %s: %v\n", vault.UserPath(p), friendly(err))
			continue
		}
		fmt.Println(target)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d files not copied", failed, len(paths))
	}
	return nil
}

func pullOne(v *vault.Vault, e vault.Entry, target string) error {
	plain, err := v.Download(e)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".orosdrive-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(plain)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if t := e.ModTime(); !t.IsZero() {
		_ = os.Chtimes(tmp.Name(), t, t)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
