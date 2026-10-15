// Command ttc runs the text-thread-casting server and admin commands.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hbroek/text-thread-casting/internal/store"
	"github.com/hbroek/text-thread-casting/internal/web"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  ttc serve    [-addr :8080] [-domain localhost] [-scheme http] [-db ttc.db] [-dev] [-v] [-poll 5m]
  ttc user add [-db ttc.db] [-display NAME] [-bio TEXT] [-password PW] USERNAME
  ttc user passwd [-db ttc.db] -password PW USERNAME
  ttc user token  [-db ttc.db] USERNAME      (issues a new API token, invalidating the old one)
  ttc user list   [-db ttc.db]`)
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
		db := fs.String("db", "ttc.db", "SQLite file")
		dev := fs.Bool("dev", false, "allow fetching feeds from loopback/private addresses (local development only)")
		verbose := fs.Bool("v", false, "log every API call")
		poll := fs.Duration("poll", 5*time.Minute, "how often to refresh followed feeds")
		fs.Parse(os.Args[2:])
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
		if len(os.Args) < 3 || (os.Args[2] != "add" && os.Args[2] != "passwd" && os.Args[2] != "token" && os.Args[2] != "list") {
			usage()
		}
		fs := flag.NewFlagSet("user "+os.Args[2], flag.ExitOnError)
		db := fs.String("db", "ttc.db", "SQLite file")
		display := fs.String("display", "", "display name")
		bio := fs.String("bio", "", "bio")
		pw := fs.String("password", "", "login password for the web UI (min 8 characters)")
		fs.Parse(os.Args[3:])
		want := 1
		if os.Args[2] == "list" {
			want = 0
		}
		if fs.NArg() != want {
			usage()
		}
		st, err := store.Open(*db)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		switch os.Args[2] {
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
