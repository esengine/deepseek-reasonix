import type { ProviderPresetView } from "./providerCatalogTypes";
import type { AgentView, BotSettingsView, NetworkView, PermissionsView, ProviderView, SandboxView, ToolApprovalMode } from "./types";

export interface SettingsView {
  modelSettingsFingerprint?: string;
  defaultModel: string;
  plannerModel: string;
  visionModel: string;
  webSearchModel?: string;
  webSearchModels?: string[];
  webSearchModelStatus?: string;
  webSearchModelReason?: string;
  effectiveWebSearchModel?: string;
  webSearchModelOverridden?: boolean;
  subagentModel: string;
  subagentEffort: string;
  autoPlan: string;
  providers: ProviderView[];
  officialProviders: ProviderView[];
  providerPresets: ProviderPresetView[];
  permissions: PermissionsView;
  sandbox: SandboxView;
  network: NetworkView;
  agent: AgentView;
  bot: BotSettingsView;
  desktopLanguage: string; // "" | "en" | "zh"; empty = auto
  desktopCurrency?: string; // "" | "CNY" | "USD"; absent/empty = follow language
  desktopLayoutStyle: string; // "workbench" | "creation"
  desktopTheme: string; // "auto" | "dark" | "light"
  desktopThemeStyle: string;
  desktopTerminalTheme: string; // "auto" follows app | "dark" | "light"
  closeBehavior: string; // "background" | "quit"
  displayMode: string; sessionExperience?: "standard" | "deep"; reasoningDisplayMode: string; reasoningDisplayModeExplicit?: boolean;
  statusBarStyle: string; // "icon" | "text"
  statusBarItems: string[]; // ordered visible status bar item ids
  defaultToolApprovalMode: ToolApprovalMode | string; // default for newly-created sessions
  checkUpdates: boolean; // check for new versions on startup
  updateChannel: string; // compatibility field; always "stable"
  telemetry: boolean; // anonymous launch ping + scrubbed next-launch native crash diagnostics
  metrics: boolean; // aggregate quality/lifecycle metrics (anonymous signal/bucket counts)
  configPath: string;
  shadowedByPath?: string; // workspace reasonix.toml that outranks configPath, when one exists
  cacheContext: string; // per-project user attribution id sent to providers as user_id
  cacheContextProject?: boolean; // true only when editing a project workspace (field hidden for global settings)
  providerKinds: string[]; // provider implementations the kernel registered (for the kind picker)
  autoApproveTools: boolean;
  bypass: boolean; // legacy JSON key for live YOLO/full-access tool auto-approval
  conversationWidth?: string; // "standard" | "full"; absent from older Wails payloads
}
