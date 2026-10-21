package feed

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// charsetReader lets the XML decoder read feeds that declare an older encoding, such as
// <?xml version="1.0" encoding="ISO-8859-1"?>. Without it the standard library refuses everything but
// UTF-8. Like browsers, it reads the Latin-1 labels as Windows-1252, which is what such feeds actually
// contain (curly quotes and dashes sit where Latin-1 has control characters). Other encodings are refused.
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	var table map[byte]rune
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "us-ascii", "ascii", "utf8":
		return in, nil
	case "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "latin-1", "l1", "cp819", "windows-1252", "cp1252", "x-cp1252":
		table = windows1252
	case "iso-8859-15", "iso8859-15", "latin9", "latin-9":
		table = iso885915
	default:
		return nil, fmt.Errorf("unsupported encoding %q", label)
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Grow(len(raw) + len(raw)/8)
	for _, c := range raw {
		if r, ok := table[c]; ok {
			out.WriteRune(r)
		} else if c < 0x80 {
			out.WriteByte(c)
		} else {
			out.WriteRune(rune(c)) // 0xA0-0xFF are the same code points in Unicode
		}
	}
	if !utf8.Valid(out.Bytes()) {
		return nil, fmt.Errorf("invalid %s text", label)
	}
	return &out, nil
}

// windows1252 maps the bytes 0x80-0x9F, where Windows-1252 differs from Latin-1. The five bytes it
// leaves undefined are read as the control characters they would be in Latin-1.
var windows1252 = map[byte]rune{
	0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„', 0x85: '…', 0x86: '†', 0x87: '‡', 0x88: 'ˆ', 0x89: '‰',
	0x8A: 'Š', 0x8B: '‹', 0x8C: 'Œ', 0x8E: 'Ž', 0x91: '‘', 0x92: '’', 0x93: '“', 0x94: '”', 0x95: '•',
	0x96: '–', 0x97: '—', 0x98: '˜', 0x99: '™', 0x9A: 'š', 0x9B: '›', 0x9C: 'œ', 0x9E: 'ž', 0x9F: 'Ÿ',
}

// iso885915 is Latin-9: Latin-1 with the euro sign and a few letters swapped in.
var iso885915 = map[byte]rune{
	0xA4: '€', 0xA6: 'Š', 0xA8: 'š', 0xB4: 'Ž', 0xB8: 'ž', 0xBC: 'Œ', 0xBD: 'œ', 0xBE: 'Ÿ',
}
