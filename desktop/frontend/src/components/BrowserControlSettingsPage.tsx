import { useCallback, useEffect, useRef, useState } from "react";
import { Cookie, Globe, ShieldCheck, Trash2 } from "lucide-react";
import { SettingsField, SettingsSection } from "./SettingsForm";
import { desktopHost, type BrowserControlState, type ChromeImportFailure } from "../lib/desktopHost";
import { useT, type DictKey } from "../lib/i18n";

// Each failure the shell can report gets its own sentence: "no Chrome profile"
// and "the keychain prompt was denied" need different user actions.
const IMPORT_FAILURE_KEYS: Record<ChromeImportFailure, DictKey> = {
  "chrome-missing": "settings.browser.import.chromeMissing",
  "profile-not-found": "settings.browser.import.profileNotFound",
  "cookies-unreadable": "settings.browser.import.cookiesUnreadable",
  "safe-storage-denied": "settings.browser.import.safeStorageDenied",
  "safe-storage-unavailable": "settings.browser.import.safeStorageUnavailable",
  "unsupported-platform": "settings.browser.import.unsupportedPlatform",
};

const WARNING_KEYS = {
  "invalid-config": "settings.browser.warningInvalid",
  "unreadable-config": "settings.browser.warningUnreadable",
  "unsupported-version": "settings.browser.warningUnsupported",
} as const;

export function BrowserControlSettingsPage() {
  const t = useT();
  const host = desktopHost();
  const api = host.native.browserControl;
  // undefined = still loading; null = the shell has no browser-control store.
  const [state, setState] = useState<BrowserControlState | null | undefined>(undefined);
  const [pending, setPending] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirmClearAll, setConfirmClearAll] = useState(false);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    void api.get().then(
      (value) => {
        if (mounted.current) setState(value);
      },
      () => {
        if (mounted.current) setError(t("settings.browser.loadFailed"));
      },
    );
    return () => {
      mounted.current = false;
    };
  }, [api, t]);

  const run = useCallback(async (label: string, action: () => Promise<string>) => {
    setPending(label);
    setError(null);
    setNotice(null);
    try {
      setNotice(await action());
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally {
      setPending(null);
    }
  }, []);
  const busy = pending !== null;

  const setControl = (enabled: boolean) =>
    void run("control", async () => {
      setState(await api.setEnabled(enabled));
      return t(enabled ? "settings.browser.controlOn" : "settings.browser.controlOff");
    });

  const setCertificates = (enabled: boolean) =>
    void run("certificates", async () => {
      setState(await api.setIgnoreCertificateErrors(enabled));
      return t(enabled ? "settings.browser.certificatesOn" : "settings.browser.certificatesOff");
    });

  const importChrome = () =>
    void run("import", async () => {
      const outcome = await api.importChromeLogin();
      if (!outcome.ok) throw new Error(t(IMPORT_FAILURE_KEYS[outcome.reason]));
      return t("settings.browser.import.done", { profile: outcome.profile, cookies: outcome.cookies, skipped: outcome.skipped });
    });

  const clearCache = () =>
    void run("clearCache", async () => {
      await api.clearCache();
      return t("settings.browser.clearCache.done");
    });

  const clearAll = () =>
    void run("clearAll", async () => {
      await api.clearAllData();
      setConfirmClearAll(false);
      return t("settings.browser.clearAll.done");
    });

  if (host.kind !== "electron") {
    return (
      <div className="banner banner--warning" role="status">
        <span>{t("settings.browser.desktopOnly")}</span>
      </div>
    );
  }
  if (state === null) {
    return (
      <div className="banner banner--warning" role="status">
        <span>{t("settings.browser.loadFailed")}</span>
      </div>
    );
  }
  if (!state) return <div className="empty">{t("settings.loading")}</div>;

  return (
    <>
      {error && <div className="banner banner--error" role="alert"><span>{error}</span></div>}
      {notice && <div className="banner banner--success" role="status"><span>{notice}</span></div>}
      {state.warning && <div className="banner banner--warning" role="status"><span>{t(WARNING_KEYS[state.warning])}</span></div>}
      <SettingsSection title={t("settings.browser.basics")} description={t("settings.browser.basicsHint")}>
        <SettingsField label={t("settings.browser.control")} hint={t("settings.browser.controlHint")} icon={<Globe size={18} />}>
          <input
            className="provider-capability-row__switch"
            type="checkbox"
            role="switch"
            aria-label={t("settings.browser.control")}
            checked={state.controlEnabled}
            disabled={busy || !state.writable}
            onChange={(event) => setControl(event.currentTarget.checked)}
          />
        </SettingsField>
        <SettingsField label={t("settings.browser.import.title")} hint={t("settings.browser.import.hint")} icon={<Cookie size={18} />}>
          <button className="btn btn--small" type="button" disabled={busy} onClick={importChrome}>
            {t(pending === "import" ? "settings.browser.import.running" : "settings.browser.import.action")}
          </button>
        </SettingsField>
      </SettingsSection>
      <SettingsSection title={t("settings.browser.security")} description={t("settings.browser.securityHint")}>
        <SettingsField label={t("settings.browser.ignoreCertificates")} hint={t("settings.browser.ignoreCertificatesHint")} icon={<ShieldCheck size={18} />}>
          <input
            className="provider-capability-row__switch"
            type="checkbox"
            role="switch"
            aria-label={t("settings.browser.ignoreCertificates")}
            checked={state.ignoreCertificateErrors}
            disabled={busy || !state.writable}
            onChange={(event) => setCertificates(event.currentTarget.checked)}
          />
        </SettingsField>
      </SettingsSection>
      <SettingsSection title={t("settings.browser.data")} description={t("settings.browser.dataHint")}>
        <SettingsField label={t("settings.browser.clearCache.title")} hint={t("settings.browser.clearCache.hint")} icon={<Trash2 size={18} />}>
          <button className="btn btn--small" type="button" disabled={busy} onClick={clearCache}>
            {t("settings.browser.clearCache.action")}
          </button>
        </SettingsField>
        <SettingsField label={t("settings.browser.clearAll.title")} hint={t("settings.browser.clearAll.hint")} icon={<Trash2 size={18} />}>
          {confirmClearAll ? (
            <div className="settings-inline-controls">
              <button className="btn btn--small btn--danger" type="button" disabled={busy} onClick={clearAll}>
                {t("settings.browser.clearAll.confirm")}
              </button>
              <button className="btn btn--small" type="button" disabled={busy} onClick={() => setConfirmClearAll(false)}>
                {t("common.cancel")}
              </button>
            </div>
          ) : (
            <button className="btn btn--small btn--danger" type="button" disabled={busy} onClick={() => setConfirmClearAll(true)}>
              {t("settings.browser.clearAll.action")}
            </button>
          )}
        </SettingsField>
      </SettingsSection>
    </>
  );
}
