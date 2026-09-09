import { lazy, memo, startTransition, Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";

async function loadMarkdownView<T>(component: Promise<T>): Promise<T> {
  await import("./MarkdownImage.css");
  return component;
}

const MarkdownRenderer = lazy(() => loadMarkdownView(import("./MarkdownRenderer")));
let historyView: typeof import("./MarkdownHistory").default | undefined;
let historyViewPromise: Promise<typeof import("./MarkdownHistory")> | undefined;
export function preloadMarkdownHistory(): Promise<typeof import("./MarkdownHistory")> {
  return historyViewPromise ??= loadMarkdownView(import("./MarkdownHistory")).then(module => {
    historyView = module.default;
    return module;
  });
}
const LazyMarkdownHistory = lazy(preloadMarkdownHistory);
const STREAMING_TAIL_THRESHOLD = 8_000;
const FINALIZE_SETTLE_MS = 50;
const FINALIZE_IDLE_TIMEOUT_MS = 1_000;
const MARKDOWN_SECTION_TARGET_CHARS = 12_000;
const CROSS_SECTION_REFERENCE_RE = /(?:^ {0,3}\[[^\]\n]+\]:|\[\^[^\]\n]+\])/m;
const CROSS_SECTION_CONTAINER_RE = /^ {0,3}(?:>|(?:[-+*]|\d{1,9}[.)])(?:[ \t]+|$)|<)/;

function scanMarkdownSections(text: string): { boundaries: number[]; hasCrossSectionContainer: boolean } {
  const boundaries = [0];
  let lineStart = 0;
  let fence: { marker: string; length: number } | null = null;
  let displayMath = false;
  let boundaryAfterFence = false;
  let hasCrossSectionContainer = false;

  const addBoundary = (offset: number) => {
    if (offset > 0 && boundaries[boundaries.length - 1] !== offset) boundaries.push(offset);
  };

  while (lineStart < text.length) {
    const newline = text.indexOf("\n", lineStart);
    const lineEnd = newline === -1 ? text.length : newline + 1;
    const line = text.slice(lineStart, newline === -1 ? text.length : newline).replace(/\r$/, "");
    const trimmed = line.trim();

    if (fence) {
      const close = new RegExp(`^ {0,3}${fence.marker}{${fence.length},}[ \\t]*$`);
      if (close.test(line)) {
        fence = null;
        boundaryAfterFence = true;
      }
      lineStart = lineEnd;
      continue;
    }

    if (displayMath) {
      if (trimmed === "$$") {
        displayMath = false;
        boundaryAfterFence = true;
      }
      lineStart = lineEnd;
      continue;
    }

    if (boundaryAfterFence && trimmed !== "") {
      addBoundary(lineStart);
      boundaryAfterFence = false;
    }

    if (CROSS_SECTION_CONTAINER_RE.test(line)) {
      hasCrossSectionContainer = true;
    }
    const fenceMatch = /^ {0,3}(`{3,}|~{3,})/.exec(line);
    if (fenceMatch) {
      addBoundary(lineStart);
      fence = { marker: fenceMatch[1][0], length: fenceMatch[1].length };
    } else if (trimmed === "$$") {
      addBoundary(lineStart);
      displayMath = true;
    } else if (/^ {0,3}#{1,6}(?:[ \t]+|$)/.test(line)) {
      addBoundary(lineStart);
    }
    lineStart = lineEnd;
  }

  return { boundaries, hasCrossSectionContainer };
}

// Stable top-level sections let React.memo retain completed Markdown while the
// streaming tail changes. Cross-section references intentionally stay in one
// renderer because their definitions can affect nodes anywhere in the document.
export function splitStableMarkdownSections(text: string): string[] {
  if (text.length < MARKDOWN_SECTION_TARGET_CHARS || CROSS_SECTION_REFERENCE_RE.test(text)) return [text];
  const { boundaries, hasCrossSectionContainer } = scanMarkdownSections(text);
  if (hasCrossSectionContainer) return [text];
  if (boundaries.length === 1) return [text];

  const sections = boundaries.map((start, index) => text.slice(start, boundaries[index + 1] ?? text.length));
  const chunks: string[] = [];
  let current = "";
  for (const section of sections) {
    if (current && current.length + section.length > MARKDOWN_SECTION_TARGET_CHARS) {
      chunks.push(current);
      current = section;
    } else {
      current += section;
    }
  }
  if (current) chunks.push(current);
  return chunks.length > 1 ? chunks : [text];
}

type IdleWindow = Window & {
  requestIdleCallback?: (callback: () => void, options?: { timeout: number }) => number;
  cancelIdleCallback?: (handle: number) => void;
};

function scheduleMarkdownFinalization(callback: () => void): () => void {
  const idleWindow = window as IdleWindow;
  let cancelled = false;
  let idleHandle: number | null = null;
  let frameHandle: number | null = null;
  const timeoutHandle = window.setTimeout(() => {
    if (cancelled) return;
    const run = () => {
      if (cancelled) return;
      startTransition(callback);
    };
    if (idleWindow.requestIdleCallback) {
      idleHandle = idleWindow.requestIdleCallback(run, { timeout: FINALIZE_IDLE_TIMEOUT_MS });
    } else {
      frameHandle = requestAnimationFrame(run);
    }
  }, FINALIZE_SETTLE_MS);

  return () => {
    cancelled = true;
    window.clearTimeout(timeoutHandle);
    if (idleHandle !== null) idleWindow.cancelIdleCallback?.(idleHandle);
    if (frameHandle !== null) cancelAnimationFrame(frameHandle);
  };
}

export function streamingMarkdownCommitInterval(textLength: number): number {
  if (textLength >= 32_000) return 300;
  if (textLength >= 8_000) return 150;
  return 50;
}

const STREAMING_LIST_ITEM_RE = /^ {0,3}(?:[*+-]|\d{1,9}[.)])(?:[ \t]+|$)/;
const STREAMING_THEMATIC_BREAK_RE = /^ {0,3}(?:(?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})[ \t]*$/;

function isStreamingListItemLine(line: string): boolean {
  return STREAMING_LIST_ITEM_RE.test(line) && !STREAMING_THEMATIC_BREAK_RE.test(line);
}

// Live parse prefix: last completed block. A later list marker commits prior
// items only — the new item stays in the tail so indented continuations can join.
// An open code fence commits only up to the fence line: the tail renders the
// growing code with code styling (splitStreamingTailFence), so streaming a
// large block no longer re-parses the whole document on every commit.
export function streamingCommitTarget(text: string): string {
  let lineStart = 0;
  let fence: { marker: string; length: number } | null = null;
  let fenceStart = 0;
  let displayMath = false;
  let boundary = 0;
  while (lineStart < text.length) {
    const newline = text.indexOf("\n", lineStart);
    const lineEnd = newline === -1 ? text.length : newline + 1;
    const line = text.slice(lineStart, newline === -1 ? text.length : newline).replace(/\r$/, "");
    const terminated = newline !== -1;
    if (fence) {
      if (new RegExp(`^ {0,3}${fence.marker}{${fence.length},}[ \\t]*$`).test(line)) {
        fence = null;
        if (terminated) boundary = lineEnd;
      }
    } else if (displayMath) {
      if (line.trim() === "$$") {
        displayMath = false;
        if (terminated) boundary = lineEnd;
      }
    } else {
      const fenceMatch = /^ {0,3}(`{3,}|~{3,})/.exec(line);
      if (fenceMatch) {
        fence = { marker: fenceMatch[1][0], length: fenceMatch[1].length };
        fenceStart = lineStart;
      } else if (line.trim() === "$$") displayMath = true;
      else if (terminated && line.trim() === "") boundary = lineEnd;
      // A heading interrupts a paragraph, so a partial heading line already
      // completes everything before it; a terminated one is itself complete.
      else if (/^ {0,3}#{1,6}[ \t]+/.test(line)) boundary = terminated ? lineEnd : lineStart;
      else if (isStreamingListItemLine(line)) boundary = lineStart;
      // GFM table rows commit per terminated line. A row is any terminated
      // line that contains '|'. Non-pipe lines stay in the tail because a
      // plain line after table rows is swallowed as a single-cell row by the
      // final remark-gfm parse.
      else if (terminated && line.includes("|")) boundary = lineEnd;
    }
    lineStart = lineEnd;
  }
  return fence ? text.slice(0, fenceStart) : displayMath ? text : text.slice(0, boundary);
}

type StreamingTailFence = { head: string; lang: string; code: string };

// Split a streaming tail around an unclosed code fence so the fence body can
// render with code styling before the closing fence arrives. Bail out cheaply
// when no fence marker exists; otherwise mirror the fence state machine from
// streamingCommitTarget in one forward pass over the tail.
export function splitStreamingTailFence(text: string): StreamingTailFence | null {
  if (!text.includes("```") && !text.includes("~~~")) return null;
  let lineStart = 0;
  let fence: { marker: string; length: number } | null = null;
  let fenceStart = 0;
  let fenceBodyStart = 0;
  let lang = "";
  while (lineStart < text.length) {
    const newline = text.indexOf("\n", lineStart);
    const lineEnd = newline === -1 ? text.length : newline + 1;
    const line = text.slice(lineStart, newline === -1 ? text.length : newline).replace(/\r$/, "");
    if (fence) {
      if (new RegExp(`^ {0,3}${fence.marker}{${fence.length},}[ \\t]*$`).test(line)) fence = null;
    } else {
      const fenceMatch = /^ {0,3}(`{3,}|~{3,})([^\n]*)$/.exec(line);
      if (fenceMatch) {
        fence = { marker: fenceMatch[1][0], length: fenceMatch[1].length };
        fenceStart = lineStart;
        fenceBodyStart = lineEnd;
        lang = fenceMatch[2].trim();
      }
    }
    lineStart = lineEnd;
  }
  if (!fence) return null;
  return { head: text.slice(0, fenceStart), lang, code: text.slice(fenceBodyStart) };
}

export function useRenderedMarkdownText(text: string, streaming: boolean, holdIdleFinalization = false): string {
  const [renderedText, setRenderedText] = useState(text);
  const latestTextRef = useRef(text);
  const frameRef = useRef<number | null>(null);
  const timeoutRef = useRef<number | null>(null);
  const lastCommitAtRef = useRef(0);
  const wasStreamingRef = useRef(streaming);
  const finalizingTextRef = useRef<string | null>(null);
  const cancelFinalizationRef = useRef<(() => void) | null>(null);
  const finalizationStartedAtRef = useRef(0);
  const finalizationLengthRef = useRef(0);

  latestTextRef.current = text;

  useLayoutEffect(() => {
    const endedStreaming = wasStreamingRef.current && !streaming;
    wasStreamingRef.current = streaming;
    if (streaming) {
      cancelFinalizationRef.current?.();
      cancelFinalizationRef.current = null;
      finalizingTextRef.current = null;
      // A bounded live preview occasionally advances its window and drops an
      // old prefix. Discard the stale parsed tree before paint; the complete
      // replacement stays visible through StreamingMarkdownTail and is parsed
      // later under the normal adaptive budget.
      if (renderedText !== "" && !text.startsWith(renderedText)) {
        setRenderedText("");
      }
      return;
    }
    lastCommitAtRef.current = 0;
    if (frameRef.current !== null) {
      cancelAnimationFrame(frameRef.current);
      frameRef.current = null;
    }
    if (timeoutRef.current !== null) {
      window.clearTimeout(timeoutRef.current);
      timeoutRef.current = null;
    }
    if (renderedText === text) {
      cancelFinalizationRef.current?.();
      cancelFinalizationRef.current = null;
      finalizingTextRef.current = null;
      if (finalizationStartedAtRef.current > 0) {
        performance.measure("reasonix:markdown-finalize", {
          start: finalizationStartedAtRef.current,
          end: performance.now(),
          detail: { textLength: finalizationLengthRef.current },
        });
        finalizationStartedAtRef.current = 0;
        finalizationLengthRef.current = 0;
      }
      return;
    }

    const canFinalizeWhenIdle =
      (endedStreaming || finalizingTextRef.current !== null) &&
      text.length >= STREAMING_TAIL_THRESHOLD &&
      text.startsWith(renderedText);
    if (canFinalizeWhenIdle) {
      // The worker path owns the final parse of a completed stream: holding
      // here keeps the committed prefix frozen instead of re-parsing the full
      // document on the main thread at idle time.
      if (holdIdleFinalization) return;
      if (finalizingTextRef.current === text) return;
      cancelFinalizationRef.current?.();
      finalizingTextRef.current = text;
      cancelFinalizationRef.current = scheduleMarkdownFinalization(() => {
        cancelFinalizationRef.current = null;
        finalizationStartedAtRef.current = performance.now();
        finalizationLengthRef.current = latestTextRef.current.length;
        setRenderedText(latestTextRef.current);
      });
      return;
    }

    cancelFinalizationRef.current?.();
    cancelFinalizationRef.current = null;
    finalizingTextRef.current = null;
    setRenderedText(text);
  }, [renderedText, streaming, text, holdIdleFinalization]);

  useLayoutEffect(() => {
    if (streaming) lastCommitAtRef.current = performance.now();
  }, [renderedText, streaming]);

  useEffect(() => {
    if (!streaming || frameRef.current !== null || timeoutRef.current !== null) return;
    if (streamingCommitTarget(text).length <= renderedText.length) return;
    const commit = () => {
      timeoutRef.current = null;
      frameRef.current = requestAnimationFrame(() => {
        frameRef.current = null;
        // Recompute at commit time: only ever advance to a newer boundary.
        const target = streamingCommitTarget(latestTextRef.current);
        setRenderedText((prev) => (target.length > prev.length ? target : prev));
      });
    };
    const now = performance.now();
    const elapsed = lastCommitAtRef.current === 0 ? Number.POSITIVE_INFINITY : now - lastCommitAtRef.current;
    const delay = streamingMarkdownCommitInterval(text.length) - elapsed;
    if (delay <= 0) commit();
    else timeoutRef.current = window.setTimeout(commit, delay);
  }, [renderedText, streaming, text]);

  useEffect(() => () => {
    if (frameRef.current !== null) cancelAnimationFrame(frameRef.current);
    if (timeoutRef.current !== null) window.clearTimeout(timeoutRef.current);
    cancelFinalizationRef.current?.();
  }, []);

  return renderedText;
}

const StreamingMarkdownTail = memo(function StreamingMarkdownTail({ text }: { text: string }) {
  const elementRef = useRef<HTMLDivElement>(null);
  const previousTextRef = useRef("");

  useLayoutEffect(() => {
    const element = elementRef.current;
    if (!element) return;
    const previousText = previousTextRef.current;
    const previous = splitStreamingTailFence(previousText);
    const next = splitStreamingTailFence(text);
    // Append-only fast paths keep per-frame updates to one text-node append.
    if (next && previous && next.head === previous.head && next.lang === previous.lang && next.code.startsWith(previous.code)) {
      const textNode = element.querySelector("code")?.firstChild;
      if (textNode?.nodeType === Node.TEXT_NODE) {
        (textNode as Text).appendData(next.code.slice(previous.code.length));
        previousTextRef.current = text;
        return;
      }
    }
    if (!next && !previous && text.startsWith(previousText)) {
      const textNode = element.firstChild;
      if (element.childNodes.length === 1 && textNode?.nodeType === Node.TEXT_NODE) {
        (textNode as Text).appendData(text.slice(previousText.length));
        previousTextRef.current = text;
        return;
      }
    }
    element.textContent = "";
    if (next) {
      if (next.head) element.appendChild(document.createTextNode(next.head));
      const pre = document.createElement("pre");
      pre.className = "code md--stream-tail-code";
      if (next.lang) pre.setAttribute("data-lang", next.lang);
      const code = document.createElement("code");
      code.textContent = next.code;
      pre.appendChild(code);
      element.appendChild(pre);
    } else {
      element.textContent = text;
    }
    previousTextRef.current = text;
  }, [text]);

  return <div ref={elementRef} className="md md--stream-tail" data-transcript-selection-source-fallback />;
});

export const Markdown = memo(function Markdown({
  text,
  plainStatusBlocks = false,
  streaming = false,
  cacheKey,
  wasStreamed,
}: {
  text: string;
  plainStatusBlocks?: boolean;
  streaming?: boolean;
  /** Stable transcript item key shared by live-footer and virtualized hosts. */
  cacheKey?: string;
  /** The item originated in the live renderer, even if this is a remount. */
  wasStreamed?: boolean;
}) {
  // legacyMode: the worker/inline pipeline failed (Worker unavailable AND the
  // in-process parse threw) — fall back to the pre-Phase-E behavior:
  // the idle-time main-thread finalization below parses the full document.
  const [legacyMode, setLegacyMode] = useState(false);
  const handleWorkerError = useCallback(() => setLegacyMode(true), []);
  // While the worker owns the final parse of a completed stream, hold the
  // idle finalization so the full document is not ALSO parsed main-thread.
  const holdIdleFinalization = !streaming && !legacyMode;
  const renderedText = useRenderedMarkdownText(text, streaming, holdIdleFinalization);
  const sections = useMemo(() => splitStableMarkdownSections(renderedText), [renderedText]);
  const pendingText = (streaming || text.length >= STREAMING_TAIL_THRESHOLD) && text.startsWith(renderedText)
    ? text.slice(renderedText.length)
    : "";
  // A row that ever streamed keeps its already-parsed committed sections as
  // the parse-in-flight fallback; a fresh history mount shows the full text
  // as plain first (never truncated) until worker blocks swap in.
  const wasStreamingRef = useRef(Boolean(wasStreamed));
  if (streaming) wasStreamingRef.current = true;
  // Finalize timing parity with the old idle path: measure from stream
  // completion (or mount, for history) to the worker blocks swapping in.
  const finalizeStartRef = useRef(0);
  const finalizeLengthRef = useRef(0);
  useEffect(() => {
    if (streaming || legacyMode || finalizeStartRef.current > 0) return;
    if (text.length < STREAMING_TAIL_THRESHOLD) return;
    finalizeStartRef.current = performance.now();
    finalizeLengthRef.current = text.length;
  }, [streaming, legacyMode, text]);
  const handleWorkerParsed = useCallback(() => {
    if (finalizeStartRef.current === 0) return;
    performance.measure("reasonix:markdown-finalize", {
      start: finalizeStartRef.current,
      end: performance.now(),
      detail: { textLength: finalizeLengthRef.current },
    });
    finalizeStartRef.current = 0;
    finalizeLengthRef.current = 0;
  }, []);

  const committedView = (
    <>
      <Suspense fallback={<div className="md" data-transcript-geometry-pending data-transcript-selection-source-fallback>{renderedText}</div>}>
        {sections.length === 1 ? (
          <MarkdownRenderer text={renderedText} plainStatusBlocks={plainStatusBlocks} />
        ) : (
          <div className="md" data-markdown-sections={sections.length}>
            {sections.map((section, index) => (
              <MarkdownRenderer key={index} text={section} plainStatusBlocks={plainStatusBlocks} bare />
            ))}
          </div>
        )}
      </Suspense>
      {pendingText && <StreamingMarkdownTail text={pendingText} />}
    </>
  );

  if (streaming || legacyMode) return committedView;

  const MarkdownHistory = historyView ?? LazyMarkdownHistory;
  const historyFallback = wasStreamingRef.current
    ? committedView
    : <div className="md" data-transcript-geometry-pending data-transcript-selection-source-fallback>{text}</div>;
  return (
    <Suspense fallback={historyFallback}>
      <MarkdownHistory
        text={text}
        plainStatusBlocks={plainStatusBlocks}
        cacheKey={cacheKey}
        fallback={historyFallback}
        onParsed={handleWorkerParsed}
        onError={handleWorkerError}
      />
    </Suspense>
  );
});
