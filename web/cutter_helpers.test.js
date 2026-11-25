'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const cutter = require('./cutter_helpers.js');

let passed = 0;
let failed = 0;

function test(name, fn) {
  try {
    fn();
    passed += 1;
    console.log(`PASS: ${name}`);
  } catch (err) {
    failed += 1;
    console.error(`FAIL: ${name}`);
    console.error(err);
  }
}

test('parseTimecode parses HH:MM:SS, MM:SS, numeric, decimal, and HMS formats', () => {
  assert.equal(cutter.parseTimecode('1:28:38'), 5318);
  assert.equal(cutter.parseTimecode('01:02:03'), 3723);
  assert.equal(cutter.parseTimecode('0:00:00'), 0);
  assert.equal(cutter.parseTimecode('1:28:38.5'), 5318.5);
  assert.equal(cutter.parseTimecode('28:38'), 1718);
  assert.equal(cutter.parseTimecode('00:45'), 45);
  assert.equal(cutter.parseTimecode('90:30'), 5430);
  assert.equal(cutter.parseTimecode('01:02.5'), 62.5);
  assert.equal(cutter.parseTimecode('45'), 45);
  assert.equal(cutter.parseTimecode(45), 45);
  assert.equal(cutter.parseTimecode(0), 0);
  assert.equal(cutter.parseTimecode('0'), 0);
  assert.equal(cutter.parseTimecode('45.5'), 45.5);
  assert.equal(cutter.parseTimecode(45.5), 45.5);
  assert.equal(cutter.parseTimecode('  1:28:38  '), 5318);
  assert.equal(cutter.parseTimecode('1h28m38s'), 5318);
  assert.equal(cutter.parseTimecode('2m15s'), 135);
  assert.equal(cutter.parseTimecode('1H 28M 38S'), 5318);
  assert.equal(cutter.parseTimecode('45s'), 45);
  assert.equal(cutter.parseTimecode('1h'), 3600);
  assert.equal(cutter.parseTimecode('2m'), 120);
  assert.equal(cutter.parseTimecode('2m15.5s'), 135.5);
});

test('parseTimecode rejects negative, empty, non-numeric, and out-of-range MM/SS typos', () => {
  const invalidInputs = [
    -1,
    -45.5,
    NaN,
    Infinity,
    -Infinity,
    null,
    undefined,
    true,
    false,
    {},
    [],
    '',
    '   ',
    '-10',
    '-0:30',
    '1:-05',
    'abc',
    '45px',
    '1:75',
    '00:60',
    '1:60:00',
    '1:28:60',
    '1:75:00',
    '1:2:3:4',
    ':30',
    '1:',
    '1: 28',
    '1h28m38',
    '1m2h',
    'h',
  ];

  for (const input of invalidInputs) {
    assert.equal(
      cutter.parseTimecode(input),
      null,
      `Expected null for invalid input: ${JSON.stringify(input)} (${typeof input})`
    );
  }
});

test('formatTimecode formats seconds into MM:SS or H:MM:SS and clamps invalid/negative values', () => {
  assert.equal(cutter.formatTimecode(0), '00:00');
  assert.equal(cutter.formatTimecode(45), '00:45');
  assert.equal(cutter.formatTimecode(60), '01:00');
  assert.equal(cutter.formatTimecode(1718), '28:38');
  assert.equal(cutter.formatTimecode(3600), '1:00:00');
  assert.equal(cutter.formatTimecode(3723), '1:02:03');
  assert.equal(cutter.formatTimecode(5318), '1:28:38');

  assert.equal(cutter.formatTimecode(-1), '00:00');
  assert.equal(cutter.formatTimecode(-999), '00:00');
  assert.equal(cutter.formatTimecode(NaN), '00:00');
  assert.equal(cutter.formatTimecode(Infinity), '00:00');
  assert.equal(cutter.formatTimecode(-Infinity), '00:00');
  assert.equal(cutter.formatTimecode(null), '00:00');
  assert.equal(cutter.formatTimecode(undefined), '00:00');
  assert.equal(cutter.formatTimecode('abc'), '00:00');
});

test('nudgeRange shifts start or end within [0, maxDurSec] and preserves >= 1s gap', () => {
  assert.deepEqual(cutter.nudgeRange(100, 137, 'start', -5, 500), { startSec: 95, endSec: 137 });
  assert.deepEqual(cutter.nudgeRange(100, 137, 'start', +1, 500), { startSec: 101, endSec: 137 });
  assert.deepEqual(cutter.nudgeRange(100, 137, 'end', -1, 500), { startSec: 100, endSec: 136 });
  assert.deepEqual(cutter.nudgeRange(100, 137, 'end', +5, 500), { startSec: 100, endSec: 142 });

  // Clamp at lower bound 0
  assert.deepEqual(cutter.nudgeRange(3, 30, 'start', -5, 500), { startSec: 0, endSec: 30 });

  // Clamp at upper bound maxDurSec
  assert.deepEqual(cutter.nudgeRange(10, 498, 'end', +5, 500), { startSec: 10, endSec: 500 });

  // Preserve minimum 1-second gap when nudging start past end - 1
  assert.deepEqual(cutter.nudgeRange(28, 30, 'start', +5, 500), { startSec: 29, endSec: 30 });

  // Preserve minimum 1-second gap when nudging end below start + 1
  assert.deepEqual(cutter.nudgeRange(10, 12, 'end', -5, 500), { startSec: 10, endSec: 11 });

  // No upper clamp when maxDurSec is 0 or omitted
  assert.deepEqual(cutter.nudgeRange(10, 30, 'end', +50, 0), { startSec: 10, endSec: 80 });
  assert.deepEqual(cutter.nudgeRange(10, 30, 'end', +50), { startSec: 10, endSec: 80 });
});

test('formatRangeSummary returns duration label, validation error, and soft duration warnings', () => {
  assert.deepEqual(cutter.formatRangeSummary(5318, 5355), {
    valid: true,
    durationSec: 37,
    startText: '1:28:38',
    endText: '1:29:15',
    label: 'Durasi: 37 dtk (1:28:38 → 1:29:15)',
    warning: '',
  });

  // Invalid when end <= start
  const equalRange = cutter.formatRangeSummary(100, 100);
  assert.equal(equalRange.valid, false);
  assert.equal(equalRange.durationSec, 0);
  assert.equal(equalRange.startText, '01:40');
  assert.equal(equalRange.endText, '01:40');
  assert.equal(equalRange.warning, 'End harus lebih besar dari Start');

  const reversedRange = cutter.formatRangeSummary(120, 90);
  assert.equal(reversedRange.valid, false);
  assert.equal(reversedRange.durationSec, 0);
  assert.equal(reversedRange.warning, 'End harus lebih besar dari Start');

  const invalidNumbers = cutter.formatRangeSummary(-5, 10);
  assert.equal(invalidNumbers.valid, false);
  assert.equal(invalidNumbers.warning, 'End harus lebih besar dari Start');

  // Soft warning: < 5 sec
  const shortRange = cutter.formatRangeSummary(10, 14);
  assert.equal(shortRange.valid, true);
  assert.equal(shortRange.durationSec, 4);
  assert.equal(shortRange.label, 'Durasi: 4 dtk (00:10 → 00:14)');
  assert.equal(shortRange.warning, 'Terlalu pendek (<5 dtk)');

  // Exact boundary 5 sec -> no warning
  const minOkRange = cutter.formatRangeSummary(10, 15);
  assert.equal(minOkRange.valid, true);
  assert.equal(minOkRange.durationSec, 5);
  assert.equal(minOkRange.warning, '');

  // Exact boundary 120 sec -> no warning
  const maxOkRange = cutter.formatRangeSummary(10, 130);
  assert.equal(maxOkRange.valid, true);
  assert.equal(maxOkRange.durationSec, 120);
  assert.equal(maxOkRange.warning, '');

  // Soft warning: > 120 sec
  const longRange = cutter.formatRangeSummary(10, 131);
  assert.equal(longRange.valid, true);
  assert.equal(longRange.durationSec, 121);
  assert.equal(longRange.label, 'Durasi: 121 dtk (00:10 → 02:11)');
  assert.equal(longRange.warning, 'Cukup panjang (>2 menit)');
});

test('preset16x9TopCenterWM returns exact verified preset as a fresh object', () => {
  const p1 = cutter.preset16x9TopCenterWM();
  assert.deepEqual(p1, {
    aspectRatio: '16:9',
    cropMode: 'original',
    resolution: '1080p',
    watermark: {
      position: 'custom',
      customX: 0.40,
      customY: 0.36,
    },
  });

  // Ensure defensive fresh copy
  p1.aspectRatio = '9:16';
  p1.watermark.customX = 0.99;
  const p2 = cutter.preset16x9TopCenterWM();
  assert.equal(p2.aspectRatio, '16:9');
  assert.equal(p2.watermark.customX, 0.40);
  assert.equal(p2.watermark.customY, 0.36);
});

test('UMD module attaches window.KFCutter in a browser environment', () => {
  const src = fs.readFileSync(path.join(__dirname, 'cutter_helpers.js'), 'utf8');
  const sandbox = {};
  sandbox.window = sandbox;
  sandbox.globalThis = sandbox;
  vm.runInNewContext(src, sandbox, { filename: 'cutter_helpers.js' });

  assert.equal(typeof sandbox.window.KFCutter, 'object');
  assert.equal(sandbox.window.KFCutter.parseTimecode('1:28:38'), 5318);
  assert.equal(sandbox.window.KFCutter.formatTimecode(5318), '1:28:38');
  assert.deepEqual({ ...sandbox.window.KFCutter.nudgeRange(10, 20, 'end', 5, 100) }, {
    startSec: 10,
    endSec: 25,
  });
  assert.equal(
    sandbox.window.KFCutter.formatRangeSummary(5318, 5355).label,
    'Durasi: 37 dtk (1:28:38 → 1:29:15)'
  );
  assert.deepEqual({ ...sandbox.window.KFCutter.preset16x9TopCenterWM().watermark }, {
    position: 'custom',
    customX: 0.40,
    customY: 0.36,
  });
});

console.log(`\nSummary: ${passed} passed, ${failed} failed`);
if (failed > 0) {
  process.exit(1);
}
