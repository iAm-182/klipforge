(function (root, factory) {
  'use strict';
  const api = factory();
  if (typeof module === 'object' && module.exports) {
    module.exports = api;
  }
  if (typeof window !== 'undefined') {
    window.KFCutter = api;
  } else if (root && typeof module !== 'object') {
    root.KFCutter = api;
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  const PLAIN_NUMBER_RE = /^\d+(?:\.\d+)?$/;
  const INT_PART_RE = /^\d+$/;
  const SEC_PART_RE = /^\d+(?:\.\d+)?$/;
  const HMS_RE = /^(?:(\d+)\s*h)?\s*(?:(\d+)\s*m)?\s*(?:(\d+(?:\.\d+)?)\s*s)?$/i;

  function roundMillis(value) {
    return Math.round(value * 1000) / 1000;
  }

  function parseTimecode(input) {
    if (typeof input === 'number') {
      if (!Number.isFinite(input) || input < 0) return null;
      return input === 0 ? 0 : input;
    }

    if (typeof input !== 'string') {
      return null;
    }

    const raw = input.trim();
    if (!raw) {
      return null;
    }

    if (PLAIN_NUMBER_RE.test(raw)) {
      const value = Number(raw);
      return Number.isFinite(value) && value >= 0 ? value : null;
    }

    if (raw.includes(':')) {
      const parts = raw.split(':');
      if (parts.length === 2) {
        const [minStr, secStr] = parts;
        if (!INT_PART_RE.test(minStr) || !SEC_PART_RE.test(secStr)) {
          return null;
        }
        const min = Number(minStr);
        const sec = Number(secStr);
        if (!Number.isFinite(min) || !Number.isFinite(sec) || sec >= 60) {
          return null;
        }
        return min * 60 + sec;
      }

      if (parts.length === 3) {
        const [hrStr, minStr, secStr] = parts;
        if (!INT_PART_RE.test(hrStr) || !INT_PART_RE.test(minStr) || !SEC_PART_RE.test(secStr)) {
          return null;
        }
        const hr = Number(hrStr);
        const min = Number(minStr);
        const sec = Number(secStr);
        if (!Number.isFinite(hr) || !Number.isFinite(min) || !Number.isFinite(sec) || min >= 60 || sec >= 60) {
          return null;
        }
        return hr * 3600 + min * 60 + sec;
      }

      return null;
    }

    const hmsMatch = HMS_RE.exec(raw);
    if (hmsMatch && (hmsMatch[1] !== undefined || hmsMatch[2] !== undefined || hmsMatch[3] !== undefined)) {
      const hr = hmsMatch[1] !== undefined ? Number(hmsMatch[1]) : 0;
      const min = hmsMatch[2] !== undefined ? Number(hmsMatch[2]) : 0;
      const sec = hmsMatch[3] !== undefined ? Number(hmsMatch[3]) : 0;
      const total = hr * 3600 + min * 60 + sec;
      return Number.isFinite(total) && total >= 0 ? total : null;
    }

    return null;
  }

  function formatTimecode(sec) {
    if (typeof sec === 'boolean' || sec === null || sec === undefined || sec === '') {
      return '00:00';
    }
    const num = Number(sec);
    if (!Number.isFinite(num) || num <= 0) {
      return '00:00';
    }

    const total = Math.round(num);
    const h = Math.floor(total / 3600);
    const m = Math.floor((total % 3600) / 60);
    const s = total % 60;
    const pad = (n) => String(n).padStart(2, '0');
    return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`;
  }

  function nudgeRange(startSec, endSec, which, deltaSec, maxDurSec) {
    let start = Number(startSec);
    let end = Number(endSec);
    const delta = Number(deltaSec);
    const maxNum = Number(maxDurSec);
    const hasMax = Number.isFinite(maxNum) && maxNum > 0;
    const max = hasMax ? maxNum : Infinity;

    if (!Number.isFinite(start) || start < 0) start = 0;
    if (!Number.isFinite(end) || end < 0) end = 0;
    const safeDelta = Number.isFinite(delta) ? delta : 0;

    if (hasMax) {
      start = Math.min(start, max);
      end = Math.min(end, max);
    }

    if (which === 'start') {
      start = start + safeDelta;
      if (start < 0) start = 0;
      const maxStart = end - 1;
      if (maxStart >= 0) {
        if (start > maxStart) start = maxStart;
      } else {
        start = 0;
        end = Math.min(max, 1);
      }
    } else if (which === 'end') {
      end = end + safeDelta;
      if (hasMax && end > max) end = max;
      const minEnd = start + 1;
      if (end < minEnd) {
        end = hasMax ? Math.min(max, minEnd) : minEnd;
      }
    }

    start = Math.max(0, Math.min(start, max));
    end = Math.max(0, Math.min(end, max));
    if (end < start + 1) {
      if (start + 1 <= max) {
        end = start + 1;
      } else if (end - 1 >= 0) {
        start = end - 1;
      }
    }

    return {
      startSec: roundMillis(start),
      endSec: roundMillis(end),
    };
  }

  function formatRangeSummary(startSec, endSec) {
    const startInvalidType = typeof startSec === 'boolean' || startSec === null || startSec === undefined || startSec === '';
    const endInvalidType = typeof endSec === 'boolean' || endSec === null || endSec === undefined || endSec === '';
    const start = startInvalidType ? NaN : Number(startSec);
    const end = endInvalidType ? NaN : Number(endSec);

    const startText = formatTimecode(start);
    const endText = formatTimecode(end);

    if (!Number.isFinite(start) || !Number.isFinite(end) || start < 0 || end <= start) {
      return {
        valid: false,
        durationSec: 0,
        startText,
        endText,
        label: '',
        warning: 'End harus lebih besar dari Start',
      };
    }

    const durationSec = roundMillis(end - start);
    const durText = Number.isInteger(durationSec)
      ? String(durationSec)
      : String(Math.round(durationSec * 10) / 10);
    const label = `Durasi: ${durText} dtk (${startText} → ${endText})`;

    let warning = '';
    if (durationSec < 5) {
      warning = 'Terlalu pendek (<5 dtk)';
    } else if (durationSec > 120) {
      warning = 'Cukup panjang (>2 menit)';
    }

    return {
      valid: true,
      durationSec,
      startText,
      endText,
      label,
      warning,
    };
  }

  const H1_CRITERIA = [
    { key: 'h1TawaFacecam', label: 'F1 Tawa keras di facecam' },
    { key: 'h1VisualKejadian', label: 'F2 Kejadian/fisika terlihat di frame' },
    { key: 'h1ArcUtuh', label: 'F3 Arc setup→payoff 20–55 dtk utuh' },
    { key: 'h1HookMutePass', label: 'F4 Hook 1–3 dtk lolos mute test / 1,5 dtk wajah+event' },
  ];

  function preset16x9TopCenterWM() {
    return {
      aspectRatio: '16:9',
      cropMode: 'original',
      resolution: '1080p',
      subtitlesEnabled: false,
      sceneSnap: false,
      silenceTrim: false,
      watermark: {
        enabled: true,
        position: 'custom',
        customX: 0.375,
        customY: 0.0,
        scale: 0.25,
        opacity: 0.96,
        onOutro: false,
      },
    };
  }

  function evaluateH1Checklist(flags) {
    const src = flags && typeof flags === 'object' ? flags : {};
    const missing = [];
    let passedCount = 0;
    for (let i = 0; i < H1_CRITERIA.length; i += 1) {
      const item = H1_CRITERIA[i];
      if (Boolean(src[item.key])) {
        passedCount += 1;
      } else {
        missing.push(item.label);
      }
    }
    return {
      passedCount,
      total: H1_CRITERIA.length,
      publishable: passedCount === H1_CRITERIA.length,
      missing,
    };
  }

  return {
    parseTimecode,
    formatTimecode,
    nudgeRange,
    formatRangeSummary,
    preset16x9TopCenterWM,
    evaluateH1Checklist,
  };
});
