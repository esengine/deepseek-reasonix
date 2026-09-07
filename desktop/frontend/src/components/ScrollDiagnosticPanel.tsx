import { useCallback, useEffect, useRef, useState } from "react";
import { Activity, CircleStop, Download, Flag, X } from "lucide-react";
import { app } from "../lib/bridge";
import { useI18n, type Locale } from "../lib/i18n";
import { useToast } from "../lib/toast";
import "./ScrollDiagnosticPanel.css";
import {
  isTranscriptScrollDiagnosticsBuild,
  transcriptScrollDiagnostics,
  type TranscriptScrollDiagnosticSnapshot,
} from "../lib/transcriptScrollDiagnostics";

const diagnosticsAvailable = isTranscriptScrollDiagnosticsBuild(
  typeof __BUILD_CHANNEL__ === "string" ? __BUILD_CHANNEL__ : "stable",
  import.meta.env?.DEV,
);

type DiagnosticCopy = {
  start: string;
  startHint: string;
  recording: (seconds: number, events: number) => string;
  ready: (events: number) => string;
  mark: string;
  markHint: string;
  stopExport: string;
  exportLabel: string;
  exported: string;
  exportFailed: (error: string) => string;
};

// Keep diagnostic-only copy in this lazy chunk so stable builds do not carry
// strings for a panel that is compiled out of the product surface.
const COPY: Record<Exclude<Locale, "es">, DiagnosticCopy> = {
  en: {
    start: "Scroll diagnostics",
    startHint: "Start a local 90-second scroll trace",
    recording: (seconds, events) => `Recording - ${seconds}s - ${events} events`,
    ready: (events) => `Trace ready - ${events} events`,
    mark: "Mark issue",
    markHint: "Mark the moment the issue appears",
    stopExport: "Stop & export",
    exportLabel: "Export",
    exported: "Scroll diagnostics exported",
    exportFailed: (error) => `Could not export diagnostics: ${error}`,
  },
  zh: {
    start: "滚动诊断",
    startHint: "开始一段仅保存在本地的 90 秒滚动记录",
    recording: (seconds, events) => `记录中 - ${seconds} 秒 - ${events} 条事件`,
    ready: (events) => `记录已停止 - ${events} 条事件`,
    mark: "标记问题",
    markHint: "问题出现时点击标记",
    stopExport: "停止并导出",
    exportLabel: "导出",
    exported: "滚动诊断文件已导出",
    exportFailed: (error) => `无法导出诊断文件：${error}`,
  },
  "zh-TW": {
    start: "捲動診斷",
    startHint: "開始一段僅儲存在本機的 90 秒捲動記錄",
    recording: (seconds, events) => `記錄中 - ${seconds} 秒 - ${events} 筆事件`,
    ready: (events) => `記錄已停止 - ${events} 筆事件`,
    mark: "標記問題",
    markHint: "問題出現時按一下標記",
    stopExport: "停止並匯出",
    exportLabel: "匯出",
    exported: "捲動診斷檔案已匯出",
    exportFailed: (error) => `無法匯出診斷檔案：${error}`,
  },
};

function diagnosticSample(element: HTMLElement | null, totalRows: number): Record<string, unknown> | null {
  if (!element) return null;
  const viewport = element.getBoundingClientRect();
  let firstVisibleIndex: number | undefined;
  let firstVisibleTop: number | undefined;
  const mountedRows = element.querySelectorAll<HTMLElement>(".transcript__row[data-logical-index]");
  for (const row of mountedRows) {
    const rect = row.getBoundingClientRect();
    if (rect.bottom <= viewport.top || rect.top >= viewport.bottom) continue;
    const index = Number(row.dataset.logicalIndex);
    if (Number.isFinite(index)) firstVisibleIndex = index;
    firstVisibleTop = rect.top - viewport.top;
    break;
  }
  const bottomDistance = element.scrollHeight - element.scrollTop - element.clientHeight;
  return {
    scrollTop: element.scrollTop,
    scrollHeight: element.scrollHeight,
    clientHeight: element.clientHeight,
    bottomDistance,
    mountedRows: mountedRows.length,
    totalRows,
    firstVisibleIndex,
    firstVisibleTop,
    mode: element.dataset.scrollMode,
    atBottom: bottomDistance <= 4,
    scrollable: element.scrollHeight - element.clientHeight > 4,
  };
}

export default function ScrollDiagnosticPanel({
  scrollElement,
  totalRows,
}: {
  scrollElement: HTMLElement | null;
  totalRows: number;
}) {
  const { locale, t } = useI18n();
  const copy = COPY[locale as keyof typeof COPY] ?? COPY.en;
  const { showToast } = useToast();
  const scrollElementRef = useRef(scrollElement);
  const totalRowsRef = useRef(totalRows);
  scrollElementRef.current = scrollElement;
  totalRowsRef.current = totalRows;
  const [snapshot, setSnapshot] = useState<TranscriptScrollDiagnosticSnapshot>(() => transcriptScrollDiagnostics.getSnapshot());
  const [exporting, setExporting] = useState(false);

  const refresh = useCallback(() => setSnapshot(transcriptScrollDiagnostics.getSnapshot()), []);
  useEffect(() => transcriptScrollDiagnostics.subscribe(refresh), [refresh]);
  useEffect(() => {
    if (snapshot.status !== "recording") return;
    const timer = window.setInterval(refresh, 500);
    return () => window.clearInterval(timer);
  }, [refresh, snapshot.status]);

  const start = useCallback(() => {
    transcriptScrollDiagnostics.start(() => diagnosticSample(scrollElementRef.current, totalRowsRef.current));
  }, []);

  const exportTrace = useCallback(async () => {
    setExporting(true);
    try {
      const payload = transcriptScrollDiagnostics.stop();
      const serialized = JSON.stringify(payload);
      const path = app.ExportScrollDiagnostics
        ? await app.ExportScrollDiagnostics(serialized)
        : await app.SaveExportFile("reasonix-scroll-diagnostics-browser.json", serialized, false)
          .then(() => "reasonix-scroll-diagnostics-browser.json");
      if (path) {
        showToast(copy.exported, "info");
        transcriptScrollDiagnostics.reset();
      }
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error);
      showToast(copy.exportFailed(detail), "error");
    } finally {
      setExporting(false);
    }
  }, [copy, showToast]);

  if (!diagnosticsAvailable) return null;

  if (snapshot.status === "idle") {
    return (
      <div className="scroll-diagnostics" data-testid="scroll-diagnostics">
        <button
          type="button"
          className="scroll-diagnostics__start"
          onClick={start}
          title={copy.startHint}
        >
          <Activity size={15} aria-hidden="true" />
          <span>{copy.start}</span>
        </button>
      </div>
    );
  }

  const elapsedSeconds = Math.min(90, Math.floor(snapshot.durationMs / 1_000));
  const stopped = snapshot.status === "stopped";
  return (
    <div className={`scroll-diagnostics scroll-diagnostics--${snapshot.status}`} data-testid="scroll-diagnostics">
      <span className="scroll-diagnostics__status" role="status">
        <span className="scroll-diagnostics__dot" aria-hidden="true" />
        {stopped
          ? copy.ready(snapshot.eventCount)
          : copy.recording(elapsedSeconds, snapshot.eventCount)}
      </span>
      {!stopped && (
        <button
          type="button"
          className="scroll-diagnostics__icon-button"
          onClick={() => transcriptScrollDiagnostics.mark()}
          aria-label={copy.mark}
          title={copy.markHint}
        >
          <Flag size={15} aria-hidden="true" />
        </button>
      )}
      <button
        type="button"
        className="scroll-diagnostics__export"
        onClick={() => void exportTrace()}
        disabled={exporting}
        title={stopped ? copy.exportLabel : copy.stopExport}
      >
        {stopped ? <Download size={15} aria-hidden="true" /> : <CircleStop size={15} aria-hidden="true" />}
        <span>{stopped ? copy.exportLabel : copy.stopExport}</span>
      </button>
      <button
        type="button"
        className="scroll-diagnostics__icon-button"
        onClick={() => transcriptScrollDiagnostics.reset()}
        aria-label={t("common.cancel")}
        title={t("common.cancel")}
      >
        <X size={15} aria-hidden="true" />
      </button>
    </div>
  );
}
