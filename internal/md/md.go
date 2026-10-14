// Package md renders Markdown to HTML. Raw HTML in the source is escaped.
package md

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

var conv = goldmark.New(goldmark.WithExtensions(extension.Linkify, extension.Strikethrough))

// Render converts Markdown to HTML. Raw HTML blocks are omitted by goldmark's default (safe) mode.
func Render(src string) (string, error) {
	var b bytes.Buffer
	if err := conv.Convert([]byte(src), &b); err != nil {
		return "", err
	}
	return b.String(), nil
}
