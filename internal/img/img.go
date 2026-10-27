// Package img prepares uploaded pictures for publishing: it checks that they are what they claim to be, takes out
// everything that is not the picture (location, camera, time, thumbnails, comments, trailing data), and can make
// them smaller. JPEG, PNG and GIF only, with the standard library.
package img

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// Mode is how much to change a picture.
type Mode int

const (
	// Original keeps the picture as it is: its metadata is removed without recompressing it (a photo that is
	// stored turned, with an orientation flag, is turned and recompressed once, since the flag is metadata).
	Original Mode = iota
	// Compact makes the longest side at most Limits.MaxSide, and keeps whichever of the result and the
	// metadata-free original is smaller.
	Compact
)

// Limits are the server's bounds.
type Limits struct {
	MaxBytes  int // the largest picture kept, and the largest file accepted at the Original size
	MaxInput  int // the largest file accepted at the Compact size, which is made smaller (0: MaxBytes)
	MaxPixels int // width times height, so a small file cannot unpack to something huge
	MaxSide   int // the longest side of a Compact picture
}

// DefaultLimits are used when a server sets none.
var DefaultLimits = Limits{MaxBytes: 5 << 20, MaxInput: 25 << 20, MaxPixels: 24_000_000, MaxSide: 1600}

var (
	ErrUnsupported = errors.New("only JPEG, PNG and GIF pictures are accepted")
	ErrTooLarge    = errors.New("the picture is too large")
	ErrTooManyPx   = errors.New("the picture has too many pixels")
	ErrCorrupt     = errors.New("the file is not a valid picture")
)

// Result is a picture ready to store.
type Result struct {
	Data          []byte
	Mime, Ext     string
	Width, Height int
}

const (
	jpegQualityCompact = 80
	jpegQualityTurned  = 92
	maxGIFFrames       = 300
)

// Process checks, cleans and optionally shrinks a picture. The kind is taken from the bytes, not from what the
// sender called the file.
func Process(data []byte, mode Mode, lim Limits) (*Result, error) {
	if lim.MaxBytes <= 0 {
		lim = DefaultLimits
	}
	if lim.MaxPixels <= 0 {
		lim.MaxPixels = DefaultLimits.MaxPixels
	}
	in := lim.MaxBytes
	if mode == Compact && lim.MaxInput > in {
		in = lim.MaxInput // a photo straight from a phone is made smaller, so it may be larger than what is kept
	}
	if len(data) > in {
		return nil, ErrTooLarge
	}
	var res *Result
	var err error
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		res, err = processJPEG(data, mode, lim)
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		res, err = processPNG(data, mode, lim)
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		res, err = processGIF(data, mode, lim)
	default:
		return nil, ErrUnsupported
	}
	if err == nil && len(res.Data) > lim.MaxBytes {
		return nil, ErrTooLarge // even made smaller, it is more than is kept
	}
	return res, err
}

func checkSize(cfg image.Config, lim Limits) error {
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return ErrCorrupt
	}
	if cfg.Width*cfg.Height > lim.MaxPixels {
		return ErrTooManyPx
	}
	return nil
}

// Fitted returns the size of a picture w by h shrunk so that its longest side is at most side.
func Fitted(w, h, side int) (int, int) { return fitted(w, h, side) }

// fitted returns the size of a picture w by h shrunk so that its longest side is at most side.
func fitted(w, h, side int) (int, int) {
	long := max(w, h)
	if side <= 0 || long <= side {
		return w, h
	}
	return max(1, w*side/long), max(1, h*side/long)
}

// ---- JPEG ----

func processJPEG(data []byte, mode Mode, lim Limits) (*Result, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrCorrupt
	}
	if err := checkSize(cfg, lim); err != nil {
		return nil, err
	}
	orient := jpegOrientation(data)
	icc := jpegICC(data)
	stripped, ok := stripJPEG(data)
	if !ok {
		return nil, ErrCorrupt
	}
	src, err := jpeg.Decode(bytes.NewReader(stripped)) // proves the picture decodes, and gives its pixels
	if err != nil {
		return nil, ErrCorrupt
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	nw, nh := w, h
	if mode == Compact {
		nw, nh = fitted(w, h, lim.MaxSide)
	}
	turned := orient > 1
	if !turned && nw == w && nh == h && mode == Original {
		return &Result{Data: stripped, Mime: "image/jpeg", Ext: "jpg", Width: w, Height: h}, nil
	}
	if !turned && nw == w && nh == h && mode == Compact && len(stripped) < 40<<10 {
		return &Result{Data: stripped, Mime: "image/jpeg", Ext: "jpg", Width: w, Height: h}, nil // small already
	}
	pic := toRGBA(src)
	if turned {
		pic = orientPicture(pic, orient)
		w, h = pic.Bounds().Dx(), pic.Bounds().Dy()
		nw, nh = w, h
		if mode == Compact {
			nw, nh = fitted(w, h, lim.MaxSide)
		}
	}
	if nw != w || nh != h {
		pic = resize(pic, nw, nh)
	}
	q := jpegQualityCompact
	if mode == Original {
		q = jpegQualityTurned
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, pic, &jpeg.Options{Quality: q}); err != nil {
		return nil, err
	}
	out := withICC(buf.Bytes(), icc)
	if !turned && mode == Compact && len(out) >= len(stripped) {
		return &Result{Data: stripped, Mime: "image/jpeg", Ext: "jpg", Width: w, Height: h}, nil
	}
	return &Result{Data: out, Mime: "image/jpeg", Ext: "jpg", Width: nw, Height: nh}, nil
}

// jpegSegments walks a JPEG. For each marker it calls keep with the marker and the whole segment (marker,
// length and payload); entropy-coded data after a scan header is passed on with marker 0. It reports false
// for a file that is not well formed. It stops at the end-of-image marker, so data appended after the
// picture is never seen.
func jpegSegments(data []byte, keep func(marker byte, seg []byte)) bool {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return false
	}
	keep(0xD8, data[:2])
	i := 2
	for i < len(data) {
		if data[i] != 0xFF {
			return false
		}
		for i < len(data) && data[i] == 0xFF { // fill bytes
			i++
		}
		if i >= len(data) {
			return false
		}
		m := data[i]
		i++
		switch {
		case m == 0xD9:
			keep(0xD9, []byte{0xFF, 0xD9})
			return true
		case m == 0x01 || m >= 0xD0 && m <= 0xD7:
			keep(m, []byte{0xFF, m})
			continue
		}
		if i+2 > len(data) {
			return false
		}
		n := int(binary.BigEndian.Uint16(data[i:]))
		if n < 2 || i+n > len(data) {
			return false
		}
		seg := data[i-2 : i+n]
		i += n
		keep(m, seg)
		if m == 0xDA { // a scan: entropy-coded data follows, up to the next marker that is not a restart
			start := i
			for i < len(data) {
				if data[i] == 0xFF && i+1 < len(data) {
					b := data[i+1]
					if b == 0 || b >= 0xD0 && b <= 0xD7 || b == 0xFF {
						i++
						if b != 0xFF {
							i++
						}
						continue
					}
					break
				}
				i++
			}
			if i >= len(data) {
				return false
			}
			keep(0, data[start:i])
		}
	}
	return false
}

// stripJPEG copies a JPEG without anything that is not the picture: it keeps the tables, the frame, the scans, the
// JFIF and Adobe headers and the colour profile, and drops EXIF, XMP, IPTC, comments, thumbnails, other
// application segments and anything after the end of the image.
func stripJPEG(data []byte) ([]byte, bool) {
	var out bytes.Buffer
	ok := jpegSegments(data, func(m byte, seg []byte) {
		switch {
		case m == 0xE0 && bytes.HasPrefix(payload(seg), []byte("JFIF\x00")),
			m == 0xE2 && bytes.HasPrefix(payload(seg), []byte("ICC_PROFILE\x00")),
			m == 0xEE && bytes.HasPrefix(payload(seg), []byte("Adobe")),
			m == 0, m == 0xD8, m == 0xD9, m == 0xDB, m == 0xC4, m == 0xDA, m == 0xDD, m == 0xDC,
			m >= 0xC0 && m <= 0xCF && m != 0xC8 && m != 0xC4,
			m >= 0xD0 && m <= 0xD7:
			out.Write(seg)
		}
	})
	return out.Bytes(), ok
}

func payload(seg []byte) []byte {
	if len(seg) < 4 {
		return nil
	}
	return seg[4:]
}

// jpegICC returns the colour profile segments of a JPEG, which a recompressed picture needs to keep its colours.
func jpegICC(data []byte) [][]byte {
	var out [][]byte
	jpegSegments(data, func(m byte, seg []byte) {
		if m == 0xE2 && bytes.HasPrefix(payload(seg), []byte("ICC_PROFILE\x00")) {
			out = append(out, append([]byte(nil), seg...))
		}
	})
	return out
}

// withICC puts colour profile segments right after the start of a JPEG.
func withICC(jpg []byte, icc [][]byte) []byte {
	if len(icc) == 0 || len(jpg) < 2 {
		return jpg
	}
	var out bytes.Buffer
	out.Write(jpg[:2])
	for _, s := range icc {
		out.Write(s)
	}
	out.Write(jpg[2:])
	return out.Bytes()
}

// jpegOrientation reads the EXIF orientation (1 when there is none or it cannot be read).
func jpegOrientation(data []byte) int {
	o := 1
	jpegSegments(data, func(m byte, seg []byte) {
		p := payload(seg)
		if m != 0xE1 || !bytes.HasPrefix(p, []byte("Exif\x00\x00")) {
			return
		}
		t := p[6:]
		if len(t) < 8 {
			return
		}
		var bo binary.ByteOrder
		switch string(t[:2]) {
		case "II":
			bo = binary.LittleEndian
		case "MM":
			bo = binary.BigEndian
		default:
			return
		}
		off := int(bo.Uint32(t[4:]))
		if off < 8 || off+2 > len(t) {
			return
		}
		n := int(bo.Uint16(t[off:]))
		for k := 0; k < n; k++ {
			e := off + 2 + k*12
			if e+12 > len(t) {
				return
			}
			if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 { // Orientation, SHORT
				if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
					o = v
				}
				return
			}
		}
	})
	return o
}

// ---- PNG ----

// keptPNG are the chunks that are part of the picture or how it is shown; everything else (text, EXIF, time,
// private chunks) is dropped.
var keptPNG = map[string]bool{
	"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true, "tRNS": true, "gAMA": true, "cHRM": true,
	"sRGB": true, "iCCP": true, "sBIT": true, "bKGD": true, "pHYs": true, "acTL": true, "fcTL": true, "fdAT": true,
}

func stripPNG(data []byte) ([]byte, bool, bool) { // data, ok, animated
	var out bytes.Buffer
	out.Write(data[:8])
	i, animated := 8, false
	for i+12 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[i:]))
		if n < 0 || i+12+n > len(data) {
			return nil, false, false
		}
		typ := string(data[i+4 : i+8])
		if typ == "acTL" {
			animated = true
		}
		if keptPNG[typ] {
			out.Write(data[i : i+12+n])
		}
		i += 12 + n
		if typ == "IEND" {
			return out.Bytes(), true, animated
		}
	}
	return nil, false, false
}

func processPNG(data []byte, mode Mode, lim Limits) (*Result, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrCorrupt
	}
	if err := checkSize(cfg, lim); err != nil {
		return nil, err
	}
	stripped, ok, animated := stripPNG(data)
	if !ok {
		return nil, ErrCorrupt
	}
	src, err := png.Decode(bytes.NewReader(stripped))
	if err != nil {
		return nil, ErrCorrupt
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	orig := &Result{Data: stripped, Mime: "image/png", Ext: "png", Width: w, Height: h}
	if mode == Original || animated { // an animated PNG would lose its frames
		return orig, nil
	}
	nw, nh := fitted(w, h, lim.MaxSide)
	pic := toRGBA(src)
	if nw != w || nh != h {
		pic = resize(pic, nw, nh)
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, pic); err != nil {
		return nil, err
	}
	if buf.Len() >= len(stripped) && nw == w {
		return orig, nil
	}
	return &Result{Data: buf.Bytes(), Mime: "image/png", Ext: "png", Width: nw, Height: nh}, nil
}

// ---- GIF ----

func processGIF(data []byte, mode Mode, lim Limits) (*Result, error) {
	cfg, err := gif.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrCorrupt
	}
	if err := checkSize(cfg, lim); err != nil {
		return nil, err
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil || len(g.Image) == 0 {
		return nil, ErrCorrupt
	}
	if len(g.Image) > maxGIFFrames || len(g.Image)*cfg.Width*cfg.Height > 4*lim.MaxPixels {
		return nil, ErrTooManyPx
	}
	w, h := cfg.Width, cfg.Height
	nw, nh := fitted(w, h, lim.MaxSide)
	if mode == Compact && len(g.Image) == 1 && (nw != w || nh != h) {
		// a still picture is scaled and put back in the palette it had
		pal := g.Image[0].Palette
		pic := resize(toRGBA(g.Image[0]), nw, nh)
		dst := image.NewPaletted(pic.Bounds(), pal)
		draw.FloydSteinberg.Draw(dst, pic.Bounds(), pic, image.Point{})
		g.Image[0], g.Config.Width, g.Config.Height = dst, nw, nh
		w, h = nw, nh
	}
	g.Config.ColorModel = nil // let the encoder write each frame's own palette
	if len(g.Image) == 1 && g.Image[0].Palette == nil {
		g.Image[0].Palette = palette.Plan9
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil { // comments and application extensions are not written back
		return nil, err
	}
	return &Result{Data: buf.Bytes(), Mime: "image/gif", Ext: "gif", Width: w, Height: h}, nil
}

// ---- pixels ----

func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// orientPicture turns a picture the way its EXIF orientation says it is shown.
func orientPicture(src *image.RGBA, o int) *image.RGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			default:
				sx, sy = x, y
			}
			s := src.PixOffset(sx, sy)
			d := dst.PixOffset(x, y)
			copy(dst.Pix[d:d+4], src.Pix[s:s+4])
		}
	}
	return dst
}

// span is how much each source pixel counts towards one destination pixel.
type span struct {
	first int
	w     []float32
}

func spans(sn, dn int) []span {
	out := make([]span, dn)
	scale := float64(sn) / float64(dn)
	for i := range out {
		lo, hi := float64(i)*scale, float64(i+1)*scale
		first, last := int(lo), min(int(hi), sn-1)
		if float64(last) == hi && last > first {
			last--
		}
		s := span{first: first}
		var sum float32
		for k := first; k <= last; k++ {
			a, b := max(lo, float64(k)), min(hi, float64(k+1))
			wt := float32(b - a)
			if wt < 0 {
				wt = 0
			}
			s.w = append(s.w, wt)
			sum += wt
		}
		if sum > 0 {
			for k := range s.w {
				s.w[k] /= sum
			}
		}
		out[i] = s
	}
	return out
}

// resize shrinks a picture by averaging the source pixels each new one covers, one direction at a time.
func resize(src *image.RGBA, dw, dh int) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	hs, vs := spans(sw, dw), spans(sh, dh)
	tmp := image.NewRGBA(image.Rect(0, 0, dw, sh))
	for y := 0; y < sh; y++ {
		row := src.Pix[y*src.Stride : y*src.Stride+sw*4]
		for x, s := range hs {
			var r, g, b, a float32
			for k, wt := range s.w {
				p := (s.first + k) * 4
				r += float32(row[p]) * wt
				g += float32(row[p+1]) * wt
				b += float32(row[p+2]) * wt
				a += float32(row[p+3]) * wt
			}
			o := y*tmp.Stride + x*4
			tmp.Pix[o], tmp.Pix[o+1], tmp.Pix[o+2], tmp.Pix[o+3] = round8(r), round8(g), round8(b), round8(a)
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y, s := range vs {
		for x := 0; x < dw; x++ {
			var r, g, b, a float32
			for k, wt := range s.w {
				p := (s.first+k)*tmp.Stride + x*4
				r += float32(tmp.Pix[p]) * wt
				g += float32(tmp.Pix[p+1]) * wt
				b += float32(tmp.Pix[p+2]) * wt
				a += float32(tmp.Pix[p+3]) * wt
			}
			o := y*dst.Stride + x*4
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = round8(r), round8(g), round8(b), round8(a)
		}
	}
	return dst
}

func round8(v float32) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}

// ---- profile pictures ----

// Avatar makes a profile picture from a JPEG, PNG or GIF (the first frame): the middle square of the picture, turned
// the right way up, at most size pixels each way, with no metadata. A picture with transparency stays a PNG, any
// other is a JPEG. Like Compact, it accepts a file as large as a phone photo.
func Avatar(data []byte, lim Limits, size int) (*Result, error) {
	if lim.MaxBytes <= 0 {
		lim = DefaultLimits
	}
	if lim.MaxPixels <= 0 {
		lim.MaxPixels = DefaultLimits.MaxPixels
	}
	if len(data) > max(lim.MaxBytes, lim.MaxInput) {
		return nil, ErrTooLarge
	}
	var src image.Image
	var cfg image.Config
	var err error
	orient, icc := 1, [][]byte(nil)
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		if cfg, err = jpeg.DecodeConfig(bytes.NewReader(data)); err == nil {
			if err = checkSize(cfg, lim); err != nil {
				return nil, err
			}
			src, err = jpeg.Decode(bytes.NewReader(data))
			orient, icc = jpegOrientation(data), jpegICC(data)
		}
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		if cfg, err = png.DecodeConfig(bytes.NewReader(data)); err == nil {
			if err = checkSize(cfg, lim); err != nil {
				return nil, err
			}
			src, err = png.Decode(bytes.NewReader(data))
		}
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		if cfg, err = gif.DecodeConfig(bytes.NewReader(data)); err == nil {
			if err = checkSize(cfg, lim); err != nil {
				return nil, err
			}
			src, err = gif.Decode(bytes.NewReader(data))
		}
	default:
		return nil, ErrUnsupported
	}
	if err != nil || src == nil {
		if errors.Is(err, ErrTooManyPx) {
			return nil, err
		}
		return nil, ErrCorrupt
	}
	pic := toRGBA(src)
	if orient > 1 {
		pic = orientPicture(pic, orient)
	}
	w, h := pic.Bounds().Dx(), pic.Bounds().Dy()
	side := min(w, h)
	x0, y0 := (w-side)/2, (h-side)/2
	sq := toRGBA(pic.SubImage(image.Rect(x0, y0, x0+side, y0+side)))
	if size > 0 && side > size {
		sq = resize(sq, size, size)
		side = size
	}
	var buf bytes.Buffer
	if !sq.Opaque() {
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, sq); err != nil {
			return nil, err
		}
		return &Result{Data: buf.Bytes(), Mime: "image/png", Ext: "png", Width: side, Height: side}, nil
	}
	if err := jpeg.Encode(&buf, sq, &jpeg.Options{Quality: 88}); err != nil {
		return nil, err
	}
	return &Result{Data: withICC(buf.Bytes(), icc), Mime: "image/jpeg", Ext: "jpg", Width: side, Height: side}, nil
}
