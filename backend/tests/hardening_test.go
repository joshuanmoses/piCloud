package tests

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshuanmoses/piCloud/backend/internal/logging"
	"github.com/joshuanmoses/piCloud/backend/internal/malware"
)

// Phase 10: hardening.
//
// Two additions, and the tests are shaped by what each is actually for. Rate
// limiting protects the *server* from a caller who has already authenticated —
// account lockout has protected the account since Phase 2, and this is the
// other half. Malware scanning protects everyone downstream from content the
// uploader may not know is hostile.

// --- Rate limiting ------------------------------------------------------------

// The limit must refuse rather than degrade. A server that slows down under
// load and never says no is a server that eventually falls over.
func TestExceedingAWriteLimitIsRefusedWithAnInstruction(t *testing.T) {
	h := newHarnessWithEnv(t, map[string]string{"RATE_LIMIT_WRITE": "3"})
	c := h.signedIn("josh")

	for i := range 3 {
		rec := c.do(http.MethodPost, "/api/v1/nodes/folders",
			map[string]any{"parent_id": "root", "name": fmt.Sprintf("Folder %d", i)})
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d returned %d, want 201: %s", i, rec.Code, rec.Body)
		}
	}

	rec := c.do(http.MethodPost, "/api/v1/nodes/folders",
		map[string]any{"parent_id": "root", "name": "One too many"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body)
	}

	// Retry-After turns a wall into an instruction: a well-behaved client
	// waits rather than retrying immediately and making it worse.
	retry := rec.Header().Get("Retry-After")
	if retry == "" {
		t.Error("no Retry-After header on a rate-limited response")
	} else if seconds, err := strconv.Atoi(retry); err != nil || seconds < 1 {
		t.Errorf("Retry-After = %q, want a positive number of seconds", retry)
	}

	// And the folder was genuinely not created.
	if names := c.listNames(""); len(names) != 3 {
		t.Errorf("got %d folders, want 3 — the refused request created one anyway", len(names))
	}
}

// The budgets are independent on purpose: exhausting one must not block work
// that costs something entirely different.
func TestExhaustingOneBudgetDoesNotBlockAnother(t *testing.T) {
	h := newHarnessWithEnv(t, map[string]string{
		"RATE_LIMIT_WRITE": "2",
		"RATE_LIMIT_READ":  "100000",
	})
	c := h.signedIn("josh")

	c.mkdir("root", "First")
	c.mkdir("root", "Second")

	// The write budget is now spent.
	if rec := c.do(http.MethodPost, "/api/v1/nodes/folders",
		map[string]any{"parent_id": "root", "name": "Third"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the third write returned %d, want 429", rec.Code)
	}

	// Reading is unaffected.
	for range 5 {
		if rec := c.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusOK {
			t.Fatalf("a read returned %d after the write budget was spent: %s", rec.Code, rec.Body)
		}
	}
}

// Keyed on the account rather than the address, because a household behind one
// NAT address shares an IP and limiting them collectively would punish the
// family for one person's sync client.
func TestOneUsersLimitDoesNotThrottleAnother(t *testing.T) {
	h := newHarnessWithEnv(t, map[string]string{"RATE_LIMIT_WRITE": "2"})
	first := h.signedIn("josh")
	second := h.signedIn("ana")

	first.mkdir("root", "One")
	first.mkdir("root", "Two")
	if rec := first.do(http.MethodPost, "/api/v1/nodes/folders",
		map[string]any{"parent_id": "root", "name": "Three"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the first user was not limited: %d", rec.Code)
	}

	// The second user — same test client address — is unaffected.
	rec := second.do(http.MethodPost, "/api/v1/nodes/folders",
		map[string]any{"parent_id": "root", "name": "Mine"})
	if rec.Code != http.StatusCreated {
		t.Errorf("the second user was throttled by the first user's activity: %d %s",
			rec.Code, rec.Body)
	}
}

// Search is the most expensive read the API offers and gets its own budget,
// so a search-as-you-type client cannot exhaust the one a listing needs.
func TestSearchHasItsOwnBudget(t *testing.T) {
	h := newHarnessWithEnv(t, map[string]string{
		"RATE_LIMIT_SEARCH": "2",
		"RATE_LIMIT_READ":   "100000",
	})
	c := h.signedIn("josh")
	c.upload("root", "Report.txt", []byte("x"))

	c.do(http.MethodGet, "/api/v1/search?q=report", nil)
	c.do(http.MethodGet, "/api/v1/search?q=report", nil)

	if rec := c.do(http.MethodGet, "/api/v1/search?q=report", nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the third search returned %d, want 429: %s", rec.Code, rec.Body)
	}
	// Listing still works.
	if rec := c.do(http.MethodGet, "/api/v1/nodes", nil); rec.Code != http.StatusOK {
		t.Errorf("listing was blocked by the search budget: %d", rec.Code)
	}
}

// The portals are the only endpoints a stranger can reach at all, and they have
// no account to key on.
func TestTheAnonymousPortalIsLimitedByAddress(t *testing.T) {
	h := newHarnessWithEnv(t, map[string]string{"RATE_LIMIT_ANONYMOUS": "3"})
	owner := h.signedIn("owner")
	folderID := owner.mkdir("root", "Published")

	rec := owner.do(http.MethodPost, "/api/v1/public-links", map[string]any{
		"node_id": folderID.String(), "kind": "download",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	var body struct {
		Link struct {
			URL string `json:"url"`
		} `json:"link"`
	}
	decodeInto(t, rec, &body)
	token := body.Link.URL[strings.LastIndex(body.Link.URL, "/")+1:]

	visitor := h.newVisitor()

	for i := range 3 {
		if rec := visitor.open(token); rec.Code != http.StatusOK {
			t.Fatalf("portal request %d returned %d: %s", i, rec.Code, rec.Body)
		}
	}
	if rec := visitor.open(token); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the fourth portal request returned %d, want 429: %s", rec.Code, rec.Body)
	}
}

// A misconfiguration must not become an outage. Zero means "no limit", never
// "refuse everything".
func TestALimitOfZeroSwitchesTheLimitOffRatherThanRefusingEverything(t *testing.T) {
	h := newHarnessWithEnv(t, map[string]string{"RATE_LIMIT_WRITE": "0"})
	c := h.signedIn("josh")

	for i := range 20 {
		rec := c.do(http.MethodPost, "/api/v1/nodes/folders",
			map[string]any{"parent_id": "root", "name": fmt.Sprintf("Folder %d", i)})
		if rec.Code != http.StatusCreated {
			t.Fatalf("request %d returned %d with the limit disabled: %s", i, rec.Code, rec.Body)
		}
	}
}

func TestRateLimitingCanBeSwitchedOffEntirely(t *testing.T) {
	h := newHarnessWithEnv(t, map[string]string{
		"RATE_LIMIT_ENABLED": "false",
		"RATE_LIMIT_WRITE":   "1",
	})
	c := h.signedIn("josh")

	c.mkdir("root", "One")
	if rec := c.do(http.MethodPost, "/api/v1/nodes/folders",
		map[string]any{"parent_id": "root", "name": "Two"}); rec.Code != http.StatusCreated {
		t.Errorf("a limit applied despite RATE_LIMIT_ENABLED=false: %d %s", rec.Code, rec.Body)
	}
}

// --- Response headers ---------------------------------------------------------

// The narrowing that matters: an injected <style> element can define rules
// matching anything on the page and exfiltrate attribute values through
// selector-driven requests. A style *attribute* can only affect the element it
// is already on, and React genuinely needs those.
func TestTheContentSecurityPolicyForbidsInjectedStyleElements(t *testing.T) {
	h := newHarness(t)
	c := h.newClient()

	rec := c.do(http.MethodGet, "/api/v1/config", nil)
	csp := rec.Header().Get("Content-Security-Policy")

	if strings.Contains(csp, "style-src 'self' 'unsafe-inline'") {
		t.Errorf("CSP still allows inline styles blanket-wide: %q", csp)
	}
	for _, directive := range []string{
		"style-src 'self'",
		"style-src-attr 'unsafe-inline'",
		"style-src-elem 'self'",
		"script-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP is missing %q: %s", directive, csp)
		}
	}
}

// --- Malware scanning ----------------------------------------------------------

// The property that matters: infected content never reaches the store and
// never gets a metadata row. Refusing it *after* it landed would leave the
// bytes on disk and the file in somebody's folder.
func TestAnInfectedUploadIsRefusedAndStoresNothing(t *testing.T) {
	h := newHarnessWithScanner(t, clamdSaying("stream: Eicar-Test-Signature FOUND\x00"), nil)
	c := h.signedIn("josh")

	before := h.blobCount()

	_, rec := c.upload("root", "Invoice.pdf", []byte("X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR"))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body)
	}

	// The signature is named, so a person can tell a false positive from a
	// real detection.
	if !strings.Contains(rec.Body.String(), "Eicar-Test-Signature") {
		t.Errorf("the response does not name the signature: %s", rec.Body)
	}

	if names := c.listNames(""); len(names) != 0 {
		t.Errorf("the refused upload appears in the folder: %v", names)
	}
	if after := h.blobCount(); after != before {
		t.Errorf("blob count went from %d to %d — infected content reached the store", before, after)
	}
}

func TestACleanUploadIsScannedAndStored(t *testing.T) {
	h := newHarnessWithScanner(t, clamdSaying("stream: OK\x00"), nil)
	c := h.signedIn("josh")

	id, rec := c.upload("root", "Report.txt", []byte("perfectly ordinary content"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if got := c.download(id); got != "perfectly ordinary content" {
		t.Errorf("content = %q", got)
	}
}

// Both answers are defensible when clamd cannot be reached, which is exactly
// why it is configuration rather than a decision made here.
func TestAnUnreachableScannerFollowsTheConfiguredChoice(t *testing.T) {
	t.Run("refuses when told to", func(t *testing.T) {
		h := newHarnessWithScanner(t, "127.0.0.1:1", map[string]string{
			"CLAMAV_REJECT_ON_ERROR": "true",
			"CLAMAV_TIMEOUT":         "1s",
		})
		c := h.signedIn("josh")

		_, rec := c.upload("root", "Report.txt", []byte("content"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 — nothing is wrong with the file, so "+
				"the client should be told to retry: %s", rec.Code, rec.Body)
		}
		if names := c.listNames(""); len(names) != 0 {
			t.Errorf("a file was stored despite the refusal: %v", names)
		}
	})

	t.Run("accepts unscanned when told to", func(t *testing.T) {
		h := newHarnessWithScanner(t, "127.0.0.1:1", map[string]string{
			"CLAMAV_REJECT_ON_ERROR": "false",
			"CLAMAV_TIMEOUT":         "1s",
		})
		c := h.signedIn("josh")

		_, rec := c.upload("root", "Report.txt", []byte("content"))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
		}
	})
}

// An operator who caps the scan size has decided content above it goes through
// unscanned. Refusing it instead would make the cap a de facto upload limit,
// which is not what the setting says.
func TestContentOverTheScanLimitIsStoredUnscanned(t *testing.T) {
	// A scanner that would refuse everything, and a limit that means it is
	// never consulted.
	h := newHarnessWithScanner(t, clamdSaying("stream: Everything FOUND\x00"), map[string]string{
		"CLAMAV_MAX_SCAN_SIZE": "10",
	})
	c := h.signedIn("josh")

	_, rec := c.upload("root", "Large.bin", []byte(strings.Repeat("x", 500)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for content over the scan limit: %s", rec.Code, rec.Body)
	}
}

// The resumable path stages content the same way and must be scanned the same
// way; scanning only the single-request path would leave the obvious hole.
func TestAResumableUploadIsScannedToo(t *testing.T) {
	h := newHarnessWithScanner(t, clamdSaying("stream: Eicar-Test-Signature FOUND\x00"), nil)
	c := h.signedIn("josh")

	content := []byte("a chunked payload that the scanner will refuse")

	rec := c.do(http.MethodPost, "/api/v1/uploads", map[string]any{
		"parent_id": "root", "name": "Chunked.bin", "size": len(content),
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("begin: %d %s", rec.Code, rec.Body)
	}
	var session struct {
		ID string `json:"id"`
	}
	decodeInto(t, rec, &session)

	rec = c.raw(http.MethodPut, "/api/v1/uploads/"+session.ID+"/chunk?offset=0",
		"application/octet-stream", bytes.NewReader(content))
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("chunk: %d %s", rec.Code, rec.Body)
	}

	rec = c.do(http.MethodPost, "/api/v1/uploads/"+session.ID+"/complete", map[string]any{})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("complete: %d, want 422 — the resumable path is not scanned: %s",
			rec.Code, rec.Body)
	}
	if names := c.listNames(""); len(names) != 0 {
		t.Errorf("the refused chunked upload appears in the folder: %v", names)
	}
}

func TestScanningIsOffByDefault(t *testing.T) {
	h := newHarness(t)
	c := h.signedIn("josh")

	// Nothing is listening anywhere; if scanning were on by default this
	// would fail one way or the other.
	_, rec := c.upload("root", "Report.txt", []byte("content"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d with scanning off by default: %s", rec.Code, rec.Body)
	}
}

// --- Helpers -------------------------------------------------------------------

// clamdSaying starts a fake clamd that answers every INSTREAM with one reply,
// and returns its address.
//
// A real ClamAV in the test suite would mean a gigabyte of signatures and a
// minute of startup for every run. What is under test here is PiFiles' side of
// the exchange — that content is scanned before it is committed, and that each
// verdict leads where it should — and a fake answers that exactly.
func clamdSaying(reply string) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic("cannot start a fake clamd: " + err.Error())
	}

	go func() {
		defer listener.Close()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

				command := make([]byte, 10)
				if _, err := io.ReadFull(conn, command); err != nil {
					return
				}
				header := make([]byte, 4)
				for {
					if _, err := io.ReadFull(conn, header); err != nil {
						return
					}
					n := binary.BigEndian.Uint32(header)
					if n == 0 {
						break
					}
					if _, err := io.CopyN(io.Discard, conn, int64(n)); err != nil {
						return
					}
				}
				_, _ = conn.Write([]byte(reply))
			}()
		}
	}()

	return listener.Addr().String()
}

// newHarnessWithScanner builds a harness with malware scanning pointed at the
// given address.
func newHarnessWithScanner(t testing.TB, address string, extra map[string]string) *harness {
	t.Helper()

	env := map[string]string{
		"MALWARE_SCANNING_ENABLED": "true",
		"CLAMAV_ADDRESS":           address,
		"CLAMAV_TIMEOUT":           "10s",
		"CLAMAV_REJECT_ON_ERROR":   "true",
	}
	for key, value := range extra {
		env[key] = value
	}

	h := newHarnessWithEnv(t, env)
	h.files.SetScanner(malware.NewScanner(malware.Config{
		Enabled:       true,
		Address:       h.cfg.Malware.Address,
		Timeout:       h.cfg.Malware.Timeout,
		MaxScanSize:   h.cfg.Malware.MaxScanSize,
		RejectOnError: h.cfg.Malware.RejectOnError,
	}, logging.Discard()), h.cfg.Malware.Quarantine)
	return h
}
