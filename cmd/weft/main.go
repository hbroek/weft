// Command weft runs the Weft server and admin commands.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/hbroek/weft/internal/store"
	"github.com/hbroek/weft/internal/web"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  weft serve    [-addr :8080] [-domain localhost] [-scheme http] [-data weft] [-dev] [-v] [-poll 5m|0] [-cname-target HOST] [-internal 127.0.0.1:8081] [-trust-proxy] [-no-rate-limits] [-max-image-mb 5] [-max-upload-mb 25] [-media-quota-mb 200] [-image-side 1600] [-image-megapixels 24] [-site DIR] [-max-users N] [-signups] [-admin] [-admin-public]
  weft user add [-data weft] [-display NAME] [-bio TEXT] [-avatar URL] [-password PW [-permanent]] USERNAME
                  (the password is temporary: the user must change it at the first sign-in, unless -permanent;
                  without -password one is made and shown)
  weft user set [-data weft] [-display NAME] [-bio TEXT] [-avatar URL] [-email ADDRESS] USERNAME   (changes only the fields given; -avatar "" removes it)
  weft user passwd [-data weft] [-password PW [-permanent]] USERNAME   (temporary like add: the user changes it at the next sign-in; without -password one is made and shown)
  weft user token  [-data weft] USERNAME      (issues a new API token, invalidating the old one)
  weft user follow [-data weft] [-domain localhost] [-scheme http] [-port 8080] USERNAME TARGET...
                  TARGET is a feed or site URL, or a bare user name on this instance (alice -> http://alice.localhost:8080/).
                  The target's server must be running.
  weft user list   [-data weft]
  weft user admin  [-data weft] [-off] USERNAME   (marks the account as an administrator's: it can sign in to the administration pages and is not counted toward -max-users; -off takes the mark off)
  weft user suspend|restore [-data weft] USERNAME   (switch an account off, every address answers 410, or on again; restore also brings back a removed account while its file is in the trash)
  weft user remove [-data weft] -yes USERNAME   (delete an account: it answers 410, the file waits 30 days in trash/, the name stays taken; administrators cannot be removed)
  weft waitlist list   [-data weft] [-new] [-full]  (a table of who asked for an account: reviewed mark, name, email, where they write; -new: only those not reviewed)
  weft waitlist review [-data weft] [-undo] ID  (mark a request as read; -undo takes the mark off)
  weft waitlist remove [-data weft] ID  (answered or refused)
  weft split    -db OLD.db -data DIR     (convert a single-file database into the per-user layout in DIR; OLD.db is not changed)
  weft export   [-data weft] [-domain localhost] [-o FILE] USERNAME   (writes FILE, default USERNAME.weft.zip: the account, without sessions or API token)
  weft import   [-data weft] FILE        (creates the user in FILE; prints a new API token; refuses a name that is taken)
  weft move     [-data weft] [-cancel] USERNAME NEWADDRESS   (USERNAME has moved to the server at NEWADDRESS, e.g. https://alice.example.org: every request to the old address, the feed too, gets a permanent redirect there, and the file goes to trash/ for 30 days; export and import it on the new server first; -cancel undoes a move while the file is still in the trash)
  weft fsck     [-data weft] [-full] [-fix] [USERNAME...]   (checks instance.db, every user's file, and that the directory matches the files; -full also checks every index entry; -fix rebuilds the directory (reindex) when that is all that is wrong; exits 1 if problems remain)
  weft reindex  [-data weft]   (rebuilds the directory's follows and work from the users' files)
  weft backup   [-data weft] -to DIR [-full]   (consistent snapshots of instance.db and each changed user; safe while the server runs; then back DIR up with restic or rsync)
  weft restore  -from DIR -data DIR2     (build a data folder from a backup; DIR2 must not exist or be empty)`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", ":8080", "listen address")
		domain := fs.String("domain", "localhost", "hosted base domain; name.<domain> serves user name")
		scheme := fs.String("scheme", "http", "http or https, used in absolute URLs")
		db := fs.String("data", "weft", "data folder: instance.db and users/")
		dev := fs.Bool("dev", false, "allow fetching feeds from loopback/private addresses (local development only)")
		verbose := fs.Bool("v", false, "log every API call")
		poll := fs.Duration("poll", 5*time.Minute, "how often to refresh followed feeds (0 turns the background refresh off)")
		cname := fs.String("cname-target", "", "host name users point a custom domain's CNAME record at (shown on the profile page)")
		trustProxy := fs.Bool("trust-proxy", false, "take the caller's address from X-Forwarded-For, for the rate limits (only behind a reverse proxy that sets it)")
		noLimits := fs.Bool("no-rate-limits", false, "turn the rate limits off (load tests)")
		internal := fs.String("internal", "", "private address for the reverse proxy's \"may I get a certificate for this host?\" question, e.g. 127.0.0.1:8081 (never a public address)")
		maxImage := fs.Int("max-image-mb", 5, "the largest picture a user may upload, in MB")
		maxUpload := fs.Int("max-upload-mb", 25, "the largest picture accepted at the Compact size, in MB (it is made smaller before it is kept)")
		quota := fs.Int("media-quota-mb", 200, "how much each user may store in pictures, in MB (0 for no limit)")
		side := fs.Int("image-side", 1600, "the longest side, in pixels, of a picture uploaded at the Compact size")
		pixels := fs.Int("image-megapixels", 24, "the most pixels (in millions) one picture may have")
		maxUsers := fs.Int("max-users", 0, "the number of accounts the landing page says there are places for (0: no number is shown)")
		admin := fs.Bool("admin", false, "serve the administration pages (for accounts marked with weft user admin) on the -internal address")
		adminPublic := fs.Bool("admin-public", false, "with -admin, also serve them at admin.<domain> on the public address: only behind something that checks who asks (Cloudflare Access, a VPN)")
		signups := fs.Bool("signups", false, "let people sign up with weft.<their domain> at www.<domain>/signup (off: the operator makes the accounts)")
		site := fs.String("site", "", "folder of static files (the landing page) served at www.<domain>")
		fs.Parse(os.Args[2:])
		mediaQuota := int64(*quota) << 20
		if *quota == 0 {
			mediaQuota = -1
		}
		if err := serve(*addr, *internal, *db, web.Config{BaseDomain: *domain, Scheme: *scheme, AllowPrivate: *dev, Verbose: *verbose, CNAMETarget: *cname, TrustProxy: *trustProxy, NoLimits: *noLimits,
			MaxImageBytes: *maxImage << 20, MaxUploadBytes: *maxUpload << 20, MaxImagePixels: *pixels * 1_000_000, CompactSide: *side, MediaQuota: mediaQuota, SiteDir: *site, MaxUsers: *maxUsers, Signups: *signups, Admin: *admin, AdminPublic: *adminPublic}, *poll); err != nil {
			log.Fatal(err)
		}
	case "waitlist":
		if len(os.Args) < 3 || (os.Args[2] != "list" && os.Args[2] != "remove" && os.Args[2] != "review") {
			usage()
		}
		fs := flag.NewFlagSet("waitlist "+os.Args[2], flag.ExitOnError)
		db := fs.String("data", "weft", "data folder: instance.db and users/")
		undo := fs.Bool("undo", false, "review: take the reviewed mark off")
		onlyNew := fs.Bool("new", false, "list: only the requests not yet reviewed")
		full := fs.Bool("full", false, "list: show the links and the note in full, not cut to one line")
		fs.Parse(os.Args[3:])
		st, err := store.Open(*db)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		if os.Args[2] == "review" {
			id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
			if err != nil || fs.NArg() != 1 {
				usage()
			}
			if err := st.SetReviewed(id, !*undo); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("%d marked %s\n", id, map[bool]string{true: "not reviewed", false: "reviewed"}[*undo])
			break
		}
		if os.Args[2] == "remove" {
			id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
			if err != nil || fs.NArg() != 1 {
				usage()
			}
			if err := st.RemoveWaiting(id); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("%d removed\n", id)
			break
		}
		list, err := st.Waitlist()
		if err != nil {
			log.Fatal(err)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tDATE\tREVIEWED\tNAME\tEMAIL\tWHERE THEY WRITE\tNOTE")
		oneLine := func(s string) string {
			s = strings.Join(strings.Fields(s), " ")
			if r := []rune(s); !*full && len(r) > 70 {
				s = string(r[:69]) + "…"
			}
			return s
		}
		shown := 0
		for _, w := range list {
			if *onlyNew && w.Reviewed {
				continue
			}
			mark := "-"
			if w.Reviewed {
				mark = "yes"
			}
			shown++
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", w.ID, w.Created.Format("2006-01-02"), mark, w.Name, w.Contact, oneLine(w.Links), oneLine(w.Note))
		}
		tw.Flush()
		fmt.Printf("%d shown, %d waiting\n", shown, len(list))
	case "fsck":
		fs := flag.NewFlagSet("fsck", flag.ExitOnError)
		db := fs.String("data", "weft", "data folder: instance.db and users/")
		full := fs.Bool("full", false, "check every index entry too (slower)")
		fix := fs.Bool("fix", false, "rebuild the directory when that is all that is wrong")
		fs.Parse(os.Args[2:])
		st, err := store.Open(*db)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		report := func() *store.FsckReport {
			rep, err := st.Fsck(context.Background(), *full, fs.Args()...)
			if err != nil {
				log.Fatal(err)
			}
			return rep
		}
		rep := report()
		fixable := 0
		for _, p := range rep.Problems {
			if p.Fixes {
				fixable++
			}
		}
		if *fix && fixable > 0 {
			fmt.Printf("rebuilding the directory to fix %d problem(s)\n", fixable)
			if err := st.Reindex(); err != nil {
				log.Fatal(err)
			}
			rep = report()
		}
		for _, p := range rep.Problems {
			fmt.Println(p)
		}
		if len(rep.Old) > 0 {
			fmt.Printf("%d user file(s) have an older schema and are upgraded when opened\n", len(rep.Old))
		}
		fmt.Printf("%d user file(s) checked, %d problem(s)\n", rep.Users, len(rep.Problems))
		if len(rep.Problems) > 0 {
			st.Close()
			os.Exit(1)
		}
	case "reindex":
		fs := flag.NewFlagSet("reindex", flag.ExitOnError)
		db := fs.String("data", "weft", "data folder: instance.db and users/")
		fs.Parse(os.Args[2:])
		st, err := store.Open(*db)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		if err := st.Reindex(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("directory rebuilt")
	case "move":
		fs := flag.NewFlagSet("move", flag.ExitOnError)
		db := fs.String("data", "weft", "data folder: instance.db and users/")
		cancel := fs.Bool("cancel", false, "undo a move (possible while the file is still in the trash)")
		fs.Parse(os.Args[2:])
		want := 2
		if *cancel {
			want = 1
		}
		if fs.NArg() != want {
			usage()
		}
		st, err := store.Open(*db)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		if *cancel {
			if _, err := st.MovePending(fs.Arg(0)); err == nil { // a move to another server that has not finished
				if err := st.MoveAbort(fs.Arg(0)); err != nil {
					log.Fatal(err)
				}
				fmt.Printf("the move of %s was cancelled; a running server notices within seconds\n", fs.Arg(0))
				return
			}
			if err := st.Unmove(fs.Arg(0)); err != nil {
				log.Fatal(err)
			}
			return
		}
		if err := st.MarkMoved(fs.Arg(0), fs.Arg(1)); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s now redirects to %s; a running server puts the file in %s within a minute\n", fs.Arg(0), fs.Arg(1), filepath.Join(*db, "trash"))
	case "split":
		fs := flag.NewFlagSet("split", flag.ExitOnError)
		old := fs.String("db", "", "single-file database to read (never modified)")
		data := fs.String("data", "", "data folder to create (must not exist or be empty)")
		fs.Parse(os.Args[2:])
		if *old == "" || *data == "" {
			usage()
		}
		rep, err := store.Split(*old, *data)
		if rep != nil {
			fmt.Printf("%d users written to %s\n", rep.Users, *data)
			for _, t := range []string{"users", "sessions", "posts", "follows", "replies", "subreplies", "likes", "likes_in", "wm_queue"} {
				fmt.Printf("  %-11s %6d\n", t, rep.Rows[t])
			}
			for _, t := range []string{"feeds", "items", "feed_likes"} {
				fmt.Printf("  %-11s %6d  (instance.db)\n", t, rep.Shared[t])
			}
			for _, d := range rep.Dropped {
				fmt.Println("  left behind:", d)
			}
		}
		if err != nil {
			log.Fatal(err)
		}
	case "export":
		fs := flag.NewFlagSet("export", flag.ExitOnError)
		data := fs.String("data", "weft", "data folder")
		domain := fs.String("domain", "localhost", "base domain the user is served under, recorded in the export")
		out := fs.String("o", "", "file to write (default USERNAME.weft.zip)")
		fs.Parse(os.Args[2:])
		if fs.NArg() != 1 {
			usage()
		}
		name := fs.Arg(0)
		if *out == "" {
			*out = name + ".weft.zip"
		}
		st, err := store.Open(*data)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		sp, release, err := st.Space(name)
		if err != nil {
			log.Fatalf("no user %q", name)
		}
		defer release()
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // an export holds the password hash: private
		if err != nil {
			log.Fatal(err)
		}
		if err := sp.Export(f, *domain); err != nil {
			f.Close()
			os.Remove(*out)
			log.Fatal(err)
		}
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
		fmt.Println(*out)
	case "import":
		fs := flag.NewFlagSet("import", flag.ExitOnError)
		data := fs.String("data", "weft", "data folder")
		fs.Parse(os.Args[2:])
		if fs.NArg() != 1 {
			usage()
		}
		st, err := store.Open(*data)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		name, tok, err := st.Import(fs.Arg(0))
		if err != nil {
			st.Close()
			log.Fatal(err)
		}
		fmt.Fprintf(os.Stderr, "imported %s; the API token below is new and shown once\n", name)
		fmt.Println(tok)
	case "backup":
		fs := flag.NewFlagSet("backup", flag.ExitOnError)
		data := fs.String("data", "weft", "data folder")
		to := fs.String("to", "", "backup folder")
		full := fs.Bool("full", false, "copy every user, not only those who changed")
		fs.Parse(os.Args[2:])
		if *to == "" {
			usage()
		}
		st, err := store.Open(*data)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		rep, err := st.Backup(*to, *full)
		if rep != nil {
			fmt.Printf("%d users: %d copied, %d unchanged, %.1f MB written to %s\n", rep.Users, rep.Copied, rep.Unchanged, float64(rep.Bytes)/1e6, *to)
		}
		if err != nil {
			st.Close()
			log.Fatal(err)
		}
	case "restore":
		fs := flag.NewFlagSet("restore", flag.ExitOnError)
		from := fs.String("from", "", "backup folder")
		data := fs.String("data", "", "data folder to create (must not exist or be empty)")
		fs.Parse(os.Args[2:])
		if *from == "" || *data == "" {
			usage()
		}
		rep, err := store.Restore(*from, *data)
		if rep != nil {
			fmt.Printf("%d users restored to %s (%.1f MB)\n", rep.Users, *data, float64(rep.Bytes)/1e6)
		}
		if err != nil {
			log.Fatal(err)
		}
	case "user":
		if len(os.Args) < 3 || (os.Args[2] != "add" && os.Args[2] != "passwd" && os.Args[2] != "token" && os.Args[2] != "list" && os.Args[2] != "follow" && os.Args[2] != "set" && os.Args[2] != "admin" && os.Args[2] != "suspend" && os.Args[2] != "restore" && os.Args[2] != "remove") {
			usage()
		}
		fs := flag.NewFlagSet("user "+os.Args[2], flag.ExitOnError)
		db := fs.String("data", "weft", "data folder: instance.db and users/")
		display := fs.String("display", "", "display name")
		bio := fs.String("bio", "", "bio")
		avatar := fs.String("avatar", "", "avatar image address (http:// or https://); without one the first letter of the name is shown")
		pw := fs.String("password", "", "login password for the web UI (min 8 characters)")
		permanent := fs.Bool("permanent", false, "add, passwd: the password is the user's own, not a temporary one to be changed at the first sign-in")
		domain := fs.String("domain", "localhost", "follow: base domain for bare user names")
		scheme := fs.String("scheme", "http", "follow: scheme for bare user names")
		port := fs.String("port", "8080", "follow: port for bare user names (empty for none)")
		email := fs.String("email", "", "add, set: where a mail connector warns the owner (nothing sends mail by itself yet)")
		off := fs.Bool("off", false, "admin: take the administrator mark off")
		yes := fs.Bool("yes", false, "remove: do not ask")
		fs.Parse(os.Args[3:])
		want := 1
		if os.Args[2] == "list" {
			want = 0
		}
		if os.Args[2] == "follow" {
			if fs.NArg() < 2 {
				usage()
			}
		} else if fs.NArg() != want {
			usage()
		}
		st, err := store.Open(*db)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		if *avatar != "" {
			if u, err := url.Parse(*avatar); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				log.Fatal("avatar must be a full http:// or https:// address")
			}
		}
		// space opens the named user's data, or exits; the data is released when the command ends
		var releases []func()
		defer func() {
			for _, r := range releases {
				r()
			}
		}()
		space := func(name string) (*store.Space, *store.User) {
			sp, release, err := st.Space(name)
			if err != nil {
				log.Fatalf("no user %q", name)
			}
			releases = append(releases, release)
			u, err := sp.User()
			if err != nil {
				log.Fatalf("no user %q", name)
			}
			return sp, u
		}
		switch os.Args[2] {
		case "set":
			sp, u := space(fs.Arg(0))
			given := map[string]bool{}
			fs.Visit(func(f *flag.Flag) { given[f.Name] = true }) // only change what was asked for
			d, b, a := u.Display, u.Bio, u.Avatar
			if given["display"] {
				d = *display
			}
			if given["bio"] {
				b = *bio
			}
			if given["avatar"] {
				a = *avatar
			}
			if err := sp.UpdateProfile(u.ID, d, b, a); err != nil {
				log.Fatal(err)
			}
			if given["email"] {
				if err := st.SetEmail(fs.Arg(0), *email); err != nil {
					log.Fatal(err)
				}
			}
			return
		case "follow":
			space(fs.Arg(0)) // exits if there is no such user
			// An admin command run by the operator, so fetching loopback addresses is fine.
			srv := web.New(web.Config{BaseDomain: *domain, Scheme: *scheme, AllowPrivate: true}, st)
			failed := false
			for _, t := range fs.Args()[1:] {
				if !strings.Contains(t, "://") {
					host := t + "." + *domain
					if *port != "" {
						host += ":" + *port
					}
					t = *scheme + "://" + host + "/"
				}
				feedURL, err := srv.Follow(context.Background(), fs.Arg(0), t)
				if err != nil {
					fmt.Printf("%s: could not follow %s: %v\n", fs.Arg(0), t, err)
					failed = true
					continue
				}
				fmt.Printf("%s follows %s\n", fs.Arg(0), feedURL)
			}
			if failed {
				os.Exit(1)
			}
			return
		case "admin":
			if err := st.SetAdmin(fs.Arg(0), !*off); err != nil {
				log.Fatal(err)
			}
			if *off {
				fmt.Printf("%s is no longer an administrator\n", fs.Arg(0))
			} else {
				fmt.Printf("%s is an administrator: they can sign in to the administration pages (weft serve -admin) and do not count toward the cap\n", fs.Arg(0))
			}
			return
		case "suspend", "restore", "remove":
			// The change is in instance.db; a running server sees it within ten seconds, and moves a removed
			// account's file to the trash within a minute.
			var err error
			switch os.Args[2] {
			case "suspend":
				err = st.Suspend(fs.Arg(0))
			case "restore":
				err = st.Restore(fs.Arg(0))
			default:
				if !*yes {
					log.Fatalf("remove %s: its addresses answer \"gone\" and its file waits %d days in the trash, then is deleted; the name stays taken. Run it again with -yes to do it", fs.Arg(0), store.TrashDays)
				}
				err = st.Remove(fs.Arg(0))
			}
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("%s: %s done\n", fs.Arg(0), os.Args[2])
			return
		case "list":
			names, err := st.UserNames()
			if err != nil {
				log.Fatal(err)
			}
			for _, n := range names {
				fmt.Println(n)
			}
			return
		case "token":
			sp, _ := space(fs.Arg(0))
			tok, err := sp.ResetToken()
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(tok)
			return
		}
		if (os.Args[2] == "passwd" || os.Args[2] == "add") && *permanent && *pw == "" {
			log.Fatal("-permanent needs -password: a generated password is always temporary")
		}
		if (os.Args[2] == "passwd" || os.Args[2] == "add") && *pw != "" && len(*pw) < store.MinPassword {
			log.Fatal("password must be at least 8 characters")
		}
		if os.Args[2] == "passwd" {
			sp, _ := space(fs.Arg(0))
			setUserPassword(sp, fs.Arg(0), *pw, *permanent)
			return
		}
		tok, err := st.AddUser(fs.Arg(0), *display, *bio)
		if err != nil {
			log.Fatal(err)
		}
		sp, u := space(fs.Arg(0))
		setUserPassword(sp, fs.Arg(0), *pw, *permanent)
		if *avatar != "" {
			if err := sp.UpdateProfile(u.ID, u.Display, u.Bio, *avatar); err != nil {
				log.Fatal(err)
			}
		}
		if *email != "" {
			if err := st.SetEmail(fs.Arg(0), *email); err != nil {
				log.Fatal(err)
			}
		}
		fmt.Println(tok)
	default:
		usage()
	}
}

// setUserPassword gives a user a password the user must replace at the next sign-in, since whoever runs the
// command knows it. With none given it makes one and shows it, because nobody else has seen it; that goes to
// the console (stderr) so that what a script reads from stdout, the API token, stays just that. With
// permanent the password is the user's own, and one must have been given.
func setUserPassword(sp *store.Space, name, pw string, permanent bool) {
	generated := pw == ""
	if generated {
		pw = store.GeneratePassword()
	}
	set := sp.SetTemporaryPassword
	if permanent {
		set = sp.SetPassword
	}
	if err := set(pw); err != nil {
		log.Fatal(err)
	}
	if generated {
		fmt.Fprintf(os.Stderr, "temporary password for %s: %s\n", name, pw)
	}
	if !permanent {
		fmt.Fprintf(os.Stderr, "%s must choose a new password at the first sign-in\n", name)
	}
}

// serve runs the server until it is told to stop (Ctrl-C or SIGTERM) or cannot listen. It then shuts
// down in order: stop taking requests and let those in progress finish, stop the background refresh
// and wait for it, and only then close the database, which writes out the write-ahead log and removes
// the -wal and -shm files, in the shared database and in every user's.
func serve(addr, internal, dataDir string, cfg web.Config, poll time.Duration) error {
	unlock, err := store.Lock(dataDir)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := store.Open(dataDir)
	if err != nil {
		return err
	}
	log.Printf("data folder %s: up to %d user databases open at once (the process may open %d files)", dataDir, st.PoolCap(), st.OpenFiles())
	if st.PoolCap() < 64 {
		log.Printf("warning: that is few; raise the limit on open files (ulimit -n) so that busy hours do not keep reopening databases")
	}
	if probs, err := st.CheckDirectory(); err != nil {
		log.Printf("checking the directory: %v", err)
	} else if len(probs) > 0 {
		for i, p := range probs {
			if i == 5 {
				log.Printf("... and %d more", len(probs)-5)
				break
			}
			log.Printf("directory check: %s", p)
		}
		log.Printf("warning: the directory and the files disagree; run `weft fsck -data %s`", dataDir)
	}
	srv := web.New(cfg, st)
	if cfg.Admin {
		switch {
		case internal == "" && !cfg.AdminPublic:
			log.Printf("warning: -admin has nowhere to be served: give -internal ADDRESS (a private address) or -admin-public")
		case cfg.AdminPublic:
			log.Printf("the administration pages are at admin.%s on the public address: keep them behind Cloudflare Access or a VPN", cfg.BaseDomain)
		}
		if internal != "" {
			log.Printf("the administration pages are at http://%s/ (private address)", internal)
		}
	}

	stopSignals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bg, cancelBackground := context.WithCancel(context.Background())
	pollDone := srv.StartPoller(bg, poll)

	hs := web.NewHTTPServer(addr, srv, web.DefaultTimeouts)
	listenErr := make(chan error, 2)
	go func() { listenErr <- hs.ListenAndServe() }()
	var is *http.Server
	if internal != "" {
		is = web.NewHTTPServer(internal, srv.Internal(), web.DefaultTimeouts)
		go func() { listenErr <- is.ListenAndServe() }()
		log.Printf("answering the proxy's certificate questions on %s", internal)
	}
	log.Printf("listening on %s, users at <name>.%s (dev=%v)", addr, cfg.BaseDomain, cfg.AllowPrivate)

	var result error
	select {
	case err := <-listenErr:
		result = err // could not listen, or the server stopped by itself
	case <-stopSignals.Done():
		log.Printf("shutting down: finishing requests in progress")
	}
	stop() // a second Ctrl-C now ends the program at once

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := hs.Shutdown(ctx); err != nil {
		log.Printf("requests did not finish in time: %v", err)
		hs.Close()
	}
	if is != nil {
		is.Close()
	}
	cancelBackground()
	select {
	case <-pollDone:
	case <-time.After(15 * time.Second):
		log.Printf("background work did not stop in time")
	}
	if err := st.Close(); err != nil {
		log.Printf("closing the database: %v", err)
	} else {
		log.Printf("database closed")
	}
	if errors.Is(result, http.ErrServerClosed) {
		result = nil
	}
	return result
}
