package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

// anchorID is the id of the timeline row for the post at target, so that a page can send the reader back to
// the post they came from (/timeline#p-…). It is made from the address, which both the row and the link know.
func anchorID(target string) string {
	h := sha256.Sum256([]byte(target))
	return "p-" + hex.EncodeToString(h[:6])
}

// timelineBack returns raw if it is a timeline page of this site (the path /timeline, with any query) and "" if
// it is anything else: it comes from a form field, and must not send anyone elsewhere.
func timelineBack(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path != "/timeline" || strings.ContainsAny(raw, "\\\r\n") {
		return ""
	}
	u.Fragment = ""
	return u.RequestURI()
}

// withAnchor appends the fragment that scrolls to the row of target.
func withAnchor(path, target string) string {
	if target == "" {
		return path
	}
	return path + "#" + anchorID(target)
}
