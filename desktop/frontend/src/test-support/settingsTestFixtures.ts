import { act } from "react";

import type { SettingsView } from "../lib/types";

export function flushPromises(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

export function installCanvasMock(win: Window) {
  const canvasElement = (win as Window & typeof globalThis).HTMLCanvasElement;
  Object.defineProperty(canvasElement.prototype, "getContext", {
    configurable: true,
    value(type: string) {
      if (type !== "2d") return null;
      return {
        font: "",
        measureText: () => ({ width: 0 }),
      } as unknown as CanvasRenderingContext2D;
    },
  });
}

export async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    await act(async () => {
      await flushPromises();
    });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

export function baseSettings(displayMode: "standard" | "compact" = "standard"): SettingsView {
  return {
    modelSettingsFingerprint: "test-model-settings-revision",
    defaultModel: "",
    plannerModel: "",
    visionModel: "",
    subagentModel: "",
    subagentEffort: "",
    autoPlan: "off",
    providers: [],
    officialProviders: [],
    providerPresets: [],
    permissions: { mode: "ask", allow: [], ask: [], deny: [] },
    sandbox: { bash: "enforce", network: false, workspaceRoot: "", allowWrite: [], effectiveWorkspaceRoot: "/work", effectiveWriteRoots: ["/work"], shell: "auto", shellCapabilities: [{ id: "bash", variant: "system", available: true, path: "/bin/bash", source: "path" }] },
    network: { proxyMode: "auto", proxyUrl: "", noProxy: "", proxy: { type: "socks5", server: "", port: 0, username: "", password: "" } },
    agent: { temperature: 0, maxSteps: 0, plannerMaxSteps: 0, maxSubagentDepth: 2, maxSubagentConcurrency: 6, maxParallelWriters: 3, systemPrompt: "", reasoningLanguage: "auto", compactRatio: 0.80 },
    bot: {
      enabled: false,
      model: "",
      toolApprovalMode: "",
      maxSteps: 0,
      debounceMs: 0,
      queueMode: "steer",
      queueCap: 20,
      queueDrop: "summarize",
      ignoreSelfMessages: true,
      selfUserIds: { qq: [], feishu: [], weixin: [], dingtalk: [] },
      control: { enabled: false, addr: "127.0.0.1:37913", tokenEnv: "REASONIX_BOT_CONTROL_TOKEN" },
      pairing: { enabled: true, requestTtlMinutes: 60, maxPendingPerPlatform: 3 },
      routes: [],
      allowlist: {
        enabled: false,
        allowAll: false,
        qqUsers: [],
        feishuUsers: [],
        weixinUsers: [],
        qqApprovers: [],
        feishuApprovers: [],
        weixinApprovers: [],
        qqAdmins: [],
        feishuAdmins: [],
        weixinAdmins: [],
        qqGroups: [],
        feishuGroups: [],
        weixinGroups: [],
        dingtalkUsers: [],
        dingtalkApprovers: [],
        dingtalkAdmins: [],
        dingtalkGroups: [],
      },
      qq: {
        enabled: false,
        appId: "",
        appSecretEnv: "",
        secretSet: false,
        sandbox: false,
        model: "",
        toolApprovalMode: "ask",
        workspaceRoot: "",
        access: { enabled: true, allowAll: false, pairingEnabled: true, users: [], groups: [], approvers: [], admins: [] },
      },
      feishu: { enabled: false, domain: "feishu", appId: "", appSecretEnv: "", secretSet: false, verificationToken: "", mode: "webhook", webhookPort: 0, requireMention: false },
      weixin: { enabled: false, accountId: "", tokenEnv: "", tokenSet: false, apiBase: "" },
      dingtalk: { enabled: false, clientId: "", clientSecretEnv: "DINGTALK_CLIENT_SECRET", secretSet: false, botName: "", requireMention: true, model: "", toolApprovalMode: "", workspaceRoot: "", access: { enabled: true, allowAll: false, pairingEnabled: true, users: [], groups: [], approvers: [], admins: [] } },
      connections: [],
    },
    desktopLanguage: "en",
    desktopLayoutStyle: "workbench",
    desktopTheme: "auto",
    desktopThemeStyle: "graphite",
    desktopTerminalTheme: "auto",
    closeBehavior: "background",
    displayMode,
    reasoningDisplayMode: "auto",
    reasoningDisplayModeExplicit: false,
    statusBarStyle: "text",
    statusBarItems: ["model", "workspace", "git_branch", "cache", "balance"],
    defaultToolApprovalMode: "auto",
    checkUpdates: true,
    updateChannel: "stable",
    telemetry: true,
    metrics: true,
    configPath: "/tmp/reasonix/config.toml",
    cacheContext: "",
    providerKinds: [],
    autoApproveTools: false,
    bypass: false,
  };
}
