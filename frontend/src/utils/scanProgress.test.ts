import { afterEach, describe, expect, it } from 'vitest';

import {
  DEFAULT_ESTIMATE_MS,
  SAMPLE_LIMIT,
  estimateMs,
  formatDuration,
  progressFraction,
  progressLabel,
  readSamples,
  recordSample,
  remainingMs
} from './scanProgress';

afterEach(() => {
  localStorage.clear();
});

describe('estimateMs', () => {
  it('starts from the measured default before this browser has scanned', () => {
    expect(estimateMs([])).toBe(DEFAULT_ESTIMATE_MS);
    expect(estimateMs()).toBe(DEFAULT_ESTIMATE_MS);
  });

  it('uses the median, so one cold start does not skew every later estimate', () => {
    // A first scan that had to load the model takes far longer than the rest.
    expect(estimateMs([30_000, 32_000, 120_000])).toBe(32_000);
    expect(estimateMs([30_000, 34_000])).toBe(32_000);
  });

  it('clamps absurd samples', () => {
    expect(estimateMs([1])).toBeGreaterThanOrEqual(5_000);
    expect(estimateMs([9_999_999])).toBeLessThanOrEqual(180_000);
  });
});

describe('recordSample', () => {
  it('keeps only the most recent samples so the estimate can adapt', () => {
    for (let i = 1; i <= SAMPLE_LIMIT + 3; i++) {
      recordSample(i * 1000);
    }
    const samples = readSamples();
    expect(samples).toHaveLength(SAMPLE_LIMIT);
    // The earliest ones have aged out.
    expect(samples).not.toContain(1000);
    expect(samples).toContain((SAMPLE_LIMIT + 3) * 1000);
  });

  it('ignores nonsense rather than poisoning the estimate', () => {
    recordSample(0);
    recordSample(-5);
    recordSample(NaN);
    recordSample(Infinity);
    expect(readSamples()).toHaveLength(0);
  });

  it('survives corrupt storage', () => {
    localStorage.setItem('mpb_scan_durations', 'not json');
    expect(readSamples()).toEqual([]);
    recordSample(30_000);
    expect(readSamples()).toEqual([30_000]);
  });

  it('discards non-numeric entries left by anything else', () => {
    localStorage.setItem('mpb_scan_durations', JSON.stringify([30_000, 'x', null, 31_000]));
    expect(readSamples()).toEqual([30_000, 31_000]);
  });
});

describe('progressFraction', () => {
  it('runs from empty to nearly full across the estimate', () => {
    expect(progressFraction(0, 30_000)).toBe(0);
    expect(progressFraction(15_000, 30_000)).toBeCloseTo(0.45, 2);
    expect(progressFraction(30_000, 30_000)).toBeCloseTo(0.9, 2);
  });

  it('never reaches full while the request is still open', () => {
    // A filled bar on an unfinished request reads as a hang. Completion is the
    // response arriving, not the timer expiring.
    for (const elapsed of [30_000, 60_000, 120_000, 600_000]) {
      const f = progressFraction(elapsed, 30_000);
      expect(f).toBeLessThan(1);
      expect(f).toBeLessThanOrEqual(0.99);
    }
  });

  it('keeps creeping forward past the estimate rather than stalling', () => {
    const at = [31_000, 45_000, 60_000, 90_000].map((ms) => progressFraction(ms, 30_000));
    for (let i = 1; i < at.length; i++) {
      expect(at[i]).toBeGreaterThan(at[i - 1]);
    }
  });

  it('stays in range for degenerate input', () => {
    expect(progressFraction(-1, 30_000)).toBe(0);
    expect(progressFraction(NaN, 30_000)).toBe(0);
    expect(progressFraction(1000, 0)).toBeLessThanOrEqual(0.99);
    expect(progressFraction(1000, 0)).toBeGreaterThanOrEqual(0);
  });

  it('is monotonic through the whole run', () => {
    let previous = -1;
    for (let ms = 0; ms <= 120_000; ms += 1000) {
      const f = progressFraction(ms, 35_000);
      expect(f).toBeGreaterThanOrEqual(previous);
      previous = f;
    }
  });
});

describe('remainingMs', () => {
  it('counts down and then stops at zero', () => {
    expect(remainingMs(0, 30_000)).toBe(30_000);
    expect(remainingMs(10_000, 30_000)).toBe(20_000);
    expect(remainingMs(30_000, 30_000)).toBe(0);
    expect(remainingMs(90_000, 30_000)).toBe(0);
  });
});

describe('formatDuration', () => {
  it('formats as m:ss', () => {
    expect(formatDuration(0)).toBe('0:00');
    expect(formatDuration(9_400)).toBe('0:09');
    expect(formatDuration(35_000)).toBe('0:35');
    expect(formatDuration(60_000)).toBe('1:00');
    expect(formatDuration(125_000)).toBe('2:05');
  });

  it('never shows a negative duration', () => {
    expect(formatDuration(-5_000)).toBe('0:00');
  });
});

describe('progressLabel', () => {
  it('reports elapsed, remaining and the estimate', () => {
    const label = progressLabel(10_000, 35_000);
    expect(label).toContain('0:10 elapsed');
    expect(label).toContain('0:25 left');
    expect(label).toContain('0:35');
  });

  it('stops inventing a countdown once the estimate is spent', () => {
    const label = progressLabel(60_000, 35_000);
    expect(label).toContain('1:00 elapsed');
    expect(label).toMatch(/taking longer/i);
    expect(label).not.toContain('left');
  });
});
