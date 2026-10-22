package store

import (
	"fmt"
	"strings"
)

// A user's name is also a host name (name.example.com) and a file name (users/ab/name.db). nameRE keeps it
// safe for both, and is all that is asked of a name that already exists. CheckName is what a new name must
// also satisfy, so that it can never be mistaken for something the server or the network already means.

// reserved names: addresses that mean something on a host, accounts of a Linux machine (a name on a shared
// system is often one of them), and names of the pages and services around Weft.
var reserved = map[string]bool{}

func init() {
	for _, n := range strings.Fields(`
		www www1 www2 web mail smtp imap pop pop3 mx mx1 mx2 ns ns1 ns2 ns3 ns4 dns ftp sftp ssh git svn
		webmail autoconfig autodiscover localhost localdomain broadcasthost wpad proxy
		cdn static assets img images media files download downloads upload uploads
		root daemon bin sys sync games man lp news uucp backup list irc nobody sudo wheel operator
		www-data admin administrator adm postmaster hostmaster webmaster abuse security noc
		postfix dovecot nginx apache caddy mysql postgres redis docker systemd
		noreply no-reply mailer-daemon
		weft api app auth login logout signup register account accounts settings status support help info
		about contact terms privacy internal metrics health dashboard console feed feeds user users
		instance trash backups test example`) {
		reserved[n] = true
	}
}

// CheckName reports why name cannot be given to a new user, in words that can be shown to whoever chose it.
// The error is ErrInvalid.
func CheckName(name string) error {
	bad := func(why string) error { return fmt.Errorf("%w: %q %s", ErrInvalid, name, why) }
	switch {
	case !nameRE.MatchString(name):
		return bad("is not a name: use 1 to 31 lowercase letters, digits and hyphens, starting with a letter or a digit")
	case strings.HasSuffix(name, "-"):
		return bad("must not end with a hyphen")
	case len(name) >= 4 && name[2:4] == "--":
		return bad("must not have hyphens in the third and fourth place (that is how internationalised host names, xn--, are written)")
	case reserved[name]:
		return bad("is reserved")
	}
	return nil
}
