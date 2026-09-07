import type { SettingsView } from "./types";
import { normalizeStatusBarItems } from "./statusBarItems";
import { applyMockLegacyReasoningMode, applyMockSessionExperience } from "./sessionExperienceMock";

/** Browser fixtures share one preference snapshot and its compatibility mirrors. */
export function createDesktopPreferencesMock(settings: SettingsView) {
  return {
    async SetCloseBehavior(mode: string) {
      settings.closeBehavior = mode === "quit" ? "quit" : "background";
    },
    async SetDisplayMode() { applyMockSessionExperience(settings, "standard"); },
    async SetStatusBarStyle(style: string) {
      settings.statusBarStyle = style === "text" ? "text" : "icon";
    },
    async SetStatusBarItems(items: string[]) {
      settings.statusBarItems = normalizeStatusBarItems(items);
    },
    async SetDesktopLanguage(lang: string) {
      settings.desktopLanguage = lang === "en" || lang === "zh" || lang === "zh-TW" || lang === "es" ? lang : "";
    },
    async SetDesktopCurrency(currency: string) {
      settings.desktopCurrency = currency === "CNY" || currency === "USD" ? currency : "";
    },
    async SetDesktopCheckUpdates(enabled: boolean) {
      settings.checkUpdates = enabled;
    },
    async SetDesktopUpdateChannel(channel: string) {
      void channel;
      settings.updateChannel = "stable";
    },
    async SetDesktopTelemetry(enabled: boolean) {
      settings.telemetry = enabled;
    },
    async SetDesktopMetrics(enabled: boolean) {
      settings.metrics = enabled;
    },
    async SetDesktopConversationWidth(width: string) { settings.conversationWidth = width; },
    async SetReasoningDisplayMode(mode: "hidden" | "summary" | "auto" | "expanded") { applyMockLegacyReasoningMode(settings, mode); },
    async SetSessionExperience(mode: "standard" | "deep") { applyMockSessionExperience(settings, mode); },
    async SetExpandThinking() { applyMockSessionExperience(settings, "standard"); },
    async MigrateDesktopPreferences(language: string, theme: string, style: string) {
      if (!settings.desktopLanguage) settings.desktopLanguage = language === "en" || language === "zh" || language === "zh-TW" || language === "es" ? language : "";
      if (!settings.desktopTheme && !settings.desktopThemeStyle) {
        settings.desktopTheme = theme === "auto" || theme === "light" ? theme : "dark";
        settings.desktopThemeStyle = style;
      }
    },
  };
}
