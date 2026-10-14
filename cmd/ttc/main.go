// Command ttc runs the text-thread-casting server and admin commands.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/hbroek/text-thread-casting/internal/store"
	"github.com/hbroek/text-thread-casting/internal/web"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  ttc serve    [-addr :8080] [-domain localhost] [-scheme http] [-db ttc.db]
  ttc user add [-db ttc.db] [-display NAME] [-bio TEXT] USERNAME`)
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
		fs.Parse(os.Args[2:])
		st, err := store.Open(*db)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		log.Printf("listening on %s, users at <name>.%s", *addr, *domain)
		log.Fatal(http.ListenAndServe(*addr, web.New(web.Config{BaseDomain: *domain, Scheme: *scheme}, st)))
	case "user":
		if len(os.Args) < 3 || os.Args[2] != "add" {
			usage()
		}
		fs := flag.NewFlagSet("user add", flag.ExitOnError)
		db := fs.String("db", "ttc.db", "SQLite file")
		display := fs.String("display", "", "display name")
		bio := fs.String("bio", "", "bio")
		fs.Parse(os.Args[3:])
		if fs.NArg() != 1 {
			usage()
		}
		st, err := store.Open(*db)
		if err != nil {
			log.Fatal(err)
		}
		defer st.Close()
		tok, err := st.AddUser(fs.Arg(0), *display, *bio)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(tok)
	default:
		usage()
	}
}
