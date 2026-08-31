package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/joshuanmoses/piCloud/backend/internal/logging"
)

type contextKey string

const (
	requestIDKey contextKey = "request_id"
	realIPKey    contextKey = "real_ip"
	schemeKey    contextKey = "scheme"
)

// RequestIDHeader is the header carrying the correlation identifier, both
// inbound (from a trusted proxy) and outbound.
const RequestIDHeader = "X-Request-Id"

// RequestIDFromContext returns the request correlation ID, or "" when absent.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// ClientIPFromContext returns the resolved client IP. Rate limiting, lockout,
// and audit logging all depend on this being trustworthy, which is why it is
// resolved once in middleware rather than re-derived per handler.
func ClientIPFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ip, _ := ctx.Value(realIPKey).(string)
	return ip
}

// RequestSchemeFromContext reports the scheme the client actually used —
// "https" or "http" — as opposed to the scheme of the hop between the proxy
// and the application, which is always http.
func RequestSchemeFromContext(ctx context.Context) string {
	if ctx == nil {
		return "http"
	}
	if scheme, _ := ctx.Value(schemeKey).(string); scheme != "" {
		return scheme
	}
	return "http"
}

// RequestIsHTTPS reports whether the client's connection was encrypted.
func RequestIsHTTPS(ctx context.Context) bool {
	return RequestSchemeFromContext(ctx) == "https"
}

// ForwardedProto resolves the scheme the client actually used.
//
// The application always receives plain HTTP from Caddy, so r.TLS is nil even
// when the user is on HTTPS. X-Forwarded-Proto carries the truth, but only
// when it comes from a trusted proxy; otherwise any client could claim its
// connection was encrypted.
//
// This matters concretely: Strict-Transport-Security must never be sent over
// a plain-HTTP connection. Doing so on a LAN install pins the browser to HTTPS
// for that hostname, and since such an install has no certificate, the user is
// locked out until they manually clear the HSTS entry.
func ForwardedProto(isTrustedProxy func(string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			} else {
				peer, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					peer = r.RemoteAddr
				}
				if isTrustedProxy != nil && isTrustedProxy(peer) {
					if forwarded := forwardedProtoHeader(r); forwarded != "" {
						scheme = forwarded
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), schemeKey, scheme)))
		})
	}
}

func forwardedProtoHeader(r *http.Request) string {
	if v := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))); v == "http" || v == "https" {
		return v
	}
	// RFC 7239: Forwarded: proto=https;for=...
	if fwd := r.Header.Get("Forwarded"); fwd != "" {
		for _, part := range strings.Split(fwd, ";") {
			key, value, found := strings.Cut(strings.TrimSpace(part), "=")
			if found && strings.EqualFold(key, "proto") {
				v := strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`))
				if v == "http" || v == "https" {
					return v
				}
			}
		}
	}
	return ""
}

// RequestID assigns each request a correlation identifier and echoes it back.
//
// An inbound X-Request-Id is honoured only from a trusted proxy, and only when
// it looks sane — otherwise a client could inject arbitrary text into log
// lines, or poison correlation by reusing one ID across requests.
func RequestID(isTrustedProxy func(string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := ""
			if inbound := r.Header.Get(RequestIDHeader); inbound != "" {
				remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
				if isTrustedProxy != nil && isTrustedProxy(remoteIP) && isSafeRequestID(inbound) {
					id = inbound
				}
			}
			if id == "" {
				id = newRequestID()
			}

			w.Header().Set(RequestIDHeader, id)
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func newRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is fatal for a security-sensitive service, but a
		// request ID is not itself a secret; degrade to a timestamp rather than
		// taking the process down.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// isSafeRequestID accepts only short, printable, log-safe identifiers.
func isSafeRequestID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !alnum && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

// RealIP resolves the client address, honouring X-Forwarded-For only when the
// immediate peer is a trusted proxy.
//
// This matters: if forwarded headers were trusted unconditionally, any client
// could set X-Forwarded-For and defeat per-IP rate limiting and account
// lockout, and could forge the source address recorded in the audit log.
func RealIP(isTrustedProxy func(string) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				peer = r.RemoteAddr
			}

			clientIP := peer
			if isTrustedProxy != nil && isTrustedProxy(peer) {
				if forwarded := forwardedClientIP(r); forwarded != "" {
					clientIP = forwarded
				}
			}

			ctx := context.WithValue(r.Context(), realIPKey, clientIP)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// forwardedClientIP extracts the originating address from proxy headers.
func forwardedClientIP(r *http.Request) string {
	// X-Real-Ip is a single value and is what Caddy sets by default.
	if v := strings.TrimSpace(r.Header.Get("X-Real-Ip")); v != "" {
		if ip := net.ParseIP(v); ip != nil {
			return ip.String()
		}
	}
	// X-Forwarded-For is "client, proxy1, proxy2"; the leftmost entry is the
	// original client as reported by the first proxy in the chain.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
			return ip.String()
		}
	}
	return ""
}

// TrustedProxyChecker compiles a CIDR list into a membership test. Entries may
// be CIDRs ("10.0.0.0/8") or bare addresses ("127.0.0.1"). Invalid entries are
// reported rather than silently dropped, because silently trusting nothing —
// or everything — is a security-relevant misconfiguration.
func TrustedProxyChecker(cidrs []string) (func(string) bool, error) {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, raw := range cidrs {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("trusted proxy %q is not a valid IP address or CIDR", raw)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is not a valid CIDR: %w", raw, err)
		}
		nets = append(nets, network)
	}

	return func(addr string) bool {
		ip := net.ParseIP(strings.TrimSpace(addr))
		if ip == nil {
			return false
		}
		for _, n := range nets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}, nil
}

// SecurityHeaders sets response headers that harden the browser environment.
//
// Caddy sets an overlapping set at the edge; duplicating them here means the
// application is still hardened when it is reached directly — during
// development, from another container, or if the proxy is misconfigured.
func SecurityHeaders() func(http.Handler) http.Handler {
	// The SPA is fully self-hosted: no CDN, no inline scripts, no external
	// fonts. 'self' everywhere is therefore achievable, and anything looser
	// would be a needless XSS foothold.
	//
	// style-src is split rather than blanket 'unsafe-inline'.
	//
	// React sets element style *attributes* directly — a progress bar's width,
	// a tag's colour — and there is no way round that short of pre-generating
	// a class for every possible value. But nothing needs an inline <style>
	// *element*, and those are the dangerous half: a <style> block can define
	// rules matching anything on the page, exfiltrate attribute values through
	// selector-based requests, or overlay the interface to mislead a click. An
	// attribute can only affect the element it is already on.
	//
	// So: style-src 'self' forbids injected <style> blocks and remote
	// stylesheets, and style-src-attr 'unsafe-inline' permits exactly the
	// attributes React needs. Browsers without style-src-attr fall back to
	// style-src and lose the attributes; that degrades layout, not safety, and
	// every browser this targets has supported it since 2020.
	//
	// This is a narrowing, not a removal, and it is worth saying so plainly.
	csp := strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"style-src-attr 'unsafe-inline'",
		"style-src-elem 'self'",
		"img-src 'self' data: blob:",
		"media-src 'self' blob:",
		"font-src 'self' data:",
		"connect-src 'self'",
		// Previews render inside a sandboxed frame served from this origin.
		"frame-src 'self' blob:",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", csp)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")

			// HSTS is gated on the connection actually being encrypted, not on
			// the environment being production. A plain-HTTP LAN install is a
			// supported deployment, and sending HSTS there would pin the
			// browser to a scheme the server cannot serve.
			if RequestIsHTTPS(r.Context()) {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Recoverer converts a handler panic into a 500 instead of killing the process
// and dropping every other in-flight request.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler is the documented way to abort a response;
			// it must propagate so the server closes the connection.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}

			logging.FromContext(r.Context()).Error("panic recovered",
				slog.Any("panic", rec),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("stack", string(debug.Stack())),
			)

			// If the handler already began writing, the status is fixed and
			// anything more would corrupt the body.
			if ww, ok := w.(*responseRecorder); ok && ww.wroteHeader {
				return
			}
			WriteError(w, r, ErrInternal(fmt.Errorf("panic: %v", rec)))
		}()
		next.ServeHTTP(w, r)
	})
}

// responseRecorder captures status and byte count for request logging.
type responseRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer, which
// keeps Flush, Hijack, and SetWriteDeadline working for SSE and streaming
// downloads.
func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// RequestLogger emits one structured line per request and attaches a
// request-scoped logger to the context.
func RequestLogger(logger *slog.Logger, enabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := RequestIDFromContext(r.Context())
			reqLogger := logger.With("request_id", reqID)
			ctx := logging.WithLogger(r.Context(), reqLogger)
			r = r.WithContext(ctx)

			if !enabled {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			elapsed := time.Since(start)

			// Query strings can carry share tokens and search terms; log only
			// whether one was present.
			attrs := []any{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("bytes", rec.bytes),
				slog.Int64("duration_ms", elapsed.Milliseconds()),
				slog.String("ip", ClientIPFromContext(ctx)),
			}
			if r.URL.RawQuery != "" {
				attrs = append(attrs, slog.Bool("has_query", true))
			}

			switch {
			case rec.status >= 500:
				reqLogger.Error("request", attrs...)
			case rec.status >= 400:
				reqLogger.Warn("request", attrs...)
			default:
				reqLogger.Info("request", attrs...)
			}
		})
	}
}

// NoCache marks a response as uncacheable. Applied to every API route: file
// listings and session state must never be served from a shared cache.
func NoCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
		h.Set("Pragma", "no-cache")
		h.Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

// Timeout bounds handler execution. It is applied only to routes that cannot
// legitimately run long; uploads, downloads, and SSE streams are excluded,
// since cutting those off is a bug, not a protection.
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
