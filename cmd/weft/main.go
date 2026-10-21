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
	"strings"
	"syscall"
	"time"

	"github.com/hbroek/weft/internal/store"
	"github.com/hbroek/weft/internal/web"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  weft serve    [-addr :8080] [-domain localhost] [-scheme http] [-db weft.db] [-dev] [-v] [-poll 5m|0]
  weft user add [-db weft.db] [-display NAME] [-bio TEXT] [-avatar URL] [-password PW] USERNAME
  weft user set [-db weft.db] [-display NAME] [-bio TEXT] [-avatar URL] USERNAME   (changes only the fields given; -avatar "" removes it)
  weft user passwd [-db weft.db] -password PW USERNAME
  weft user token  [-db weft.db] USERNAME      (issues a new API token, invalidating the old one)
  weft user follow [-db weft.db] [-domain localhost] [-scheme http] [-port 8080] USERNAME TARGET...
                  TARGET is a feed or site URL, or a bare user name on this instance (alice -> http://alice.localhost:8080/).
                  The target's server must be running.
  weft user list   [-db weft.db]`)
	os.Exit(2)
}

// legacyDB renames a database file left from before the project was renamed, with its WAL files,
// when the new file does not exist yet. Only the default name is touched.
func legacyDB(path string) {
	if filepath.Base(path) != "weft.db" {
		return
	}
	if _, err := os.Stat(path); err == nil {
		return
	}
	old := filepath.Join(filepath.Dir(path), "ttc.db")
	if _, err := os.Stat(old); err != nil {
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(old+suffix, path+suffix); err != nil && suffix == "" {
			log.Fatal(err)
		}
	}
	log.Printf("renamed %s to %s", old, path)
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
		db := fs.String("db", "weft.db", "SQLite file")
		dev := fs.Bool("dev", false, "allow fetching feeds from loopback/private addresses (local development only)")
		verbose := fs.Bool("v", false, "log every API call")
		poll := fs.Duration("poll", 5*time.Minute, "how often to refresh followed feeds (0 turns the background refresh off)")
		fs.Parse(os.Args[2:])
		legacyDB(*db)
		if err := serve(*addr, *db, web.Config{BaseDomain: *domain, Scheme: *scheme, AllowPrivate: *dev, Verbose: *verbose}, *poll); err != nil {
			log.Fatal(err)
		}
	case "user":
		if len(os.Args) < 3 || (os.Args[2] != "add" && os.Args[2] != "passwd" && os.Args[2] != "token" && os.Args[2] != "list" && os.Args[2] != "follow" && os.Args[2] != "set") {
			usage()
		}
		fs := flag.NewFlagSet("user "+os.Args[2], flag.ExitOnError)
		db := fs.String("db", "weft.db", "SQLite file")
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
		legacyDB(*db)
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
		switch os.Args[2] {
		case "set":
			u, err := st.UserByName(fs.Arg(0))
			if err != nil {
				log.Fatalf("no user %q", fs.Arg(0))
			}
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
			if err := st.UpdateProfile(u.ID, d, b, a); err != nil {
				log.Fatal(err)
			}
			return
		case "follow":
			u, err := st.UserByName(fs.Arg(0))
			if err != nil {
				log.Fatalf("no user %q", fs.Arg(0))
			}
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
				feedURL, err := srv.Follow(context.Background(), u.ID, t)
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
			tok, err := st.ResetToken(fs.Arg(0))
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(tok)
			return
		}
		if os.Args[2] == "passwd" {
			if err := st.SetPassword(fs.Arg(0), *pw); err != nil {
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
		if *pw != "" {
			if err := st.SetPassword(fs.Arg(0), *pw); err != nil {
				log.Fatal(err)
			}
		}
		if *avatar != "" {
			if u, err := st.UserByName(fs.Arg(0)); err == nil {
				if err := st.UpdateProfile(u.ID, u.Display, u.Bio, *avatar); err != nil {
					log.Fatal(err)
				}
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
// the -wal and -shm files.
func serve(addr, dbPath string, cfg web.Config, poll time.Duration) error {
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
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
