'use strict';
/*
 * 분포 계산(lib/stats.js) — 합계가 감추는 이상치를 화면으로 끌어올리는 장치.
 *
 * 검증하는 불변식:
 *   ① 선형 보간 분위 — 표본이 적어도 p95 와 p99 가 갈린다(최근접 순위면 붙는다)
 *   ② 0 을 버리지 않는다 — 0턴·0원 세션은 실재하는 관측이고 빼면 분포가 낙관적으로 왜곡된다
 *   ③ 비유한값만 버리고 버린 개수를 돌려준다
 *   ④ 정렬되지 않은 입력도 같은 답을 낸다
 */
const { test, describe } = require('node:test');
const assert = require('node:assert/strict');

const stats = require('../lib/stats');

describe('① 선형 보간 분위', () => {
  const ten = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];

  test('1..10 의 p50/p95/p99', () => {
    const s = stats.summarize(ten);
    assert.equal(s.n, 10);
    assert.equal(s.min, 1);
    assert.equal(s.max, 10);
    assert.equal(s.avg, 5.5);
    assert.equal(s.p.p50, 5.5);                       // (10-1)*0.50 = 4.5 → 5 와 6 사이
    assert.ok(Math.abs(s.p.p95 - 9.55) < 1e-9);       // 8.55 → 9 와 10 사이
    assert.ok(Math.abs(s.p.p99 - 9.91) < 1e-9);       // 8.91 → 9 와 10 사이
  });

  test('표본이 적어도 p95 와 p99 가 붙지 않는다 — 최근접 순위였다면 같은 값이 된다', () => {
    const s = stats.summarize(ten);
    assert.notEqual(s.p.p95, s.p.p99);
  });

  test('경계 — 빈 배열·단일 값', () => {
    const empty = stats.summarize([]);
    assert.equal(empty.n, 0);
    assert.deepEqual(empty.p, {});
    assert.equal(empty.min, null);

    const one = stats.summarize([42]);
    assert.equal(one.n, 1);
    assert.equal(one.p.p50, 42);
    assert.equal(one.p.p99, 42);
  });

  test('분위 목록을 바꿀 수 있다', () => {
    const s = stats.summarize(ten, [0.25, 0.75]);
    assert.deepEqual(Object.keys(s.p).sort(), ['p25', 'p75']);
  });
});

describe('② 0 을 버리지 않는다', () => {
  test('0 이 섞이면 분포가 아래로 당겨진다', () => {
    const withZeros = stats.summarize([0, 0, 0, 0, 10]);
    assert.equal(withZeros.n, 5);
    assert.equal(withZeros.min, 0);
    assert.equal(withZeros.p.p50, 0);
    // 0 을 걸렀다면 n=1, p50=10 이 됐을 것이다.
    assert.notEqual(withZeros.p.p50, 10);
  });
});

describe('③ 비유한값만 버린다', () => {
  test('NaN·Infinity·null·문자열은 제외하고 개수를 돌려준다', () => {
    const s = stats.summarize([1, 2, NaN, Infinity, null, undefined, 'abc', 3]);
    assert.equal(s.n, 3);
    assert.equal(s.dropped, 5);
    assert.equal(s.p.p50, 2);
  });

  test('숫자 문자열은 살린다(DB 드라이버가 문자열로 주는 경우가 있다)', () => {
    const s = stats.summarize(['1', '2', '3']);
    assert.equal(s.n, 3);
    assert.equal(s.p.p50, 2);
  });

  test('배열이 아닌 입력도 죽지 않는다', () => {
    assert.equal(stats.summarize(null).n, 0);
    assert.equal(stats.summarize(undefined).n, 0);
    assert.equal(stats.summarize('nope').n, 0);
  });
});

describe('④ 입력 순서에 의존하지 않는다', () => {
  test('뒤섞인 입력이 정렬된 입력과 같은 답을 낸다', () => {
    const sorted = stats.summarize([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
    const shuffled = stats.summarize([7, 2, 10, 1, 5, 9, 3, 8, 4, 6]);
    assert.deepEqual(shuffled.p, sorted.p);
    assert.equal(shuffled.min, sorted.min);
    assert.equal(shuffled.max, sorted.max);
  });

  test('호출자의 배열을 훼손하지 않는다', () => {
    const input = [3, 1, 2];
    stats.summarize(input);
    assert.deepEqual(input, [3, 1, 2], '입력 배열을 제자리 정렬해 호출자를 망가뜨렸다');
  });
});
