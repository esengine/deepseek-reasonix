package hostrpc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"

	"reasonix/internal/extension/rpcwire"
)

// Hooks are the lifecycle owners behind the desktop/* requests. A nil hook
// answers its request with success and no effect.
type Hooks struct {
	Hello            func(HelloParams) (HelloResult, error)
	Start            func(ctx context.Context) error
	DOMReady         func(ctx context.Context) error
	RendererAttached func(ctx context.Context, generation int) error
	BeforeClose      func(ctx context.Context, reason string) (prevent bool)
	Shutdown         func(ctx context.Context) error
	HostEvent        func(ctx context.Context, name string, payload json.RawMessage) error
	BrowserControl   func(ctx context.Context, enabled bool) error
}

// ServerConfig assembles one service process's identity around its registry.
type ServerConfig struct {
	Registry   *Registry
	Contract   Contract
	Hooks      Hooks
	Identity   Identity
	Generation string
}

// Event is the params object of a desktop/event notification.
type Event struct {
	Seq        int64  `json:"seq"`
	Generation string `json:"generation"`
	Name       string `json:"name"`
	Args       []any  `json:"args"`
}

// Server answers the shell over one rpcwire connection.
type Server struct {
	conn   *rpcwire.Conn
	cfg    ServerConfig
	digest string

	ready   atomic.Bool
	helloMu sync.Mutex

	emitMu sync.Mutex
	seq    int64

	done     chan struct{}
	doneOnce sync.Once
}

// NewServer registers the desktop/* handlers on conn. Call Serve afterwards;
// conn must not be served by anyone else.
func NewServer(conn *rpcwire.Conn, cfg ServerConfig) *Server {
	s := &Server{conn: conn, cfg: cfg, digest: cfg.Contract.Digest(), done: make(chan struct{})}
	conn.Handle("desktop/hello", s.hello)
	conn.Handle("desktop/start", s.gated(s.start))
	conn.Handle("desktop/domReady", s.gated(s.domReady))
	conn.Handle("desktop/rendererAttached", s.gated(s.rendererAttached))
	conn.Handle("desktop/beforeClose", s.gated(s.beforeClose))
	conn.Handle("desktop/shutdown", s.gated(s.shutdown))
	conn.Handle("desktop/hostEvent", s.gated(s.hostEvent))
	conn.Handle("desktop/browserControl", s.gated(s.browserControl))
	conn.Handle("desktop/invoke", s.gated(s.invoke))
	return s
}

// Serve pumps the connection until the shell closes its end, the context
// ends, or desktop/shutdown has been acknowledged. A clean end returns nil.
func (s *Server) Serve(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() { errc <- s.conn.Serve(ctx) }()
	select {
	case err := <-errc:
		return err
	case <-s.done:
		return nil
	}
}

// Emit writes one desktop/event notification. Sequence numbers are assigned
// and written under one lock, so the wire order equals the call order.
func (s *Server) Emit(name string, args ...any) {
	if args == nil {
		args = []any{}
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.seq++
	err := s.conn.Notify("desktop/event", Event{Seq: s.seq, Generation: s.cfg.Generation, Name: name, Args: args})
	if err != nil {
		slog.Warn("desktop host: event not delivered", "name", name, "seq", s.seq, "err", err)
	}
}

// Request issues a host/* reverse request and decodes the result into result
// when it is non-nil.
func (s *Server) Request(ctx context.Context, method string, params any, result any) error {
	raw, err := s.conn.Request(ctx, method, params)
	if err != nil {
		return err
	}
	if result == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, result)
}

func (s *Server) gated(h rpcwire.RequestHandler) rpcwire.RequestHandler {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		if !s.ready.Load() {
			return nil, notReady()
		}
		return h(ctx, params)
	}
}

func (s *Server) hello(_ context.Context, raw json.RawMessage) (any, error) {
	var p HelloParams
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	s.helloMu.Lock()
	defer s.helloMu.Unlock()
	if s.ready.Load() {
		return nil, &rpcwire.RPCError{Code: rpcwire.ErrInvalidRequest, Message: "desktop/hello already completed"}
	}
	if err := validateHello(p, s.digest, s.cfg.Identity); err != nil {
		return nil, err
	}
	var result HelloResult
	if s.cfg.Hooks.Hello != nil {
		var err error
		if result, err = s.cfg.Hooks.Hello(p); err != nil {
			return nil, &rpcwire.RPCError{Code: rpcwire.ErrInternal, Message: "hello: " + err.Error()}
		}
	}
	result.ProtocolVersion = ProtocolVersion
	result.ContractDigest = s.digest
	result.Service = ServiceInfo{
		BuildInfo: BuildInfo{Version: s.cfg.Identity.Version, Channel: s.cfg.Identity.Channel, Commit: s.cfg.Identity.Commit},
		PID:       os.Getpid(),
	}
	result.RuntimeGeneration = s.cfg.Generation
	s.ready.Store(true)
	return result, nil
}

func (s *Server) start(ctx context.Context, _ json.RawMessage) (any, error) {
	return empty(runHook(ctx, s.cfg.Hooks.Start))
}

func (s *Server) domReady(ctx context.Context, _ json.RawMessage) (any, error) {
	return empty(runHook(ctx, s.cfg.Hooks.DOMReady))
}

func (s *Server) rendererAttached(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		RendererGeneration int `json:"rendererGeneration"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if s.cfg.Hooks.RendererAttached == nil {
		return empty(nil)
	}
	return empty(s.cfg.Hooks.RendererAttached(ctx, p.RendererGeneration))
}

func (s *Server) beforeClose(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Reason string `json:"reason"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	prevent := false
	if s.cfg.Hooks.BeforeClose != nil {
		prevent = s.cfg.Hooks.BeforeClose(ctx, p.Reason)
	}
	return map[string]bool{"prevent": prevent}, nil
}

func (s *Server) shutdown(ctx context.Context, _ json.RawMessage) (any, error) {
	if err := runHook(ctx, s.cfg.Hooks.Shutdown); err != nil {
		return nil, internalError(err)
	}
	return rpcwire.RespondThen(struct{}{}, func(error) {
		s.doneOnce.Do(func() { close(s.done) })
	}), nil
}

func (s *Server) hostEvent(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Name    string          `json:"name"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if s.cfg.Hooks.HostEvent == nil {
		return empty(nil)
	}
	return empty(s.cfg.Hooks.HostEvent(ctx, p.Name, p.Payload))
}

// browserControl carries the shell's capability switch for the built-in
// browser; the shell owns the persisted value and pushes it on every change.
func (s *Server) browserControl(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	if p.Enabled == nil {
		return nil, &rpcwire.RPCError{Code: rpcwire.ErrInvalidParams, Message: "enabled is required"}
	}
	if s.cfg.Hooks.BrowserControl == nil {
		return empty(nil)
	}
	return empty(s.cfg.Hooks.BrowserControl(ctx, *p.Enabled))
}

func (s *Server) invoke(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Method string            `json:"method"`
		Args   []json.RawMessage `json:"args"`
	}
	if err := decodeParams(raw, &p); err != nil {
		return nil, err
	}
	result, err := s.cfg.Registry.Invoke(ctx, p.Method, p.Args)
	if err == nil {
		return result, nil
	}
	data := map[string]any{"method": p.Method}
	var unknown *UnknownMethodError
	var invalid *InvalidArgsError
	var panicked *PanicError
	switch {
	case errors.As(err, &unknown):
		return nil, &rpcwire.RPCError{Code: rpcwire.ErrMethodNotFound, Message: err.Error(), Data: data}
	case errors.As(err, &invalid):
		return nil, &rpcwire.RPCError{Code: rpcwire.ErrInvalidParams, Message: err.Error(), Data: data}
	case errors.As(err, &panicked):
		slog.Error("desktop host: bound method panicked", "method", p.Method, "panic", panicked.Value, "stack", string(panicked.Stack))
		return nil, &rpcwire.RPCError{Code: rpcwire.ErrInternal, Message: err.Error(), Data: data}
	}
	return nil, &rpcwire.RPCError{Code: CodeBusiness, Message: err.Error(), Data: data}
}

func decodeParams(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return &rpcwire.RPCError{Code: rpcwire.ErrInvalidParams, Message: "invalid params: " + err.Error()}
	}
	return nil
}

func runHook(ctx context.Context, hook func(context.Context) error) error {
	if hook == nil {
		return nil
	}
	return hook(ctx)
}

func empty(err error) (any, error) {
	if err != nil {
		return nil, internalError(err)
	}
	return struct{}{}, nil
}

func internalError(err error) error {
	var rpcErr *rpcwire.RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	return &rpcwire.RPCError{Code: rpcwire.ErrInternal, Message: err.Error()}
}
