export type InlineMathDecision = "math" | "literal";

const PURE_NUMBER = /^\d+(?:,\d{3})*(?:\.\d+)?$/;

/**
 * Decide how an `inlineMath` node produced by remark-math should render.
 *
 * Pure-number spans with glued delimiters are mathematical by default
 * (assistant-ui `escapeCurrencyDollars` parity): prose currency pairs such
 * as `$5 and $10` never reach this classifier with a numeric body, because
 * their word-containing spans match no math pattern below and restore
 * verbatim. Other non-math content is also restored verbatim as text.
 */
export function classifyInlineMath(math: string): InlineMathDecision {
  if (!math || math !== math.trim() || math.includes("\n")) return "literal";
  if (math.includes("://") || math.includes("](")) return "literal";

  if (PURE_NUMBER.test(math)) return "math";

  // A percentage enclosed in explicit delimiters is mathematical notation.
  // latexNormalizeForKatex escapes its `%` for KaTeX.
  if (/^\d+(?:,\d{3})*(?:\.\d+)?%$/.test(math)) return "math";

  // Number followed by variable: implicit multiplication (2.5x, 3y^2).
  if (/^\d+(?:\.\d+)?[A-Za-z](?:[A-Za-z0-9^_{}]*)?$/.test(math)) return "math";
  // Number with LaTeX escape: 10\%, 5\cdot3.
  if (/^\d+(?:\.\d+)?\\(?:%|[A-Za-z]+)(?:\{[^{}]*\})?(?:[A-Za-z0-9\\{}^_+\-*/=<>.()]*)$/.test(math)) return "math";

  // Unary plus/minus: +2, -x, +\alpha, - 3.14.
  if (/^[+\-]\s*(?:\d+(?:\.\d+)?|[A-Za-z\\])/.test(math)) return "math";

  // LaTeX command (\alpha, \frac{x}{y}, \tfrac12, ...).
  if (/\\[A-Za-z]+/.test(math)) return "math";
  if (/[\^_{}|]/.test(math)) return "math";
  if (/^[+\-=<>±∓]$/.test(math)) return "math";
  if (/\b(?:alpha|beta|gamma|sum|int|prod|lim|infty|sqrt|frac|sin|cos|tan|log|ln|max|min|partial|nabla|left|right)\b/.test(math)) return "math";
  if (/^[A-Za-z]{1,6}\s*\([^)]{1,80}\)$/.test(math)) return "math";
  if (/^[A-Za-z]'{1,}\s*\([^)]{1,80}\)$/.test(math)) return "math";
  if (/^(?:\(\d+\))+$/.test(math)) return "math";
  if (/[A-Za-z0-9)\]}]\s*[+\-*/=<>]\s*[+\-]?\s*[A-Za-z0-9([{\\]/.test(math)) return "math";

  // One-sided relation/equality against an implicit operand. The complete
  // anchors are important: `=1 dollar` must not be accepted by a prefix match.
  const operand = String.raw`(?:[+\-]?\s*(?:\d+(?:\.\d+)?|[A-Za-z]|\\[A-Za-z]+))`;
  const relation = String.raw`(?:<=?|>=?|=|≠|≤|≥)`;
  if (new RegExp(`^(?:${relation}\\s*${operand}|${operand}\\s*${relation})$`).test(math)) return "math";

  if (/^\(?(?:[A-Za-z0-9]|\\[A-Za-z]+)(?:\s*,\s*(?:[A-Za-z0-9]|\\[A-Za-z]+)){1,10}\)?$/.test(math)) return "math";
  if (/^\[[A-Za-z0-9^_+\-,.\\\s{}]+\]$/.test(math)) return "math";

  if (/[A-Za-z]\s+[A-Za-z]/.test(math)) return "literal";
  if (/^[A-Z][A-Z0-9_]{1,}$/.test(math)) return "literal";
  if (/^v\d+(?:\.\d+)*$/i.test(math)) return "literal";
  if (/^[A-Za-z]{2,}$/.test(math)) return "literal";

  if (/^(?:[A-Za-z]|\\[A-Za-z]+)'{1,}$/.test(math)) return "math";
  return /^[A-Za-z]$/.test(math) ? "math" : "literal";
}

export function isLikelyInlineMath(math: string): boolean {
  return classifyInlineMath(math) === "math";
}
