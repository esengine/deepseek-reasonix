import { useMemo, useState } from "react";
import { money } from "../i18n/format";
import { t } from "../i18n";
import type { ModelEntry } from "../port/port";
import { accountKey, accountLabel, disambiguate } from "./vendors";

// Models only. Which protocol reaches them is the connection's business, chosen
// once above — an earlier version put a route selector on every row, so picking
// a model silently moved the session to another endpoint.


export interface Vendor {
  key: string;
  label: string;
  host: string;
  // The config entry's own name, shown only when one host holds two accounts.
  hint: string;
  // Models this account offers under each protocol it answers on.
  byKind: Record<string, ModelEntry[]>;
  kinds: string[];
}

export function groupVendors(models: ModelEntry[]): Vendor[] {
  const out = new Map<string, Vendor>();
  for (const m of models) {
    const host = m.vendor || m.provider || "";
    const key = accountKey(host, m.keyEnv);
    let v = out.get(key);
    if (!v) {
      v = { key, label: "", host, hint: m.provider, byKind: {}, kinds: [] };
      out.set(key, v);
    }
    const kind = m.kind || "openai";
    if (!v.byKind[kind]) {
      v.byKind[kind] = [];
      v.kinds.push(kind);
    }
    v.byKind[kind].push(m);
  }
  // The connection list names the same accounts from the same rule; a picker
  // calling one of them something else is two names for one thing.
  for (const v of out.values()) {
    const entries = Object.values(v.byKind).flat();
    v.label = accountLabel(v.host, entries.map((m) => ({ name: m.provider, preset: m.preset })));
  }
  return disambiguate([...out.values()]);
}

// Which protocol this account is on right now: the one holding the running
// model, else the one holding its default, else the first that answered.
export function activeKind(v: Vendor, current?: string): string {
  for (const kind of v.kinds) {
    if (v.byKind[kind].some((m) => m.ref === current)) return kind;
  }
  for (const kind of v.kinds) {
    if (v.byKind[kind].some((m) => m.default)) return kind;
  }
  return v.kinds[0];
}

function contextLabel(tokens?: number): string {
  if (!tokens || tokens <= 0) return "";
  if (tokens >= 1_000_000) {
    const m = tokens / 1_000_000;
    return `${Number.isInteger(m) ? m : m.toFixed(1)}M`;
  }
  return `${Math.round(tokens / 1024)}K`;
}

function priceLabel(m: ModelEntry): string {
  const p = m.price;
  if (!p) return "";
  const code = p.currency ?? "";
  return `${money(p.input, code)} / ${money(p.output, code)}`;
}

// Every tag needs something in the config or the catalog behind it. An inferred
// "reads images" badge is worse than a blank row: it sends the user to a request
// the endpoint rejects, with nothing on screen explaining why.
function tagsFor(m: ModelEntry): [string, string][] {
  const out: [string, string][] = [];
  if (m.vision) out.push(["vis", "读图"]);
  if (m.efforts && m.efforts.length > 1) out.push(["think", "推理"]);
  const ctx = contextLabel(m.contextWindow);
  if (ctx) out.push(["ctx", ctx]);
  const price = priceLabel(m);
  if (price) out.push(["price", price]);
  return out;
}

const VISION_QUERY = ["图", "读图", "看图", "vision", "vl"];

function matches(m: ModelEntry, q: string): boolean {
  if (!q) return true;
  if (VISION_QUERY.some((v) => v === q)) return m.vision === true;
  return m.model.toLowerCase().includes(q) || m.provider.toLowerCase().includes(q);
}

interface Props {
  models: ModelEntry[];
  current?: string;
  busy: string;
  // Which protocol each account is showing, as chosen in the connection list.
  protocol: Record<string, string>;
  onPick: (ref: string) => void;
}

export function Models({ models, current, busy, protocol, onPick }: Props) {
  const [q, setQ] = useState("");
  const vendors = useMemo(() => groupVendors(models), [models]);
  const query = q.trim().toLowerCase();

  if (models.length === 0) return <div className="empty">{t("读不到模型列表。")}</div>;

  const shown = vendors.map((v) => {
    const kind = protocol[v.key] ?? activeKind(v, current);
    return { v, rows: (v.byKind[kind] ?? []).filter((m) => matches(m, query)) };
  });
  const total = vendors.reduce((n, v) => n + (v.byKind[protocol[v.key] ?? activeKind(v, current)]?.length ?? 0), 0);
  const hits = shown.reduce((n, s) => n + s.rows.length, 0);
  const live = shown.filter((s) => s.rows.length > 0);

  return (
    <>
      {total > 8 && (
        <div className="models-find">
          <input
            type="search"
            value={q}
            spellCheck={false}
            placeholder={t("搜模型名，或输入「图」只看能读图的…")}
            aria-label={t("搜索模型")}
            onChange={(e) => setQ(e.target.value)}
          />
          {query && (
            <span className="cnt">
              {hits} / {total}
            </span>
          )}
        </div>
      )}
      {live.map(({ v, rows }) => (
        <div className="mgrp" key={v.key}>
          {/* The header only earns its place when more than one account is
              configured; with one, the models are simply the list. */}
          {vendors.length > 1 && (
            <div className="mgrp-hd">
              <span className="nm">{v.label}</span>
              <span className="url">{v.host}</span>
              <span className="n">{t("{n} 个模型", { n: rows.length })}</span>
            </div>
          )}
          {rows.map((m) => (
            <button
              key={m.ref}
              className="mrow"
            data-action="model.select"
              data-on={m.ref === current ? "" : undefined}
              disabled={busy !== ""}
              onClick={() => onPick(m.ref)}
            >
              <span className="mark" />
              <span className="nm">{m.model}</span>
              <span className="caps">
                {tagsFor(m).map(([k, label]) => (
                  <i className="cap" data-k={k} key={k}>
                    {t(label)}
                  </i>
                ))}
              </span>
            </button>
          ))}
        </div>
      ))}
      {live.length === 0 && <div className="empty">{t("没有匹配的模型。")}</div>}
    </>
  );
}
