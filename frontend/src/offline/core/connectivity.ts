/**
 * Connectivity detection (OFF-04): navigator.onLine is only a hint — the truth is a real round trip to
 * /api/health with a short timeout. Probes every few seconds (faster right after a drop, slower when
 * hidden) and immediately on online/offline/visibility events, so a change is noticed within ≤ 5 s.
 */
import { probeInterval } from "./backoff";

export type Reachability = "online" | "offline" | "unknown";

export interface ConnectivitySnapshot {
  status: Reachability;
  since: number;
  lastProbeAt: number | null;
  latencyMs: number | null;
}

type Listener = () => void;

const PROBE_TIMEOUT_MS = 4_000;

class Connectivity {
  private snap: ConnectivitySnapshot = { status: "unknown", since: Date.now(), lastProbeAt: null, latencyMs: null };
  private listeners = new Set<Listener>();
  private timer: ReturnType<typeof setTimeout> | null = null;
  private probing: Promise<boolean> | null = null;
  private started = false;
  probeUrl = "/api/health";

  get = () => this.snap;

  subscribe = (l: Listener): (() => void) => {
    this.listeners.add(l);
    return () => void this.listeners.delete(l);
  };

  private set(status: Reachability, latencyMs: number | null) {
    const changed = status !== this.snap.status;
    this.snap = { status, since: changed ? Date.now() : this.snap.since, lastProbeAt: Date.now(), latencyMs };
    if (changed) this.listeners.forEach((l) => l());
  }

  /** One real reachability check. */
  probe = (): Promise<boolean> => {
    if (this.probing) return this.probing;
    this.probing = (async () => {
      if (typeof navigator !== "undefined" && navigator.onLine === false) {
        this.set("offline", null);
        return false;
      }
      const ctrl = new AbortController();
      const timer = setTimeout(() => ctrl.abort(), PROBE_TIMEOUT_MS);
      const t0 = performance.now();
      try {
        const res = await fetch(`${this.probeUrl}?t=${Date.now()}`, { cache: "no-store", credentials: "omit", signal: ctrl.signal, headers: { Accept: "application/json" } });
        // any HTTP answer from our API means the network path works; 5xx from a proxy with no API counts as offline
        const ok = res.status < 500 && (res.headers.get("content-type") ?? "").includes("json");
        this.set(ok ? "online" : "offline", Math.round(performance.now() - t0));
        return ok;
      } catch {
        this.set("offline", null);
        return false;
      } finally {
        clearTimeout(timer);
        this.probing = null;
        this.schedule();
      }
    })();
    return this.probing;
  };

  /** Callers that saw a network failure report it, triggering an immediate re-check. */
  reportFailure = () => void this.probe();

  private schedule() {
    if (!this.started) return;
    if (this.timer) clearTimeout(this.timer);
    const visible = typeof document === "undefined" || document.visibilityState === "visible";
    const st = this.snap.status === "online" ? "online" : "offline";
    this.timer = setTimeout(() => void this.probe(), probeInterval(st, Date.now() - this.snap.since, visible));
  }

  start() {
    if (this.started || typeof window === "undefined") return;
    this.started = true;
    window.addEventListener("online", this.probe);
    window.addEventListener("offline", () => {
      this.set("offline", null);
      this.schedule();
    });
    document.addEventListener("visibilitychange", () => document.visibilityState === "visible" && void this.probe());
    void this.probe();
  }
}

export const connectivity = new Connectivity();
