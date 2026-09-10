import { app, clipboard, dialog, ipcMain, net, protocol, screen, session, shell } from "electron";
import { readdirSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { IPC, type BrowserTakeoverKind } from "../shared/ipc.js";
import { ActionExecutor } from "./browser/actions.js";
import { DocumentRegistry } from "./browser/documents.js";
import { DownloadTracker } from "./browser/downloads.js";
import { ElectronGuestViewFactory } from "./browser/electronGuestViews.js";
import { GrantRegistry } from "./browser/grants.js";
import { buildBrowserHostCalls } from "./browser/hostCalls.js";
import { browserLayoutInDIP } from "./browser/layout.js";
import { BrowserSurfaceManager, SHARED_PARTITION } from "./browser/surfaceManager.js";
import { BrowserControlStore, loadBrowserControlBootstrap, type BrowserSession } from "./browserControl.js";
import { BrowserControlHost } from "./browserControlHost.js";
import type { CookieSink } from "./chromeImport.js";
import { emptyContract, loadContract, type LoadedContract } from "./contract.js";
import { DialogHost } from "./dialogs.js";
import { renderFailurePage, type ShellAction } from "./failurePage.js";
import { buildHelloParams, describeHandshakeFailure, validateHelloResult, type HelloResult } from "./handshake.js";
import { reasonixHome } from "./home.js";
import { buildHostCallTable, dispatchHostCall, type ScreenInfo } from "./hostCalls.js";
import { firstExisting, iconCandidates } from "./icons.js";
import { registerRendererIpc } from "./ipc.js";
import { QuitSequencer } from "./lifecycle.js";
import { createLogger, errorText, RotatingFile } from "./log.js";
import { installApplicationMenu } from "./menu.js";
import { record } from "./params.js";
import { APP_INDEX_URL, APP_SCHEME, registerAppProtocol, resolveDistRoot } from "./protocol.js";
import { RemoteWindowHost } from "./remoteWindows.js";
import { ServiceSupervisor } from "./service.js";
import { claimShellInstance } from "./singleInstance.js";
import { TrayHost } from "./tray.js";
import { DEFAULT_GEOMETRY, MainWindow } from "./window.js";
import { AppZoomStore } from "./zoomStore.js";
import { GraphicsSettingsStore, loadGraphicsBootstrap } from "./graphics.js";

const MAIN_WINDOW_PERMISSIONS = new Set(["clipboard-read", "clipboard-sanitized-write", "fullscreen", "notifications"]);
const TAKEOVER_KINDS = new Set<string>(["mousedown", "keydown", "wheel", "touchstart", "pointerdown"]);

function safeDirName(value: string): string {
  const name = value.replace(/[^A-Za-z0-9._-]+/g, "_");
  return name === "" ? "user" : name;
}

app.setName("Reasonix");
const dev = (process.env.REASONIX_DEV ?? "").trim() !== "";
const home = reasonixHome({ env: process.env, platform: process.platform, homedir, cwd: () => process.cwd() });
if (home === "") {
  console.error("reasonix-desktop-shell: cannot resolve the Reasonix data home (set REASONIX_HOME)");
  app.exit(1);
} else if (!claimShellInstance(app, home, dev)) {
  app.quit();
} else {
  const graphics = loadGraphicsBootstrap(app.getPath("userData"), process.env, process.argv);
  if (graphics.shouldDisable) app.disableHardwareAcceleration();
  bootstrap(home);
}

function bootstrap(dataHome: string): void {
  const graphicsBootstrap = loadGraphicsBootstrap(app.getPath("userData"), process.env, process.argv);
  const graphics = new GraphicsSettingsStore(graphicsBootstrap.configPath, graphicsBootstrap);
  const logsDir = join(app.getPath("userData"), "logs");
  const log = createLogger(new RotatingFile(join(logsDir, "shell.log")), !app.isPackaged);
  log.info(`graphics acceleration: saved=${graphics.current.hardwareAcceleration} startup=${graphics.current.startupEnabled} override=${graphics.current.override} warning=${graphics.current.warning ?? "none"}`);
  app.on("gpu-info-update", () => {
    try { log.info(`graphics feature status: ${JSON.stringify(app.getGPUFeatureStatus())}`); } catch (error) { log.warn(`graphics status unavailable: ${errorText(error)}`); }
  });
  const serviceLog = new RotatingFile(join(logsDir, "service.log"));
  process.on("uncaughtException", (error) => log.error(`uncaught exception: ${errorText(error)}`));
  process.on("unhandledRejection", (reason) => log.error(`unhandled rejection: ${errorText(reason)}`));

  protocol.registerSchemesAsPrivileged([
    { scheme: APP_SCHEME, privileges: { standard: true, secure: true, supportFetchAPI: true, corsEnabled: false, stream: true } },
  ]);

  const contractPath = join(__dirname, "desktopContract.json");
  let contract: LoadedContract;
  try {
    contract = loadContract(contractPath);
  } catch (error) {
    log.warn(`desktop contract unavailable (${errorText(error)}); every desktop/invoke will be rejected`);
    contract = emptyContract();
  }
  const distRoot = resolveDistRoot({ env: process.env, appPath: app.getAppPath(), resourcesPath: process.resourcesPath, packaged: app.isPackaged });
  const devURL = (process.env.REASONIX_ELECTRON_DEV_URL ?? "").trim();
  const appURL = devURL !== "" ? devURL : APP_INDEX_URL;
  const zoomStore = new AppZoomStore(join(dataHome, "electron-app-zoom.json"), join(dataHome, "desktop-zoom.json"));
  const icons = iconCandidates({ platform: process.platform, appPath: app.getAppPath(), resourcesPath: process.resourcesPath, packaged: app.isPackaged });
  const windowIcon = process.platform === "darwin" ? undefined : (firstExisting(icons.window) ?? undefined);
  const serviceBinary = (process.env.REASONIX_DESKTOP_SERVICE ?? "").trim()
    || join(process.resourcesPath, "service", process.platform === "win32" ? "reasonix-desktop.exe" : "reasonix-desktop");

  let domReadyGeneration = "";

  const mainWindow = new MainWindow({
    preloadPath: join(__dirname, "preload.cjs"),
    appURL,
    platform: process.platform,
    icon: windowIcon,
    log,
    onRendererLost: (reason) => {
      browser.pauseForRendererLoss(reason);
      for (const tab of browser.all()) documents.invalidateTab(tab.id);
    },
    onAppDomReady: (rendererGeneration) => {
      const generation = service.generation;
      if (generation === "") return;
      const attach = () => service.request("desktop/rendererAttached", { rendererGeneration }).catch((error: unknown) => {
        log.warn(`rendererAttached failed: ${errorText(error)}`);
      });
      if (domReadyGeneration === generation) {
        void attach();
        return;
      }
      domReadyGeneration = generation;
      void service.request("desktop/domReady", {})
        .catch((error: unknown) => log.warn(`domReady failed: ${errorText(error)}`))
        .then(attach);
    },
    onCloseRequested: async () => record(await service.request("desktop/beforeClose", { reason: "window" })).prevent === true,
    onCloseAllowed: () => lifecycle.approve(),
    onShellAction: (action: ShellAction) => {
      if (action === "open-logs") void shell.openPath(logsDir);
      else if (action === "restart") void service.restart().catch(() => undefined);
      else lifecycle.approve();
    },
    zoomStore,
  });

  const browserControlBootstrap = loadBrowserControlBootstrap(app.getPath("userData"));
  const browserControlStore = new BrowserControlStore(browserControlBootstrap.configPath, browserControlBootstrap);
  const browserControl = new BrowserControlHost({
    store: browserControlStore,
    sharedSession: () => session.fromPartition(SHARED_PARTITION) as unknown as BrowserSession & { cookies: CookieSink },
    log,
    platform: process.platform,
    home: homedir(),
    env: process.env,
    list: (path) => readdirSync(path),
    onControlEnabled: (enabled) => {
      // The Go host reads this when it builds a session's tool set, so the new
      // value reaches new sessions without disturbing a running turn.
      void service.request("desktop/browserControl", { enabled }).catch((error: unknown) => {
        log.warn(`browser control push failed: ${errorText(error)}`);
      });
    },
  });
  log.info(`browser control: enabled=${browserControlBootstrap.state.controlEnabled} ignoreCertificateErrors=${browserControlBootstrap.state.ignoreCertificateErrors} warning=${browserControlBootstrap.state.warning ?? "none"}`);

  const downloads = new DownloadTracker({
    tabForWebContents: (id) => {
      const tab = browser.all().find((entry) => entry.view.page.id === id);
      return tab ? { id: tab.id, taskId: tab.taskId } : undefined;
    },
    defaultDirectory: (taskId) => join(app.getPath("userData"), "downloads", safeDirName(taskId)),
    onUpdate: (download) => mainWindow.send(IPC.browserDownload, download),
    log,
  });
  const guestViews = new ElectronGuestViewFactory({
    window: () => mainWindow.browserWindow,
    preloadPath: join(__dirname, "guest-preload.cjs"),
    log,
    onSession: (partition, guestSession) => {
      browserControl.trackSession(partition, guestSession);
      guestSession.on("will-download", (_event, item, contents) => downloads.handleWillDownload(item, contents.id));
    },
  });
  const browser = new BrowserSurfaceManager({
    views: guestViews,
    contentSize: () => mainWindow.contentSize(),
    onTakeover: (tab, reason) => void service.hostEvent("browser.takeover", { tabId: tab.id, epoch: tab.epoch, reason }),
    onCrash: (tab, reason) => void service.hostEvent("browser.crash", { tabId: tab.id, epoch: tab.epoch, reason }),
    log,
  });
  browser.subscribe((tabs) => mainWindow.send(IPC.browserTabs, tabs));
  const grants = new GrantRegistry({ generation: () => service.generation });
  const documents = new DocumentRegistry();
  const actions = new ActionExecutor({ surfaces: browser, documents });

  const remote = new RemoteWindowHost({
    platform: process.platform,
    icon: windowIcon,
    log,
    onClosed: (hostKey) => void service.hostEvent("remoteWindow.closed", { hostKey }),
  });
  const tray = new TrayHost({
    platform: process.platform,
    iconPath: firstExisting(icons.tray),
    onOpen: () => {
      mainWindow.show("tray");
      void service.hostEvent("tray.open", {});
    },
    onQuit: () => void service.hostEvent("tray.quit", {}),
    log,
  });
  const dialogs = new DialogHost(dialog, () => mainWindow.browserWindow ?? undefined);

  const lifecycle = new QuitSequencer({
    service: {
      beforeClose: async (reason) => record(await service.request("desktop/beforeClose", { reason })).prevent === true,
      shutdown: () => service.shutdown(),
    },
    app: {
      quit: () => app.quit(),
      relaunch: (args: string[], execPath?: string) => {
        if (execPath) delete process.env.REASONIX_DESKTOP_SERVICE;
        app.relaunch({ args, ...(execPath ? { execPath } : {}) });
      },
    },
    // Website views go first: a WebContents closing after its window is
    // gone is the ordering that left orphaned renderers in the prototype.
    onCloseAllowed: () => {
      browser.destroyAll();
      mainWindow.allowClose();
      remote.closeAll();
      tray.destroy();
    },
    log,
  });

  const hostCalls = buildHostCallTable({
    window: mainWindow,
    dialogs,
    tray,
    remote,
    lifecycle,
    openExternal: (url) => {
      new URL(url);
      return shell.openExternal(url);
    },
    hideApp: () => {
      if (process.platform === "darwin") app.hide();
      else mainWindow.hide();
    },
    screens: (): ScreenInfo[] => {
      const primary = screen.getPrimaryDisplay().id;
      return screen.getAllDisplays().map((display) => ({
        x: display.bounds.x,
        y: display.bounds.y,
        width: display.bounds.width,
        height: display.bounds.height,
        scale: display.scaleFactor,
        primary: display.id === primary,
      }));
    },
    browser: buildBrowserHostCalls({ surfaces: browser, grants, documents, actions, downloads }),
  });

  const service = new ServiceSupervisor(
    {
      binary: serviceBinary,
      args: ["--host-rpc"],
      env: process.env,
      onStderr: (chunk) => {
        serviceLog.write(chunk);
        if (!app.isPackaged) process.stderr.write(chunk);
      },
      log,
    },
    {
      hello: async (client) => validateHelloResult(await client.request("desktop/hello", buildHelloParams({
        contractDigest: contract.digest,
        version: app.isPackaged ? app.getVersion() : "dev",
        channel: process.env.REASONIX_CHANNEL || "dev",
        commit: process.env.REASONIX_COMMIT || "dev",
        hostVersion: process.versions.electron,
        chromeVersion: process.versions.chrome,
        platform: process.platform,
        arch: process.arch,
        home: dataHome,
        dev,
      }), 10_000)),
      onRequest: (method, params) => dispatchHostCall(hostCalls, method, params),
      onEvent: (frame) => mainWindow.send(IPC.event, frame),
      onState: (state) => {
        mainWindow.send(IPC.serviceState, state);
        grants.observeGeneration(state.generation);
        if (state.phase !== "ready") {
          browser.pauseForRendererLoss(`service ${state.phase}`);
          documents.clear();
        }
      },
      onReady: async (hello: HelloResult) => {
        log.info(`desktop service ready: generation ${hello.runtimeGeneration}, pid ${hello.service.pid}`);
        // A restarted service starts with the capability on, so the persisted
        // switch is replayed before any session can be built.
        void service.request("desktop/browserControl", { enabled: browserControl.state().controlEnabled })
          .catch((error: unknown) => log.warn(`browser control push failed: ${errorText(error)}`));
        if (!mainWindow.browserWindow) {
          try {
            await zoomStore.load();
            mainWindow.create(hello.window);
          } catch (error) {
            log.warn(`app zoom initialization failed: ${errorText(error)}`);
            mainWindow.create(DEFAULT_GEOMETRY);
          }
        }
        // Reattach the surviving renderer after a service restart. Reloading
        // would destroy unsent composer drafts; desktop:resync repairs reads.
        if (!mainWindow.reattachApp()) void mainWindow.loadApp();
      },
      onFailed: (error) => {
        const failure = describeHandshakeFailure(error);
        log.error(`desktop service failed: ${failure.name}: ${failure.detail}`);
        if (!mainWindow.browserWindow) mainWindow.create(DEFAULT_GEOMETRY);
        void mainWindow.showFailure(renderFailurePage(failure, logsDir));
      },
    },
  );

  // Session end and scripted shutdowns deliver SIGTERM; quit through the same
  // sequence as the menu so Go snapshots sessions before the process ends.
  process.on("SIGTERM", () => lifecycle.requestQuit());
  app.on("second-instance", (_event, argv) => {
    mainWindow.focusForSecondInstance();
    void service.hostEvent("secondInstance", { argv });
  });
  app.on("activate", () => mainWindow.show("activate"));
  app.on("before-quit", (event) => {
    if (!lifecycle.onBeforeQuit()) event.preventDefault();
  });
  app.on("window-all-closed", () => {
    // Go decides when the process ends; a hidden main window keeps running.
  });

  void app.whenReady().then(() => {
    if (process.platform === "darwin") {
      const dockIcon = firstExisting(icons.window);
      if (dockIcon && app.dock) app.dock.setIcon(dockIcon);
    }
    registerAppProtocol({
      protocol,
      fetch: (input, init) => net.fetch(input, init),
      distRoot,
      resources: () => service.helloResult?.resources ?? null,
      log,
    });
    session.defaultSession.setPermissionRequestHandler((contents, permission, callback) => {
      callback(mainWindow.isTrustedSender(contents, contents.mainFrame) && MAIN_WINDOW_PERMISSIONS.has(permission));
    });
    registerRendererIpc({
      ipcMain,
      contract,
      window: mainWindow,
      invoke: (method, args) => service.invoke(method, args),
      serviceState: () => service.current,
      clipboard,
      graphics,
      browserControl,
      openExternal: (url) => shell.openExternal(url),
      browser: {
        list: () => browser.list(),
        open: async (url, options) => browser.view(await browser.open(url, options)),
        close: (tabId) => browser.close(tabId),
        activate: (tabId) => browser.activate(tabId),
        navigate: async (tabId, target) => {
          await browser.navigate(tabId, target);
        },
        setZoom: (tabId, factor) => browser.setZoom(tabId, factor),
        toggleDevTools: (tabId) => browser.toggleDevTools(tabId),
        resume: (tabId) => browser.resume(tabId),
        takeover: (tabId) => browser.takeover(tabId, "user takeover"),
        setLayout: (rect) => browser.setLayout(browserLayoutInDIP(rect, mainWindow.browserWindow?.webContents.getZoomFactor() ?? 1)),
        setOverlay: (active) => browser.setOverlay(active),
      },
      log,
    });
    // Reports from the guest preload: the sender must be one of our website
    // views, which takeoverFromSender checks by WebContents id.
    ipcMain.on(IPC.browserTakeover, (event, payload: unknown) => {
      const kind = typeof payload === "object" && payload !== null ? (payload as { kind?: unknown }).kind : undefined;
      if (typeof kind !== "string" || !TAKEOVER_KINDS.has(kind)) return;
      browser.takeoverFromSender(event.sender.id, kind as BrowserTakeoverKind);
    });
    installApplicationMenu({
      platform: process.platform,
      openSettings: () => mainWindow.sendShellEvent("app:open-settings", service.generation),
      toggleDevTools: () => mainWindow.toggleDevTools(),
      showWindow: () => mainWindow.show("menu"),
      quit: () => lifecycle.requestQuit(),
      zoomIn: () => { void mainWindow.stepAppZoom(1); },
      zoomOut: () => { void mainWindow.stepAppZoom(-1); },
      resetZoom: () => { void mainWindow.resetAppZoom(); },
    });
    log.info(`shell starting: service ${serviceBinary}, ui ${appURL}, dist ${distRoot}, home ${dataHome}`);
    return service.start().catch(() => undefined);
  }).catch((error: unknown) => {
    log.error(`shell bootstrap failed: ${errorText(error)}`);
    app.exit(1);
  });
}
