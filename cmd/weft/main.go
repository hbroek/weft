// Command weft runs the Weft server and admin commands.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/store"
	"github.com/hbroek/weft/internal/web"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  weft serve    [-addr :8080] [-domain localhost] [-scheme http] [-db weft.db] [-dev] [-v] [-poll 5m]
  weft user add [-db weft.db] [-display NAME] [-bio TEXT] [-password PW] USERNAME
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
		poll := fs.Duration("poll", 5*time.Minute, "how often to refresh followed feeds")
		fs.Parse(os.Args[2:])
		legacyDB(*db)
		st, err := store.Open(*db)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		srv := web.New(web.Config{BaseDomain: *domain, Scheme: *scheme, AllowPrivate: *dev, Verbose: *verbose}, st)
		srv.StartPoller(context.Background(), *poll)
		log.Printf("listening on %s, users at <name>.%s (dev=%v)", *addr, *domain, *dev)
		log.Fatal(http.ListenAndServe(*addr, srv))
	case "user":
		if len(os.Args) < 3 || (os.Args[2] != "add" && os.Args[2] != "passwd" && os.Args[2] != "token" && os.Args[2] != "list" && os.Args[2] != "follow") {
			usage()
		}
		fs := flag.NewFlagSet("user "+os.Args[2], flag.ExitOnError)
		db := fs.String("db", "weft.db", "SQLite file")
		display := fs.String("display", "", "display name")
		bio := fs.String("bio", "", "bio")
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
		switch os.Args[2] {
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
		fmt.Println(tok)
	default:
		usage()
	}
}
