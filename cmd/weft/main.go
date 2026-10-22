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
	"strings"
	"syscall"
	"time"

	"github.com/hbroek/weft/internal/store"
	"github.com/hbroek/weft/internal/web"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  weft serve    [-addr :8080] [-domain localhost] [-scheme http] [-data weft] [-dev] [-v] [-poll 5m|0]
  weft user add [-data weft] [-display NAME] [-bio TEXT] [-avatar URL] [-password PW] USERNAME
  weft user set [-data weft] [-display NAME] [-bio TEXT] [-avatar URL] USERNAME   (changes only the fields given; -avatar "" removes it)
  weft user passwd [-data weft] -password PW USERNAME
  weft user token  [-data weft] USERNAME      (issues a new API token, invalidating the old one)
  weft user follow [-data weft] [-domain localhost] [-scheme http] [-port 8080] USERNAME TARGET...
                  TARGET is a feed or site URL, or a bare user name on this instance (alice -> http://alice.localhost:8080/).
                  The target's server must be running.
  weft user list   [-data weft]
  weft split    -db OLD.db -data DIR     (convert a single-file database into the per-user layout in DIR; OLD.db is not changed)
  weft export   [-data weft] [-domain localhost] [-o FILE] USERNAME   (writes FILE, default USERNAME.weft.zip: the account, without sessions or API token)
  weft import   [-data weft] FILE        (creates the user in FILE; prints a new API token; refuses a name that is taken)
  weft backup   [-data weft] -to DIR [-full]   (consistent snapshots of instance.db and each changed user; safe while the server runs; then rsync DIR)
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
		fs.Parse(os.Args[2:])
		if err := serve(*addr, *db, web.Config{BaseDomain: *domain, Scheme: *scheme, AllowPrivate: *dev, Verbose: *verbose}, *poll); err != nil {
			log.Fatal(err)
		}
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
		if len(os.Args) < 3 || (os.Args[2] != "add" && os.Args[2] != "passwd" && os.Args[2] != "token" && os.Args[2] != "list" && os.Args[2] != "follow" && os.Args[2] != "set") {
			usage()
		}
		fs := flag.NewFlagSet("user "+os.Args[2], flag.ExitOnError)
		db := fs.String("data", "weft", "data folder: instance.db and users/")
		display := fs.String("display", "", "display name")
		bio := fs.String("bio", "", "bio")
		avatar := fs.String("avatar", "", "avatar image address (http:// or https://); without one the first letter of the name is shown")
		pw := fs.String("password", "", "login password for the web UI (min 8 characters)")
		domain := fs.String("domain", "localhost", "follow: base domain for bare user names")
		scheme := fs.String("scheme", "http", "follow: scheme for bare user names")
		port := fs.String("port", "8080", "follow: port for bare user names (empty for none)")
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
		// space opens the named user's data, or exits
		space := func(name string) (*store.Space, *store.User) {
			sp, _, err := st.Space(name)
			if err != nil {
				log.Fatalf("no user %q", name)
			}
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
		if os.Args[2] == "passwd" {
			sp, _ := space(fs.Arg(0))
			if err := sp.SetPassword(*pw); err != nil {
				log.Fatal(err)
			}
			return
		}
		if *pw != "" && len(*pw) < 8 {
			log.Fatal("password must be at least 8 characters")
		}
		tok, err := st.AddUser(fs.Arg(0), *display, *bio)
		if err != nil {
			log.Fatal(err)
		}
		sp, u := space(fs.Arg(0))
		if *pw != "" {
			if err := sp.SetPassword(*pw); err != nil {
				log.Fatal(err)
			}
		}
		if *avatar != "" {
			if err := sp.UpdateProfile(u.ID, u.Display, u.Bio, *avatar); err != nil {
				log.Fatal(err)
			}
		}
		fmt.Println(tok)
	default:
		usage()
	}
}

// serve runs the server until it is told to stop (Ctrl-C or SIGTERM) or cannot listen. It then shuts
// down in order: stop taking requests and let those in progress finish, stop the background refresh
// and wait for it, and only then close the database, which writes out the write-ahead log and removes
// the -wal and -shm files, in the shared database and in every user's.
func serve(addr, dataDir string, cfg web.Config, poll time.Duration) error {
	st, err := store.Open(dataDir)
	if err != nil {
		return err
	}
	log.Printf("data folder %s: up to %d user databases open at once", dataDir, st.PoolCap())
	srv := web.New(cfg, st)

	stopSignals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	bg, cancelBackground := context.WithCancel(context.Background())
	pollDone := srv.StartPoller(bg, poll)

	hs := &http.Server{Addr: addr, Handler: srv}
	listenErr := make(chan error, 1)
	go func() { listenErr <- hs.ListenAndServe() }()
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
