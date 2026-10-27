package web

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/hbroek/weft/internal/img"
	"github.com/hbroek/weft/internal/store"
)

// bodyImage matches the picture of a post as the Markdown renderer writes it. The icons and avatars of the
// page carry loading or width themselves and are left alone.
var bodyImage = regexp.MustCompile(`<img src="([^"]*)"([^>]*)>`)

// sizePictures tells the browser how to treat the pictures in the body of a page. All but the first are loaded
// when they come near the screen, and none stops the page from drawing. Pictures kept on this server get their
// width and height, so the text below does not jump when a picture arrives, and a large one is asked for in
// its list size (see mediasize.go) and links to the picture itself. The first picture is not deferred,
// because it is often the largest thing in view.
func sizePictures(page []byte, sp *store.Space) []byte {
	first := true
	var out bytes.Buffer
	last := 0
	for _, loc := range bodyImage.FindAllSubmatchIndex(page, -1) {
		src, rest := page[loc[2]:loc[3]], string(page[loc[4]:loc[5]])
		if strings.Contains(rest, "loading=") || strings.Contains(rest, "width=") {
			continue
		}
		path, w, h, linked := string(src), 0, 0, false
		var orig string
		if sp != nil {
			if name, ok := strings.CutPrefix(html.UnescapeString(path), "/media/"); ok && mediaName.MatchString(name) {
				if m, err := sp.MediaMeta(name); err == nil && m.Width > 0 && m.Height > 0 {
					w, h = m.Width, m.Height
					if scalable(m, listSide) {
						orig = path
						path += "?w=" + strconv.Itoa(listSide)
						w, h = img.Fitted(w, h, listSide)
						linked = !insideLink(page[:loc[0]])
					}
					rest += fmt.Sprintf(` width="%d" height="%d" style="--r:%.4f"`, w, h, float64(w)/float64(h))
					// --r is width over height: the style sheet keeps a tall picture within its maximum height
					// by narrowing it, which the width attribute alone would not do
				}
			}
		}
		out.Write(page[last:loc[0]])
		last = loc[1]
		if linked {
			out.WriteString(`<a href="` + orig + `">`)
		}
		out.WriteString(`<img src="` + path + `"` + rest)
		if !first {
			out.WriteString(` loading="lazy"`)
		}
		out.WriteString(` decoding="async">`)
		if linked {
			out.WriteString(`</a>`)
		}
		first = false
	}
	out.Write(page[last:])
	return out.Bytes()
}

// insideLink reports whether the text before a picture leaves a link open, where another link cannot go.
func insideLink(before []byte) bool {
	open := bytes.LastIndex(before, []byte("<a "))
	return open > bytes.LastIndex(before, []byte("</a>"))
}
