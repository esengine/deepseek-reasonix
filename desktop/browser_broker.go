package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"reasonix/internal/browser"
	"reasonix/internal/remote/forward"
)

// The desktop browser broker is the local end of the remote browser channel:
// a loopback listener behind SSH reverse forwards, keyed by per-generation
// tokens that a reconnect revokes at once.

// browserBrokerForwardName prefixes the per-host reverse forward the remote
// serve's REASONIX_BROWSER_BROKER endpoint points at.
const browserBrokerForwardName = "browser-broker:"

// browserBrokerRoute binds one token to one host connection generation.
type browserBrokerRoute struct {
	hostID string
	gen    *managedHost
	ctx    context.Context
	cancel context.CancelFunc
}

// browserSessionResolution is what the broker resolves one request's session
// header into: the desktop executor bound to that session's tab and the
// workspace whose SFTP scratch area relays captures back.
type browserSessionResolution struct {
	exec      browser.Executor
	workspace string
}

// browserSessionResolver maps (host, remote session path) to the desktop
// executor that owns it. An unknown or foreign session must fail with
// browser.ErrNoGrant so the wire handler answers 409 no_grant.
type browserSessionResolver func(hostID, sessionPath string) (browserSessionResolution, error)

type browserBroker struct {
	lifecycleMu sync.Mutex
	mu          sync.Mutex
	ln          net.Listener
	server      *http.Server
	port        int
	routes      map[string]*browserBrokerRoute
	byHost      map[string]string
	resolve     browserSessionResolver
	// current reports whether gen is still the live connection for hostID;
	// a replaced generation's token stops authenticating immediately.
	current func(hostID string, gen *managedHost) bool
	// connFor returns the generation's SSH client for the capture relay.
	connFor func(hostID string, gen *managedHost) sftpConn
	// newRelay builds the capture relay for a connection; nil uses the SFTP
	// relay. Tests substitute a fake.
	newRelay func(conn sftpConn) FileRelay
	onRevoke func(hostID string)
}

func newBrowserBroker(resolve browserSessionResolver, current func(string, *managedHost) bool, connFor func(string, *managedHost) sftpConn) *browserBroker {
	return &browserBroker{
		routes:  map[string]*browserBrokerRoute{},
		byHost:  map[string]string{},
		resolve: resolve,
		current: current,
		connFor: connFor,
	}
}

// register mints a fresh token for (hostID, gen), replacing the host's
// previous token. Returns the token and the broker's loopback port.
func (b *browserBroker) register(hostID string, gen *managedHost) (string, int, error) {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", 0, fmt.Errorf("browser broker: mint token: %w", err)
	}
	token := hex.EncodeToString(buf)
	b.mu.Lock()
	if b.ln == nil {
		b.mu.Unlock()
		return "", 0, fmt.Errorf("browser broker: not running")
	}
	replaced := false
	if old := b.byHost[hostID]; old != "" {
		if route := b.routes[old]; route != nil && route.cancel != nil {
			route.cancel()
		}
		delete(b.routes, old)
		replaced = true
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.routes[token] = &browserBrokerRoute{hostID: hostID, gen: gen, ctx: ctx, cancel: cancel}
	b.byHost[hostID] = token
	port := b.port
	b.mu.Unlock()
	if replaced && b.onRevoke != nil {
		b.onRevoke(hostID)
	}
	return token, port, nil
}

// revokeHost drops every token minted for hostID (serve stop, disconnect).
func (b *browserBroker) revokeHost(hostID string) {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	b.mu.Lock()
	if token := b.byHost[hostID]; token != "" {
		if route := b.routes[token]; route != nil && route.cancel != nil {
			route.cancel()
		}
		delete(b.routes, token)
		delete(b.byHost, hostID)
	}
	b.mu.Unlock()
	if b.onRevoke != nil {
		b.onRevoke(hostID)
	}
}

func (b *browserBroker) close() {
	b.lifecycleMu.Lock()
	defer b.lifecycleMu.Unlock()
	b.mu.Lock()
	server, listener := b.server, b.ln
	hosts := make([]string, 0, len(b.byHost))
	for _, route := range b.routes {
		if route.cancel != nil {
			route.cancel()
		}
		hosts = append(hosts, route.hostID)
	}
	b.server, b.ln = nil, nil
	b.routes = map[string]*browserBrokerRoute{}
	b.byHost = map[string]string{}
	b.mu.Unlock()
	if b.onRevoke != nil {
		for _, hostID := range hosts {
			b.onRevoke(hostID)
		}
	}
	if server != nil {
		_ = server.Close()
	}
	if listener != nil {
		_ = listener.Close()
	}
}

func (b *browserBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Unauthenticated liveness for the reverse-tunnel probe, mirroring the
	// credential proxy: the listener is only reachable through the tunnel.
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	token := bearerToken(r.Header.Get("Authorization"))
	b.mu.Lock()
	route := b.routes[token]
	b.mu.Unlock()
	if token == "" || route == nil || (b.current != nil && !b.current(route.hostID, route.gen)) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="reasonix-browser-broker"`)
		http.Error(w, "invalid or stale browser broker token", http.StatusUnauthorized)
		return
	}
	exec := &brokerSessionExecutor{broker: b, route: route}
	if route.ctx != nil {
		ctx, cancel := context.WithCancel(r.Context())
		body := r.Body
		stop := context.AfterFunc(route.ctx, func() { cancel(); _ = body.Close() })
		defer stop()
		defer cancel()
		r = r.WithContext(ctx)
	}
	browser.NewHTTPHandler(exec, token).ServeHTTP(w, r)
}

// brokerSessionExecutor is the per-request executor the broker serves: every
// method resolves the request's session header to the desktop tab that owns
// it, so one host token can never drive another session's browser.
type brokerSessionExecutor struct {
	broker *browserBroker
	route  *browserBrokerRoute
}

func (s *brokerSessionExecutor) resolve(ctx context.Context) (browserSessionResolution, error) {
	if !s.current(ctx) {
		return browserSessionResolution{}, browser.ErrNoGrant
	}
	res, err := s.broker.resolve(s.route.hostID, browser.SessionFromContext(ctx))
	if err != nil {
		return browserSessionResolution{}, err
	}
	if res.exec == nil || !s.current(ctx) {
		return browserSessionResolution{}, browser.ErrNoGrant
	}
	return res, nil
}

func (s *brokerSessionExecutor) current(ctx context.Context) bool {
	return ctx.Err() == nil && (s.route.ctx == nil || s.route.ctx.Err() == nil) && (s.broker.current == nil || s.broker.current(s.route.hostID, s.route.gen))
}

func (s *brokerSessionExecutor) Available(ctx context.Context) bool {
	res, err := s.resolve(ctx)
	if err != nil {
		return false
	}
	if a, ok := res.exec.(browser.Availability); ok {
		return a.Available(ctx)
	}
	return true
}

func (s *brokerSessionExecutor) Tabs(ctx context.Context) ([]browser.Tab, error) {
	res, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return res.exec.Tabs(ctx)
}

func (s *brokerSessionExecutor) Open(ctx context.Context, req browser.OpenRequest) (browser.Tab, error) {
	res, err := s.resolve(ctx)
	if err != nil {
		return browser.Tab{}, err
	}
	return res.exec.Open(ctx, req)
}

func (s *brokerSessionExecutor) Navigate(ctx context.Context, req browser.NavigateRequest) (browser.Tab, error) {
	res, err := s.resolve(ctx)
	if err != nil {
		return browser.Tab{}, err
	}
	return res.exec.Navigate(ctx, req)
}

func (s *brokerSessionExecutor) Snapshot(ctx context.Context, req browser.SnapshotRequest) (browser.Snapshot, error) {
	res, err := s.resolve(ctx)
	if err != nil {
		return browser.Snapshot{}, err
	}
	return res.exec.Snapshot(ctx, req)
}

// Screenshot relays the capture file onto the remote host before answering:
// the path the serve receives must be local to the serve, never a desktop
// path it cannot read.
func (s *brokerSessionExecutor) Screenshot(ctx context.Context, req browser.ScreenshotRequest) (browser.Screenshot, error) {
	res, err := s.resolve(ctx)
	if err != nil {
		return browser.Screenshot{}, err
	}
	shot, err := res.exec.Screenshot(ctx, req)
	if err != nil {
		return browser.Screenshot{}, err
	}
	shot.Path, err = s.relay(ctx, res.workspace, shot.Path)
	if err != nil {
		return browser.Screenshot{}, err
	}
	return shot, nil
}

func (s *brokerSessionExecutor) Downloads(ctx context.Context, req browser.DownloadsRequest) ([]browser.Download, error) {
	res, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	downloads, err := res.exec.Downloads(ctx, req)
	if err != nil {
		return nil, err
	}
	for i, d := range downloads {
		if strings.TrimSpace(d.Path) == "" {
			continue
		}
		downloads[i].Path, err = s.relay(ctx, res.workspace, d.Path)
		if err != nil {
			return nil, err
		}
	}
	return downloads, nil
}

func (s *brokerSessionExecutor) Act(ctx context.Context, req browser.ActRequest) (browser.ActResult, error) {
	res, err := s.resolve(ctx)
	if err != nil {
		return browser.ActResult{}, err
	}
	if req.Action == browser.ActionUpload {
		owner, ok := res.exec.(interface{ captureDir() (string, error) })
		if !ok || s.broker.connFor == nil {
			return browser.ActResult{}, fmt.Errorf("browser upload: no staging owner")
		}
		conn := s.broker.connFor(s.route.hostID, s.route.gen)
		if conn == nil {
			return browser.ActResult{}, browser.ErrNoGrant
		}
		newRelay := s.broker.newRelay
		if newRelay == nil {
			newRelay = func(c sftpConn) FileRelay { return sftpFileRelay{conn: c} }
		}
		relay, ok := newRelay(conn).(browserUploadRelay)
		if !ok {
			return browser.ActResult{}, fmt.Errorf("browser upload: relay cannot receive remote files")
		}
		scratch, err := owner.captureDir()
		if err != nil {
			return browser.ActResult{}, err
		}
		dir, err := os.MkdirTemp(scratch, "remote-upload-")
		if err != nil {
			return browser.ActResult{}, err
		}
		defer os.RemoveAll(dir)
		files := make([]string, 0, len(req.Files))
		for _, remote := range req.Files {
			local, err := relay.Fetch(ctx, res.workspace, remote, dir)
			if err != nil {
				return browser.ActResult{}, err
			}
			files = append(files, local)
		}
		req.Files = files
	}
	if !s.current(ctx) {
		return browser.ActResult{}, browser.ErrNoGrant
	}
	return res.exec.Act(ctx, req)
}

func (s *brokerSessionExecutor) Close(ctx context.Context, req browser.CloseRequest) error {
	res, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	return res.exec.Close(ctx, req)
}

// relay stages one desktop capture file onto the remote host through the
// connection generation's SFTP channel.
func (s *brokerSessionExecutor) relay(ctx context.Context, workspace, localPath string) (string, error) {
	if strings.TrimSpace(localPath) == "" {
		return "", nil
	}
	if s.broker.connFor == nil {
		return "", fmt.Errorf("browser broker: no file relay for this connection")
	}
	conn := s.broker.connFor(s.route.hostID, s.route.gen)
	if conn == nil {
		return "", fmt.Errorf("browser broker: host %q connection is gone", s.route.hostID)
	}
	newRelay := s.broker.newRelay
	if newRelay == nil {
		newRelay = func(c sftpConn) FileRelay { return sftpFileRelay{conn: c} }
	}
	return newRelay(conn).Stage(ctx, workspace, localPath)
}

// browserBrokerPort returns the broker's loopback port, starting the listener
// on first use. The broker serves every remote host off one port; tokens keep
// the hosts apart.
func (a *App) browserBrokerPort() (int, error) {
	a.browserBrokerMu.Lock()
	defer a.browserBrokerMu.Unlock()
	if a.browserBroker != nil {
		return a.browserBroker.port, nil
	}
	if !a.hostMode() {
		return 0, fmt.Errorf("browser broker: the desktop shell is not attached")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("browser broker: listen: %w", err)
	}
	b := newBrowserBroker(a.resolveRemoteBrowserSession, a.remoteHostGenerationCurrent, a.remoteHostGenerationClient)
	b.onRevoke = a.revokeRemoteBrowserHost
	b.ln = ln
	b.port = ln.Addr().(*net.TCPAddr).Port
	b.server = &http.Server{
		Handler:           b,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	a.browserBroker = b
	a.goSafe("browserBroker", func() { _ = b.server.Serve(ln) })
	return b.port, nil
}

// registerBrowserBrokerRoute starts the broker if needed and mints the
// (host, generation) token a remote serve bootstrap hands over.
func (a *App) registerBrowserBrokerRoute(hostID string, gen *managedHost) (string, int, error) {
	if _, err := a.browserBrokerPort(); err != nil {
		return "", 0, err
	}
	a.browserBrokerMu.Lock()
	b := a.browserBroker
	a.browserBrokerMu.Unlock()
	if b == nil {
		return "", 0, fmt.Errorf("browser broker: not running")
	}
	return b.register(hostID, gen)
}

func (a *App) revokeBrowserBrokerRoutes(hostID string) {
	a.browserBrokerMu.Lock()
	b := a.browserBroker
	a.browserBrokerMu.Unlock()
	if b != nil {
		b.revokeHost(hostID)
	}
}

func (a *App) closeBrowserBroker() {
	a.browserBrokerMu.Lock()
	b := a.browserBroker
	a.browserBroker = nil
	a.browserBrokerMu.Unlock()
	if b != nil {
		b.close()
	}
}

// closeRemoteBrokers tears down the loopback brokers that serve remote hosts.
func (a *App) closeRemoteBrokers() {
	a.closeCredentialProxy()
	a.closeBrowserBroker()
}

// remoteHostGenerationCurrent fences broker routes to their connection
// generation: once the manager swaps or drops the host, minted tokens die.
func (a *App) remoteHostGenerationCurrent(hostID string, gen *managedHost) bool {
	a.remoteMu.Lock()
	rt := a.remoteRuntime
	a.remoteMu.Unlock()
	m, ok := rt.(*desktopRemoteManager)
	if !ok || m == nil {
		return false
	}
	return m.isCurrent(hostID, gen)
}

func (a *App) remoteHostGenerationClient(hostID string, gen *managedHost) sftpConn {
	a.remoteMu.Lock()
	rt := a.remoteRuntime
	a.remoteMu.Unlock()
	m, ok := rt.(*desktopRemoteManager)
	if !ok || m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hosts[hostID] != gen {
		return nil
	}
	return gen.client
}

// resolveRemoteBrowserSession maps a remote serve's session path to the
// desktop executor of the remote tab displaying it. Any session this desktop
// does not show for that host is refused with browser.ErrNoGrant, so a token
// can never reach a foreign session's tabs.
func (a *App) resolveRemoteBrowserSession(hostID, sessionPath string) (browserSessionResolution, error) {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" || !a.hostMode() {
		return browserSessionResolution{}, browser.ErrNoGrant
	}
	a.remoteTabMu.Lock()
	var tab *remoteTab
	for _, t := range a.remoteTabs {
		if t == nil || t.ref.HostID != hostID {
			continue
		}
		t.sessionMu.Lock()
		path := strings.TrimSpace(t.session.path)
		t.sessionMu.Unlock()
		if path != "" && path == sessionPath {
			tab = t
			break
		}
	}
	a.remoteTabMu.Unlock()
	if tab == nil {
		return browserSessionResolution{}, fmt.Errorf("%w: no desktop tab serves session %s", browser.ErrNoGrant, sessionPath)
	}
	return browserSessionResolution{
		exec:      a.browserExecutorForRemoteTab(tab, sessionPath),
		workspace: tab.ref.Workspace,
	}, nil
}

// browserExecutorForRemoteTab returns the cached executor for one remote
// tab's browser surface; a session rotation re-scopes the grant.
func (a *App) browserExecutorForRemoteTab(tab *remoteTab, sessionPath string) browser.Executor {
	if tab == nil || !a.hostMode() || a.browserControl.off() {
		return nil
	}
	key := "remote/" + tab.id
	a.browserExecMu.Lock()
	defer a.browserExecMu.Unlock()
	if a.browserExecutors == nil {
		a.browserExecutors = map[string]*hostBrowserExecutor{}
	}
	if exec, ok := a.browserExecutors[key]; ok {
		if exec.sessionKey == sessionPath {
			return exec
		}
		// A session rotation creates a new immutable owner; mutating the old
		// executor races in-flight calls and lets them inherit the new grant.
		a.revokeBrowserExecutor(exec)
	}
	exec := &hostBrowserExecutor{
		app: a, host: a.hostShell.server, tabID: tab.id,
		grantID: newBrowserGrantID(), sessionKey: sessionPath,
	}
	a.browserExecutors[key] = exec
	return exec
}

// ensureBrowserBrokerForward opens (idempotently) the reverse tunnel from the
// remote loopback to the desktop broker, mirroring the credential proxy's
// forward. Returns the actually bound remote port.
func ensureBrowserBrokerForward(c desktopSSHClient, hostID string, desktopPort int) (int, error) {
	name := browserBrokerForwardName + hostID
	target := fmt.Sprintf("127.0.0.1:%d", desktopPort)
	for _, f := range c.Forwards().List() {
		if f.Spec.Name == name && f.Spec.TargetAddr == target && f.Up {
			if port, ok := portOfAddr(f.BoundAddr); ok {
				return port, nil
			}
		}
	}
	bound, err := c.Forwards().Replace(forward.Spec{
		Name:       name,
		Direction:  forward.Remote,
		BindAddr:   "127.0.0.1:0",
		TargetAddr: target,
	})
	if err != nil {
		return 0, err
	}
	port, ok := portOfAddr(bound)
	if !ok {
		return 0, fmt.Errorf("browser broker: reverse tunnel bound unexpected address %q", bound)
	}
	return port, nil
}
