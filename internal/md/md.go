// Package md renders Markdown to HTML. Raw HTML in the source is escaped.
package md

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

var conv = goldmark.New(
	goldmark.WithExtensions(extension.Linkify, extension.Strikethrough, extension.Table, extension.TaskList),
	// Runs before the task list's own renderer (the lowest number goes first), which would write a form control.
	goldmark.WithRendererOptions(renderer.WithNodeRenderers(util.Prioritized(taskEmoji{}, 100))),
)

// taskEmoji writes the box of a task list item (- [ ] and - [x]) as a symbol with a text for screen readers,
// not as <input type="checkbox">. A page of this server holds posts from anyone, and a form control in them is
// more than a post needs. The boxes are for reading: the source is where they are changed.
type taskEmoji struct{}

func (taskEmoji) RegisterFuncs(r renderer.NodeRendererFuncRegisterer) {
	r.Register(east.KindTaskCheckBox, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if n.(*east.TaskCheckBox).IsChecked {
				w.WriteString(`<span class="task" role="img" aria-label="done">✅</span> `)
			} else {
				w.WriteString(`<span class="task" role="img" aria-label="not done">⬜</span> `)
			}
		}
		return ast.WalkContinue, nil
	})
}

// Render converts Markdown to HTML. Raw HTML blocks are omitted by goldmark's default (safe) mode.
func Render(src string) (string, error) {
	var b bytes.Buffer
	if err := conv.Convert([]byte(src), &b); err != nil {
		return "", err
	}
	return b.String(), nil
}
