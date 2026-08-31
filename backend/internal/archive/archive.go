// Package archive streams folders and multi-file selections as ZIP.
//
// Streaming, never staging. Building the archive in a temporary file first
// would need as much free disk as the archive is large, and on a Raspberry Pi
// with a modest SSD that is exactly the case that fails. Instead entries are
// written straight to the response as they are read, so the peak memory cost
// is one 256 KB buffer regardless of whether the folder holds 10 MB or 200 GB.
//
// The consequence is that the total size cannot be known in advance, so no
// Content-Length is sent and the browser shows an indeterminate progress bar.
// That is the right trade: a progress bar is worth less than the ability to
// download a folder larger than the free space on the server.
package archive

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/nodes"
	"github.com/joshuanmoses/piCloud/backend/internal/permissions"
	"github.com/joshuanmoses/piCloud/backend/internal/storage"
)

// Limits on what one archive may contain. A request that would exceed either is
// refused up front rather than half-served: a truncated ZIP is worse than a
// clear refusal, because the user does not find out until they try to open it.
const (
	// MaxEntries caps the number of files in one archive.
	MaxEntries = 10_000
	// MaxDepth caps how deep the walk descends. Structurally the tree cannot
	// contain a cycle, but a bound costs nothing and turns a corrupted parent
	// pointer into an error rather than an endless download.
	MaxDepth = 64
)

// ErrTooManyEntries means the selection exceeds MaxEntries.
var ErrTooManyEntries = fmt.Errorf("archive: more than %d files", MaxEntries)

// ErrEmpty means the selection contains nothing that can be archived.
var ErrEmpty = errors.New("archive: nothing to archive")

// ErrDenied means the caller may not download the requested item.
//
// The API layer checks permission before it gets here; this is the second
// check, and it exists because an archive walk reads content directly from
// storage and a missed check would hand over a whole subtree.
var ErrDenied = errors.New("archive: not permitted")

// Builder streams ZIP archives.
type Builder struct {
	nodes   *nodes.Store
	perms   *permissions.Store
	storage *storage.Storage
	logger  *slog.Logger
}

// NewBuilder wires the parts together.
func NewBuilder(nodeStore *nodes.Store, perms *permissions.Store, st *storage.Storage, logger *slog.Logger) *Builder {
	if logger == nil {
		logger = slog.Default()
	}
	return &Builder{nodes: nodeStore, perms: perms, storage: st, logger: logger.With("component", "archive")}
}

// Result reports what an archive run produced.
type Result struct {
	Files      int
	Folders    int
	Bytes      int64
	Skipped    int
	Incomplete bool
}

// WriteFolder streams a folder and everything beneath it.
//
// Permission is checked once on the folder and then inherited by the walk,
// which mirrors how the permission model itself works: a grant on a folder
// covers its subtree. Every node visited is a descendant of a folder the caller
// may read, so no descendant needs its own check.
func (b *Builder) WriteFolder(ctx context.Context, w io.Writer, folderID, userID uuid.UUID) (*Result, error) {
	folder, err := b.nodes.Get(ctx, folderID)
	if err != nil {
		return nil, err
	}
	if folder.Kind != nodes.KindFolder {
		return nil, nodes.ErrNotAFolder
	}

	decision, err := b.perms.Evaluate(ctx, folderID, userID)
	if err != nil {
		return nil, err
	}
	if !decision.Allows(permissions.Read | permissions.Download) {
		return nil, ErrDenied
	}

	zw := zip.NewWriter(w)
	result := &Result{}
	buf := make([]byte, storage.CopyBufferSize)

	// The folder's own name is the archive's top-level directory, so extracting
	// produces one folder rather than scattering its contents.
	//
	// A root folder is the exception: it has no name — "My Files" is a label
	// the interface applies, not something stored — so wrapping in it would
	// produce a directory called "unnamed". Its children go at the top level
	// instead, which is what "download everything" should give you.
	if folder.ParentID == nil {
		err = b.walkFolderContents(ctx, zw, folder, "", 0, result, buf)
	} else {
		err = b.walk(ctx, zw, folder, sanitizeSegment(folder.Name), 0, result, buf)
	}
	if err != nil {
		// The header is already on the wire, so the response cannot become an
		// error. Close what we can and report upward; the handler logs it and
		// the client gets a truncated archive it will refuse to open — which is
		// the correct signal that the download failed.
		_ = zw.Close()
		return result, err
	}

	if err := zw.Close(); err != nil {
		return result, fmt.Errorf("archive: cannot finalize: %w", err)
	}
	return result, nil
}

// WriteSelection streams an explicit list of nodes.
//
// Unlike a folder walk, each top-level selection is checked individually: the
// client chose these, and there is no single ancestor whose grant covers them
// all. A node the caller cannot read is skipped silently rather than refused,
// so a selection that includes something newly unshared still delivers the rest
// — and the skip count tells the handler to say so.
func (b *Builder) WriteSelection(ctx context.Context, w io.Writer, nodeIDs []uuid.UUID, userID uuid.UUID) (*Result, error) {
	if len(nodeIDs) == 0 {
		return nil, ErrEmpty
	}
	if len(nodeIDs) > MaxEntries {
		return nil, ErrTooManyEntries
	}

	decisions, err := b.perms.EvaluateMany(ctx, nodeIDs, userID)
	if err != nil {
		return nil, err
	}

	// Resolve first, so a selection where nothing is readable fails before the
	// response headers are written and can still be a clean 404.
	type entry struct {
		node *nodes.Node
		name string
	}
	var entries []entry
	used := make(map[string]int, len(nodeIDs))
	skipped := 0

	for _, id := range nodeIDs {
		decision, ok := decisions[id]
		if !ok || !decision.Allows(permissions.Read|permissions.Download) {
			skipped++
			continue
		}
		node, err := b.nodes.Get(ctx, id)
		if err != nil {
			if errors.Is(err, nodes.ErrNotFound) {
				skipped++
				continue
			}
			return nil, err
		}
		entries = append(entries, entry{node: node, name: uniqueName(used, sanitizeSegment(node.Name))})
	}

	if len(entries) == 0 {
		return nil, ErrEmpty
	}

	zw := zip.NewWriter(w)
	result := &Result{Skipped: skipped}
	buf := make([]byte, storage.CopyBufferSize)

	for _, e := range entries {
		if err := b.walk(ctx, zw, e.node, e.name, 0, result, buf); err != nil {
			_ = zw.Close()
			return result, err
		}
	}

	if err := zw.Close(); err != nil {
		return result, fmt.Errorf("archive: cannot finalize: %w", err)
	}
	return result, nil
}

// walk adds one node — and, for a folder, everything beneath it — under
// archivePath.
func (b *Builder) walk(
	ctx context.Context,
	zw *zip.Writer,
	node *nodes.Node,
	archivePath string,
	depth int,
	result *Result,
	buf []byte,
) error {
	if err := ctx.Err(); err != nil {
		// The client hung up, or the request deadline passed. Stop reading
		// from disk immediately rather than finishing an archive nobody will
		// receive.
		return err
	}
	if depth > MaxDepth {
		return fmt.Errorf("archive: folder nesting exceeds %d levels", MaxDepth)
	}
	if result.Files+result.Folders > MaxEntries {
		return ErrTooManyEntries
	}

	if node.Kind == nodes.KindFolder {
		return b.walkFolder(ctx, zw, node, archivePath, depth, result, buf)
	}
	return b.writeFile(ctx, zw, node, archivePath, result, buf)
}

func (b *Builder) walkFolder(
	ctx context.Context,
	zw *zip.Writer,
	folder *nodes.Node,
	archivePath string,
	depth int,
	result *Result,
	buf []byte,
) error {
	// An explicit directory entry, so an empty folder survives the round trip
	// instead of vanishing from the extracted tree.
	header := &zip.FileHeader{
		Name:     archivePath + "/",
		Method:   zip.Store,
		Modified: folder.UpdatedAt,
	}
	header.SetMode(0o755)
	if _, err := zw.CreateHeader(header); err != nil {
		return fmt.Errorf("archive: cannot add folder entry: %w", err)
	}
	result.Folders++

	return b.walkFolderContents(ctx, zw, folder, archivePath, depth, result, buf)
}

// walkFolderContents adds a folder's children under prefix, without writing an
// entry for the folder itself.
//
// An empty prefix places them at the top level of the archive, which is how a
// root folder — whose name is not a name — is exported.
func (b *Builder) walkFolderContents(
	ctx context.Context,
	zw *zip.Writer,
	folder *nodes.Node,
	prefix string,
	depth int,
	result *Result,
	buf []byte,
) error {
	// Paged, so a folder with 50,000 children is streamed rather than loaded.
	used := make(map[string]int)
	offset := 0
	for {
		page, err := b.nodes.List(ctx, nodes.ListParams{
			ParentID: folder.ID,
			Limit:    nodes.MaxPageSize,
			Offset:   offset,
			Sort:     nodes.SortByName,
		})
		if err != nil {
			return err
		}
		if len(page.Nodes) == 0 {
			return nil
		}

		for _, child := range page.Nodes {
			name := uniqueName(used, sanitizeSegment(child.Name))
			childPath := name
			if prefix != "" {
				childPath = prefix + "/" + name
			}
			if err := b.walk(ctx, zw, child, childPath, depth+1, result, buf); err != nil {
				return err
			}
		}

		if !page.HasMore {
			return nil
		}
		offset += len(page.Nodes)
	}
}

// writeFile copies one file's content into the archive.
func (b *Builder) writeFile(
	ctx context.Context,
	zw *zip.Writer,
	node *nodes.Node,
	archivePath string,
	result *Result,
	buf []byte,
) error {
	if node.ContentHash == nil || *node.ContentHash == "" {
		// A file row with no content: an upload that never completed. Skip it
		// rather than fail the whole archive.
		result.Skipped++
		result.Incomplete = true
		return nil
	}

	src, err := b.storage.OpenBlob(*node.ContentHash)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// Metadata without content. Real, and worth an operator's
			// attention, but not a reason to abandon the other 999 files.
			b.logger.Error("archive: file content is missing from storage",
				"node_id", node.ID, "content", (*node.ContentHash)[:12])
			result.Skipped++
			result.Incomplete = true
			return nil
		}
		return err
	}
	defer src.Close()

	header := &zip.FileHeader{
		Name:     archivePath,
		Modified: node.UpdatedAt,
		Method:   compressionFor(node),
	}
	header.SetMode(0o644)
	// Tell the reader the uncompressed size up front where we know it, so a
	// streaming extractor can show progress per file.
	header.UncompressedSize64 = uint64(node.SizeBytes)

	entry, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("archive: cannot add file entry: %w", err)
	}

	written, err := io.CopyBuffer(entry, src, buf)
	if err != nil {
		return fmt.Errorf("archive: cannot copy %s: %w", node.ID, err)
	}

	result.Files++
	result.Bytes += written
	return nil
}

// compressionFor decides whether to spend CPU compressing an entry.
//
// Deflating a JPEG or an MP4 saves a percent or two and costs seconds of ARM
// CPU per file. Storing them uncompressed makes a photo folder download several
// times faster, which on this hardware is the difference that matters.
func compressionFor(node *nodes.Node) uint16 { return compressionForMIME(node.MimeType) }

// compressionForMIME is the decision itself, split out so it is testable
// without constructing a node.
func compressionForMIME(mimeType string) uint16 {
	base, _, _ := strings.Cut(mimeType, ";")
	base = strings.ToLower(strings.TrimSpace(base))

	switch {
	case strings.HasPrefix(base, "image/") && base != "image/bmp" && base != "image/tiff":
		return storeMethod
	case strings.HasPrefix(base, "video/"), strings.HasPrefix(base, "audio/"):
		return storeMethod
	case base == "application/zip", base == "application/gzip", base == "application/x-7z-compressed",
		base == "application/x-rar-compressed", base == "application/x-bzip2", base == "application/x-xz":
		return storeMethod
	case base == "application/pdf":
		// PDFs vary: text-heavy ones compress well, image-heavy ones do not.
		// Deflate is the better bet on average.
		return deflateMethod
	}
	return deflateMethod
}

// The two compression methods, named so the choice reads as a decision rather
// than as two constants from the zip package.
const (
	storeMethod   = zip.Store
	deflateMethod = zip.Deflate
)

// sanitizeSegment makes one path component safe to extract.
//
// A ZIP entry name is attacker-controlled data as far as the *extracting*
// machine is concerned: "../../.bashrc" in an entry name is the Zip Slip
// vulnerability, and while PiFiles validates names on the way in, an archive
// built here may be extracted by any tool anywhere. Stripping separators and
// dot segments here means an archive from this server cannot be the vector even
// if a bad name somehow reached the database.
func sanitizeSegment(name string) string {
	// Strip anything that could be read as a separator or a drive letter.
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\x00", "")

	// Control characters confuse extractors and terminals both.
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '_'
		}
		return r
	}, name)

	name = strings.TrimSpace(name)
	// A leading dot segment, or a name that is nothing but dots.
	if name == "" || strings.Trim(name, ".") == "" {
		return "unnamed"
	}
	// Belt and braces: whatever path.Clean makes of it, take only the last
	// element and confirm it is not a traversal.
	cleaned := path.Base(path.Clean(name))
	if cleaned == "." || cleaned == ".." || cleaned == "/" {
		return "unnamed"
	}
	return cleaned
}

// uniqueName disambiguates entries that would otherwise collide.
//
// Two siblings in one folder cannot collide — the database forbids names that
// differ only by case. Two collisions remain possible and both matter: a
// *selection* archive puts items from different folders side by side at the top
// level, and sanitizeSegment can map two distinct names onto one. An archive
// containing both would extract with one silently overwriting the other.
// Suffixing preserves the data, which is the point of the download.
func uniqueName(used map[string]int, name string) string {
	key := strings.ToLower(name)
	n, seen := used[key]
	if !seen {
		used[key] = 1
		return name
	}

	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for {
		n++
		candidate := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if _, taken := used[strings.ToLower(candidate)]; !taken {
			used[key] = n
			used[strings.ToLower(candidate)] = 1
			return candidate
		}
	}
}

// FileName derives the archive's download name from what is being archived.
func FileName(base string, multiple bool) string {
	if multiple || base == "" {
		return fmt.Sprintf("pifiles-%s.zip", time.Now().Format("2006-01-02"))
	}
	return sanitizeSegment(base) + ".zip"
}
