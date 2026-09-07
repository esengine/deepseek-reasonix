import type { Translator } from "./i18n";

export interface RecoveryStatus {
  phase?: string;
  reason?: string;
  next_attempt_at?: number;
  waited_ms?: number;
  waiting?: boolean;
  turn_id?: string;
  attempt_id?: string;
  requires_user?: boolean;
  silent?: boolean;
}

export interface RecoveryEventFields {
  recovery?: RecoveryStatus;
  retryAttempt?: number;
  retryMax?: number;
}

export function recoveryStatusText(t: Translator, retry: { attempt: number; max: number; recovery?: RecoveryStatus }, now: number): string {
  if (!retry.recovery?.waiting) return t("status.retrying", { attempt: retry.attempt, max: retry.max });
  const phase = t(retry.recovery.phase === "connect" ? "status.recoveryNetwork" : "status.recoveryProvider");
  const seconds = Math.max(0, Math.ceil(((retry.recovery.next_attempt_at ?? now) - now) / 1000));
  return t("status.recoveryWaiting", { seconds, phase });
}
