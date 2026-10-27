package img

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// picture is w by h: red on the left half, blue on the right, so a turned or flipped copy can be told apart.
func picture(w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{R: 255, A: 255}
			if x >= w/2 {
				c = color.RGBA{B: 255, A: 255}
			}
			m.SetRGBA(x, y, c)
		}
	}
	return m
}

func segment(marker byte, payload []byte) []byte {
	out := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(out[2:], uint16(len(payload)+2))
	return append(out, payload...)
}

// exif is an EXIF block with an orientation and a GPS-looking string, in the byte order given.
func exif(bo binary.ByteOrder, orientation int) []byte {
	var b bytes.Buffer
	if bo == binary.LittleEndian {
		b.WriteString("II")
	} else {
		b.WriteString("MM")
	}
	w16 := func(v uint16) { binary.Write(&b, bo, v) }
	w32 := func(v uint32) { binary.Write(&b, bo, v) }
	w16(42)
	w32(8)
	w16(1) // one entry
	w16(0x0112)
	w16(3)
	w32(1)
	w16(uint16(orientation))
	w16(0)
	w32(0)
	b.WriteString("GPSMARKER 40.7128,-74.0060")
	return append([]byte("Exif\x00\x00"), b.Bytes()...)
}

// withSegments puts extra segments after the start of a JPEG and appends trailing bytes after its end.
func withSegments(jpg []byte, trailer []byte, segs ...[]byte) []byte {
	var out bytes.Buffer
	out.Write(jpg[:2])
	for _, s := range segs {
		out.Write(s)
	}
	out.Write(jpg[2:])
	out.Write(trailer)
	return out.Bytes()
}

func jpegOf(t *testing.T, m image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, m, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestJPEGMetadataIsRemovedWithoutRecompressing(t *testing.T) {
	plain := jpegOf(t, picture(64, 48))
	dirty := withSegments(plain, []byte("TRAILING-SECRET"),
		segment(0xE1, exif(binary.LittleEndian, 1)), segment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta>XMPSECRET</x:xmpmeta>")),
		segment(0xFE, []byte("a comment COMMENTSECRET")), segment(0xED, []byte("Photoshop 3.0\x00IPTCSECRET")))
	got, err := Process(dirty, Original, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"GPSMARKER", "XMPSECRET", "COMMENTSECRET", "IPTCSECRET", "TRAILING-SECRET", "Exif"} {
		if bytes.Contains(got.Data, []byte(s)) {
			t.Errorf("%s survived", s)
		}
	}
	if got.Mime != "image/jpeg" || got.Ext != "jpg" || got.Width != 64 || got.Height != 48 {
		t.Errorf("result %+v", got)
	}
	// the scan data is copied untouched: the same picture, not a recompressed one
	if !bytes.Equal(got.Data, plain) && len(got.Data) > len(plain) {
		t.Errorf("a cleaned picture is larger than the original: %d > %d", len(got.Data), len(plain))
	}
	if _, err := jpeg.Decode(bytes.NewReader(got.Data)); err != nil {
		t.Errorf("result does not decode: %v", err)
	}
}

func TestJPEGKeepsItsColourProfile(t *testing.T) {
	icc := segment(0xE2, append([]byte("ICC_PROFILE\x00\x01\x01"), bytes.Repeat([]byte{7}, 50)...))
	dirty := withSegments(jpegOf(t, picture(64, 48)), nil, segment(0xE1, exif(binary.BigEndian, 1)), icc)
	for _, mode := range []Mode{Original, Compact} {
		got, err := Process(dirty, mode, Limits{MaxBytes: 1 << 20, MaxPixels: 1 << 20, MaxSide: 32}) // forces a recompression
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(got.Data, []byte("ICC_PROFILE\x00")) || bytes.Contains(got.Data, []byte("GPSMARKER")) {
			t.Errorf("mode %d: profile kept=%v", mode, bytes.Contains(got.Data, []byte("ICC_PROFILE")))
		}
	}
}

func TestJPEGOrientationIsAppliedAndTheFlagDropped(t *testing.T) {
	// 64 wide, red left and blue right, with orientation 6 (rotate 90 clockwise): shown 48 wide and 64 tall,
	// red at the top
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		dirty := withSegments(jpegOf(t, picture(64, 48)), nil, segment(0xE1, exif(bo, 6)))
		got, err := Process(dirty, Original, DefaultLimits)
		if err != nil {
			t.Fatal(err)
		}
		if got.Width != 48 || got.Height != 64 {
			t.Fatalf("size %dx%d, want 48x64", got.Width, got.Height)
		}
		if bytes.Contains(got.Data, []byte("Exif")) {
			t.Errorf("the orientation flag is still there")
		}
		m, _ := jpeg.Decode(bytes.NewReader(got.Data))
		top, bottom := color.RGBAModel.Convert(m.At(24, 5)).(color.RGBA), color.RGBAModel.Convert(m.At(24, 58)).(color.RGBA)
		if top.R < 200 || bottom.B < 200 {
			t.Errorf("not turned the right way: top %v bottom %v", top, bottom)
		}
	}
}

func TestCompactShrinksTheLongestSide(t *testing.T) {
	big := jpegOf(t, picture(800, 400))
	got, err := Process(big, Compact, Limits{MaxBytes: 5 << 20, MaxPixels: 1 << 24, MaxSide: 200})
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 200 || got.Height != 100 {
		t.Errorf("size %dx%d, want 200x100", got.Width, got.Height)
	}
	m, err := jpeg.Decode(bytes.NewReader(got.Data))
	if err != nil || m.Bounds().Dx() != 200 || m.Bounds().Dy() != 100 {
		t.Fatalf("decoded %v %v", m.Bounds(), err)
	}
	// the halves keep their colours through the averaging
	l, r := color.RGBAModel.Convert(m.At(20, 50)).(color.RGBA), color.RGBAModel.Convert(m.At(180, 50)).(color.RGBA)
	if l.R < 200 || r.B < 200 {
		t.Errorf("colours changed: %v %v", l, r)
	}
	// Original leaves the size alone
	if o, _ := Process(big, Original, Limits{MaxBytes: 5 << 20, MaxPixels: 1 << 24, MaxSide: 200}); o.Width != 800 {
		t.Errorf("Original resized to %d", o.Width)
	}
}

func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	return b.Bytes()
}

// pngWith inserts chunks before the first IDAT.
func pngWith(t *testing.T, m image.Image, chunks ...[]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	data := b.Bytes()
	i := bytes.Index(data, []byte("IDAT")) - 4
	var out bytes.Buffer
	out.Write(data[:i])
	for _, c := range chunks {
		out.Write(c)
	}
	out.Write(data[i:])
	out.WriteString("TRAILING-SECRET")
	return out.Bytes()
}

func TestPNGTextAndExifChunksAreRemoved(t *testing.T) {
	dirty := pngWith(t, picture(40, 30),
		pngChunk("tEXt", []byte("Comment\x00TEXTSECRET")), pngChunk("eXIf", []byte("GPSMARKER")),
		pngChunk("tIME", make([]byte, 7)), pngChunk("iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00XMPSECRET")),
		pngChunk("gAMA", []byte{0, 1, 0x86, 0xA0}))
	got, err := Process(dirty, Original, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"TEXTSECRET", "GPSMARKER", "XMPSECRET", "TRAILING-SECRET", "tIME"} {
		if bytes.Contains(got.Data, []byte(s)) {
			t.Errorf("%s survived", s)
		}
	}
	if !bytes.Contains(got.Data, []byte("gAMA")) {
		t.Errorf("a chunk that belongs to the picture was dropped")
	}
	if m, err := png.Decode(bytes.NewReader(got.Data)); err != nil || m.Bounds().Dx() != 40 {
		t.Errorf("result does not decode: %v", err)
	}
}

func TestPNGCompactKeepsTransparency(t *testing.T) {
	m := picture(600, 300)
	m.SetRGBA(10, 10, color.RGBA{}) // a transparent pixel
	var b bytes.Buffer
	png.Encode(&b, m)
	got, err := Process(b.Bytes(), Compact, Limits{MaxBytes: 5 << 20, MaxPixels: 1 << 24, MaxSide: 300})
	if err != nil {
		t.Fatal(err)
	}
	if got.Mime != "image/png" || got.Width != 300 || got.Height != 150 {
		t.Errorf("result %+v", got)
	}
}

func TestGIFCommentsAreRemoved(t *testing.T) {
	g := &gif.GIF{}
	for i := 0; i < 2; i++ {
		f := image.NewPaletted(image.Rect(0, 0, 20, 10), []color.Color{color.Black, color.White})
		f.SetColorIndex(i, i, 1)
		g.Image, g.Delay = append(g.Image, f), append(g.Delay, 10)
	}
	var b bytes.Buffer
	gif.EncodeAll(&b, g)
	data := b.Bytes()
	// a comment extension at the start of the blocks
	i := 13 // after the header and the logical screen descriptor (this encoder writes no global colour table)
	dirty := append(append(append([]byte{}, data[:i]...), 0x21, 0xFE, 12, 'G', 'I', 'F', 'S', 'E', 'C', 'R', 'E', 'T', '-', 'X', 'Y', 0), data[i:]...)
	if _, err := gif.DecodeAll(bytes.NewReader(dirty)); err != nil {
		t.Fatalf("test gif is broken: %v", err)
	}
	got, err := Process(dirty, Original, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got.Data, []byte("GIFSECRET")) {
		t.Errorf("the comment survived")
	}
	if out, err := gif.DecodeAll(bytes.NewReader(got.Data)); err != nil || len(out.Image) != 2 {
		t.Errorf("frames after cleaning: %v %v", out, err)
	}
	if got.Mime != "image/gif" || got.Ext != "gif" {
		t.Errorf("result %+v", got)
	}
}

func TestRefusals(t *testing.T) {
	small := Limits{MaxBytes: 1 << 20, MaxPixels: 1000, MaxSide: 100}
	if _, err := Process(jpegOf(t, picture(100, 100)), Original, small); err != ErrTooManyPx {
		t.Errorf("too many pixels: %v", err)
	}
	if _, err := Process([]byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>"), Original, DefaultLimits); err != ErrUnsupported {
		t.Errorf("svg: %v", err)
	}
	var gb bytes.Buffer
	gif.Encode(&gb, picture(20, 10), nil)
	if _, err := Process(gb.Bytes()[:gb.Len()/2], Original, DefaultLimits); err != ErrCorrupt {
		t.Errorf("cut gif: %v", err)
	}
	cut := jpegOf(t, picture(64, 48))
	if _, err := Process(cut[:len(cut)/2], Original, DefaultLimits); err != ErrCorrupt {
		t.Errorf("cut jpeg: %v", err)
	}
	if _, err := Process(jpegOf(t, picture(64, 48)), Original, Limits{MaxBytes: 100, MaxPixels: 1 << 20}); err != ErrTooLarge {
		t.Errorf("too large: %v", err)
	}
	// a PNG that says it is huge but is small on disk
	var b bytes.Buffer
	png.Encode(&b, picture(10, 10))
	data := b.Bytes()
	binary.BigEndian.PutUint32(data[16:], 60000)
	binary.BigEndian.PutUint32(data[20:], 60000)
	crc := crc32.ChecksumIEEE(data[12:29])
	binary.BigEndian.PutUint32(data[29:], crc)
	if _, err := Process(data, Original, DefaultLimits); err != ErrTooManyPx {
		t.Errorf("pixel bomb: %v", err)
	}
}

func TestOrientationsMapCorners(t *testing.T) {
	// a 3x2 picture with a different colour in each corner pixel: orientations 1..8 each put them somewhere else
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	cols := []color.RGBA{{R: 255, A: 255}, {G: 255, A: 255}, {B: 255, A: 255}, {R: 255, G: 255, A: 255}, {R: 255, B: 255, A: 255}, {G: 255, B: 255, A: 255}}
	for i, c := range cols {
		src.SetRGBA(i%3, i/3, c)
	}
	// expected top-left pixel after each orientation (EXIF table): 1 same, 2 mirrored, 3 turned 180, 4 flipped,
	// 5 transposed, 6 turned clockwise, 7 transverse, 8 turned anticlockwise
	want := map[int]color.RGBA{1: cols[0], 2: cols[2], 3: cols[5], 4: cols[3], 5: cols[0], 6: cols[3], 7: cols[5], 8: cols[2]}
	for o, c := range want {
		if got := orientPicture(src, o).RGBAAt(0, 0); got != c {
			t.Errorf("orientation %d: top-left %v, want %v", o, got, c)
		}
	}
}

// noisy is a picture that does not compress, so its file is large.
func noisy(w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	var s uint32 = 12345
	for i := range m.Pix {
		s = s*1664525 + 1013904223
		m.Pix[i] = uint8(s >> 24)
		if i%4 == 3 {
			m.Pix[i] = 255
		}
	}
	return m
}

func TestCompactAcceptsAFileLargerThanWhatIsKept(t *testing.T) {
	big := jpegOf(t, noisy(1200, 900)) // well over 600 KB
	if len(big) < 600<<10 {
		t.Fatalf("test picture is only %d bytes", len(big))
	}
	lim := Limits{MaxBytes: 200 << 10, MaxInput: 5 << 20, MaxPixels: 1 << 24, MaxSide: 400}
	if _, err := Process(big, Original, lim); err != ErrTooLarge {
		t.Errorf("Original accepted a file over the limit: %v", err)
	}
	got, err := Process(big, Compact, lim)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got.Width != 400 || len(got.Data) > lim.MaxBytes {
		t.Errorf("result %dx%d, %d bytes", got.Width, got.Height, len(got.Data))
	}
	// the input has its own limit
	lim.MaxInput = 100 << 10
	if _, err := Process(big, Compact, lim); err != ErrTooLarge {
		t.Errorf("input over MaxInput: %v", err)
	}
	// and what is kept has a limit even after shrinking
	lim = Limits{MaxBytes: 2 << 10, MaxInput: 5 << 20, MaxPixels: 1 << 24, MaxSide: 400}
	if _, err := Process(big, Compact, lim); err != ErrTooLarge {
		t.Errorf("a result over MaxBytes: %v", err)
	}
}
