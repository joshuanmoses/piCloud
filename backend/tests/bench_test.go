package tests

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/joshuanmoses/piCloud/backend/internal/search"
)

// Benchmarks for the paths that decide whether PiFiles is usable on a
// Raspberry Pi.
//
// These run against a real PostgreSQL, so the numbers include the round trip —
// which is the point. A microbenchmark of the Go code alone would measure the
// part that was never in question.
//
// The machine running them is not a Pi, so the absolute figures are optimistic.
// What they are good for is *ratios* and *shape*: whether a listing is one
// query or N, whether a deep tree costs more than a shallow one, and whether
// anything is accidentally quadratic. Those hold across hardware; the constants
// do not.
//
//	make bench

// BenchmarkFolderListing measures the single hottest query in the product.
func BenchmarkFolderListing(b *testing.B) {
	for _, size := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("%d_children", size), func(b *testing.B) {
			h := newHarness(b)
			c := h.signedIn("josh")

			folder := c.mkdir("root", "Folder")
			for i := range size {
				c.mustUpload(folder.String(), fmt.Sprintf("file-%04d.txt", i), []byte("x"))
			}

			target := "/api/v1/nodes?parent_id=" + folder.String() + "&limit=500"
			b.ResetTimer()

			for b.Loop() {
				rec := c.do(http.MethodGet, target, nil)
				if rec.Code != http.StatusOK {
					b.Fatalf("listing failed: %d", rec.Code)
				}
			}
		})
	}
}

// BenchmarkPermissionEvaluation measures the recursive ancestry walk at
// several depths.
//
// This is the query BUILD_STATUS flagged as "the first thing to measure": every
// content request runs it, and it recurses up the tree. If cost grows sharply
// with depth, a deeply nested library is the case that suffers.
func BenchmarkPermissionEvaluation(b *testing.B) {
	for _, depth := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("depth_%d", depth), func(b *testing.B) {
			h := newHarness(b)
			c := h.signedIn("josh")

			parent := "root"
			var deepest uuid.UUID
			for i := range depth {
				deepest = c.mkdir(parent, fmt.Sprintf("level-%d", i))
				parent = deepest.String()
			}

			user := h.mustUser("josh")
			b.ResetTimer()

			for b.Loop() {
				if _, err := h.permissions.Evaluate(b.Context(), deepest, user.ID); err != nil {
					b.Fatalf("Evaluate: %v", err)
				}
			}
		})
	}
}

// BenchmarkBatchedPermissionEvaluation confirms a listing costs one query
// rather than one per row.
//
// The shape is what matters: if per-item cost is flat as the batch grows, the
// batching is real. If it climbs, something is evaluating per row.
func BenchmarkBatchedPermissionEvaluation(b *testing.B) {
	for _, count := range []int{10, 100} {
		b.Run(fmt.Sprintf("%d_items", count), func(b *testing.B) {
			h := newHarness(b)
			c := h.signedIn("josh")

			folder := c.mkdir("root", "Folder")
			ids := make([]uuid.UUID, 0, count)
			for i := range count {
				ids = append(ids, c.mkdir(folder.String(), fmt.Sprintf("child-%d", i)))
			}

			user := h.mustUser("josh")
			b.ResetTimer()

			for b.Loop() {
				if _, err := h.permissions.EvaluateMany(b.Context(), ids, user.ID); err != nil {
					b.Fatalf("EvaluateMany: %v", err)
				}
			}
			b.ReportMetric(float64(count), "items/op")
		})
	}
}

// BenchmarkSearch measures the trigram scan, which is the most expensive read
// the API offers and the reason it has its own rate-limit budget.
func BenchmarkSearch(b *testing.B) {
	h := newHarness(b)
	c := h.signedIn("josh")

	folder := c.mkdir("root", "Library")
	for i := range 500 {
		c.mustUpload(folder.String(), fmt.Sprintf("Quarterly Report %04d.pdf", i), []byte("x"))
	}

	user := h.mustUser("josh")
	backend := search.NewPostgres(h.db)
	b.ResetTimer()

	for b.Loop() {
		results, err := backend.Search(b.Context(), search.Query{
			Text: "quarterly", UserID: user.ID, Limit: 50,
		})
		if err != nil {
			b.Fatalf("Search: %v", err)
		}
		if results.Total == 0 {
			b.Fatal("the search matched nothing; the benchmark is measuring the wrong thing")
		}
	}
}

// BenchmarkUpload measures the whole write path: hash, write, commit, and the
// metadata transaction.
//
// Sizes span the boundary where hashing starts to dominate. A Pi 4 hashes
// SHA-256 at roughly 200 MB/s per core, so the ratio between these tells you
// whether the fixed costs or the bytes are in charge.
func BenchmarkUpload(b *testing.B) {
	for _, size := range []int{1 << 10, 1 << 20, 8 << 20} {
		b.Run(formatBenchSize(size), func(b *testing.B) {
			h := newHarness(b)
			c := h.signedIn("josh")

			// Distinct content per iteration, or deduplication would make this
			// a benchmark of the dedupe path rather than of an upload.
			payload := bytes.Repeat([]byte("pifiles benchmark payload "), size/26+1)[:size]

			b.SetBytes(int64(size))
			b.ResetTimer()

			i := 0
			for b.Loop() {
				i++
				payload[0] = byte(i)
				payload[1] = byte(i >> 8)

				rec := c.raw(http.MethodPost,
					fmt.Sprintf("/api/v1/files?name=bench-%d.bin&parent_id=root", i),
					"application/octet-stream", bytes.NewReader(payload))
				if rec.Code != http.StatusCreated {
					b.Fatalf("upload failed: %d %s", rec.Code, rec.Body)
				}
			}
		})
	}
}

// BenchmarkDownload measures the read path, which is dominated by the fixed
// 256 KB copy buffer rather than by anything PiFiles computes.
func BenchmarkDownload(b *testing.B) {
	h := newHarness(b)
	c := h.signedIn("josh")

	const size = 4 << 20
	payload := bytes.Repeat([]byte("x"), size)
	id := c.mustUpload("root", "large.bin", payload)

	target := "/api/v1/nodes/" + id.String() + "/download"
	b.SetBytes(size)
	b.ResetTimer()

	for b.Loop() {
		rec := c.do(http.MethodGet, target, nil)
		if rec.Code != http.StatusOK {
			b.Fatalf("download failed: %d", rec.Code)
		}
	}
}

// BenchmarkArchive measures the streaming ZIP walk over a realistic folder.
func BenchmarkArchive(b *testing.B) {
	h := newHarness(b)
	c := h.signedIn("josh")

	folder := c.mkdir("root", "Photos")
	payload := bytes.Repeat([]byte("y"), 32<<10)
	for i := range 100 {
		content := append([]byte(fmt.Sprintf("%04d", i)), payload...)
		c.mustUpload(folder.String(), fmt.Sprintf("photo-%04d.jpg", i), content)
	}

	target := "/api/v1/nodes/" + folder.String() + "/archive"
	b.ResetTimer()

	for b.Loop() {
		rec := c.do(http.MethodGet, target, nil)
		if rec.Code != http.StatusOK {
			b.Fatalf("archive failed: %d", rec.Code)
		}
	}
}

// --- Bench helpers -------------------------------------------------------------

// mustUpload stores a file and returns its identifier, failing the caller if
// the upload does not succeed.
//
// The ordinary upload helper returns the recorder so a test can assert on a
// refusal; a benchmark only ever wants the happy path, and checking the code at
// every call site would bury the thing being measured.
func (c *client) mustUpload(parentID, name string, content []byte) uuid.UUID {
	c.t.Helper()
	id, rec := c.upload(parentID, name, content)
	if rec != nil && rec.Code != http.StatusCreated {
		c.t.Fatalf("upload %q: %d %s", name, rec.Code, rec.Body)
	}
	return id
}

// formatBenchSize names an upload size for the sub-benchmark.
func formatBenchSize(bytes int) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%dMiB", bytes>>20)
	case bytes >= 1<<10:
		return fmt.Sprintf("%dKiB", bytes>>10)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
