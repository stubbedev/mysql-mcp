// Package source turns the validated config into live, lazily-connected
// database handles. Each named source owns one *sql.DB connection pool and,
// when configured, one SSH tunnel that the database driver dials through.
package source

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/stubbedev/mysql-mcp/internal/config"
	"github.com/stubbedev/mysql-mcp/internal/engine"
)

// Source is a single configured database plus its connection state.
type Source struct {
	cfg     *config.SourceConfig
	eng     engine.Engine
	netName string

	mu  sync.Mutex
	db  *sql.DB
	ssh *sshTunnel
}

// Name returns the source's logical name.
func (s *Source) Name() string { return s.cfg.Name() }

// Readonly reports whether the source forbids writes.
func (s *Source) Readonly() bool { return s.cfg.Readonly }

// Remote reports whether the source is tunneled over SSH.
func (s *Source) Remote() bool { return s.cfg.IsRemote() }

// Engine returns the source's SQL dialect engine.
func (s *Source) Engine() engine.Engine { return s.eng }

// EngineName returns the configured engine name.
func (s *Source) EngineName() string { return s.cfg.Engine }

// DB returns the lazily-opened connection pool for the source. The pool is
// created on first use; the database is not contacted until a query runs, so a
// temporarily unreachable database does not prevent the server from starting.
func (s *Source) DB() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db, nil
	}
	dsn, err := s.eng.DSN(s.cfg, s.netName)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(s.eng.Driver(), dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", s.Name(), err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)
	s.db = db
	return db, nil
}

// Ping verifies connectivity, establishing the SSH tunnel if needed.
func (s *Source) Ping(ctx context.Context) error {
	db, err := s.DB()
	if err != nil {
		return err
	}
	return db.PingContext(ctx)
}

func (s *Source) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
	if s.ssh != nil {
		s.ssh.close()
	}
}

// Registry holds all configured sources, keyed by name.
type Registry struct {
	sources map[string]*Source
}

// NewRegistry builds a Registry from validated config, selecting an engine for
// each source and registering an SSH dialer for tunneled sources.
func NewRegistry(cfg *config.Config) (*Registry, error) {
	r := &Registry{sources: make(map[string]*Source, len(cfg.Sources))}
	for name, sc := range cfg.Sources {
		eng, err := engine.For(sc.Engine)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", name, err)
		}
		src := &Source{cfg: sc, eng: eng, netName: "tcp"}
		if sc.SSH != nil {
			tunnel := newSSHTunnel(sc.SSH)
			src.ssh = tunnel
			src.netName = registerSSHDialer(tunnel)
		}
		r.sources[name] = src
	}
	return r, nil
}

// Get returns the named source.
func (r *Registry) Get(name string) (*Source, error) {
	src, ok := r.sources[name]
	if !ok {
		return nil, fmt.Errorf("unknown source %q (known: %v)", name, r.Names())
	}
	return src, nil
}

// Names returns all source names, sorted.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.sources))
	for name := range r.sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// List returns all sources, sorted by name.
func (r *Registry) List() []*Source {
	out := make([]*Source, 0, len(r.sources))
	for _, name := range r.Names() {
		out = append(out, r.sources[name])
	}
	return out
}

// Close releases all pools and SSH tunnels.
func (r *Registry) Close() {
	for _, s := range r.sources {
		s.close()
	}
}
