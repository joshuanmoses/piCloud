package previews

import (
	"bytes"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// pngOf builds a solid-colour PNG of the given size.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding the fixture failed: %v", err)
	}
	return buf.Bytes()
}

func TestAThumbnailFitsInsideTheBoundingBoxAndKeepsItsShape(t *testing.T) {
	// A wide image: the width is the constraint.
	thumb, err := Generate(bytes.NewReader(pngOf(t, 1600, 400)))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if thumb.Width != MaxWidth {
		t.Errorf("width = %d, want the box width %d", thumb.Width, MaxWidth)
	}
	if thumb.Height != 120 {
		t.Errorf("height = %d, want 120 (a 4:1 image scaled to %d wide)", thumb.Height, MaxWidth)
	}
	if thumb.Height > MaxHeight {
		t.Errorf("height %d escaped the bounding box", thumb.Height)
	}
	if thumb.SourceWidth != 1600 || thumb.SourceHeight != 400 {
		t.Errorf("source dimensions = %dx%d, want 1600x400", thumb.SourceWidth, thumb.SourceHeight)
	}
}

func TestATallImageIsConstrainedByItsHeight(t *testing.T) {
	thumb, err := Generate(bytes.NewReader(pngOf(t, 300, 1200)))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if thumb.Height != MaxHeight {
		t.Errorf("height = %d, want the box height %d", thumb.Height, MaxHeight)
	}
	if thumb.Width != 120 {
		t.Errorf("width = %d, want 120", thumb.Width)
	}
}

// A thumbnail larger than the original would be more bytes and no more detail.
func TestASmallImageIsNotEnlarged(t *testing.T) {
	thumb, err := Generate(bytes.NewReader(pngOf(t, 64, 48)))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if thumb.Width != 64 || thumb.Height != 48 {
		t.Errorf("got %dx%d, want the original 64x48", thumb.Width, thumb.Height)
	}
}

func TestTheOutputIsAlwaysJPEGWhateverWentIn(t *testing.T) {
	thumb, err := Generate(bytes.NewReader(pngOf(t, 800, 600)))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(thumb.Data)); err != nil {
		t.Fatalf("the output does not decode as JPEG: %v", err)
	}
	// Serving it as anything else would let a browser sniff it.
	if ContentType != "image/jpeg" {
		t.Errorf("ContentType = %q, want image/jpeg", ContentType)
	}
}

// The important one: a small file that declares an enormous canvas must be
// refused from its header, before anything allocates pixels for it.
func TestADecompressionBombIsRejectedFromItsHeaderAlone(t *testing.T) {
	// A PNG header claiming 40000x40000 — 1.6 billion pixels, which as RGBA
	// would ask for over 6 GB on a board that has four.
	bomb := forgedPNGHeader(t, 40000, 40000)

	_, err := Generate(bytes.NewReader(bomb))
	if err == nil {
		t.Fatal("Generate accepted an image far beyond the pixel limit")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %v, want it to name the size limit", err)
	}
}

func TestSomethingThatIsNotAnImageIsUnsupportedRatherThanFatal(t *testing.T) {
	_, err := Generate(strings.NewReader("This is a text file pretending to be a PNG."))
	if err == nil {
		t.Fatal("Generate accepted a text file")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error = %v, want an unsupported-format error", err)
	}
}

func TestAnEmptyFileIsUnsupported(t *testing.T) {
	if _, err := Generate(bytes.NewReader(nil)); err == nil {
		t.Fatal("Generate accepted an empty file")
	}
}

// Reading an unbounded amount into memory is how a large upload becomes an
// out-of-memory kill on a Pi.
func TestASourceLargerThanTheLimitIsRefusedWithoutBeingRead(t *testing.T) {
	oversized := bytes.NewReader(make([]byte, MaxSourceBytes+1024))

	_, err := Generate(oversized)
	if err == nil {
		t.Fatal("Generate accepted a source past MaxSourceBytes")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %v, want it to name the size limit", err)
	}
}

func TestSupportableJudgesTheServersOwnTypeNotTheClientsClaim(t *testing.T) {
	cases := map[string]bool{
		"image/jpeg":               true,
		"image/png":                true,
		"IMAGE/PNG":                true,
		"image/jpeg; charset=utf8": true,
		"image/webp":               true,
		"image/svg+xml":            false, // SVG is script, not a raster image.
		"application/pdf":          false,
		"video/mp4":                false,
		"":                         false,
	}
	for mimeType, want := range cases {
		if got := Supportable(mimeType); got != want {
			t.Errorf("Supportable(%q) = %v, want %v", mimeType, got, want)
		}
	}
}

func TestAnExtremeAspectRatioNeverRoundsADimensionToZero(t *testing.T) {
	// 5000x1 would scale to 480x0.096, which truncates to zero and makes a
	// rectangle no encoder accepts.
	w, h := fitWithin(5000, 1, MaxWidth, MaxHeight)
	if w < 1 || h < 1 {
		t.Errorf("fitWithin(5000, 1) = %dx%d, want both at least 1", w, h)
	}
}

// forgedPNGHeader builds a valid PNG signature and IHDR declaring the given
// dimensions, with no image data. DecodeConfig reads only this far.
func forgedPNGHeader(t *testing.T, width, height uint32) []byte {
	t.Helper()

	// Encode a real 1x1 PNG, then overwrite the dimensions in its IHDR and fix
	// the chunk's CRC. Hand-assembling the whole file would be more code and
	// less obviously correct.
	base := pngOf(t, 1, 1)

	out := append([]byte(nil), base...)
	// Signature is 8 bytes; then a 4-byte length, "IHDR", then width and height.
	const ihdrDataStart = 8 + 4 + 4
	putUint32(out[ihdrDataStart:], width)
	putUint32(out[ihdrDataStart+4:], height)

	// The CRC covers the chunk type and data: 4 + 13 bytes from "IHDR".
	const crcStart = 8 + 4
	crc := crc32IEEE(out[crcStart : crcStart+4+13])
	putUint32(out[crcStart+4+13:], crc)

	return out
}

func putUint32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func crc32IEEE(data []byte) uint32 {
	return crc32.Checksum(data, crc32.MakeTable(crc32.IEEE))
}
