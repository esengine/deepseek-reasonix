import { useCallback, useEffect, useRef, useState } from "react";
import { Activity, Download, Flag, X } from "lucide-react";
import { app } from "../lib/bridge";
import { useToast } from "../lib/toast";
import { useI18n, type Locale } from "../lib/i18n";
import {
  frontendDiagnosticSample,
  frontendDiagnostics,
  type FrontendDiagnosticSnapshot,
} from "../lib/frontendDiagnostics";

const defaultFilename = (reportId: string) => `reasonix-frontend-diagnostics-${reportId.slice(0, 8) || "trace"}.json`;
const COPY: Record<Exclude<Locale, "es">, {
  start: string;
  startHint: string;
  recordingLabel: string;
  readyLabel: string;
  recording: (seconds: number, events: number) => string;
  ready: (events: number) => string;
  export: string;
  stopExport: string;
  mark: string;
  cancel: string;
}> = {
  en: {
    start: "Frontend diagnostics",
    startHint: "Start recording a local frontend interaction timeline",
    recordingLabel: "Frontend diagnostics · on",
    readyLabel: "Frontend diagnostics · ready",
    recording: (seconds, events) => `Recording - ${seconds}s - ${events} events`,
    ready: (events) => `Trace ready - ${events} events`,
    export: "Export",
    stopExport: "Stop & export",
    mark: "Mark current issue",
    cancel: "Cancel recording",
  },
  zh: {
    start: "前端诊断",
    startHint: "开始记录一段仅保存在本地的前端交互时间线",
    recordingLabel: "前端诊断 · 已开启",
    readyLabel: "前端诊断 · 待导出",
    recording: (seconds, events) => `记录中 - ${seconds} 秒 - ${events} 条事件`,
    ready: (events) => `记录完成 - ${events} 条事件`,
    export: "导出",
    stopExport: "停止并导出",
    mark: "标记当前问题",
    cancel: "取消记录",
  },
  "zh-TW": {
    start: "前端診斷",
    startHint: "開始記錄一段僅儲存在本機的前端互動時間線",
    recordingLabel: "前端診斷 · 已開啟",
    readyLabel: "前端診斷 · 待匯出",
    recording: (seconds, events) => `記錄中 - ${seconds} 秒 - ${events} 筆事件`,
    ready: (events) => `記錄完成 - ${events} 筆事件`,
    export: "匯出",
    stopExport: "停止並匯出",
    mark: "標記目前問題",
    cancel: "取消記錄",
  },
};

export type FrontendDiagnosticsControlProps = {
  scrollElement?: HTMLElement | null;
  totalRows?: number;
  embedded?: boolean;
};

export function FrontendDiagnosticsControl({
  scrollElement,
  totalRows = 0,
  embedded = false,
}: FrontendDiagnosticsControlProps) {
  const { locale } = useI18n();
  const copy = COPY[locale as keyof typeof COPY] ?? COPY.en;
  const scrollElementRef = useRef<HTMLElement | null>(scrollElement ?? null);
  const totalRowsRef = useRef(totalRows);
  scrollElementRef.current = scrollElement ?? null;
  totalRowsRef.current = totalRows;
  const { showToast } = useToast();
  const [snapshot, setSnapshot] = useState<FrontendDiagnosticSnapshot>(() => frontendDiagnostics.getSnapshot());
  const [exporting, setExporting] = useState(false);

  const refresh = useCallback(() => setSnapshot(frontendDiagnostics.getSnapshot()), []);
  useEffect(() => frontendDiagnostics.subscribe(refresh), [refresh]);
  useEffect(() => {
    if (snapshot.status !== "recording") return;
    const timer = window.setInterval(refresh, 500);
    return () => window.clearInterval(timer);
  }, [refresh, snapshot.status]);

  const start = useCallback(() => {
    frontendDiagnostics.start(() => frontendDiagnosticSample(scrollElementRef.current, totalRowsRef.current));
  }, []);

  const exportTrace = useCallback(async () => {
    setExporting(true);
    try {
      const payload = frontendDiagnostics.stop();
      const path = await app.PickExportFile(defaultFilename(payload.manifest.reportId), "application/json");
      if (path) {
        await app.SaveExportFile(path, JSON.stringify(payload, null, 2), false);
        frontendDiagnostics.reset();
      }
    } catch (error) {
      showToast(error instanceof Error ? error.message : String(error), "error");
    } finally {
      setExporting(false);
    }
  }, [showToast]);

  const toggle = useCallback(() => {
    if (snapshot.status === "idle") {
      start();
      return;
    }
    void exportTrace();
  }, [exportTrace, snapshot.status, start]);
  const rootClass = `scroll-diagnostics frontend-diagnostics${embedded ? " frontend-diagnostics--embedded" : ""}`;

  if (snapshot.status === "idle") {
    return (
      <div className={rootClass} data-testid="frontend-diagnostics">
        <button
          type="button"
          className="frontend-diagnostics__toggle"
          role="switch"
          aria-checked="false"
          onClick={start}
          title={copy.startHint}
        >
          <Activity size={15} aria-hidden="true" />
          <span>{copy.start}</span>
        </button>
      </div>
    );
  }

  const elapsedSeconds = Math.min(120, Math.floor(snapshot.durationMs / 1_000));
  const recording = snapshot.status === "recording";
  return (
    <div className={`${rootClass} scroll-diagnostics--${snapshot.status}`} data-testid="frontend-diagnostics">
      <button
        type="button"
        className={`frontend-diagnostics__toggle${recording ? " frontend-diagnostics__toggle--on" : ""}`}
        role="switch"
        aria-checked={recording}
        onClick={toggle}
        disabled={exporting}
        title={recording ? copy.stopExport : copy.export}
      >
        <Activity size={15} aria-hidden="true" />
        <span>{recording ? copy.recordingLabel : copy.readyLabel}</span>
      </button>
      <span className="scroll-diagnostics__status" role="status">
        <span className="scroll-diagnostics__dot" aria-hidden="true" />
        {recording ? copy.recording(elapsedSeconds, snapshot.eventCount) : copy.ready(snapshot.eventCount)}
      </span>
      {recording && (
        <button
          type="button"
          className="scroll-diagnostics__icon-button"
          onClick={() => frontendDiagnostics.mark()}
          aria-label={copy.mark}
          title={copy.mark}
        >
          <Flag size={15} aria-hidden="true" />
        </button>
      )}
      <button
        type="button"
        className="scroll-diagnostics__export"
        onClick={() => void exportTrace()}
        disabled={exporting}
        title={recording ? copy.stopExport : copy.export}
      >
        <Download size={15} aria-hidden="true" />
        <span>{copy.export}</span>
      </button>
      <button
        type="button"
        className="scroll-diagnostics__icon-button"
        onClick={() => frontendDiagnostics.reset()}
        aria-label={copy.cancel}
        title={copy.cancel}
      >
        <X size={15} aria-hidden="true" />
      </button>
    </div>
  );
}
