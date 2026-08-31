package previews

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"

	// Registered for their decoders. These are the formats Supportable admits.
	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Thumbnail is a generated preview image.
type Thumbnail struct {
	// Data is the encoded JPEG.
	Data []byte
	// Width and Height are the thumbnail's dimensions, not the source's.
	Width  int
	Height int
	// SourceWidth and SourceHeight are the original's, kept so the interface
	// can show them and so an already-small image is not upscaled twice.
	SourceWidth  int
	SourceHeight int
}

// Generate decodes an image and returns a bounded JPEG thumbnail.
//
// r is read at most MaxSourceBytes; a file larger than that is refused rather
// than streamed into memory. The dimensions are checked from the header before
// the pixels are decoded, so a malicious image cannot exhaust memory by
// declaring an enormous canvas.
func Generate(r io.Reader) (*Thumbnail, error) {
	// Cap the read. One extra byte, so overflow is detectable rather than
	// silently producing a truncated image that fails to decode confusingly.
	limited := io.LimitReader(r, MaxSourceBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("previews: cannot read source: %w", err)
	}
	if len(buf) > MaxSourceBytes {
		return nil, fmt.Errorf("%w: source exceeds %d bytes", ErrTooLarge, MaxSourceBytes)
	}
	if len(buf) == 0 {
		return nil, fmt.Errorf("%w: empty file", ErrUnsupported)
	}

	// Read the header only. DecodeConfig allocates nothing proportional to the
	// image, so a bomb is rejected before it can cost anything.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("%w: %s reports zero dimensions", ErrUnsupported, format)
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxSourcePixels {
		return nil, fmt.Errorf("%w: %dx%d exceeds %d pixels",
			ErrTooLarge, cfg.Width, cfg.Height, MaxSourcePixels)
	}

	src, _, err := image.Decode(bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}

	w, h := fitWithin(cfg.Width, cfg.Height, MaxWidth, MaxHeight)

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// CatmullRom is slower than the alternatives but visibly better on
	// photographs, and this runs in the background where the extra tens of
	// milliseconds cost nobody anything.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: JPEGQuality}); err != nil {
		return nil, fmt.Errorf("previews: cannot encode thumbnail: %w", err)
	}

	return &Thumbnail{
		Data:         out.Bytes(),
		Width:        w,
		Height:       h,
		SourceWidth:  cfg.Width,
		SourceHeight: cfg.Height,
	}, nil
}

// fitWithin scales (w, h) to fit inside (maxW, maxH), preserving the aspect
// ratio and never enlarging. An image already smaller than the box is copied at
// its own size: upscaling adds bytes and no detail.
func fitWithin(w, h, maxW, maxH int) (int, int) {
	if w <= maxW && h <= maxH {
		return w, h
	}

	// Compare cross-multiplied rather than in floating point, so the choice of
	// limiting dimension is exact.
	if int64(w)*int64(maxH) > int64(h)*int64(maxW) {
		// Width is the constraint.
		scaled := int(int64(h) * int64(maxW) / int64(w))
		return maxW, atLeastOne(scaled)
	}
	scaled := int(int64(w) * int64(maxH) / int64(h))
	return atLeastOne(scaled), maxH
}

// atLeastOne keeps an extreme aspect ratio from rounding a dimension to zero,
// which would make an image.Rect that no encoder accepts.
func atLeastOne(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
