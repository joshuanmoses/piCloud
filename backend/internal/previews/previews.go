// Package previews generates and serves image thumbnails.
//
// Only raster images are handled here, and deliberately so. PDFs, text,
// Markdown, audio and video are previewed in the browser from the ordinary
// download endpoint, which costs the server nothing; rasterising a PDF or
// transcoding a video on a Raspberry Pi would cost far more than it is worth
// and would drag in a large native toolchain besides.
//
// Generation runs in the background queue rather than on the request path. A
// 40-megapixel photograph takes seconds to decode on an ARM board, and no
// upload should wait for that.
package previews

import (
	"errors"
	"strings"
)

// Errors returned by this package.
var (
	// ErrNotFound means no preview record exists.
	ErrNotFound = errors.New("previews: not found")
	// ErrUnsupported means the file is not a format previews can render. It is
	// a permanent condition: retrying will not change the format.
	ErrUnsupported = errors.New("previews: unsupported format")
	// ErrTooLarge means the image exceeds the decode limits below.
	ErrTooLarge = errors.New("previews: image is too large to render safely")
)

// Status is the state of a node's preview.
type Status string

const (
	// StatusPending means generation is queued or in progress.
	StatusPending Status = "pending"
	// StatusReady means a thumbnail is on disk.
	StatusReady Status = "ready"
	// StatusUnavailable means no thumbnail can be made. Recorded so a file
	// that cannot be rendered is not re-queued on every listing.
	StatusUnavailable Status = "unavailable"
)

// Thumbnail dimensions and limits.
const (
	// MaxWidth and MaxHeight bound the generated thumbnail. The aspect ratio is
	// preserved, so this is a bounding box rather than a target size.
	MaxWidth  = 480
	MaxHeight = 480

	// MaxSourceBytes caps the size of a file we will attempt to decode. Beyond
	// this the decode cost stops being worth a thumbnail on this hardware.
	MaxSourceBytes = 50 << 20 // 50 MiB

	// MaxSourcePixels caps the decoded dimensions. This is the real defence:
	// a 100 KB "decompression bomb" PNG can declare a 30000x30000 canvas, which
	// would ask for 3.6 GB of memory on a board that has 4. The dimensions are
	// read from the header before any pixels are decoded.
	MaxSourcePixels = 80_000_000 // 80 megapixels

	// JPEGQuality trades a little fidelity for a much smaller thumbnail. At
	// this size the difference is not visible.
	JPEGQuality = 80
)

// ContentType is the MIME type of every generated thumbnail. Always JPEG,
// whatever the source: one format keeps serving simple, and JPEG is the
// smallest of the universally supported options at thumbnail sizes.
const ContentType = "image/jpeg"

// Supportable reports whether a MIME type is one we can generate a thumbnail
// for.
//
// The MIME type here is the server's own sniffed value, never the client's
// claim. A file that lies about being a PNG simply fails to decode, which is
// recorded as unavailable — it is not executed, and it is not trusted.
func Supportable(mimeType string) bool {
	switch normalizeMIME(mimeType) {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp", "image/tiff":
		return true
	}
	return false
}

// normalizeMIME strips parameters and case from a MIME type.
func normalizeMIME(mimeType string) string {
	base, _, _ := strings.Cut(mimeType, ";")
	return strings.ToLower(strings.TrimSpace(base))
}
