import { useEffect, useRef, useState } from "react";
import { ShieldAlert } from "lucide-react";
import { app } from "../lib/bridge";
import { useI18n } from "../lib/i18n";

const COPY = {
  en: ["Recovered {n} pending instructions", "Inbox is paused", "Review queued work before continuing.", "Review queue", "Continue", "Keep paused", "Paused"],
  zh: ["已恢复 {n} 条待处理指令", "收件箱已暂停", "请检查队列后再继续。", "查看队列", "继续执行", "保持暂停", "已暂停"],
  "zh-TW": ["已恢復 {n} 條待處理指令", "收件匣已暫停", "請檢查佇列後再繼續。", "查看佇列", "繼續執行", "保持暫停", "已暫停"],
} as const;

export function InboxRecoveryBanner({
  count,
  recovered,
  disabled,
  tabId,
  onReview,
  onResumed,
  onError,
}: {
  count: number;
  recovered: boolean;
  disabled: boolean;
  tabId: string;
  onReview: () => void;
  onResumed: () => void;
  onError: (error: unknown) => void;
}) {
  const { locale } = useI18n();
  const [recoveredTitle, pausedTitle, body, review, resume, keepPaused, paused] = COPY[locale as keyof typeof COPY] ?? COPY.en;
  const title = recovered ? recoveredTitle.replace("{n}", String(count)) : pausedTitle;
  const [busy, setBusy] = useState(false);
  const [keptPaused, setKeptPaused] = useState(false);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const setPaused = async (paused: boolean) => {
    if (busy) return;
    setBusy(true);
    try {
      await app.SetInboxPaused(tabId, paused);
      if (!mountedRef.current) return;
      if (paused) setKeptPaused(true);
      else onResumed();
    } catch (error) {
      if (mountedRef.current) onError(error);
    } finally {
      if (mountedRef.current) setBusy(false);
    }
  };

  return (
    <section className="composer-guidance-shelf composer-inbox-recovery" role="status" aria-label={title}>
      <div className="banner banner--warning banner--actionable" style={{ flexWrap: "wrap" }}>
        <ShieldAlert size={17} aria-hidden="true" />
        <span className="banner__msg">
          <strong>{title}</strong>
          {` — ${body}`}
        </span>
        <span className="banner__spacer" />
        <button className="btn btn--small" type="button" disabled={busy} onClick={onReview}>
          {review}
        </button>
        <button className="btn btn--primary btn--small" type="button" disabled={disabled || busy} onClick={() => void setPaused(false)}>
          {resume}
        </button>
        <button className="btn btn--small" type="button" disabled={disabled || busy || keptPaused} onClick={() => void setPaused(true)}>
          {keptPaused ? paused : keepPaused}
        </button>
      </div>
    </section>
  );
}
