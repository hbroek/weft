package web

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/hbroek/weft/internal/store"
)

// A like count in the timeline is the one the author publishes, which only changes after the
// author's server has verified our like and we have fetched their feed again. Until then the
// count is shown with our own click applied, so the button and the number agree.
type adjKey struct {
	user   int64
	target string
}

type likeAdj struct{ base, delta int } // delta applies while the cached count still equals base

// tally is the count to show for a followed post whose cached count is cached.
func (s *Server) tally(userID int64, target string, cached int) int {
	s.adjMu.Lock()
	defer s.adjMu.Unlock()
	a, ok := s.adj[adjKey{userID, target}]
	if !ok {
		return cached
	}
	if cached != a.base { // the author's count has moved on and now includes our click
		delete(s.adj, adjKey{userID, target})
		return cached
	}
	return max(0, cached+a.delta)
}

// nudgeTally records that the user liked (d = 1) or unliked (d = -1) a followed post.
func (s *Server) nudgeTally(sp *store.Space, target string, d int) {
	userID := sp.UID()
	it, err := sp.ItemByTarget(userID, target)
	if err != nil {
		return // not a post in a followed feed: its count is not shown
	}
	shown := s.tally(userID, target, it.Likes)
	s.adjMu.Lock()
	defer s.adjMu.Unlock()
	if s.adj == nil {
		s.adj = map[adjKey]likeAdj{}
	}
	s.adj[adjKey{userID, target}] = likeAdj{base: it.Likes, delta: max(0, shown+d) - it.Likes}
}

// back redirects to the page the form was sent from, so a like keeps the filter and feed in view.
func back(w http.ResponseWriter, r *http.Request, fallback string) {
	to := fallback
	if u, err := url.Parse(r.Referer()); err == nil && u.Host == r.Host && strings.HasPrefix(u.Path, "/") && !strings.HasPrefix(u.Path, "//") {
		to = u.RequestURI()
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}
