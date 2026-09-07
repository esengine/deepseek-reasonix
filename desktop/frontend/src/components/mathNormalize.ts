// Deterministic pre-pass that repairs LLM-typical math syntax before
// remark-math parses Markdown. Semantic inline classification lives in
// remarkMathPolicy, where code boundaries and surrounding AST context are
// already known.
//
//   1. Protect Markdown code spans/fences from all math rewrites.
//   2. Protect LaTeX line-break spacing (\\[...]) from the LLM-delimiter rewrite.
//   3. \(...)/\[...] → $/$$.
//   4. Expand \yng/\young to KaTeX-compatible \boxed{array} forms.
//      Stateful: tracks `$…$` so bare macros in prose get wrapped in
//      `$…$` and macros already inside math just substitute.
//   5. Inline `$$` glued to prose gets a blank line inserted before it
//      (CommonMark requires that block math be paragraph-separated).
//   6. Escape currency dollars (`$5` followed by prose) so greedy single-$
//      pairing cannot swallow a later math span or pair across amounts
//      (assistant-ui escapeCurrencyDollars parity).
//   7. Protect pipes inside inline math before GFM table tokenisation.
//   8. Restore placeholders for remark-math. KaTeX-specific normalisation is
//      handled by the AST policy after parsing.

import { expandYoungDiagrams } from "./youngDiagrams";

// Matches $\cmd{...}...$ where the body may contain $ and one level of nested
// braces. Group 1 captures the full \cmd{...} including the outer }. After
// the closing }, [^$]*? consumes any trailing content (e.g. " + x^2") up to
// the closing $, so patterns like $\text{a} + x^2$ are handled as a whole
// rather than split at stray $ signs inside \text{}.
const TEXT_MODE_PAIR = /\$\s*(\\[A-Za-z]+\{(?:[^{}]|\{[^{}]*\})*\}[^$]*?)\s*\$/g;

const DM = "__REASONIX_MATH_DISPLAY__";
const IM = "__REASONIX_MATH_INLINE__";
const LB = "__REASONIX_LATEX_LINEBREAK__";
const ED_BASE = "REASONIXESCAPEDDOLLAR";
const INLINE_SOURCE_PREFIX = "\\reasonixInternalSourceV1{";
const INLINE_RENDER_SOURCE_PREFIX = "\\reasonixInternalRenderV1";
const INLINE_RENDER_SOURCE_RE = /\\reasonixInternalRenderV1\{([^}]*)\}\{([^}]*)\}/g;

export function normalizeMath(s: string): string {
  const protectedCode = protectMarkdownCode(s);
  let r = normalizeMathText(protectedCode.text);
  for (let i = 0; i < protectedCode.segments.length; i += 1) {
    r = r.split(`${protectedCode.prefix}${i}__`).join(protectedCode.segments[i]);
  }
  return r;
}

function normalizeMathText(s: string): string {
  // Step 1: protect LaTeX line-break spacing (\\[4pt], \\[2ex], ...) so the
  // \[ → $$ rewrite below doesn't swallow it.
  let r = s.replace(/\\\\\[/g, LB);

  // Step 2: convert LLM-native delimiters to standard $/$$ syntax. Arrow
  // functions are required because "$$" in a JS replace string means a
  // single literal $.
  r = r
    .replace(/\\\[/g, () => "$$")
    .replace(/\\\]/g, () => "$$")
    .replace(/\\\(/g, () => "$")
    .replace(/\\\)/g, () => "$");
  r = r.replace(new RegExp(LB, "g"), "\\\\[");

  // Step 2.5: expand \yng/\young macros to KaTeX-compatible \boxed{array}
  // forms after LLM-native delimiters have been converted, so macros inside
  // \(...\) or \[...\] are correctly recognised as already being in math.
  r = expandYoungDiagrams(r, ({ source, rendered }) => {
    return protectInlineMathRenderSource(source, rendered);
  });

  // Escaped dollars are literal prose dollars, not math delimiters. Hide them
  // while the structural dollar scans below run, then restore them before
  // remark-math parses the result.
  const escapedDollarToken = unusedEscapedDollarToken(r);
  r = r.split("\\$").join(escapedDollarToken);

  // Step 3+4: normalise display $$ block structure. remark-math requires
  // opening and closing $$ to sit on their own lines; LLMs often emit
  // single-line displays, opening fences glued to prose, and adjacent display
  // blocks separated by prose. A line parser avoids the old cross-block regex
  // capture that swallowed prose between two display blocks.
  r = normaliseDisplayBlocks(r);

  // Step 5: preserve the complete source of $\cmd{...}$ pairs containing a
  // stray inner $ (e.g. $\text{price is $5}$) in a parser-safe marker. The
  // AST policy restores and normalises it after remark-math has established
  // the real formula boundary.
  r = r.replace(TEXT_MODE_PAIR, (match, math: string) => {
    return math.includes("$") ? `${IM}${protectInlineMathSource(math)}${IM}` : match;
  });

  // Step 5.5: escape currency dollars so a stray amount cannot pair with a
  // later math span's opening delimiter. remark-math pairs any two `$` on a
  // line, so `budget is $100 and the answer is $42$` would otherwise merge
  // `$100 and the answer is $` into one junk span and destroy the math. A
  // single `$` immediately followed by a digit is prose currency unless the
  // span up to the next `$` reads as a math body (assistant-ui
  // `escapeCurrencyDollars` parity). Display `$$` runs and escaped dollars
  // (already hidden above) are never touched.
  r = escapeCurrencyDollars(r, escapedDollarToken);

  // Step 6: GFM identifies table cells before remark plugins can transform the
  // AST. Normalise only inline spans containing pipes at this syntax boundary
  // so formulas such as `$|x|$` cannot be split into separate cells. A pipe is
  // already an explicit math signal in the semantic classifier; all other
  // inline decisions remain deferred to remarkMathPolicy.
  r = protectInlineMathPipesForGfm(r);

  // Step 7: restore standard $/$$ delimiters for remark-math to parse.
  return r
    .replace(new RegExp(DM, "g"), () => "$$")
    .replace(new RegExp(IM, "g"), "$")
    .split(escapedDollarToken).join("\\$");
}

function protectInlineMathPipesForGfm(s: string): string {
  return s.replace(/\$([^$\n]+)\$/g, (match, math: string) => {
    let hasUnescapedPipe = false;
    let backslashes = 0;
    for (const char of math) {
      if (char === "|" && backslashes % 2 === 0) {
        hasUnescapedPipe = true;
        break;
      }
      backslashes = char === "\\" ? backslashes + 1 : 0;
    }
    return hasUnescapedPipe ? `$${protectInlineMathSource(math)}$` : match;
  });
}

// ── currency-dollar escaping (assistant-ui escapeCurrencyDollars parity) ─────

const MATH_BODY_BLANK_LINE = /\n\s*\n/;
const MATH_BODY_LATEX = /\\[A-Za-z]+|[\^_{}]/;
const MATH_BODY_ADJACENT_WORDS = /[A-Za-z0-9]\s+[A-Za-z0-9]/;
const MATH_BODY_DANGLING_OPERATOR = /[+\-*/=<>~^_]$/;

/**
 * Whether the text between two single `$` reads as an inline math expression
 * rather than the prose separating two currency amounts. A body that ends
 * the way prose between amounts does (mid-sentence space, dangling operator)
 * is rejected even when it carries LaTeX-looking syntax, since such prose may
 * itself contain `_` or `\word`; otherwise LaTeX syntax accepts the span and
 * two adjacent words reject it.
 */
function isCurrencySafeMathBody(body: string): boolean {
  if (body.length === 0) return false;
  if (MATH_BODY_BLANK_LINE.test(body)) return false;
  if (/\s$/.test(body) && !/^\s/.test(body)) return false;
  if (MATH_BODY_DANGLING_OPERATOR.test(body)) return false;
  if (MATH_BODY_LATEX.test(body)) return true;
  return !MATH_BODY_ADJACENT_WORDS.test(body);
}

function isDigitChar(char: string | undefined): boolean {
  return char !== undefined && char >= "0" && char <= "9";
}

function dollarRunLength(s: string, index: number): number {
  let length = 0;
  while (s[index + length] === "$") length += 1;
  return length;
}

function nextSingleDollar(s: string, from: number): number {
  let index = from;
  while (index < s.length) {
    if (s[index] === "$") {
      // A `$$` run is a display delimiter, not an inline closer.
      return dollarRunLength(s, index) >= 2 ? -1 : index;
    }
    index += 1;
  }
  return -1;
}

/**
 * Escapes a `$` that opens a currency amount (`$5`, `$19.99`, `$1,299`) so
 * remark-math's greedy single-dollar pairing cannot consume it — either as a
 * junk span with a later math delimiter or as one half of a cross-amount
 * pair. A `$` followed by a digit is currency unless the span up to the next
 * `$` is a plausible math body, so `$42$` and `$5x = 10$` survive while
 * `$5 and $7` and `budget is $100 … $42$` stay literal/functional.
 * Escaping via `escapedDollarToken` keeps the emitted text free of `$` so
 * later `$`-scanning steps (pipe protection, remark-math) cannot re-pair it.
 */
function escapeCurrencyDollars(s: string, escapedDollar: string): string {
  let out = "";
  let index = 0;
  while (index < s.length) {
    const char = s[index];
    // Escaped characters (LaTeX commands, \\) pass through untouched.
    if (char === "\\") {
      out += s.slice(index, index + 2);
      index += 2;
      continue;
    }
    if (char !== "$") {
      out += char;
      index += 1;
      continue;
    }
    const run = dollarRunLength(s, index);
    if (run >= 2) {
      out += s.slice(index, index + run);
      index += run;
      continue;
    }
    const close = nextSingleDollar(s, index + 1);
    const body = close === -1 ? null : s.slice(index + 1, close);
    const opensMath = body !== null
      && !isDigitChar(s[close + 1])
      && isCurrencySafeMathBody(body);
    if (opensMath) {
      out += s.slice(index, close + 1);
      index = close + 1;
      continue;
    }
    out += isDigitChar(s[index + 1]) ? escapedDollar : "$";
    index += 1;
  }
  return out;
}

function protectInlineMathSource(source: string): string {
  return `${INLINE_SOURCE_PREFIX}${encodeURIComponent(source)}}`;
}

function protectInlineMathRenderSource(source: string, rendered: string): string {
  return `${INLINE_RENDER_SOURCE_PREFIX}{${encodeURIComponent(source)}}{${encodeURIComponent(rendered)}}`;
}

export interface ResolvedInlineMathSource {
  source: string;
  rendered: string;
}

export function resolveProtectedInlineMathSource(value: string): ResolvedInlineMathSource {
  let payload = value;
  if (payload.startsWith(INLINE_SOURCE_PREFIX) && payload.endsWith("}")) {
    const encoded = payload.slice(INLINE_SOURCE_PREFIX.length, -1);
    try {
      payload = decodeURIComponent(encoded);
    } catch {
      payload = value;
    }
  }

  const replaceRenderSource = (part: "source" | "rendered"): string => {
    return payload.replace(
      INLINE_RENDER_SOURCE_RE,
      (match, encodedSource: string, encodedRendered: string) => {
        try {
          return decodeURIComponent(part === "source" ? encodedSource : encodedRendered);
        } catch {
          return match;
        }
      },
    );
  };

  return {
    source: replaceRenderSource("source"),
    rendered: replaceRenderSource("rendered"),
  };
}

export function restoreProtectedInlineMathSource(source: string): string {
  return resolveProtectedInlineMathSource(source).source;
}

function unusedEscapedDollarToken(s: string): string {
  let token = ED_BASE;
  let n = 0;
  while (s.includes(token)) {
    n += 1;
    token = `${ED_BASE}${n}`;
  }
  return token;
}

function protectMarkdownCode(s: string): { text: string; prefix: string; segments: string[] } {
  const prefix = unusedPlaceholderPrefix(s);
  const segments: string[] = [];
  let out = "";
  let i = 0;

  const pushSegment = (segment: string) => {
    const token = `${prefix}${segments.length}__`;
    segments.push(segment);
    out += token;
  };

  while (i < s.length) {
    const fenceEnd = fencedCodeEnd(s, i);
    if (fenceEnd > i) {
      pushSegment(s.slice(i, fenceEnd));
      i = fenceEnd;
      continue;
    }

    if (s[i] === "`") {
      const tickEnd = inlineCodeEnd(s, i);
      if (tickEnd > i) {
        pushSegment(s.slice(i, tickEnd));
        i = tickEnd;
        continue;
      }
    }

    out += s[i];
    i += 1;
  }

  return { text: out, prefix, segments };
}

function unusedPlaceholderPrefix(s: string): string {
  let prefix = "__REASONIX_PROTECTED_CODE__";
  let n = 0;
  while (s.includes(prefix)) {
    n += 1;
    prefix = `__REASONIX_PROTECTED_CODE_${n}__`;
  }
  return prefix;
}

function fencedCodeEnd(s: string, start: number): number {
  // Fence must be at the start of a line (or the document) — CommonMark
  // requirement. Allowing mid-line fences would swallow prose like
  // "wrap code in ```blocks``` here" into the code region.
  if (start !== 0 && s[start - 1] !== "\n") return -1;

  let markerStart = start;
  let spaces = 0;
  while (spaces < 4 && s[markerStart] === " ") {
    markerStart += 1;
    spaces += 1;
  }

  const marker = s[markerStart];
  if (marker !== "`" && marker !== "~") return -1;

  let fenceLen = 0;
  while (s[markerStart + fenceLen] === marker) fenceLen += 1;
  if (fenceLen < 3) return -1;

  const openingLineEnd = lineEnd(s, markerStart + fenceLen);

  // Single-line doc: treat the next matching fence as the closing fence.
  if (openingLineEnd >= s.length) {
    const fencePattern = marker.repeat(fenceLen);
    const nextFence = s.indexOf(fencePattern, markerStart + fenceLen);
    if (nextFence === -1) return s.length;
    return nextFence + fenceLen;
  }

  let lineStart = openingLineEnd + 1;
  while (lineStart < s.length) {
    const currentLineEnd = lineEnd(s, lineStart);
    if (isClosingFenceLine(s, lineStart, currentLineEnd, marker, fenceLen)) {
      return currentLineEnd < s.length ? currentLineEnd + 1 : currentLineEnd;
    }
    lineStart = currentLineEnd < s.length ? currentLineEnd + 1 : currentLineEnd;
  }

  return s.length;
}

function isClosingFenceLine(s: string, start: number, end: number, marker: string, minLen: number): boolean {
  let i = start;
  let spaces = 0;
  while (spaces < 4 && s[i] === " ") {
    i += 1;
    spaces += 1;
  }

  let count = 0;
  while (s[i + count] === marker) count += 1;
  if (count < minLen) return false;

  for (let j = i + count; j < end; j += 1) {
    if (s[j] !== " " && s[j] !== "\t") return false;
  }
  return true;
}

function inlineCodeEnd(s: string, start: number): number {
  let tickLen = 0;
  while (s[start + tickLen] === "`") tickLen += 1;

  const ticks = "`".repeat(tickLen);
  const end = s.indexOf(ticks, start + tickLen);
  return end < 0 ? -1 : end + tickLen;
}

function lineEnd(s: string, start: number): number {
  const end = s.indexOf("\n", start);
  return end < 0 ? s.length : end;
}

function normaliseDisplayBlocks(s: string): string {
  const lines = s.split("\n");
  const out: string[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];
    const $$idx = line.indexOf("$$");

    if ($$idx >= 0 && line.indexOf("$$", $$idx + 2) >= 0
        && !($$idx > 0 && /\d/.test(line[$$idx - 1]))) {
      const m = line.match(/^(.*?)\$\$([^\n]*?)\$\$(.*)$/);
      if (m) {
        const quote = blockquotePrefix(m[1]);
        pushDisplayBefore(out, m[1]);
        out.push(DM);
        out.push(m[2]);
        out.push(DM);
        if (m[3]) out.push(normaliseDisplayBlocks(quote ? quote + m[3].trimStart() : m[3]));
        i += 1;
        continue;
      }
    }

    if ($$idx >= 0 && line.indexOf("$$", $$idx + 2) < 0
        && !($$idx > 0 && /\d/.test(line[$$idx - 1]))) {
      const before = line.slice(0, $$idx);
      const afterOpen = line.slice($$idx + 2);
      const quote = blockquotePrefix(before);

      const formulaLines: string[] = [];
      if (afterOpen) formulaLines.push(afterOpen);

      let j = i + 1;
      let found = false;
      while (j < lines.length) {
        const rawLine = lines[j];
        const fLine = quote ? stripBlockquotePrefix(rawLine, quote) : rawLine;
        const closeIdx = fLine.indexOf("$$");
        if (closeIdx >= 0 && fLine.indexOf("$$", closeIdx + 2) < 0) {
          const formulaPart = fLine.slice(0, closeIdx);
          const afterClose = fLine.slice(closeIdx + 2);
          pushDisplayBefore(out, before);
          if (formulaPart) formulaLines.push(formulaPart);
          const formula = formulaLines.join("\n");
          out.push(DM);
          out.push(formula);
          out.push(DM);
          if (afterClose) out.push(quote ? quote + afterClose.trimStart() : afterClose);
          i = j + 1;
          found = true;
          break;
        }
        formulaLines.push(fLine);
        j += 1;
      }

      if (found) continue;
      pushDisplayBefore(out, before);
      out.push("$$");
      if (afterOpen) out.push(afterOpen);
      i += 1;
      continue;
    }

    out.push(line);
    i += 1;
  }

  return out.join("\n");
}

function pushDisplayBefore(out: string[], before: string): void {
  if (!before.trim()) return;
  out.push(before);
}

function blockquotePrefix(before: string): string | null {
  const m = before.match(/^(\s*>\s*)/);
  return m ? m[1] : null;
}

function stripBlockquotePrefix(line: string, prefix: string): string {
  if (line.startsWith(prefix)) return line.slice(prefix.length);
  const marker = prefix.trimEnd();
  if (marker && line.startsWith(marker)) {
    const rest = line.slice(marker.length);
    return rest.startsWith(" ") ? rest.slice(1) : rest;
  }
  return line;
}
