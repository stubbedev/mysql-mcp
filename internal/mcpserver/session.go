package mcpserver

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// rootHeaders let a proxy or harness inject the client's workspace roots
// without a roots/list round-trip. Values are file:// URIs or absolute paths;
// multiple roots may be comma-separated. Header roots are authoritative: they
// work even when the client never advertised the roots capability.
var rootHeaders = []string{"X-Mcp-Roots", "X-Mcp-Root", "Mcp-Roots", "Mcp-Root"}

// sessionState caches one client connection's roots/list result for the life of
// its session, so each tool call resolves the client's config without a fresh
// round-trip. stdio and HTTP sessions are identical here — the SDK hides the
// transport behind ServerSession.
//
// This cache assumes one client per MCP session, which holds for stdio and
// stateful HTTP (each connection gets a unique random session id). Roots
// injected via request header are NOT cached here — they are request-scoped
// (see Service.resolve) so a proxy multiplexing several clients over one
// session can safely vary them per request.
type sessionState struct {
	ss *mcp.ServerSession

	mu    sync.Mutex
	done  bool
	roots []string
}

// invalidate clears the roots cache so the next call re-queries (roots/list_changed).
func (s *sessionState) invalidate() {
	s.mu.Lock()
	s.done = false
	s.roots = nil
	s.mu.Unlock()
}

// rootPaths returns the client's workspace root paths, querying roots/list once
// and caching the result. The round-trip runs WITHOUT the lock held — it blocks
// on the client, and must not stall other callers; concurrent first-callers may
// duplicate the harmless query.
func (s *sessionState) rootPaths(ctx context.Context) []string {
	s.mu.Lock()
	if s.done {
		r := s.roots
		s.mu.Unlock()
		return r
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := s.ss.ListRoots(ctx, &mcp.ListRootsParams{})
	if err != nil {
		return nil // transient — leave uncached so a later call retries
	}
	var list []string
	for _, r := range res.Roots {
		if p := fileURIToPath(r.URI); p != "" {
			list = append(list, p)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done { // another concurrent caller already resolved
		return s.roots
	}
	s.roots = list
	s.done = true
	return list
}

// rootsFromHeaders extracts workspace roots from a request's headers.
func rootsFromHeaders(h http.Header) []string {
	var list []string
	for _, name := range rootHeaders {
		for _, v := range h.Values(name) {
			for _, part := range strings.Split(v, ",") {
				if part = strings.TrimSpace(part); part == "" {
					continue
				}
				if p := fileURIToPath(part); p != "" {
					list = append(list, p)
				}
			}
		}
	}
	return list
}

// fileURIToPath converts a file:// URI (or a bare absolute path) to a local
// filesystem path; "" for anything else.
func fileURIToPath(uri string) string {
	if uri == "" {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		if uri[0] == '/' { // some clients send bare absolute paths
			return uri
		}
		return ""
	}
	p := u.Path
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' { // file:///C:/x → C:/x
		p = p[1:]
	}
	return p
}
