// Progress estimation for receipt scanning.
//
// A scan is one synchronous request with no progress events to report, so the bar
// is an estimate. It is a useful one because the duration is fairly stable and
// barely depends on receipt length -- measured ~32s for a 4-item grocery receipt
// and ~37s for a 14-item restaurant check on the same hardware.
//
// Rather than hardcode that, the estimate is learned from what this deployment
// actually does: each completed scan is recorded and the estimate is the median of
// recent ones. A faster or slower inference box converges on its own timing within
// a few scans.

const STORAGE_KEY = 'mpb_scan_durations';

/** Starting estimate until this browser has seen a scan of its own. */
export const DEFAULT_ESTIMATE_MS = 35_000;

/** How many recent scans feed the median. Enough to smooth, few enough to adapt. */
export const SAMPLE_LIMIT = 5;

// Guard rails, so one absurd sample cannot make the bar useless.
const MIN_ESTIMATE_MS = 5_000;
const MAX_ESTIMATE_MS = 180_000;

/** Fraction the bar reaches at exactly the estimated duration. */
const FRACTION_AT_ESTIMATE = 0.9;

/** The bar never claims to be finished while the request is still open. */
const MAX_FRACTION = 0.99;

export function readSamples(): number[] {
  if (typeof localStorage === 'undefined') return [];
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((v): v is number => typeof v === 'number' && Number.isFinite(v) && v > 0);
  } catch {
    // Corrupt or unavailable storage is not worth failing a scan over.
    return [];
  }
}

/** Records a completed scan, keeping only the most recent SAMPLE_LIMIT. */
export function recordSample(durationMs: number): void {
  if (typeof localStorage === 'undefined') return;
  if (!Number.isFinite(durationMs) || durationMs <= 0) return;
  try {
    const next = [...readSamples(), durationMs].slice(-SAMPLE_LIMIT);
    localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  } catch {
    // Storage full or blocked; the estimate simply stays where it was.
  }
}

/**
 * Median of recent scans, clamped. Median rather than mean so a single cold-start
 * scan -- where the model still has to load -- does not skew every later estimate.
 */
export function estimateMs(samples: number[] = readSamples()): number {
  if (!samples.length) return DEFAULT_ESTIMATE_MS;
  const sorted = [...samples].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  const median = sorted.length % 2 === 1 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
  return Math.min(MAX_ESTIMATE_MS, Math.max(MIN_ESTIMATE_MS, median));
}

/**
 * Fraction complete, in [0, MAX_FRACTION].
 *
 * Linear up to the estimate, then asymptotic: an overrunning scan keeps creeping
 * forward instead of sitting at a full bar, which reads as a hang. It never
 * reaches 1 while the request is open -- completion is the response arriving, not
 * a timer expiring.
 */
export function progressFraction(elapsedMs: number, estimate: number = estimateMs()): number {
  if (!Number.isFinite(elapsedMs) || elapsedMs <= 0) return 0;
  const budget = Math.max(1, estimate);
  if (elapsedMs < budget) {
    return (elapsedMs / budget) * FRACTION_AT_ESTIMATE;
  }
  // Each further "budget" of waiting covers a shrinking share of what is left.
  const overrun = (elapsedMs - budget) / budget;
  const remaining = MAX_FRACTION - FRACTION_AT_ESTIMATE;
  return FRACTION_AT_ESTIMATE + remaining * (1 - Math.exp(-overrun));
}

/** Milliseconds left by the estimate, or 0 once it has been passed. */
export function remainingMs(elapsedMs: number, estimate: number = estimateMs()): number {
  return Math.max(0, estimate - Math.max(0, elapsedMs));
}

/** Rounds to whole seconds as m:ss, since sub-second precision is noise here. */
export function formatDuration(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes}:${String(seconds).padStart(2, '0')}`;
}

/** The line shown under the bar. */
export function progressLabel(elapsedMs: number, estimate: number = estimateMs()): string {
  const elapsed = formatDuration(elapsedMs);
  const left = remainingMs(elapsedMs, estimate);
  if (left <= 0) {
    // Do not invent a countdown once the estimate is spent.
    return `${elapsed} elapsed — taking longer than usual, still working`;
  }
  return `${elapsed} elapsed — about ${formatDuration(left)} left of ~${formatDuration(estimate)}`;
}
