package source

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"

	"github.com/go-sql-driver/mysql"
	"github.com/stubbedev/mysql-mcp/internal/config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// sshTunnel lazily establishes and caches an SSH client used to reach a remote
// database. The connection is shared by all database connections for the source.
type sshTunnel struct {
	cfg *config.SSHConfig

	mu     sync.Mutex
	client *ssh.Client
}

func newSSHTunnel(cfg *config.SSHConfig) *sshTunnel { return &sshTunnel{cfg: cfg} }

// sshDialerSeq makes each registered driver network name unique within the
// process, since mysql.RegisterDialContext panics on a duplicate name.
var sshDialerSeq atomic.Uint64

// registerSSHDialer registers a database driver dialer that routes connections
// through the tunnel and returns the unique network name to use in the DSN.
func registerSSHDialer(t *sshTunnel) string {
	name := fmt.Sprintf("mysqlmcp-ssh-%d", sshDialerSeq.Add(1))
	mysql.RegisterDialContext(name, func(ctx context.Context, addr string) (net.Conn, error) {
		return t.dial(ctx, addr)
	})
	return name
}

// deregisterSSHDialer removes a dialer from the driver's process-global
// registry. Called when a source closes so reloading a per-workspace config
// (mtime change) does not leak dialer registrations. Names are never reused, so
// this is always safe.
func deregisterSSHDialer(name string) { mysql.DeregisterDialContext(name) }

// dial opens a connection to addr through the tunnel, reconnecting once if the
// cached SSH client has gone stale.
func (t *sshTunnel) dial(ctx context.Context, addr string) (net.Conn, error) {
	client, err := t.connect()
	if err != nil {
		return nil, err
	}
	conn, err := dialThrough(ctx, client, addr)
	if err != nil {
		t.reset()
		if client, err = t.connect(); err != nil {
			return nil, err
		}
		return dialThrough(ctx, client, addr)
	}
	return conn, nil
}

// dialThrough makes ssh.Client.Dial respect context cancellation.
func dialThrough(ctx context.Context, client *ssh.Client, addr string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, e := client.Dial("tcp", addr)
		ch <- result{c, e}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.conn, r.err
	}
}

func (t *sshTunnel) connect() (*ssh.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client != nil {
		return t.client, nil
	}
	clientCfg, err := t.clientConfig()
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("%s:%d", t.cfg.Host, t.cfg.Port)
	client, err := ssh.Dial("tcp", addr, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	t.client = client
	return client, nil
}

func (t *sshTunnel) clientConfig() (*ssh.ClientConfig, error) {
	var auth []ssh.AuthMethod
	if t.cfg.PrivateKeyPath != "" {
		signer, err := loadSigner(t.cfg.PrivateKeyPath, t.cfg.PrivateKeyPassphrase)
		if err != nil {
			return nil, err
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if t.cfg.Password != "" {
		auth = append(auth, ssh.Password(t.cfg.Password))
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("ssh source %q: no auth configured (set private_key_path or password)", t.cfg.Host)
	}

	if t.cfg.KnownHostsPath == "" {
		return nil, fmt.Errorf("ssh source %q: known_hosts_path is required for host-key verification", t.cfg.Host)
	}
	hostKeyCallback, err := knownhosts.New(t.cfg.KnownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts %s: %w", t.cfg.KnownHostsPath, err)
	}

	return &ssh.ClientConfig{
		User:            t.cfg.User,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
	}, nil
}

func loadSigner(path, passphrase string) (ssh.Signer, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key %s: %w", path, err)
	}
	if passphrase != "" {
		return ssh.ParsePrivateKeyWithPassphrase(key, []byte(passphrase))
	}
	return ssh.ParsePrivateKey(key)
}

func (t *sshTunnel) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.client != nil {
		_ = t.client.Close()
		t.client = nil
	}
}

func (t *sshTunnel) close() { t.reset() }
