package web

import (
	"net/http"

	"github.com/hbroek/weft/internal/feed"
)

// parentCard is a link card for the page a post replies to: shown from the snapshot the reply
// carries (weft:context), since a plain web page cannot be looked up like a Weft post.
type parentCard struct{ Href, Title, Description, Image, Host string }

func newParentCard(href string, p feed.ParentInfo) *parentCard {
	href = httpURL(href)
	if href == "" || p.Empty() {
		return nil
	}
	host := siteHost(href)
	return &parentCard{Href: href, Title: orStr(p.Title, host), Description: p.Description, Image: p.Image, Host: host}
}

// parentFromForm reads the parent snapshot the compose form carries in hidden fields.
func parentFromForm(r *http.Request) feed.ParentInfo {
	return feed.ParentInfo{Title: r.FormValue("ptitle"), Description: r.FormValue("pdesc"), Image: r.FormValue("pimg")}.Clean()
}
