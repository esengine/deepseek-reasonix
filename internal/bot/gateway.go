package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/boot"
	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/secrets"
	"reasonix/internal/sessioninbox"
)

// GatewayConfig 是 BotGateway 的配置。
type GatewayConfig struct {
	Model             string
	ToolApprovalMode  string
	MaxSteps          int
	QueueMode         string
	QueueCap          int
	QueueDrop         string
	PairingEnabled    bool
	PairingTTL        time.Duration
	PairingMaxPending int
	// ModelResolver 校验模型引用是否可解析且已配置（provider 存在、模型
	// 存在、API key 已配）。/model 切换前调用：校验失败则不写入覆盖，
	// 保留当前 controller 可继续聊天（失败原子性）。nil 时跳过预校验。
	ModelResolver func(ref string) error
	// IgnoreSelfMessages drops messages that are clearly sent by this bot. It
	// uses configured SelfUserIDs plus recently returned outbound message IDs.
	IgnoreSelfMessages bool
	SelfUserIDs        map[Platform][]string
	ControlEnabled     bool
	ControlAddr        string
	ControlToken       string
	// ApprovalTimeout bounds how long a tool-approval/ask prompt blocks a bot
	// session waiting for a remote user's reply. Zero falls back to
	// defaultBotApprovalTimeout so an abandoned prompt can't wedge the bot forever
	// (#4626, #4402). A negative value disables the timeout (wait indefinitely).
	ApprovalTimeout    time.Duration
	WorkspaceRoot      string
	Channels           map[Platform]ChannelConfig
	ConnectionChannels map[string]ChannelConfig
	Routes             []RouteConfig
	ConnectionAccess   map[string]AccessConfig
	Allowlist          AllowlistConfig
	Enabled            map[Platform]bool
	Debounce           time.Duration
	// OnInbound observes every allowlisted inbound message before dispatch.
	//
	// Reentrancy contract for all GatewayConfig callbacks (OnInbound,
	// OnSessionReady, OnToolApprovalModeChange): they run synchronously on
	// gateway-owned dispatch/turn goroutines; OnSessionReady can also run on a
	// controller recovery/autosave goroutine. Stop drains all of those paths
	// before returning. A callback must therefore never call Stop, nor block
	// until a goroutine that does so completes — Stop would wait on the very
	// goroutine running the callback, a guaranteed deadlock. Hosts that want to
	// shut the gateway down in reaction to a callback must trigger the shutdown
	// asynchronously.
	OnInbound func(InboundMessage)
	// OnSessionReady notifies the host after the bot has created, reused, or
	// recovered the controller for an inbound remote. Hosts may persist the
	// concrete session ID or keep the remote as a read-only channel.
	OnSessionReady func(InboundMessage, string) error
	// OnToolApprovalModeChange persists a remote IM request such as /yolo on.
	// The gateway updates the live session and in-memory defaults first; this
	// callback lets desktop save the chosen connection mode to user config.
	OnToolApprovalModeChange func(InboundMessage, string) error
	// Desktop, when the gateway is embedded in the desktop app, gives bot
	// chats a god view over desktop sessions (/desktop commands): global
	// status, event subscriptions, and remote approvals for any live desktop
	// session. Nil when the gateway runs standalone (reasonix bot start).
	Desktop DesktopBridge
}

// ChannelConfig overrides gateway defaults for one IM channel.
type ChannelConfig struct {
	Model            string
	ToolApprovalMode string
	WorkspaceRoot    string
	SessionMappings  []SessionMapping
}

// SessionMapping is the runtime subset of a saved bot connection mapping used
// to route a remote chat/user/thread back to its intended workspace.
type SessionMapping struct {
	RemoteID      string
	SessionID     string
	SessionSource string
	ChatType      string
	UserID        string
	ThreadID      string
	Scope         string
	WorkspaceRoot string
	UpdatedAt     string
}

// RouteConfig applies per-remote overrides. Empty match fields are wildcards;
// the first matching route wins.
type RouteConfig struct {
	ConnectionID string
	Platform     Platform
	ChatType     ChatType
	ChatID       string
	UserID       string
	ThreadID     string
	Channel      ChannelConfig
}

// AdapterBinding attaches an adapter instance to one saved bot connection.
// Feishu and Lark share PlatformFeishu, so ID/Domain keep their sessions,
// replies, and per-connection settings separated at runtime.
type AdapterBinding struct {
	ID       string
	Domain   string
	Platform Platform
	Adapter  Adapter
}

// AllowlistConfig 控制哪些用户/群可以使用 bot。
type AllowlistConfig struct {
	Enabled   bool
	AllowAll  bool
	Users     map[Platform][]string
	Approvers map[Platform][]string
	Admins    map[Platform][]string
	Groups    map[Platform][]string
}

// AccessConfig controls who may use one concrete bot connection.
type AccessConfig struct {
	Enabled        bool
	AllowAll       bool
	PairingEnabled bool
	Users          []string
	Groups         []string
	Approvers      []string
	Admins         []string
}

// AdapterHealthSnapshot describes the gateway's current view of one adapter.
type AdapterHealthSnapshot struct {
	ID            string    `json:"id"`
	Platform      Platform  `json:"platform"`
	Domain        string    `json:"domain,omitempty"`
	Name          string    `json:"name,omitempty"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	LastMessageAt time.Time `json:"last_message_at,omitempty"`
	LastSendAt    time.Time `json:"last_send_at,omitempty"`
	LastErrorAt   time.Time `json:"last_error_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	Messages      int64     `json:"messages"`
	Sends         int64     `json:"sends"`
	SendErrors    int64     `json:"send_errors"`
	Closed        bool      `json:"closed"`
}

// BotGateway 是 reasonix bot 消息网关，管理 Controller 生命周期、session 并发、
// 事件渲染和平台适配器。
type BotGateway struct {
	cfg      GatewayConfig
	adapters []AdapterBinding
	sessions *SessionManager
	startErr []error

	lifecycleMu sync.Mutex
	started     bool
	stopped     bool
	runCancel   context.CancelFunc
	startDone   chan struct{}
	stopDone    chan struct{}
	gatewayWG   sync.WaitGroup
	turnWG      sync.WaitGroup

	mu                      sync.Mutex
	controllers             map[string]*sessionState // session key -> active state
	pendingReactionCleanups map[string][]func()
	allowlist               map[Platform]map[string]bool
	groupAllowlist          map[Platform]map[string]bool
	selfUserIDs             map[Platform]map[string]bool
	outboundMessageIDs      map[string]time.Time
	adapterHealth           map[string]*AdapterHealthSnapshot
	controlServer           *controlHTTPServer
	sessionOverrides        map[string]sessionRuntimeOverride
	buildController         func(context.Context, boot.Options) (*control.Controller, error)

	logger *slog.Logger
}

// botController is the slice of the controller's driving port the gateway needs:
// session lifecycle, turn execution, and approval/ask handling. The bot never
// touches goals, checkpoints, or memory, so it depends on those sub-ports only —
// not the concrete *control.Controller and its ~99 methods.
type botController interface {
	control.Lifecycle
	control.TurnControl
	control.Approvals
}

type sessionState struct {
	lifecycleMu         sync.Mutex
	retired             bool
	ctrl                botController
	sink                *sessionEventSink
	leases              *control.SessionLeaseKeeper
	platform            Platform
	connectionID        string
	model               string
	workspaceRoot       string
	toolApprovalMode    string
	sessionPath         string
	onSessionTransition func(control.SessionTransitionInfo) error
	// mappingDegraded records that this state intentionally runs on a fresh
	// session because its session_mappings target could not be used at build
	// time. It keeps later messages (whose profile re-resolves the mapping)
	// from tearing the state down every turn; convergence back onto the
	// mapped file happens on the next gateway restart.
	mappingDegraded  bool
	cancel           context.CancelFunc
	pendingAsks      map[string][]event.AskQuestion
	pendingApprovals map[string]event.Approval
	lastApprovalID   string
	lastAskID        string
	createdAt        time.Time
	lastActive       time.Time
}

var errBotSessionRetired = errors.New("bot session retired during recovery")

type sessionRuntimeProfile struct {
	model            string
	workspaceRoot    string
	toolApprovalMode string
	sessionPath      string
	// sessionPathOptional marks sessionPath as a persisted session_mappings
	// binding rather than an explicit /attach: when the mapped file cannot be
	// loaded or leased, the session degrades to a fresh path instead of
	// dropping the message (#6917).
	sessionPathOptional bool
}

type sessionRuntimeOverride struct {
	channel     ChannelConfig
	sessionPath string
	label       string
}

type sessionEventSink struct {
	mu     sync.RWMutex
	target event.Sink
}

type pendingReactionAdapter interface {
	AddPendingReaction(ctx context.Context, messageID string) (func(), error)
}

const outboundEchoTTL = 10 * time.Minute

func (s *sessionEventSink) setTarget(target event.Sink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.target = target
}

func (s *sessionEventSink) Emit(e event.Event) {
	s.mu.RLock()
	target := s.target
	s.mu.RUnlock()
	if target != nil {
		target.Emit(e)
	}
}

// NewGateway 创建一个新的 BotGateway。
func NewGateway(cfg GatewayConfig, adapters map[Platform]Adapter, logger *slog.Logger) *BotGateway {
	bindings := make([]AdapterBinding, 0, len(adapters))
	for plat, adapter := range adapters {
		bindings = append(bindings, AdapterBinding{ID: string(plat), Platform: plat, Adapter: adapter})
	}
	return NewGatewayWithAdapterBindings(cfg, bindings, logger)
}

// NewGatewayWithAdapterBindings creates a gateway with one or more adapter
// instances per platform.
func NewGatewayWithAdapterBindings(cfg GatewayConfig, adapters []AdapterBinding, logger *slog.Logger) *BotGateway {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = 1500 * time.Millisecond
	}
	cfg.QueueMode = NormalizeQueueMode(cfg.QueueMode)
	if cfg.QueueCap <= 0 {
		cfg.QueueCap = DefaultQueueCap
	}
	cfg.QueueDrop = NormalizeQueueDrop(cfg.QueueDrop)
	if cfg.PairingTTL <= 0 {
		cfg.PairingTTL = defaultPairingTTL
	}
	if cfg.PairingMaxPending <= 0 {
		cfg.PairingMaxPending = defaultPairingMaxPending
	}
	gw := &BotGateway{
		cfg:                     cfg,
		adapters:                normalizeAdapterBindings(adapters),
		sessions:                NewSessionManager(cfg.Debounce),
		controllers:             make(map[string]*sessionState),
		pendingReactionCleanups: make(map[string][]func()),
		allowlist:               make(map[Platform]map[string]bool),
		groupAllowlist:          make(map[Platform]map[string]bool),
		selfUserIDs:             make(map[Platform]map[string]bool),
		outboundMessageIDs:      make(map[string]time.Time),
		adapterHealth:           make(map[string]*AdapterHealthSnapshot),
		sessionOverrides:        make(map[string]sessionRuntimeOverride),
		buildController:         boot.Build,
		logger:                  logger.With("component", "bot_gateway"),
	}
	gw.buildAllowlist()
	gw.buildSelfUserIDs()
	for _, binding := range gw.adapters {
		gw.setAdapterConfigured(binding)
	}
	return gw
}

func normalizeAdapterBindings(adapters []AdapterBinding) []AdapterBinding {
	out := make([]AdapterBinding, 0, len(adapters))
	for _, binding := range adapters {
		if binding.Adapter == nil {
			continue
		}
		if binding.Platform == "" {
			binding.Platform = binding.Adapter.Platform()
		}
		if strings.TrimSpace(binding.ID) == "" {
			binding.ID = string(binding.Platform)
		}
		binding.ID = strings.TrimSpace(binding.ID)
		binding.Domain = strings.TrimSpace(binding.Domain)
		out = append(out, binding)
	}
	return out
}

func (gw *BotGateway) buildAllowlist() {
	for _, plat := range []Platform{PlatformQQ, PlatformFeishu, PlatformWeixin, PlatformDingtalk} {
		gw.allowlist[plat] = make(map[string]bool)
		if !gw.cfg.Allowlist.Enabled {
			continue
		}
		addAllowlistUsers(gw.allowlist[plat], gw.cfg.Allowlist.Users[plat])
		addAllowlistUsers(gw.allowlist[plat], gw.cfg.Allowlist.Admins[plat])
		addAllowlistUsers(gw.allowlist[plat], gw.cfg.Allowlist.Approvers[plat])
		gw.groupAllowlist[plat] = make(map[string]bool)
		for _, gid := range gw.cfg.Allowlist.Groups[plat] {
			gw.groupAllowlist[plat][gid] = true
		}
	}
}

func addAllowlistUsers(dst map[string]bool, users []string) {
	for _, uid := range users {
		uid = strings.TrimSpace(uid)
		if uid != "" {
			dst[uid] = true
		}
	}
}

func (gw *BotGateway) buildSelfUserIDs() {
	for _, plat := range []Platform{PlatformQQ, PlatformFeishu, PlatformWeixin, PlatformDingtalk} {
		gw.selfUserIDs[plat] = stringSet(gw.cfg.SelfUserIDs[plat])
	}
}

// Start 启动所有已启用的平台适配器并开始处理消息。
func (gw *BotGateway) Start(ctx context.Context) (err error) {
	gw.lifecycleMu.Lock()
	if gw.stopped {
		gw.lifecycleMu.Unlock()
		return errors.New("bot gateway already stopped")
	}
	if gw.started {
		gw.lifecycleMu.Unlock()
		return errors.New("bot gateway already started")
	}
	gw.started = true
	runCtx, cancel := context.WithCancel(ctx)
	gw.runCancel = cancel
	startDone := make(chan struct{})
	gw.startDone = startDone
	gw.lifecycleMu.Unlock()
	defer func() {
		if err != nil {
			cancel()
		}
		gw.lifecycleMu.Lock()
		if err != nil {
			gw.runCancel = nil
		}
		close(startDone)
		gw.lifecycleMu.Unlock()
	}()

	started := make([]AdapterBinding, 0, len(gw.adapters))
	var startErr []error
	for _, binding := range gw.adapters {
		if !gw.cfg.Enabled[binding.Platform] {
			gw.logger.Info("platform disabled, skipping", "platform", binding.Platform, "connection", binding.ID)
			gw.markAdapterDisabled(binding)
			continue
		}
		gw.logger.Info("starting adapter", "platform", binding.Platform, "connection", binding.ID, "domain", binding.Domain)
		if err := binding.Adapter.Start(runCtx); err != nil {
			wrapped := fmt.Errorf("start adapter %s: %w", binding.ID, err)
			startErr = append(startErr, wrapped)
			gw.markAdapterStartFailed(binding, err)
			gw.logger.Warn("adapter start failed", "platform", binding.Platform, "connection", binding.ID, "domain", binding.Domain, "err", err)
			continue
		}
		gw.markAdapterStarted(binding)
		started = append(started, binding)
	}
	// SendToAdapter reads gw.adapters under gw.mu; publish the started set under
	// the same lock.
	gw.mu.Lock()
	gw.adapters = started
	gw.startErr = startErr
	gw.mu.Unlock()
	if len(started) == 0 && len(startErr) > 0 {
		return errors.Join(startErr...)
	}
	if err := gw.startControlServer(runCtx); err != nil {
		for _, binding := range started {
			_ = binding.Adapter.Stop()
		}
		return err
	}

	// 合并所有适配器的消息通道
	for _, binding := range gw.adapters {
		gw.gatewayWG.Go(func() {
			gw.dispatchLoop(runCtx, binding)
		})
	}

	return nil
}

func (gw *BotGateway) AdapterCount() int {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return len(gw.adapters)
}

func (gw *BotGateway) StartErrors() []error {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	out := make([]error, len(gw.startErr))
	copy(out, gw.startErr)
	return out
}

// AdapterHealth returns a stable snapshot of all configured adapter instances.
func (gw *BotGateway) AdapterHealth() []AdapterHealthSnapshot {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	out := make([]AdapterHealthSnapshot, 0, len(gw.adapterHealth))
	for _, health := range gw.adapterHealth {
		if health == nil {
			continue
		}
		out = append(out, *health)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (gw *BotGateway) setAdapterConfigured(binding AdapterBinding) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	gw.ensureAdapterHealthLocked(binding).Status = "configured"
}

func (gw *BotGateway) markAdapterDisabled(binding AdapterBinding) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	health := gw.ensureAdapterHealthLocked(binding)
	health.Status = "disabled"
	health.Closed = true
}

func (gw *BotGateway) markAdapterStarted(binding AdapterBinding) {
	now := time.Now()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	health := gw.ensureAdapterHealthLocked(binding)
	health.Status = "running"
	health.StartedAt = now
	health.LastError = ""
	health.Closed = false
}

func (gw *BotGateway) markAdapterStartFailed(binding AdapterBinding, err error) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	health := gw.ensureAdapterHealthLocked(binding)
	health.Status = "error"
	health.Closed = true
	health.LastErrorAt = time.Now()
	if err != nil {
		health.LastError = err.Error()
	}
}

func (gw *BotGateway) markAdapterMessage(binding AdapterBinding) {
	now := time.Now()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	health := gw.ensureAdapterHealthLocked(binding)
	health.Status = "running"
	health.LastMessageAt = now
	health.Messages++
	health.Closed = false
}

func (gw *BotGateway) markAdapterClosed(binding AdapterBinding) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	health := gw.ensureAdapterHealthLocked(binding)
	if health.Status == "running" {
		health.Status = "closed"
	}
	health.Closed = true
}

func (gw *BotGateway) markAdapterSend(binding AdapterBinding, err error) {
	now := time.Now()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	health := gw.ensureAdapterHealthLocked(binding)
	if err != nil {
		health.SendErrors++
		health.LastErrorAt = now
		health.LastError = err.Error()
		if health.Status == "running" {
			health.Status = "degraded"
		}
		return
	}
	health.Sends++
	health.LastSendAt = now
	if health.Status == "degraded" {
		health.Status = "running"
	}
}

func (gw *BotGateway) ensureAdapterHealthLocked(binding AdapterBinding) *AdapterHealthSnapshot {
	id := strings.TrimSpace(binding.ID)
	if id == "" && binding.Adapter != nil {
		id = binding.Adapter.Name()
	}
	if id == "" {
		id = string(binding.Platform)
	}
	health := gw.adapterHealth[id]
	if health == nil {
		health = &AdapterHealthSnapshot{ID: id}
		gw.adapterHealth[id] = health
	}
	health.Platform = binding.Platform
	health.Domain = strings.TrimSpace(binding.Domain)
	if binding.Adapter != nil {
		health.Name = binding.Adapter.Name()
	}
	if strings.TrimSpace(health.Status) == "" {
		health.Status = "configured"
	}
	return health
}

// Stop 停止所有适配器并关闭所有 session。它会等待 dispatch 与 turn goroutine
// 全部退出，所以绝不能在 GatewayConfig 回调里同步调用（见 OnInbound 的
// reentrancy contract），否则 Stop 会等待正在运行该回调的 goroutine 自己。
func (gw *BotGateway) Stop() {
	gw.lifecycleMu.Lock()
	if gw.stopped {
		stopDone := gw.stopDone
		gw.lifecycleMu.Unlock()
		if stopDone != nil {
			<-stopDone
		}
		return
	}
	gw.stopped = true
	stopDone := make(chan struct{})
	gw.stopDone = stopDone
	cancel := gw.runCancel
	gw.runCancel = nil
	startDone := gw.startDone
	gw.lifecycleMu.Unlock()
	defer close(stopDone)

	if cancel != nil {
		cancel()
	}
	if startDone != nil {
		<-startDone
	}

	// Cancel sessions that already exist before waiting for dispatch to drain.
	// A dispatch already inside handleMessage may still publish a late session,
	// so closeSessions is repeated after gatewayWG and turnWG reach zero.
	gw.closeSessions()
	for _, binding := range gw.adapters {
		if err := binding.Adapter.Stop(); err != nil {
			gw.logger.Warn("error stopping adapter", "platform", binding.Platform, "connection", binding.ID, "err", err)
		}
		gw.markAdapterClosed(binding)
	}
	gw.stopControlServer()
	gw.gatewayWG.Wait()
	gw.closeSessions()
	gw.turnWG.Wait()
	gw.closeSessions()
}

func (gw *BotGateway) closeSessions() {
	var states []*sessionState
	gw.mu.Lock()
	for key, state := range gw.controllers {
		states = append(states, state)
		delete(gw.controllers, key)
	}
	gw.mu.Unlock()
	for _, state := range states {
		gw.closeSessionState(state)
	}
}

// closeSessionState tears down a session state that has been unlinked from
// gw.controllers. runTurn publishes state.cancel under gw.mu on every turn —
// possibly after the state was already unlinked — so snapshot and clear the
// field inside the lock and invoke it outside (the same discipline as
// cancelActiveSession).
func (gw *BotGateway) closeSessionState(state *sessionState) {
	if state == nil {
		return
	}
	// Serialize retirement with recovery ownership handoffs. Stop unlinks
	// sessions before turn goroutines drain, so a recovery callback captured by
	// the controller can still arrive here. Marking the state retired under the
	// same lock prevents that callback from reacquiring a lease after teardown;
	// an already-running handoff completes before the lease is released below.
	state.lifecycleMu.Lock()
	if state.retired {
		state.lifecycleMu.Unlock()
		return
	}
	state.retired = true
	state.lifecycleMu.Unlock()

	gw.mu.Lock()
	cancel := state.cancel
	state.cancel = nil
	gw.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if state.ctrl != nil {
		state.ctrl.Close()
	}
	if state.leases != nil {
		state.leases.Release()
	}
}

// unlinkAndCloseSessionState removes state from the live gateway before closing
// it. It is used when a controller has already rotated its transcript but the
// replacement lease could not be acquired: retaining that state would let the
// next message reuse a controller that no longer owns its active session path.
func (gw *BotGateway) unlinkAndCloseSessionState(key string, state *sessionState) {
	if state == nil {
		return
	}
	gw.mu.Lock()
	if gw.controllers[key] == state {
		delete(gw.controllers, key)
	}
	gw.mu.Unlock()
	gw.closeSessionState(state)
}

func (gw *BotGateway) dispatchLoop(ctx context.Context, binding AdapterBinding) {
	for {
		select {
		case <-ctx.Done():
			gw.markAdapterClosed(binding)
			return
		case msg, ok := <-binding.Adapter.Messages():
			if !ok {
				gw.markAdapterClosed(binding)
				return
			}
			gw.markAdapterMessage(binding)
			gw.handleMessage(ctx, binding, msg)
		}
	}
}

func (gw *BotGateway) handleMessage(ctx context.Context, binding AdapterBinding, msg InboundMessage) {
	msg.Platform = binding.Platform
	if msg.ConnectionID == "" {
		msg.ConnectionID = binding.ID
	}
	if msg.Domain == "" {
		msg.Domain = binding.Domain
	}
	if gw.isSelfMessage(msg) {
		gw.logger.Debug("bot ignored self message", "platform", binding.Platform, "connection", msg.ConnectionID, "chat", hashID(msg.ChatID), "message", hashID(msg.MessageID), "user", hashID(msg.UserID))
		return
	}
	src := msg.Session()
	key := BuildSessionKey(src)
	logFields := []any{
		"platform", binding.Platform,
		"connection", msg.ConnectionID,
		"domain", msg.Domain,
		"chat_type", msg.ChatType,
		"chat", hashID(msg.ChatID),
		"user", hashID(msg.UserID),
		"operator", hashID(msg.OperatorID),
		"thread", hashID(msg.ThreadID),
		"message", hashID(msg.MessageID),
		"text_chars", len([]rune(msg.Text)),
		"session", key[:8],
	}
	gw.logger.Info("bot inbound message", logFields...)

	// allowlist 检查
	if !gw.checkAllowlist(binding.Platform, msg) {
		gw.logger.Info("user not in allowlist", "platform", binding.Platform, "connection", msg.ConnectionID, "user", hashID(msg.UserID))
		if gw.offerPairing(ctx, binding.Adapter, msg) {
			return
		}
		_ = gw.sendText(ctx, binding.Adapter, msg, "抱歉，您没有使用此 bot 的权限。")
		return
	}
	if gw.cfg.OnInbound != nil {
		gw.cfg.OnInbound(msg)
	}

	if normalized, ok := gw.normalizeApprovalShortcut(key, msg.Text); ok {
		msg.Text = normalized
	} else if normalized, ok := gw.normalizeAskShortcut(key, msg.Text); ok {
		msg.Text = normalized
	} else if _, ok := decisionShortcutCommand(msg.Text); ok && gw.sessions.IsActive(key) {
		_ = gw.sendText(ctx, binding.Adapter, msg, "没有找到可匹配的待处理操作。请重新触发一次操作后回复编号，或按消息中的 ID 使用 /approve、/deny 或 /answer。")
		return
	}

	// 斜杠命令处理
	if IsSlashBypass(msg.Text) {
		gw.logger.Info("bot slash command", logFields...)
		gw.handleSlashCommand(ctx, binding.Adapter, key, msg)
		return
	}

	// 已接管桌面会话的聊天：普通消息直接驱动那个桌面会话，不进 bot 自己的
	// 会话机器（斜杠命令仍走上面的分支，/desktop release 永远可达）。
	if gw.divertToDesktopTakeover(ctx, binding.Adapter, msg) {
		gw.logger.Info("bot message diverted to desktop takeover", logFields...)
		return
	}

	cleanup := gw.addPendingReaction(ctx, binding.Platform, binding.Adapter, msg)

	queueMode := gw.queueMode(key, msg)
	warnDeprecatedQueueDrop(gw.cfg.QueueDrop)
	if gw.sessions.IsActive(key) {
		// Busy session: durable inbox is the authority (not SessionManager.pending).
		if IsSlashBypass(msg.Text) {
			// Slash commands still acquire through the session lock below.
		} else {
			switch queueMode {
			case QueueModeSteer:
				if rec, ok := gw.steerActiveSessionDurable(ctx, binding.Adapter, key, msg); ok {
					gw.logger.Info("bot message steered into active turn", "session", key[:8], "item", rec.ItemID)
					if cleanup != nil {
						cleanup()
					}
					_ = gw.sendText(ctx, binding.Adapter, msg, formatQueuedReceipt(rec)+"（已并入当前任务）")
					return
				}
			case QueueModeInterrupt:
				gw.cancelActiveSession(key)
				runReactionCleanups(gw.takeReactionCleanups(key))
				rec, err := gw.interruptActiveSessionDurable(ctx, binding.Adapter, key, msg)
				gw.storeReactionCleanup(key, cleanup)
				if err != nil {
					gw.logger.Warn("bot interrupt enqueue failed", "session", key[:8], "err", err)
					_ = gw.sendText(ctx, binding.Adapter, msg, "排队失败："+err.Error())
					return
				}
				gw.logger.Info("bot active turn interrupted; newest message durable-queued", "session", key[:8], "item", rec.ItemID)
				_ = gw.sendText(ctx, binding.Adapter, msg, "已停止当前任务。"+formatQueuedReceipt(rec))
				return
			case QueueModeCollect:
				if rec, err := gw.collectActiveSessionDurable(ctx, binding.Adapter, key, msg); err == nil {
					gw.storeReactionCleanup(key, cleanup)
					_ = gw.sendText(ctx, binding.Adapter, msg, formatQueuedReceipt(rec))
					return
				} else if errors.Is(err, sessioninbox.ErrCapacityItems) || errors.Is(err, sessioninbox.ErrCapacityBytes) || errors.Is(err, sessioninbox.ErrItemTooLarge) {
					if cleanup != nil {
						cleanup()
					}
					_ = gw.sendText(ctx, binding.Adapter, msg, "当前会话排队已满，请稍后再发，或使用 /queue pause 后清理。")
					return
				}
			default: // followup
				if rec, err := gw.followupActiveSessionDurable(ctx, binding.Adapter, key, msg); err == nil {
					gw.storeReactionCleanup(key, cleanup)
					_ = gw.sendText(ctx, binding.Adapter, msg, formatQueuedReceipt(rec))
					return
				} else if errors.Is(err, sessioninbox.ErrCapacityItems) || errors.Is(err, sessioninbox.ErrCapacityBytes) || errors.Is(err, sessioninbox.ErrItemTooLarge) {
					if cleanup != nil {
						cleanup()
					}
					_ = gw.sendText(ctx, binding.Adapter, msg, "当前会话排队已满，请稍后再发。")
					return
				}
			}
		}
	}

	// session 并发控制 — only the active-turn lock remains here; bodies live in inbox.
	result := gw.sessions.TryAcquireWithQueue(key, msg, QueueOptions{
		Mode: QueueModeFollowup, // never drop_old; capacity enforced by inbox
		Cap:  sessioninbox.DefaultMaxItems,
		Drop: QueueDropNew,
	})
	if result.Rejected {
		gw.logger.Warn("bot queue rejected message", "session", key[:8], "pending", result.Pending, "mode", result.Mode)
		if cleanup != nil {
			cleanup()
		}
		_ = gw.sendText(ctx, binding.Adapter, msg, "当前会话排队已满，请稍后再发，或使用 /queue 管理队列。")
		return
	}
	gw.dispatchQueueResult(ctx, binding.Adapter, key, msg, cleanup, result)
}

func (gw *BotGateway) queueMode(key string, msg InboundMessage) string {
	return gw.sessions.QueueMode(key, gw.cfg.QueueMode)
}

func (gw *BotGateway) sessionAPI(key string) control.SessionAPI {
	gw.mu.Lock()
	state, ok := gw.controllers[key]
	gw.mu.Unlock()
	if !ok || state == nil || state.ctrl == nil {
		return nil
	}
	if api, ok := state.ctrl.(control.SessionAPI); ok {
		return api
	}
	return nil
}

func (gw *BotGateway) steerActiveSessionDurable(ctx context.Context, adapter Adapter, key string, msg InboundMessage) (sessioninbox.InboxReceipt, bool) {
	text := strings.TrimSpace(msg.Text)
	if text == "" && len(msg.MediaURLs) == 0 && len(msg.Media) == 0 {
		return sessioninbox.InboxReceipt{}, false
	}
	gw.mu.Lock()
	state, ok := gw.controllers[key]
	gw.mu.Unlock()
	if !ok || state.ctrl == nil {
		return sessioninbox.InboxReceipt{}, false
	}
	msg = gw.prepareDurableInboxMessage(ctx, adapter, msg, state)
	text = msg.Text
	if strings.TrimSpace(text) == "" {
		return sessioninbox.InboxReceipt{}, false
	}
	msg.Text = text
	api, ok := state.ctrl.(control.SessionAPI)
	if !ok {
		// Legacy fallback.
		if steerer, ok := state.ctrl.(interface{ TrySteer(string) bool }); ok && steerer.TrySteer(text) {
			return sessioninbox.InboxReceipt{Disposition: sessioninbox.DispositionSteerAccepted}, true
		}
		return sessioninbox.InboxReceipt{}, false
	}
	rec, err := enqueueViaInbox(api, msg, sessioninbox.IntentSteer)
	if err != nil {
		return sessioninbox.InboxReceipt{}, false
	}
	return rec, true
}

func (gw *BotGateway) cancelActiveSession(key string) {
	// state.cancel is rewritten under gw.mu on every turn (runTurn), so copy it
	// inside the lock and invoke it outside.
	var cancel context.CancelFunc
	gw.mu.Lock()
	state, ok := gw.controllers[key]
	if ok && state != nil {
		cancel = state.cancel
	}
	gw.mu.Unlock()
	if !ok || state == nil {
		return
	}
	if cancel != nil {
		cancel()
		return
	}
	if state.ctrl != nil {
		state.ctrl.Cancel()
	}
}

func (gw *BotGateway) storeReactionCleanup(key string, cleanup func()) {
	if cleanup == nil {
		return
	}
	gw.mu.Lock()
	defer gw.mu.Unlock()
	gw.pendingReactionCleanups[key] = append(gw.pendingReactionCleanups[key], cleanup)
}

func (gw *BotGateway) flushReactionCleanups(key string, cleanup func()) {
	stored := gw.takeReactionCleanups(key)
	runReactionCleanups(stored)
	if cleanup != nil {
		cleanup()
	}
}

func (gw *BotGateway) takeReactionCleanups(key string) []func() {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	stored := gw.pendingReactionCleanups[key]
	delete(gw.pendingReactionCleanups, key)
	return stored
}

func runReactionCleanups(cleanups []func()) {
	for _, cleanup := range cleanups {
		if cleanup != nil {
			cleanup()
		}
	}
}

func makeReactionCleanup(cleanups []func()) func() {
	if len(cleanups) == 0 {
		return nil
	}
	return func() {
		runReactionCleanups(cleanups)
	}
}

func (gw *BotGateway) addPendingReaction(ctx context.Context, plat Platform, adapter Adapter, msg InboundMessage) func() {
	if strings.TrimSpace(msg.MessageID) == "" {
		return nil
	}
	reactor, ok := adapter.(pendingReactionAdapter)
	if !ok {
		return nil
	}
	cleanup, err := reactor.AddPendingReaction(ctx, msg.MessageID)
	if err != nil {
		gw.logger.Warn("pending reaction failed", "platform", plat, "err", err)
		return nil
	}
	return cleanup
}

func (gw *BotGateway) isSelfMessage(msg InboundMessage) bool {
	if !gw.cfg.IgnoreSelfMessages {
		return false
	}
	actor := strings.TrimSpace(msg.UserID)
	if strings.TrimSpace(msg.OperatorID) != "" {
		actor = strings.TrimSpace(msg.OperatorID)
	}
	if actor != "" && gw.selfUserIDs[msg.Platform][actor] {
		return true
	}
	messageID := strings.TrimSpace(msg.MessageID)
	if messageID == "" {
		return false
	}
	key := outboundMessageKey(msg.Platform, msg.ConnectionID, msg.Domain, msg.ChatID, messageID)
	now := time.Now()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	gw.pruneOutboundMessagesLocked(now)
	_, ok := gw.outboundMessageIDs[key]
	return ok
}

func (gw *BotGateway) rememberOutboundMessage(platform Platform, connID, domain, chatID, messageID string) {
	messageID = strings.TrimSpace(messageID)
	if !gw.cfg.IgnoreSelfMessages || messageID == "" {
		return
	}
	now := time.Now()
	key := outboundMessageKey(platform, connID, domain, chatID, messageID)
	gw.mu.Lock()
	defer gw.mu.Unlock()
	gw.pruneOutboundMessagesLocked(now)
	gw.outboundMessageIDs[key] = now.Add(outboundEchoTTL)
}

func (gw *BotGateway) pruneOutboundMessagesLocked(now time.Time) {
	for key, expiresAt := range gw.outboundMessageIDs {
		if !expiresAt.After(now) {
			delete(gw.outboundMessageIDs, key)
		}
	}
}

func outboundMessageKey(platform Platform, connID, domain, chatID, messageID string) string {
	return strings.Join([]string{
		string(platform),
		strings.TrimSpace(connID),
		strings.TrimSpace(domain),
		strings.TrimSpace(chatID),
		strings.TrimSpace(messageID),
	}, "\x00")
}

func (gw *BotGateway) connectionAccess(msg InboundMessage) (AccessConfig, bool) {
	if gw.cfg.ConnectionAccess == nil {
		return AccessConfig{}, false
	}
	id := strings.TrimSpace(msg.ConnectionID)
	if id == "" {
		return AccessConfig{}, false
	}
	access, ok := gw.cfg.ConnectionAccess[id]
	if !ok {
		return AccessConfig{}, false
	}
	if !accessConfigActive(access) {
		return AccessConfig{}, false
	}
	return access, true
}

func accessConfigActive(access AccessConfig) bool {
	return access.Enabled ||
		access.AllowAll ||
		access.PairingEnabled ||
		len(access.Users) > 0 ||
		len(access.Groups) > 0 ||
		len(access.Approvers) > 0 ||
		len(access.Admins) > 0
}

func (gw *BotGateway) checkAllowlist(plat Platform, msg InboundMessage) bool {
	if access, ok := gw.connectionAccess(msg); ok {
		return checkConnectionAllowlist(access, msg)
	}
	if gw.cfg.Allowlist.AllowAll {
		return true
	}
	if !gw.cfg.Allowlist.Enabled {
		return false
	}
	actor := msg.UserID
	if msg.OperatorID != "" {
		actor = msg.OperatorID
	}
	if !gw.allowlist[plat][actor] {
		return false
	}
	groups := gw.groupAllowlist[plat]
	if chatUsesGroupAllowlist(msg.ChatType) && len(groups) > 0 && !groups[msg.ChatID] {
		return false
	}
	return true
}

func checkConnectionAllowlist(access AccessConfig, msg InboundMessage) bool {
	if access.AllowAll {
		return true
	}
	if !access.Enabled {
		return false
	}
	actor := msg.UserID
	if msg.OperatorID != "" {
		actor = msg.OperatorID
	}
	users := stringSet(append(append(append([]string{}, access.Users...), access.Admins...), access.Approvers...))
	groups := stringSet(access.Groups)
	actorAllowed := users[actor]
	groupAllowed := chatUsesGroupAllowlist(msg.ChatType) && groups[msg.ChatID]
	if len(users) == 0 && len(groups) == 0 {
		return false
	}
	return actorAllowed || groupAllowed
}

func (gw *BotGateway) requireCommandRole(ctx context.Context, adapter Adapter, msg InboundMessage, role string) bool {
	if gw.checkCommandRole(msg.Platform, msg, role) {
		return true
	}
	_ = gw.sendText(ctx, adapter, msg, "抱歉，你没有执行此 bot 命令的权限。")
	return false
}

func (gw *BotGateway) checkCommandRole(plat Platform, msg InboundMessage, role string) bool {
	actor := msg.UserID
	if msg.OperatorID != "" {
		actor = msg.OperatorID
	}
	if strings.TrimSpace(actor) == "" {
		return false
	}
	if access, ok := gw.connectionAccess(msg); ok {
		admins := stringSet(access.Admins)
		approvers := stringSet(access.Approvers)
		if len(admins) == 0 && len(approvers) == 0 {
			return true
		}
		if admins[actor] {
			return true
		}
		return role == "approver" && approvers[actor]
	}
	admins := stringSet(gw.cfg.Allowlist.Admins[plat])
	approvers := stringSet(gw.cfg.Allowlist.Approvers[plat])
	if len(admins) == 0 && len(approvers) == 0 {
		return true
	}
	if admins[actor] {
		return true
	}
	if role == "approver" && approvers[actor] {
		return true
	}
	return false
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func (gw *BotGateway) offerPairing(ctx context.Context, adapter Adapter, msg InboundMessage) bool {
	if access, ok := gw.connectionAccess(msg); ok {
		if !access.PairingEnabled {
			return false
		}
	} else if !gw.cfg.PairingEnabled {
		return false
	}
	req, created, err := CreateOrRefreshPairingRequest(msg, PairingConfig{
		Enabled:               true,
		RequestTTL:            gw.cfg.PairingTTL,
		MaxPendingPerPlatform: gw.cfg.PairingMaxPending,
	})
	if err != nil {
		gw.logger.Warn("bot pairing request failed", "platform", msg.Platform, "chat_type", msg.ChatType, "err", err)
		return false
	}
	prefix := "需要先完成配对。"
	if !created {
		prefix = "你已有待批准的配对请求。"
	}
	text := fmt.Sprintf("%s\n配对码: %s\n请在本机运行: reasonix bot pairing approve %s\n此码将在 %s 过期。",
		prefix, req.Code, req.Code, req.ExpiresAt.Local().Format("2006-01-02 15:04"))
	_ = gw.sendText(ctx, adapter, msg, text)
	return true
}

func chatUsesGroupAllowlist(chatType ChatType) bool {
	switch chatType {
	case ChatGroup, ChatGuild, ChatThread:
		return true
	default:
		return false
	}
}

func approvalShortcutCommand(text string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "1", "y", "yes", "ok", "同意", "批准", "允许", "允许一次":
		return "/approve", true
	case "2", "0", "n", "no", "deny", "拒绝":
		return "/deny", true
	default:
		return "", false
	}
}

func recoveryShortcutCommand(text string, canGrantTask bool) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "1", "y", "yes", "ok", "继续", "继续此变更", "continue":
		return "/recovery-continue", true
	case "2", "a", "同类", "本任务允许", "allow similar":
		if canGrantTask {
			return "/recovery-continue-task", true
		}
		return "/recovery-revise", true
	case "3":
		if canGrantTask {
			return "/recovery-revise", true
		}
		return "", false
	case "修改", "修改方案", "换个办法", "revise":
		return "/recovery-revise", true
	default:
		return "", false
	}
}

func (gw *BotGateway) pendingRecoveryCanGrantTask(key, id string) bool {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	state, ok := gw.controllers[key]
	if !ok || state.pendingApprovals == nil {
		return false
	}
	a, ok := state.pendingApprovals[id]
	return ok && a.Recovery != nil && a.Recovery.CanGrantTask
}

func (gw *BotGateway) pendingApprovalIsRecovery(key, id string) bool {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	state, ok := gw.controllers[key]
	if !ok || state.pendingApprovals == nil {
		return false
	}
	a, ok := state.pendingApprovals[id]
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(a.Kind), "recovery") || a.Recovery != nil
}

func decisionShortcutCommand(text string) (string, bool) {
	if command, ok := approvalShortcutCommand(text); ok {
		return command, true
	}
	if _, ok := askShortcutAnswer(text); ok {
		return "/answer", true
	}
	return "", false
}

func (gw *BotGateway) currentPendingApprovalID(key string) string {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	state, ok := gw.controllers[key]
	if !ok || len(state.pendingApprovals) == 0 {
		return ""
	}
	if state.lastApprovalID != "" {
		if _, ok := state.pendingApprovals[state.lastApprovalID]; ok {
			return state.lastApprovalID
		}
	}
	for id := range state.pendingApprovals {
		return id
	}
	return ""
}

func (gw *BotGateway) forgetPendingApproval(key, id string) {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	state, ok := gw.controllers[key]
	if !ok || state.pendingApprovals == nil {
		return
	}
	delete(state.pendingApprovals, id)
	if state.lastApprovalID == id {
		state.lastApprovalID = ""
		for nextID := range state.pendingApprovals {
			state.lastApprovalID = nextID
			break
		}
	}
}

func (gw *BotGateway) normalizeAskShortcut(key, text string) (string, bool) {
	raw := strings.TrimSpace(text)
	if raw == "" || strings.HasPrefix(raw, "/") {
		return "", false
	}
	askID := gw.currentPendingAskIDForReply(key)
	if askID == "" {
		return "", false
	}
	return "/answer " + askID + " " + raw, true
}

func askShortcutAnswer(text string) (string, bool) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return "", false
	}
	if strings.ContainsAny(raw, " \t\n;=") {
		return "", false
	}
	if _, err := strconv.Atoi(raw); err == nil {
		return raw, true
	}
	return "", false
}

func (gw *BotGateway) currentPendingAskIDForReply(key string) string {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	state, ok := gw.controllers[key]
	if !ok || len(state.pendingAsks) == 0 {
		return ""
	}
	if state.lastAskID != "" {
		if _, ok := state.pendingAsks[state.lastAskID]; ok {
			return state.lastAskID
		}
	}
	if len(state.pendingAsks) != 1 {
		return ""
	}
	for id := range state.pendingAsks {
		return id
	}
	return ""
}

func (gw *BotGateway) handleSlashCommandCore(ctx context.Context, adapter Adapter, key string, msg InboundMessage) {
	switch {
	case strings.HasPrefix(msg.Text, "/stop"):
		var cancel context.CancelFunc
		gw.mu.Lock()
		if state, ok := gw.controllers[key]; ok {
			cancel = state.cancel
		}
		gw.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		gw.sessions.ForceRelease(key)
		_ = gw.sendText(ctx, adapter, msg, "已停止当前任务。")

	case strings.HasPrefix(msg.Text, "/new") || strings.HasPrefix(msg.Text, "/reset"):
		var cancel context.CancelFunc
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		if ok {
			cancel = state.cancel
		}
		gw.mu.Unlock()
		if ok {
			if cancel != nil {
				cancel()
			}
			// NewSession refuses to rotate while a turn is running; the cancel
			// above is asynchronous, so give the turn a bounded window to
			// unwind before rotating.
			deadline := time.Now().Add(5 * time.Second)
			for state.ctrl.Running() && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if err := state.ctrl.NewSession(); err != nil {
				gw.logger.Warn("new session failed", "err", err)
				gw.sessions.ForceRelease(key)
				_ = gw.sendText(ctx, adapter, msg, "新会话创建失败，请稍后重试。")
				return
			}
			if state.leases != nil {
				if err := rebindBotSessionWriteAuthority(state, state.ctrl.SessionPath()); err != nil {
					gw.logger.Warn("new session lease failed", "err", control.SessionInUseMessage(err))
					gw.unlinkAndCloseSessionState(key, state)
					gw.sessions.ForceRelease(key)
					_ = gw.sendText(ctx, adapter, msg, "新会话创建失败：无法取得写入权限。请关闭其他 Reasonix 窗口或进程后重试。")
					return
				}
			}
			// /new 后把旋转出的新路径钉为会话覆盖，避免下一条消息重新解析回旧路径。
			gw.mu.Lock()
			if gw.controllers[key] == state {
				rotated := state.ctrl.SessionPath()
				state.sessionPath = rotated
				override, exists := gw.sessionOverrides[key]
				if !exists {
					override = sessionRuntimeOverride{}
				}
				override.sessionPath = rotated
				gw.sessionOverrides[key] = override
			}
			gw.mu.Unlock()
			gw.rememberSessionReady(msg, state.ctrl)
		}
		gw.sessions.ForceRelease(key)
		_ = gw.sendText(ctx, adapter, msg, "已开始新会话。")

	case strings.HasPrefix(msg.Text, "/approve"):
		if !gw.requireCommandRole(ctx, adapter, msg, "approver") {
			return
		}
		// 从消息中解析 approval ID
		parts := strings.Fields(msg.Text)
		if len(parts) < 2 {
			_ = gw.sendText(ctx, adapter, msg, "用法: /approve <id>")
			return
		}
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if ok && state.ctrl != nil {
			// Recovery cards map allow → continue for older clients that only know Approve.
			if gw.pendingApprovalIsRecovery(key, parts[1]) {
				_ = state.ctrl.ResolveRecovery(parts[1], agent.RecoveryActionContinue, "")
			} else {
				state.ctrl.Approve(parts[1], true, false, false)
			}
			gw.forgetPendingApproval(key, parts[1])
			_ = gw.sendText(ctx, adapter, msg, "已批准。")
		} else {
			_ = gw.sendText(ctx, adapter, msg, "没有找到当前会话中的待审批操作，请重新触发一次操作。")
		}

	case strings.HasPrefix(msg.Text, "/deny"):
		if !gw.requireCommandRole(ctx, adapter, msg, "approver") {
			return
		}
		parts := strings.Fields(msg.Text)
		if len(parts) < 2 {
			_ = gw.sendText(ctx, adapter, msg, "用法: /deny <id>")
			return
		}
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if ok && state.ctrl != nil {
			if gw.pendingApprovalIsRecovery(key, parts[1]) {
				_ = state.ctrl.ResolveRecovery(parts[1], agent.RecoveryActionRevise, "")
			} else {
				state.ctrl.Approve(parts[1], false, false, false)
			}
			gw.forgetPendingApproval(key, parts[1])
			_ = gw.sendText(ctx, adapter, msg, "已拒绝。")
		} else {
			_ = gw.sendText(ctx, adapter, msg, "没有找到当前会话中的待审批操作，请重新触发一次操作。")
		}

	case strings.HasPrefix(msg.Text, "/recovery-continue-task"):
		if !gw.requireCommandRole(ctx, adapter, msg, "approver") {
			return
		}
		parts := strings.Fields(msg.Text)
		if len(parts) < 2 {
			_ = gw.sendText(ctx, adapter, msg, "用法: /recovery-continue-task <id>")
			return
		}
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if ok && state.ctrl != nil {
			if err := state.ctrl.ResolveRecovery(parts[1], agent.RecoveryActionContinueTask, ""); err != nil {
				_ = gw.sendText(ctx, adapter, msg, "确认失败: "+err.Error())
				return
			}
			gw.forgetPendingApproval(key, parts[1])
			_ = gw.sendText(ctx, adapter, msg, "已继续；本任务内同类操作将自动执行，范围扩大或风险升级仍会确认。")
		} else {
			_ = gw.sendText(ctx, adapter, msg, "没有找到当前会话中的待确认操作。")
		}

	case strings.HasPrefix(msg.Text, "/recovery-continue"):
		if !gw.requireCommandRole(ctx, adapter, msg, "approver") {
			return
		}
		parts := strings.Fields(msg.Text)
		if len(parts) < 2 {
			_ = gw.sendText(ctx, adapter, msg, "用法: /recovery-continue <id>")
			return
		}
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if ok && state.ctrl != nil {
			if err := state.ctrl.ResolveRecovery(parts[1], agent.RecoveryActionContinue, ""); err != nil {
				_ = gw.sendText(ctx, adapter, msg, "确认失败: "+err.Error())
				return
			}
			gw.forgetPendingApproval(key, parts[1])
			_ = gw.sendText(ctx, adapter, msg, "已继续。")
		} else {
			_ = gw.sendText(ctx, adapter, msg, "没有找到当前会话中的待确认操作。")
		}

	case strings.HasPrefix(msg.Text, "/recovery-revise"):
		if !gw.requireCommandRole(ctx, adapter, msg, "approver") {
			return
		}
		parts := strings.Fields(msg.Text)
		if len(parts) < 2 {
			_ = gw.sendText(ctx, adapter, msg, "用法: /recovery-revise <id> [补充要求]")
			return
		}
		feedback := strings.TrimSpace(strings.Join(parts[2:], " "))
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if ok && state.ctrl != nil {
			if err := state.ctrl.ResolveRecovery(parts[1], agent.RecoveryActionRevise, feedback); err != nil {
				_ = gw.sendText(ctx, adapter, msg, "修改方案失败: "+err.Error())
				return
			}
			gw.forgetPendingApproval(key, parts[1])
			_ = gw.sendText(ctx, adapter, msg, "已拒绝当前变更并注入修改要求。")
		} else {
			_ = gw.sendText(ctx, adapter, msg, "没有找到当前会话中的恢复检查点。")
		}

	case strings.HasPrefix(msg.Text, "/recovery-stop"):
		// Backward compatibility for cards rendered by an older client: reject
		// the proposed mutation but leave task cancellation to ordinary /stop.
		if !gw.requireCommandRole(ctx, adapter, msg, "approver") {
			return
		}
		parts := strings.Fields(msg.Text)
		if len(parts) < 2 {
			_ = gw.sendText(ctx, adapter, msg, "用法: /recovery-stop <id>")
			return
		}
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		gw.mu.Unlock()
		if ok && state.ctrl != nil {
			if err := state.ctrl.ResolveRecovery(parts[1], agent.RecoveryActionRevise, "cancel this proposed action"); err != nil {
				_ = gw.sendText(ctx, adapter, msg, "取消变更失败: "+err.Error())
				return
			}
			gw.forgetPendingApproval(key, parts[1])
			_ = gw.sendText(ctx, adapter, msg, "已取消当前变更；如需停止整个任务，请使用 /stop。")
		} else {
			_ = gw.sendText(ctx, adapter, msg, "没有找到当前会话中的恢复检查点。")
		}

	case strings.HasPrefix(msg.Text, "/answer"):
		parts := strings.Fields(msg.Text)
		if len(parts) < 3 {
			_ = gw.sendText(ctx, adapter, msg, "用法: /answer <id> <选项或 q1=选项;q2=选项>")
			return
		}
		askID := parts[1]
		rawAnswer := strings.TrimSpace(strings.Join(parts[2:], " "))
		gw.mu.Lock()
		state, ok := gw.controllers[key]
		var questions []event.AskQuestion
		if ok {
			questions = state.pendingAsks[askID]
			delete(state.pendingAsks, askID)
			if state.lastAskID == askID {
				state.lastAskID = ""
				for nextID := range state.pendingAsks {
					state.lastAskID = nextID
					break
				}
			}
		}
		gw.mu.Unlock()
		if !ok || state.ctrl == nil {
			_ = gw.sendText(ctx, adapter, msg, "没有找到当前会话。")
			return
		}
		answers := parseAskAnswers(questions, rawAnswer)
		state.ctrl.AnswerQuestion(askID, answers)
		_ = gw.sendText(ctx, adapter, msg, "已提交回答。")

	case strings.HasPrefix(msg.Text, "/yolo") || strings.HasPrefix(msg.Text, "/mode"):
		if !gw.requireCommandRole(ctx, adapter, msg, "admin") {
			return
		}
		mode, statusOnly, ok := parseToolApprovalModeCommand(msg.Text)
		if !ok {
			_ = gw.sendText(ctx, adapter, msg, "用法: /yolo on|off|auto|status，或 /mode yolo|ask|auto")
			return
		}
		if statusOnly {
			_ = gw.sendText(ctx, adapter, msg, gw.toolApprovalModeStatusText(key, msg))
			return
		}
		persistErr := gw.setToolApprovalModeForMessage(key, msg, mode)
		text := toolApprovalModeChangedText(mode)
		if persistErr != nil {
			text += "\n当前会话已生效，但保存到设置失败：" + persistErr.Error()
		}
		_ = gw.sendText(ctx, adapter, msg, text)

	case strings.HasPrefix(msg.Text, "/queue"):
		if reply, handled, kick := gw.handleQueueInboxCommand(ctx, key, msg); handled {
			_ = gw.sendText(ctx, adapter, msg, reply)
			if kick {
				gw.kickInbox(ctx, adapter, key, msg)
			}
			return
		}
		mode, clear, statusOnly, ok := parseQueueCommand(msg.Text)
		if !ok {
			_ = gw.sendText(ctx, adapter, msg, "用法: /queue steer|followup|collect|interrupt|status|list|show|delete|move|pause|resume|retry|default")
			return
		}
		if statusOnly {
			_ = gw.sendText(ctx, adapter, msg, gw.queueStatusText(key, msg))
			return
		}
		if clear {
			gw.sessions.ClearQueueMode(key)
			_ = gw.sendText(ctx, adapter, msg, "已恢复默认队列模式："+queueModeLabel(gw.queueMode(key, msg))+"。")
			return
		}
		gw.sessions.SetQueueMode(key, mode)
		_ = gw.sendText(ctx, adapter, msg, "已切换队列模式："+queueModeLabel(mode)+"。")

	case slashCommandVerb(msg.Text) == "/projects":
		if !gw.requireCommandRole(ctx, adapter, msg, "admin") {
			return
		}
		query := strings.TrimSpace(strings.TrimPrefix(msg.Text, "/projects"))
		_ = gw.sendText(ctx, adapter, msg, formatBotProjects(gw.buildProjectIndex(), query, botProjectListLimit))

	case slashCommandVerb(msg.Text) == "/use":
		if !gw.requireCommandRole(ctx, adapter, msg, "admin") {
			return
		}
		_ = gw.sendText(ctx, adapter, msg, gw.handleUseProjectCommand(ctx, msg, msg.Text))

	case slashCommandVerb(msg.Text) == "/model":
		if !gw.requireCommandRole(ctx, adapter, msg, "admin") {
			return
		}
		_ = gw.sendText(ctx, adapter, msg, gw.handleModelCommand(ctx, msg, msg.Text))

	case slashCommandVerb(msg.Text) == "/sessions":
		if !gw.requireCommandRole(ctx, adapter, msg, "admin") {
			return
		}
		_ = gw.sendText(ctx, adapter, msg, gw.handleSessionsCommand(msg.Text))

	case slashCommandVerb(msg.Text) == "/attach":
		if !gw.requireCommandRole(ctx, adapter, msg, "admin") {
			return
		}
		_ = gw.sendText(ctx, adapter, msg, gw.handleAttachSessionCommand(ctx, msg, msg.Text))

	case slashCommandVerb(msg.Text) == "/search":
		if !gw.requireCommandRole(ctx, adapter, msg, "admin") {
			return
		}
		_ = gw.sendText(ctx, adapter, msg, gw.handleProjectSearchCommand(ctx, msg.Text))

	case strings.HasPrefix(msg.Text, "/desktop"):
		// God view over the embedding desktop app: listing every live desktop
		// session and answering its approvals is strictly more power than the
		// per-session approver role, so gate on admin.
		if !gw.requireCommandRole(ctx, adapter, msg, "admin") {
			return
		}
		_ = gw.sendText(ctx, adapter, msg, gw.handleDesktopCommand(msg))

	case strings.HasPrefix(msg.Text, "/status"):
		active := gw.sessions.ActiveCount()
		pending := gw.sessions.PendingCount(key)
		gw.mu.Lock()
		sessions := len(gw.controllers)
		gw.mu.Unlock()
		mode := gw.currentToolApprovalMode(key, msg)
		_ = gw.sendText(ctx, adapter, msg, fmt.Sprintf("活跃任务数: %d\n保留会话数: %d\n工具审批模式: %s\n队列模式: %s\n当前会话排队: %d\n连接健康: %s", active, sessions, toolApprovalModeLabel(mode), queueModeLabel(gw.queueMode(key, msg)), pending, gw.adapterHealthSummaryText()))

	case strings.HasPrefix(msg.Text, "/help"):
		_ = gw.sendText(ctx, adapter, msg, botHelpText())
	}
}

func (gw *BotGateway) kickInbox(ctx context.Context, adapter Adapter, key string, fallback InboundMessage) {
	if gw.sessions.IsActive(key) {
		return
	}
	next := gw.nextInboxTurn(key, fallback)
	if next == nil {
		return
	}
	if !gw.sessions.TryAcquireIdle(key) {
		return
	}
	gw.turnWG.Go(func() {
		gw.runTurnItem(ctx, adapter, key, next.msg, next.itemID, nil)
	})
}

func slashCommandVerb(text string) string {
	parts := strings.Fields(strings.TrimSpace(text))
	if len(parts) == 0 {
		return ""
	}
	return strings.ToLower(parts[0])
}

func (gw *BotGateway) handleUseProjectCommand(ctx context.Context, msg InboundMessage, text string) string {
	key := BuildSessionKey(msg.Session())
	selector := parseUseProjectSelector(text)
	if selector == "" {
		return "用法: /use project <项目 id|名称|路径>，或 /use project default 恢复默认路由。"
	}
	if isDefaultBotSelector(selector) {
		switched, err := gw.setSessionRuntimeOverride(ctx, key, msg, sessionRuntimeOverride{}, false)
		if err != nil {
			return botRuntimeSwitchFailedText("切换项目")
		}
		if !switched {
			return botRuntimeSwitchBusyText()
		}
		return "已恢复当前远端会话的默认项目路由。下一条消息会按 bot 配置重新选择 workspace。"
	}
	projects := gw.buildProjectIndex()
	project, matches := resolveBotProject(projects, selector)
	if project.Root == "" {
		if len(matches) > 0 {
			return "匹配到多个项目，请使用项目 id：\n" + formatBotProjects(matches, "", botProjectListLimit)
		}
		return "没有匹配的项目。可先用 /projects 查看当前索引。"
	}
	switched, err := gw.setSessionRuntimeOverride(ctx, key, msg, sessionRuntimeOverride{
		channel: ChannelConfig{WorkspaceRoot: project.Root},
		label:   "project:" + project.ID,
	}, true)
	if err != nil {
		return botRuntimeSwitchFailedText("切换项目")
	}
	if !switched {
		return botRuntimeSwitchBusyText()
	}
	return fmt.Sprintf("已将当前远端会话切到项目 %s %s。\n下一条消息将在 %s 中运行。", project.ID, project.Name, displayBotPath(project.Root))
}

// handleModelCommand 处理 /model：无参查询当前会话生效模型，带参切换当前
// 远端会话的模型（可选 --provider <name> 一并切换供应商）。模型以
// provider/model 写入会话运行时覆盖，仅影响当前会话。
func (gw *BotGateway) handleModelCommand(ctx context.Context, msg InboundMessage, text string) string {
	model, provider, statusOnly, ok := parseModelSelector(text)
	if !ok {
		return "用法: /model <模型名> [--provider <供应商>]，或 /model 查看当前模型。"
	}
	if statusOnly {
		// 查询会话生效模型：走完整解析（覆盖 → 通道/路由 → 全局默认），否则
		// per-channel/per-connection 的模型设置（如钉钉直配 model）会被漏报。
		effective, _, _ := gw.sessionOptionsForMessage(msg)
		if strings.TrimSpace(effective) == "" {
			return "当前会话未指定模型，使用 bot 默认模型。"
		}
		return fmt.Sprintf("当前会话模型：%s", effective)
	}
	if strings.TrimSpace(model) == "" && strings.TrimSpace(provider) != "" {
		// 仅 provider 无模型名会存成无法解析的 "provider/" 空模型，下一条消息
		// 构建会话失败；要求显式模型名。
		return "用法: /model <模型名> [--provider <供应商>]，或 /model 查看当前模型。"
	}
	key := BuildSessionKey(msg.Session())
	ref := strings.TrimSpace(model)
	if provider != "" {
		ref = strings.TrimSpace(provider) + "/" + ref
	}
	// 失败原子性：先校验模型可解析且已配置，无效则直接拒绝并保留当前
	// controller，不写入覆盖、不销毁旧会话（否则下一条消息构建失败）。
	if gw.cfg.ModelResolver != nil {
		if err := gw.cfg.ModelResolver(ref); err != nil {
			return fmt.Sprintf("模型 %s 不可用：%v", ref, err)
		}
	}
	// 复用 /use 的会话覆盖机制：只改 model，保留现有 workspace/tool 覆盖。
	var existing sessionRuntimeOverride
	gw.mu.Lock()
	existing = gw.sessionOverrides[key]
	gw.mu.Unlock()
	existing.channel.Model = ref
	switched, err := gw.setSessionRuntimeOverride(ctx, key, msg, existing, true)
	if err != nil {
		return botRuntimeSwitchFailedText("切换模型")
	}
	if !switched {
		return botRuntimeSwitchBusyText()
	}
	if provider != "" {
		return fmt.Sprintf("已将当前会话模型切换到 %s（供应商 %s）。", model, provider)
	}
	return fmt.Sprintf("已将当前会话模型切换到 %s。", ref)
}

func parseModelSelector(text string) (model, provider string, statusOnly, ok bool) {
	parts := strings.Fields(text)
	if len(parts) == 0 || strings.ToLower(strings.TrimSpace(parts[0])) != "/model" {
		return "", "", false, false
	}
	if len(parts) == 1 {
		return "", "", true, true
	}
	rest := parts[1:]
	var models, providers []string
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if strings.EqualFold(tok, "--provider") || strings.EqualFold(tok, "-p") {
			if i+1 < len(rest) {
				providers = append(providers, rest[i+1])
				i++
			}
			continue
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		models = append(models, tok)
	}
	if len(models) == 0 {
		return "", strings.Join(providers, " "), false, true
	}
	return strings.Join(models, " "), strings.Join(providers, " "), false, true
}

func parseUseProjectSelector(text string) string {
	parts := strings.Fields(text)
	if len(parts) < 2 || strings.ToLower(parts[0]) != "/use" {
		return ""
	}
	if len(parts) >= 3 && strings.EqualFold(parts[1], "project") {
		return strings.TrimSpace(strings.Join(parts[2:], " "))
	}
	return strings.TrimSpace(strings.Join(parts[1:], " "))
}

func (gw *BotGateway) handleSessionsCommand(text string) string {
	query := parseSessionsQuery(text)
	projects := gw.buildProjectIndex()
	sessions := gw.buildSessionIndex(projects)
	return formatBotSessions(sessions, query, botSessionListLimit)
}

func parseSessionsQuery(text string) string {
	parts := strings.Fields(text)
	if len(parts) <= 1 {
		return ""
	}
	if strings.EqualFold(parts[1], "search") {
		return strings.TrimSpace(strings.Join(parts[2:], " "))
	}
	return strings.TrimSpace(strings.Join(parts[1:], " "))
}

func (gw *BotGateway) handleAttachSessionCommand(ctx context.Context, msg InboundMessage, text string) string {
	key := BuildSessionKey(msg.Session())
	selector := parseAttachSessionSelector(text)
	if selector == "" {
		return "用法: /attach session <会话 id|关键词|path:...>"
	}
	projects := gw.buildProjectIndex()
	sessions := gw.buildSessionIndex(projects)
	session, matches := resolveBotSession(sessions, selector)
	if session.ID == "" {
		if len(matches) > 0 {
			return "匹配到多个会话，请使用会话 id：\n" + formatBotSessions(matches, "", botSessionListLimit)
		}
		return "没有匹配的会话。可先用 /sessions search <关键词> 查看当前索引。"
	}
	if session.SessionPath == "" {
		return "这个会话没有可恢复的 path: transcript，暂时不能 attach。"
	}
	if info, err := os.Stat(session.SessionPath); err != nil || info.IsDir() {
		return "会话文件不可用或已被移动：" + displayBotPath(session.SessionPath)
	}
	workspaceRoot := session.WorkspaceRoot
	if workspaceRoot == "" {
		project := botProjectForPath(projects, session.SessionPath)
		workspaceRoot = project.Root
	}
	switched, err := gw.setSessionRuntimeOverride(ctx, key, msg, sessionRuntimeOverride{
		channel:     ChannelConfig{WorkspaceRoot: workspaceRoot},
		sessionPath: session.SessionPath,
		label:       "session:" + session.ID,
	}, true)
	if err != nil {
		return botRuntimeSwitchFailedText("attach")
	}
	if !switched {
		return botRuntimeSwitchBusyText()
	}
	projectName := firstNonEmptyString(session.ProjectName, botProjectName(workspaceRoot), "global")
	return fmt.Sprintf("已 attach 到会话 %s（%s）。\n下一条消息会从 %s 继续。", session.ID, projectName, displayBotPath(session.SessionPath))
}

func parseAttachSessionSelector(text string) string {
	parts := strings.Fields(text)
	if len(parts) < 3 || !strings.EqualFold(parts[0], "/attach") || !strings.EqualFold(parts[1], "session") {
		return ""
	}
	return strings.TrimSpace(strings.Join(parts[2:], " "))
}

func (gw *BotGateway) handleProjectSearchCommand(ctx context.Context, text string) string {
	parts := strings.Fields(text)
	if len(parts) < 3 || !strings.EqualFold(parts[1], "all") {
		return "用法: /search all <关键词>"
	}
	query := strings.TrimSpace(strings.Join(parts[2:], " "))
	searchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	results, err := searchBotProjects(searchCtx, gw.buildProjectIndex(), query, botSearchListLimit)
	if err != nil {
		return "检索失败：" + err.Error()
	}
	return formatBotProjectSearchResults(results, botSearchListLimit)
}

func botSessionHasActiveWork(state *sessionState) bool {
	if state == nil || state.ctrl == nil {
		return false
	}
	status, ok := safeBotControllerRuntimeStatus(state.ctrl)
	if !ok {
		return true
	}
	return status.Running || status.PendingPrompt || status.BackgroundJobs > 0
}

func safeBotControllerRuntimeStatus(ctrl botController) (status control.RuntimeStatus, ok bool) {
	if ctrl == nil {
		return control.RuntimeStatus{}, false
	}
	defer func() {
		if recover() != nil {
			status = control.RuntimeStatus{}
			ok = false
		}
	}()
	return ctrl.RuntimeStatus(), true
}

func (gw *BotGateway) sessionRuntimeOverrideForMessage(msg InboundMessage) (sessionRuntimeOverride, bool) {
	key := BuildSessionKey(msg.Session())
	gw.mu.Lock()
	defer gw.mu.Unlock()
	override, ok := gw.sessionOverrides[key]
	return override, ok
}

func isDefaultBotSelector(selector string) bool {
	switch strings.ToLower(strings.TrimSpace(selector)) {
	case "default", "reset", "inherit", "global", "none", "默认", "重置":
		return true
	default:
		return false
	}
}

func parseQueueCommand(text string) (mode string, clear bool, statusOnly bool, ok bool) {
	parts := strings.Fields(text)
	if len(parts) == 0 || strings.ToLower(strings.TrimSpace(parts[0])) != "/queue" {
		return "", false, false, false
	}
	if len(parts) == 1 {
		return "", false, true, true
	}
	switch strings.ToLower(strings.TrimSpace(parts[1])) {
	case "status", "state", "show", "状态", "查看":
		return "", false, true, true
	case "default", "reset", "inherit", "默认", "重置":
		return "", true, false, true
	default:
		if normalized := NormalizeOptionalQueueMode(parts[1]); normalized != "" {
			return normalized, false, false, true
		}
		return "", false, false, false
	}
}

func (gw *BotGateway) queueStatusText(key string, msg InboundMessage) string {
	inboxN := 0
	paused := false
	if api := gw.sessionAPI(key); api != nil {
		snap := api.InboxSnapshot()
		inboxN = len(snap.Items)
		paused = snap.Paused
	}
	return fmt.Sprintf("当前队列模式：%s\n持久化 Inbox: %d%s\n全局上限: %d\n溢出策略: 拒绝新消息（queue_drop 已弃用）\n用法：/queue steer|followup|collect|interrupt|status|list|show|delete|move|pause|resume|retry|default",
		queueModeLabel(gw.queueMode(key, msg)),
		inboxN,
		map[bool]string{true: " (paused)", false: ""}[paused],
		sessioninbox.DefaultMaxItems,
	)
}

func queueModeLabel(mode string) string {
	switch NormalizeQueueMode(mode) {
	case QueueModeFollowup:
		return "逐条跟进"
	case QueueModeCollect:
		return "合并收集"
	case QueueModeInterrupt:
		return "打断重跑"
	default:
		return "即时补充"
	}
}

func (gw *BotGateway) adapterHealthSummaryText() string {
	snapshots := gw.AdapterHealth()
	if len(snapshots) == 0 {
		return "未启动"
	}
	parts := make([]string, 0, len(snapshots))
	for _, h := range snapshots {
		label := strings.TrimSpace(h.ID)
		if label == "" {
			label = string(h.Platform)
		}
		status := strings.TrimSpace(h.Status)
		if status == "" {
			status = "unknown"
		}
		parts = append(parts, fmt.Sprintf("%s=%s", label, status))
	}
	return strings.Join(parts, ", ")
}

func parseToolApprovalModeCommand(text string) (mode string, statusOnly bool, ok bool) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "", false, false
	}
	cmd := strings.ToLower(strings.TrimSpace(parts[0]))
	switch cmd {
	case "/yolo":
		if len(parts) == 1 {
			return control.ToolApprovalYolo, false, true
		}
		return parseToolApprovalModeArg(parts[1])
	case "/mode":
		if len(parts) == 1 {
			return "", true, true
		}
		return parseToolApprovalModeArg(parts[1])
	default:
		return "", false, false
	}
}

func parseToolApprovalModeArg(arg string) (mode string, statusOnly bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "status", "state", "show", "状态", "查看":
		return "", true, true
	case "on", "enable", "enabled", "true", "1", "yolo", "full", "full-access", "bypass", "开启", "打开":
		return control.ToolApprovalYolo, false, true
	case "off", "disable", "disabled", "false", "0", "ask", "询问", "关闭":
		return control.ToolApprovalAsk, false, true
	case "auto", "自动":
		return control.ToolApprovalAuto, false, true
	default:
		return "", false, false
	}
}

func (gw *BotGateway) setToolApprovalModeForMessage(key string, msg InboundMessage, mode string) error {
	mode = normalizeBotToolApprovalMode(mode)
	var ctrl botController

	gw.mu.Lock()
	if state, ok := gw.controllers[key]; ok {
		ctrl = state.ctrl
	}
	gw.updateToolApprovalModeDefaultLocked(msg, mode)
	gw.mu.Unlock()

	if ctrl != nil {
		ctrl.SetToolApprovalMode(mode)
	}
	if gw.cfg.OnToolApprovalModeChange != nil {
		return gw.cfg.OnToolApprovalModeChange(msg, mode)
	}
	return nil
}

func (gw *BotGateway) updateToolApprovalModeDefaultLocked(msg InboundMessage, mode string) {
	if id := strings.TrimSpace(msg.ConnectionID); id != "" {
		if gw.cfg.ConnectionChannels == nil {
			gw.cfg.ConnectionChannels = make(map[string]ChannelConfig)
		}
		channel := gw.cfg.ConnectionChannels[id]
		channel.ToolApprovalMode = mode
		gw.cfg.ConnectionChannels[id] = channel
		return
	}
	if msg.Platform != "" {
		if gw.cfg.Channels == nil {
			gw.cfg.Channels = make(map[Platform]ChannelConfig)
		}
		channel := gw.cfg.Channels[msg.Platform]
		channel.ToolApprovalMode = mode
		gw.cfg.Channels[msg.Platform] = channel
		return
	}
	gw.cfg.ToolApprovalMode = mode
}

func (gw *BotGateway) currentToolApprovalMode(key string, msg InboundMessage) string {
	var ctrl botController
	gw.mu.Lock()
	if state, ok := gw.controllers[key]; ok {
		ctrl = state.ctrl
	}
	gw.mu.Unlock()
	if ctrl != nil {
		return ctrl.ToolApprovalMode()
	}
	_, _, mode := gw.sessionOptionsForMessage(msg)
	return mode
}

func (gw *BotGateway) toolApprovalModeStatusText(key string, msg InboundMessage) string {
	mode := gw.currentToolApprovalMode(key, msg)
	return fmt.Sprintf("当前工具审批模式：%s\n用法：/yolo on|off|auto|status，或 /mode yolo|ask|auto", toolApprovalModeLabel(mode))
}

func toolApprovalModeChangedText(mode string) string {
	switch normalizeBotToolApprovalMode(mode) {
	case control.ToolApprovalYolo:
		return "已开启 YOLO：普通工具审批将自动放行；Ask 问题和计划批准仍会等待确认。"
	case control.ToolApprovalAuto:
		return "已切换为自动模式：策略允许的工具会自动放行，仍保留需要询问或拒绝的规则。"
	default:
		return "已切回询问模式：工具执行前会请求确认。"
	}
}

func toolApprovalModeLabel(mode string) string {
	switch normalizeBotToolApprovalMode(mode) {
	case control.ToolApprovalYolo:
		return "YOLO"
	case control.ToolApprovalAuto:
		return "自动"
	default:
		return "询问"
	}
}

func (gw *BotGateway) runTurn(ctx context.Context, adapter Adapter, key string, msg InboundMessage, cleanup func()) {
	gw.runTurnItem(ctx, adapter, key, msg, "", cleanup)
}

func (gw *BotGateway) runTurnItem(ctx context.Context, adapter Adapter, key string, msg InboundMessage, inboxItemID string, cleanup func()) {
	gw.logger.Info("bot turn started", "platform", msg.Platform, "chat_type", msg.ChatType, "chat", hashID(msg.ChatID), "session", key[:8])
	defer gw.finishTurnItem(ctx, adapter, key, msg, cleanup)

	state := gw.sessionForNewTurn(ctx, adapter, key, msg)
	if state == nil {
		return
	}
	gw.rememberSessionReady(msg, state.ctrl)

	// 构建输入文本：群聊中在消息前加上发送者名，并把 IM 媒体保存为 @附件引用。
	input := msg.Text
	if inboxItemID == "" {
		input = gw.inputTextWithMedia(ctx, adapter, msg, state)
	}
	if inboxItemID == "" && msg.ChatType == ChatGroup {
		userName := strings.TrimSpace(msg.UserName)
		if msg.ResolveUserName != nil {
			if resolved := strings.TrimSpace(msg.ResolveUserName(ctx)); resolved != "" {
				userName = resolved
			}
		}
		input = fmt.Sprintf("[%s] %s", userName, input)
	}

	// 发送"正在输入"状态
	_ = adapter.SendTyping(ctx, msg.ChatID)

	// 创建事件渲染 sink
	sink := newRenderSink(
		ctx,
		adapter,
		msg.ConnectionID,
		msg.Domain,
		msg.ChatID,
		msg.ChatType,
		msg.UserID,
		msg.MessageID,
		gw.logger,
		func(approval event.Approval) {
			gw.mu.Lock()
			if state.pendingApprovals == nil {
				state.pendingApprovals = make(map[string]event.Approval)
			}
			state.pendingApprovals[approval.ID] = approval
			state.lastApprovalID = approval.ID
			gw.mu.Unlock()
		},
		func(ask event.Ask) {
			gw.mu.Lock()
			if state.pendingAsks == nil {
				state.pendingAsks = make(map[string][]event.AskQuestion)
			}
			state.pendingAsks[ask.ID] = ask.Questions
			state.lastAskID = ask.ID
			gw.mu.Unlock()
		},
	)
	// Finish initializing the sink before publishing it as the live target: once
	// setTarget runs, other goroutines can reach this sink via state.sink.Emit.
	sink.ctrl = state.ctrl
	state.sink.setTarget(sink)
	defer state.sink.setTarget(nil)

	// 创建带取消的 context
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	gw.mu.Lock()
	live := gw.controllers[key] == state
	if live {
		state.cancel = cancel
	}
	state.lastActive = time.Now()
	gw.mu.Unlock()
	if !live {
		// The session was closed (gateway stop or runtime rebuild) after this
		// turn picked it up; a cancel published now would never be consumed, so
		// abort the turn instead of running it uncancellable.
		cancel()
	}

	// 运行一轮对话
	var err error
	if inboxItemID == "" {
		err = state.ctrl.RunTurn(turnCtx, input)
	} else if api, ok := state.ctrl.(interface {
		RunInboxTurn(context.Context, string) error
	}); ok {
		err = api.RunInboxTurn(turnCtx, inboxItemID)
	} else {
		err = fmt.Errorf("controller cannot run durable inbox item")
	}
	sink.Emit(event.Event{Kind: event.TurnDone, Err: err})
	if err != nil {
		gw.logger.Warn("turn error", "session", key[:8], "err", err)
		return
	}
	gw.logger.Info("bot turn completed", "platform", msg.Platform, "chat_type", msg.ChatType, "chat", hashID(msg.ChatID), "session", key[:8])
}

func (gw *BotGateway) inputTextWithMedia(ctx context.Context, adapter Adapter, msg InboundMessage, state *sessionState) string {
	input := msg.Text
	if len(msg.MediaURLs) == 0 && len(msg.Media) == 0 {
		return input
	}
	workspaceRoot := ""
	if state != nil && state.ctrl != nil {
		workspaceRoot = state.ctrl.WorkspaceRoot()
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		_, workspaceRoot, _ = gw.sessionOptionsForMessage(msg)
	}
	refs, errs := saveInboundMedia(ctx, workspaceRoot, msg.MediaURLs)
	itemRefs, fallbacks, itemErrs := saveInboundMediaItems(ctx, workspaceRoot, msg.Media)
	refs = append(refs, itemRefs...)
	errs = append(errs, itemErrs...)
	if len(errs) > 0 {
		gw.logger.Warn("bot media attachment failed", "platform", msg.Platform, "chat", hashID(msg.ChatID), "errors", len(errs))
		_ = gw.sendText(ctx, adapter, msg, fmt.Sprintf("有 %d 个附件保存失败；我会先处理可用内容。", len(errs)))
	}
	return appendMediaRefs(appendMediaFallbacks(input, fallbacks), refs)
}

func (gw *BotGateway) getOrCreateSession(ctx context.Context, key string, msg InboundMessage) *sessionState {
	profile := gw.sessionProfileForMessage(msg)
	var stale *sessionState
	gw.mu.Lock()
	if state, ok := gw.controllers[key]; ok {
		if !sessionStateMatchesRuntime(state, profile) {
			if botSessionHasActiveWork(state) {
				gw.mu.Unlock()
				safeBotSetToolApprovalMode(state.ctrl, profile.toolApprovalMode)
				gw.logger.Warn("bot session runtime change deferred while work is active", "platform", msg.Platform, "chat_type", msg.ChatType, "chat", hashID(msg.ChatID), "session", key[:8])
				return state
			}
			delete(gw.controllers, key)
			stale = state
			gw.mu.Unlock()
			gw.closeSessionState(stale)
			gw.logger.Warn("bot session runtime changed; rebuilding", "platform", msg.Platform, "chat_type", msg.ChatType, "chat", hashID(msg.ChatID), "session", key[:8], "old_workspace_set", strings.TrimSpace(stale.workspaceRoot) != "", "new_workspace_set", profile.workspaceRoot != "", "old_model", stale.model, "new_model", profile.model)
		} else {
			updateSessionStateRuntime(state, msg, profile)
			gw.mu.Unlock()
			safeBotSetToolApprovalMode(state.ctrl, profile.toolApprovalMode)
			gw.logger.Info("bot session reused", "platform", msg.Platform, "chat_type", msg.ChatType, "chat", hashID(msg.ChatID), "session", key[:8])
			return state
		}
	} else {
		gw.mu.Unlock()
	}

	// Create the lease owner first so recovery or intentional transitions can
	// move ownership before the controller commits to the target path.
	sessionSink := &sessionEventSink{}
	leases := control.NewSessionLeaseKeeper()
	state := &sessionState{
		sink:             sessionSink,
		leases:           leases,
		platform:         msg.Platform,
		connectionID:     strings.TrimSpace(msg.ConnectionID),
		model:            profile.model,
		workspaceRoot:    profile.workspaceRoot,
		toolApprovalMode: profile.toolApprovalMode,
		sessionPath:      profile.sessionPath,
		pendingAsks:      make(map[string][]event.AskQuestion),
		createdAt:        time.Now(),
		lastActive:       time.Now(),
	}
	state.onSessionTransition = gw.botSessionTransitionHandler(key, msg, state)
	gw.logger.Info("bot session creating", "platform", msg.Platform, "chat_type", msg.ChatType, "chat", hashID(msg.ChatID), "session", key[:8], "model", profile.model, "workspace_set", profile.workspaceRoot != "", "tool_approval_mode", profile.toolApprovalMode)
	ctrl, err := boot.Build(ctx, boot.Options{
		Model:               profile.model,
		MaxSteps:            gw.cfg.MaxSteps,
		MaxStepsKey:         "bot.max_steps",
		RequireKey:          true,
		Sink:                sessionSink,
		StatsSource:         "bot",
		WorkspaceRoot:       profile.workspaceRoot,
		SessionDir:          botSessionDir(profile.workspaceRoot),
		ApprovalTimeout:     gw.approvalTimeout(),
		OnSessionRecovered:  gw.botSessionRecoveredHandler(key, msg, state),
		OnSessionTransition: state.onSessionTransition,
	})
	if err != nil {
		leases.Release()
		gw.logger.Error("build controller failed", "err", secrets.RedactError(err))
		return nil
	}
	state.ctrl = ctrl
	if profile.sessionPath != "" {
		// A mapped binding degrades to a fresh session on failure; only an
		// explicit /attach is allowed to hard-fail the message, because the
		// user named that exact session.
		degrade := func(reason string, err error) bool {
			if !profile.sessionPathOptional {
				return false
			}
			gw.logger.Warn("mapped bot session unavailable; starting fresh", "reason", reason, "session_path", profile.sessionPath, "err", err)
			profile.sessionPath = ""
			state.sessionPath = ""
			state.mappingDegraded = true
			return true
		}
		if err := leases.Rebind(profile.sessionPath); err != nil {
			if !degrade("lease held elsewhere", err) {
				ctrl.Close()
				leases.Release()
				gw.logger.Error("attached bot session is in use", "err", control.SessionInUseMessage(err))
				return nil
			}
		} else if loaded, err := agent.LoadSession(profile.sessionPath); err != nil {
			if os.IsNotExist(err) && profile.sessionPathOptional {
				// First message on a deterministic chat→file path: pin the new
				// conversation there instead of orphaning a timestamp file.
				ctrl.SetSessionPath(profile.sessionPath)
			} else if !degrade("load failed", err) {
				ctrl.Close()
				leases.Release()
				if os.IsNotExist(err) {
					gw.logger.Error("attached bot session missing", "session_path", profile.sessionPath)
				} else {
					gw.logger.Error("attached bot session load failed", "session_path", profile.sessionPath, "err", err)
				}
				return nil
			}
		} else {
			ctrl.Resume(loaded, profile.sessionPath)
		}
	}
	ctrl.EnableInteractiveApproval()
	ctrl.SetToolApprovalMode(profile.toolApprovalMode)
	ctrl.EnsureSessionPath()
	if err := rebindBotSessionWriteAuthority(state, ctrl.SessionPath()); err != nil {
		ctrl.Close()
		leases.Release()
		gw.logger.Error("bot session lease failed", "err", control.SessionInUseMessage(err))
		return nil
	}
	var replace *sessionState
	gw.mu.Lock()
	// Re-check under the lock: while we were off-lock in boot.Build, a second
	// message for the same key may have built and registered its own session.
	// Reuse it only when it still targets this message's runtime profile.
	if existing, ok := gw.controllers[key]; ok {
		if sessionStateMatchesRuntime(existing, profile) {
			updateSessionStateRuntime(existing, msg, profile)
			gw.mu.Unlock()
			ctrl.Close()
			leases.Release()
			safeBotSetToolApprovalMode(existing.ctrl, profile.toolApprovalMode)
			gw.logger.Info("bot session built concurrently; discarding duplicate", "platform", msg.Platform, "chat", hashID(msg.ChatID), "session", key[:8])
			return existing
		}
		delete(gw.controllers, key)
		replace = existing
	}
	gw.controllers[key] = state
	gw.mu.Unlock()
	gw.closeSessionState(replace)

	gw.logger.Info("bot session created", "platform", msg.Platform, "chat_type", msg.ChatType, "chat", hashID(msg.ChatID), "session", key[:8])
	return state
}

func updateSessionStateRuntime(state *sessionState, msg InboundMessage, profile sessionRuntimeProfile) {
	if state == nil {
		return
	}
	if state.connectionID == "" {
		state.connectionID = strings.TrimSpace(msg.ConnectionID)
	}
	if state.platform == "" {
		state.platform = msg.Platform
	}
	state.model = profile.model
	state.workspaceRoot = profile.workspaceRoot
	state.toolApprovalMode = profile.toolApprovalMode
	state.sessionPath = profile.sessionPath
	state.lastActive = time.Now()
}

func (gw *BotGateway) sessionProfileForMessage(msg InboundMessage) sessionRuntimeProfile {
	override, enabled := gw.sessionRuntimeOverrideForMessage(msg)
	return gw.sessionProfileForResolvedOverride(msg, override, enabled)
}

func (gw *BotGateway) sessionProfileForResolvedOverride(msg InboundMessage, override sessionRuntimeOverride, enabled bool) sessionRuntimeProfile {
	model, workspaceRoot, toolApprovalMode := gw.sessionOptionsForResolvedOverride(msg, override, enabled)
	var sessionPath string
	sessionPathOptional := false
	if enabled {
		sessionPath = override.sessionPath
	} else {
		// After the gateway process restarts (desktop or machine reboot) the
		// in-memory override is gone: fall back to the persisted
		// session_mappings entry for this remote chat, equivalent to an
		// automatic /attach session on startup. The context then survives
		// process restarts until the user explicitly runs /new, /attach
		// session, or /use project.
		sessionPath = gw.savedSessionPathForMessage(msg)
	}
	// A persisted session_mappings binding is the durable chat→session link
	// the desktop writes into the connection config. Without consuming it
	// here, every gateway restart or runtime rebuild opened a brand-new
	// session file for the chat and the configured binding was display-only
	// (#6917, #6934).
	if sessionPath == "" {
		if mapped := gw.sessionMappingPathForMessage(msg); mapped != "" {
			sessionPath = mapped
			sessionPathOptional = true
		}
	}
	// No explicit binding: pin a deterministic per-chat file so the chat reuses
	// one conversation across restarts (dsh-dingtalk-channel's `ding-<chatId>`
	// analogue). Optional, mirroring mapping degrade semantics.
	if sessionPath == "" {
		if stable := BotSessionPathForChat(botSessionDir(workspaceRoot), msg.Session()); stable != "" {
			sessionPath = stable
			sessionPathOptional = true
		}
	}
	return sessionRuntimeProfile{
		model:               strings.TrimSpace(model),
		workspaceRoot:       strings.TrimSpace(workspaceRoot),
		toolApprovalMode:    normalizeBotToolApprovalMode(toolApprovalMode),
		sessionPath:         canonicalBotPath(sessionPath),
		sessionPathOptional: sessionPathOptional,
	}
}

// sessionMappingPathForMessage resolves the persisted session_mappings entry
// for a message to an existing session file. Only bindings that resolve to a
// present, readable file participate — a moved or deleted target quietly
// degrades to normal session creation rather than blocking the chat.
func (gw *BotGateway) sessionMappingPathForMessage(msg InboundMessage) string {
	gw.mu.Lock()
	var mappings []SessionMapping
	if msg.ConnectionID != "" {
		if channel, ok := gw.cfg.ConnectionChannels[msg.ConnectionID]; ok {
			mappings = channel.SessionMappings
		}
	}
	if len(mappings) == 0 {
		if channel, ok := gw.cfg.Channels[msg.Platform]; ok {
			mappings = channel.SessionMappings
		}
	}
	gw.mu.Unlock()
	mapping, ok := matchingSessionMapping(mappings, msg)
	if !ok {
		return ""
	}
	path := botSessionPathFromTarget(mapping.SessionID)
	if path == "" {
		path = botSessionPathFromTarget(mapping.SessionSource)
	}
	if path == "" {
		return ""
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	return path
}

// savedSessionPathForMessage restores the session file path this remote chat
// last used from the persisted session_mappings when no in-memory override
// exists. It returns an empty string unless the session file still exists and
// is not a directory, so a cleaned-up old session degrades to a fresh session
// instead of failing Resume and leaving the bot unresponsive for that chat.
// The file availability check matches /attach session.
func (gw *BotGateway) savedSessionPathForMessage(msg InboundMessage) string {
	gw.mu.Lock()
	defer gw.mu.Unlock()
	var mappings []SessionMapping
	if msg.ConnectionID != "" {
		if connChannel, ok := gw.cfg.ConnectionChannels[msg.ConnectionID]; ok {
			mappings = connChannel.SessionMappings
		}
	}
	if len(mappings) == 0 {
		if platChannel, ok := gw.cfg.Channels[msg.Platform]; ok {
			mappings = platChannel.SessionMappings
		}
	}
	mapping, ok := matchingSessionMapping(mappings, msg)
	if !ok {
		return ""
	}
	sessionPath := botSessionPathFromTarget(mapping.SessionID)
	if sessionPath == "" {
		return ""
	}
	if info, err := os.Stat(sessionPath); err != nil || info.IsDir() {
		return ""
	}
	return sessionPath
}

func sessionStateMatchesRuntime(state *sessionState, profile sessionRuntimeProfile) bool {
	if state == nil || state.ctrl == nil {
		return false
	}
	if stateModel := strings.TrimSpace(state.model); stateModel != "" && profile.model != "" && stateModel != profile.model {
		return false
	}
	stateRoot := strings.TrimSpace(state.workspaceRoot)
	wantRoot := strings.TrimSpace(profile.workspaceRoot)
	if stateRoot == "" {
		root, ok := safeBotControllerWorkspaceRoot(state.ctrl)
		if ok {
			stateRoot = strings.TrimSpace(root)
		} else if wantRoot != "" {
			return false
		}
	}
	if stateRoot != wantRoot {
		return false
	}
	// A state that already degraded off its mapped session keeps running on
	// its fresh path even though the profile re-resolves the mapping each
	// message; rebuilding here would spawn a new session per message while the
	// mapped file stays unavailable.
	if profile.sessionPathOptional && state.mappingDegraded {
		return true
	}
	if canonicalBotPath(state.sessionPath) != canonicalBotPath(profile.sessionPath) {
		return false
	}
	if profile.sessionPath != "" && canonicalBotPath(state.ctrl.SessionPath()) != canonicalBotPath(profile.sessionPath) {
		return false
	}
	return true
}

func safeBotControllerWorkspaceRoot(ctrl botController) (root string, ok bool) {
	if ctrl == nil {
		return "", false
	}
	defer func() {
		if recover() != nil {
			root = ""
			ok = false
		}
	}()
	return ctrl.WorkspaceRoot(), true
}

func safeBotSetToolApprovalMode(ctrl botController, mode string) {
	if ctrl == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	ctrl.SetToolApprovalMode(mode)
}

// defaultBotApprovalTimeout caps how long a bot session waits for a remote
// user's approval/ask reply before treating it as denied, so an abandoned
// prompt (or a dropped IM event) can't leave the session wedged forever
// (#4626, #4402). 30 minutes is generous for a human reply yet bounded.
const defaultBotApprovalTimeout = 30 * time.Minute

// approvalTimeout resolves the configured bot approval wait: zero uses the
// bounded default; a negative value opts out (wait indefinitely).
func (gw *BotGateway) approvalTimeout() time.Duration {
	switch {
	case gw.cfg.ApprovalTimeout < 0:
		return 0
	case gw.cfg.ApprovalTimeout == 0:
		return defaultBotApprovalTimeout
	default:
		return gw.cfg.ApprovalTimeout
	}
}

func botSessionDir(workspaceRoot string) string {
	if strings.TrimSpace(workspaceRoot) == "" {
		return config.SessionDir()
	}
	if dir := config.ProjectSessionDir(workspaceRoot); dir != "" {
		return dir
	}
	return config.SessionDir()
}

// BotSessionPathForChat derives the deterministic per-chat session file for a
// message with no persisted mapping or /attach binding. Reusing BuildSessionKey
// (already a stable chat-identity hash) makes the same chat hit one file across
// restarts — the chat-side analogue of dsh-dingtalk-channel's `ding-<chatId>`
// scheme. Empty when the message has no stable chat identity.
func BotSessionPathForChat(sessionDir string, src SessionSource) string {
	if strings.TrimSpace(sessionDir) == "" || strings.TrimSpace(src.ChatID) == "" {
		return ""
	}
	return filepath.Join(sessionDir, "bot-"+BuildSessionKey(src)+".jsonl")
}

func (gw *BotGateway) rememberSessionReady(msg InboundMessage, ctrl botController) {
	if gw.cfg.OnSessionReady == nil || ctrl == nil {
		return
	}
	gw.rememberSessionPath(msg, ctrl.SessionPath())
}

func (gw *BotGateway) rememberSessionPath(msg InboundMessage, sessionPath string) {
	if gw.cfg.OnSessionReady == nil {
		return
	}
	sessionID := botSessionTarget(sessionPath)
	if sessionID == "" {
		return
	}
	if err := gw.cfg.OnSessionReady(msg, sessionID); err != nil {
		gw.logger.Warn("remember bot session failed", "platform", msg.Platform, "connection", msg.ConnectionID, "err", err)
	}
}

// botSessionRecoveredHandler keeps the controller path, its writer lease, and
// the remote-to-session mapping on the same recovery generation. The lease
// handoff runs first and is failure-atomic: if the recovery path is already
// owned, the controller stays on the original path and the old lease remains
// held. Mapping updates are limited to this exact sessionState so a late
// callback from a retired controller cannot overwrite its replacement.
func (gw *BotGateway) botSessionRecoveredHandler(key string, msg InboundMessage, state *sessionState) func(control.SessionRecoveryInfo) error {
	return func(info control.SessionRecoveryInfo) error {
		if state == nil || state.leases == nil {
			return nil
		}
		// Keep the lease handoff and mapping publication atomic with respect to
		// state retirement. In particular, never let a callback that outlives
		// Stop reacquire a lease after closeSessionState has released it.
		state.lifecycleMu.Lock()
		defer state.lifecycleMu.Unlock()
		if state.retired {
			return errBotSessionRetired
		}
		if err := state.leases.HandleSessionRecovered(info); err != nil {
			return err
		}

		originalPath := canonicalBotPath(info.OriginalPath)
		recoveryPath := canonicalBotPath(info.RecoveryPath)
		live := false
		gw.mu.Lock()
		if gw.controllers[key] == state {
			live = true
			if canonicalBotPath(state.sessionPath) == originalPath {
				state.sessionPath = recoveryPath
			}
			if override, ok := gw.sessionOverrides[key]; ok && canonicalBotPath(override.sessionPath) == originalPath {
				override.sessionPath = recoveryPath
				gw.sessionOverrides[key] = override
			}
		}
		gw.mu.Unlock()

		if live {
			gw.rememberSessionPath(msg, recoveryPath)
		}
		return nil
	}
}

func botSessionTarget(sessionPath string) string {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return ""
	}
	return "path:" + sessionPath
}

func (gw *BotGateway) sessionOptionsForMessage(msg InboundMessage) (model string, workspaceRoot string, toolApprovalMode string) {
	override, enabled := gw.sessionRuntimeOverrideForMessage(msg)
	return gw.sessionOptionsForResolvedOverride(msg, override, enabled)
}

func (gw *BotGateway) sessionOptionsForResolvedOverride(msg InboundMessage, override sessionRuntimeOverride, enabled bool) (model string, workspaceRoot string, toolApprovalMode string) {
	// cfg.ToolApprovalMode / Channels / ConnectionChannels are rewritten under
	// gw.mu at runtime (/yolo, UpdateConnectionToolApprovalMode), so snapshot them
	// under a short lock and resolve outside it. Copying the ChannelConfig value is enough: writers
	// replace whole map entries and never mutate SessionMappings in place.
	gw.mu.Lock()
	model = gw.cfg.Model
	workspaceRoot = gw.cfg.WorkspaceRoot
	toolApprovalMode = normalizeBotToolApprovalMode(gw.cfg.ToolApprovalMode)
	var connChannel ChannelConfig
	connOK := false
	if msg.ConnectionID != "" {
		connChannel, connOK = gw.cfg.ConnectionChannels[msg.ConnectionID]
	}
	platChannel, platOK := gw.cfg.Channels[msg.Platform]
	gw.mu.Unlock()

	var mappings []SessionMapping
	if connOK {
		applyBotChannelOptions(connChannel, &model, &workspaceRoot, &toolApprovalMode)
		mappings = connChannel.SessionMappings
		if mapping, ok := matchingSessionMapping(mappings, msg); ok {
			workspaceRoot = workspaceRootForSessionMapping(mapping, workspaceRoot)
		}
		model, workspaceRoot, toolApprovalMode = gw.applyRouteOptions(msg, model, workspaceRoot, toolApprovalMode)
		if enabled {
			applyBotChannelOptions(override.channel, &model, &workspaceRoot, &toolApprovalMode)
		}
		return model, workspaceRoot, toolApprovalMode
	}
	if platOK {
		applyBotChannelOptions(platChannel, &model, &workspaceRoot, &toolApprovalMode)
		mappings = platChannel.SessionMappings
	}
	if mapping, ok := matchingSessionMapping(mappings, msg); ok {
		workspaceRoot = workspaceRootForSessionMapping(mapping, workspaceRoot)
	}
	model, workspaceRoot, toolApprovalMode = gw.applyRouteOptions(msg, model, workspaceRoot, toolApprovalMode)
	if enabled {
		applyBotChannelOptions(override.channel, &model, &workspaceRoot, &toolApprovalMode)
	}
	return model, workspaceRoot, toolApprovalMode
}

func (gw *BotGateway) applyRouteOptions(msg InboundMessage, model, workspaceRoot, toolApprovalMode string) (string, string, string) {
	for _, route := range gw.cfg.Routes {
		if routeMatchesMessage(route, msg) {
			applyBotChannelOptions(route.Channel, &model, &workspaceRoot, &toolApprovalMode)
			break
		}
	}
	return model, workspaceRoot, toolApprovalMode
}

func applyBotChannelOptions(channel ChannelConfig, model *string, workspaceRoot *string, toolApprovalMode *string) {
	if value := strings.TrimSpace(channel.Model); value != "" {
		*model = value
	}
	if value := strings.TrimSpace(channel.WorkspaceRoot); value != "" {
		*workspaceRoot = value
	}
	if value := normalizeOptionalBotToolApprovalMode(channel.ToolApprovalMode); value != "" {
		*toolApprovalMode = value
	}
}

func matchingSessionMapping(mappings []SessionMapping, msg InboundMessage) (SessionMapping, bool) {
	for i := range mappings {
		if sessionMappingMatches(mappings[i], msg) {
			return mappings[i], true
		}
	}
	return SessionMapping{}, false
}

func sessionMappingMatches(mapping SessionMapping, msg InboundMessage) bool {
	if strings.TrimSpace(mapping.RemoteID) != strings.TrimSpace(msg.ChatID) {
		return false
	}
	chatType, userID, threadID := sessionMappingIdentity(msg)
	mappingChatType := strings.TrimSpace(mapping.ChatType)
	if mappingChatType == "" {
		return chatType == ""
	}
	if mappingChatType != chatType {
		return false
	}
	if strings.TrimSpace(mapping.UserID) != userID {
		return false
	}
	return strings.TrimSpace(mapping.ThreadID) == threadID
}

func sessionMappingIdentity(msg InboundMessage) (chatType string, userID string, threadID string) {
	switch msg.ChatType {
	case ChatGroup, ChatGuild:
		chatType = string(msg.ChatType)
		userID = strings.TrimSpace(msg.UserID)
	case ChatThread:
		chatType = string(msg.ChatType)
		threadID = strings.TrimSpace(msg.ThreadID)
		if threadID == "" {
			threadID = strings.TrimSpace(msg.ChatID)
		}
	}
	return chatType, userID, threadID
}

func workspaceRootForSessionMapping(mapping SessionMapping, fallback string) string {
	if root := strings.TrimSpace(mapping.WorkspaceRoot); root != "" {
		return root
	}
	if strings.EqualFold(strings.TrimSpace(mapping.Scope), "global") {
		return ""
	}
	return fallback
}

func routeMatchesMessage(route RouteConfig, msg InboundMessage) bool {
	if value := strings.TrimSpace(route.ConnectionID); value != "" && value != strings.TrimSpace(msg.ConnectionID) {
		return false
	}
	if route.Platform != "" && route.Platform != msg.Platform {
		return false
	}
	if route.ChatType != "" && route.ChatType != msg.ChatType {
		return false
	}
	if value := strings.TrimSpace(route.ChatID); value != "" && value != strings.TrimSpace(msg.ChatID) {
		return false
	}
	if value := strings.TrimSpace(route.UserID); value != "" && value != strings.TrimSpace(msg.UserID) {
		return false
	}
	if value := strings.TrimSpace(route.ThreadID); value != "" && value != strings.TrimSpace(msg.ThreadID) {
		return false
	}
	return true
}

func normalizeBotToolApprovalMode(mode string) string {
	if value := normalizeOptionalBotToolApprovalMode(mode); value != "" {
		return value
	}
	return control.ToolApprovalAsk
}

func normalizeOptionalBotToolApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case control.ToolApprovalAsk:
		return control.ToolApprovalAsk
	case control.ToolApprovalAuto:
		return control.ToolApprovalAuto
	case control.ToolApprovalYolo, "full", "full-access", "bypass":
		return control.ToolApprovalYolo
	default:
		return ""
	}
}

func (gw *BotGateway) sendText(ctx context.Context, adapter Adapter, msg InboundMessage, text string) error {
	out := OutboundMessage{
		ConnectionID:   msg.ConnectionID,
		Domain:         msg.Domain,
		ChatID:         msg.ChatID,
		ChatType:       msg.ChatType,
		Text:           text,
		ReplyToMsgID:   msg.MessageID,
		SessionWebhook: msg.SessionWebhook,
	}
	binding := AdapterBinding{
		ID:       strings.TrimSpace(msg.ConnectionID),
		Domain:   strings.TrimSpace(msg.Domain),
		Platform: msg.Platform,
		Adapter:  adapter,
	}
	if binding.Platform == "" && adapter != nil {
		binding.Platform = adapter.Platform()
	}
	if binding.ID == "" && adapter != nil {
		binding.ID = adapter.Name()
	}
	result, err := gw.sendViaAdapter(ctx, binding, out)
	if err != nil {
		gw.logger.Warn("bot send failed", "platform", msg.Platform, "chat_type", msg.ChatType, "chat", hashID(msg.ChatID), "reply_to", hashID(msg.MessageID), "err", err)
		return err
	}
	gw.logger.Info("bot send completed", "platform", msg.Platform, "chat_type", msg.ChatType, "chat", hashID(msg.ChatID), "reply_to", hashID(msg.MessageID), "message", hashID(result.MessageID))
	return err
}

func (gw *BotGateway) sendViaAdapter(ctx context.Context, binding AdapterBinding, msg OutboundMessage) (SendResult, error) {
	if binding.Adapter == nil {
		return SendResult{}, errors.New("bot send: adapter is nil")
	}
	if strings.TrimSpace(msg.ConnectionID) == "" {
		msg.ConnectionID = binding.ID
	}
	if strings.TrimSpace(msg.Domain) == "" {
		msg.Domain = binding.Domain
	}
	result, err := binding.Adapter.Send(ctx, msg)
	gw.markAdapterSend(binding, err)
	for _, messageID := range result.DeliveredMessageIDs() {
		gw.rememberOutboundMessage(binding.Platform, binding.ID, binding.Domain, msg.ChatID, messageID)
	}
	return result, err
}

func parseAskAnswers(questions []event.AskQuestion, raw string) []event.AskAnswer {
	raw = strings.TrimSpace(raw)
	if len(questions) == 0 {
		return []event.AskAnswer{{Selected: []string{raw}}}
	}
	byID := make(map[string]*event.AskQuestion, len(questions))
	for i := range questions {
		q := &questions[i]
		byID[q.ID] = q
		byID[fmt.Sprintf("%d", i+1)] = q
	}
	answerMap := make(map[string][]string, len(questions))
	if strings.Contains(raw, "=") {
		for part := range strings.SplitSeq(raw, ";") {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			q := byID[strings.TrimSpace(k)]
			if q == nil {
				continue
			}
			answerMap[q.ID] = normalizeAskSelection(*q, strings.TrimSpace(v))
		}
	} else if len(questions) == 1 {
		answerMap[questions[0].ID] = normalizeAskSelection(questions[0], raw)
	}
	out := make([]event.AskAnswer, 0, len(questions))
	for _, q := range questions {
		out = append(out, event.AskAnswer{QuestionID: q.ID, Selected: answerMap[q.ID]})
	}
	return out
}

func normalizeAskSelection(q event.AskQuestion, raw string) []string {
	parts := []string{raw}
	if q.Multi && strings.Contains(raw, ",") {
		parts = strings.Split(raw, ",")
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx, err := strconv.Atoi(part); err == nil && idx >= 1 && idx <= len(q.Options) {
			out = append(out, q.Options[idx-1].Label)
			continue
		}
		out = append(out, part)
	}
	return out
}

// UpdateConnectionToolApprovalMode updates the in-memory tool approval mode for
// a single bot connection without restarting the gateway. Empty mode clears the
// connection override, so existing sessions inherit the current gateway default.
func (gw *BotGateway) UpdateConnectionToolApprovalMode(connID, mode string) {
	connID = strings.TrimSpace(connID)
	if connID == "" {
		return
	}
	mode = normalizeOptionalBotToolApprovalMode(mode)
	type controllerMode struct {
		ctrl botController
		mode string
	}
	var updates []controllerMode

	gw.mu.Lock()
	if gw.cfg.ConnectionChannels == nil {
		gw.cfg.ConnectionChannels = make(map[string]ChannelConfig)
	}
	ch := gw.cfg.ConnectionChannels[connID]
	ch.ToolApprovalMode = mode
	gw.cfg.ConnectionChannels[connID] = ch
	// Update every active session that belongs to this connection.
	for _, state := range gw.controllers {
		if state == nil || state.ctrl == nil || strings.TrimSpace(state.connectionID) != connID {
			continue
		}
		effectiveMode := mode
		if effectiveMode == "" {
			effectiveMode = normalizeBotToolApprovalMode(gw.cfg.ToolApprovalMode)
		}
		updates = append(updates, controllerMode{ctrl: state.ctrl, mode: effectiveMode})
	}
	gw.mu.Unlock()

	for _, update := range updates {
		update.ctrl.SetToolApprovalMode(update.mode)
	}
}

// SendToAdapter sends a message through the adapter identified by connID.
// Returns an error if no matching adapter is found.
func (gw *BotGateway) SendToAdapter(ctx context.Context, connID, domain string, msg OutboundMessage) (SendResult, error) {
	connID = strings.TrimSpace(connID)
	domain = strings.TrimSpace(domain)
	var target AdapterBinding
	gw.mu.Lock()
	for _, binding := range gw.adapters {
		if strings.TrimSpace(binding.ID) == connID &&
			(domain == "" || strings.EqualFold(strings.TrimSpace(binding.Domain), domain)) {
			target = binding
			break
		}
	}
	gw.mu.Unlock()
	if target.Adapter != nil {
		return gw.sendViaAdapter(ctx, target, msg)
	}
	return SendResult{}, fmt.Errorf("SendToAdapter: no adapter found for connection %q (domain %q)", connID, domain)
}

// SendTextToAdapter sends a plain text message through the adapter identified by connID.
func (gw *BotGateway) SendTextToAdapter(ctx context.Context, connID, domain, chatID string, chatType ChatType, text string) (SendResult, error) {
	return gw.SendToAdapter(ctx, connID, domain, OutboundMessage{
		ChatID:   chatID,
		ChatType: chatType,
		Text:     text,
	})
}

// TestSendToAdapter sends a test message through the adapter identified by
// connID. The adapter must implement TestSender (currently dingtalk, which
// replies to the most recent chat it learned a session webhook for). Returns
// a readable error when the adapter is missing or does not support test sends.
func (gw *BotGateway) TestSendToAdapter(ctx context.Context, connID, domain, text string) (SendResult, error) {
	connID = strings.TrimSpace(connID)
	domain = strings.TrimSpace(domain)
	var target AdapterBinding
	gw.mu.Lock()
	for _, binding := range gw.adapters {
		if strings.TrimSpace(binding.ID) == connID &&
			(domain == "" || strings.EqualFold(strings.TrimSpace(binding.Domain), domain)) {
			target = binding
			break
		}
	}
	gw.mu.Unlock()
	if target.Adapter == nil {
		return SendResult{}, fmt.Errorf("no bot adapter found for %q (domain %q)", connID, domain)
	}
	ts, ok := target.Adapter.(TestSender)
	if !ok {
		return SendResult{}, fmt.Errorf("bot adapter %q does not support test sends", connID)
	}
	return ts.TestSend(ctx, text)
}
