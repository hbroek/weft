package web

import (
	"log"

	"github.com/hbroek/weft/internal/store"
)

// Sending the Webmentions of an account again, so that receivers learn what has happened to it (Hosting spec 5.3
// and 6). After a move the new server sends one for every reply and like from the account's new addresses, and
// the receivers update their links. When an account is removed its addresses answer 410, and sending the same
// Webmentions again makes the receivers fetch the source, see that it is gone, and take the reply or like down.

// originFor is the address an account's pages are served from now, without a request to read it from.
func (s *Server) originFor(name string) string {
	host := name + "." + s.cfg.BaseDomain
	if d := s.st.Dir(name).Domain; d != "" {
		host = d
	}
	return s.cfg.Scheme + "://" + host + s.cfg.Port
}

// resendAll queues a Webmention for each published reply and like of the user, from origin, and returns how many.
func (s *Server) resendAll(sp *store.Space, userID int64, origin string) int {
	list, err := sp.Outgoing(userID)
	if err != nil {
		log.Printf("listing the replies and likes of %s to send again: %v", sp.Name(), err)
		return 0
	}
	n := 0
	for _, o := range list {
		if sp.Enqueue(store.QueueOut, userID, origin+o.Path, o.Target) == nil {
			n++
		}
	}
	if n > 0 {
		s.kick()
	}
	return n
}

// retractRemoved queues the Webmentions of every removed account whose file is still in place. It runs before the
// files are moved to the trash, which waits for the queue of a removed account to empty.
func (s *Server) retractRemoved() {
	for _, name := range s.st.NeedingRetraction() {
		sp, release, err := s.st.Space(name)
		if err != nil {
			continue
		}
		u, err := sp.User()
		if err == nil {
			n := s.resendAll(sp, u.ID, s.originFor(name))
			log.Printf("%s was removed: %d Webmention(s) queued so that receivers take its replies and likes down", name, n)
			s.st.MarkRetracted(name)
		}
		release()
	}
}
