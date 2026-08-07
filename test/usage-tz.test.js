'use strict';
/*
 * 집계 시간대(lib/tz.js) — 저장은 UTC, 집계·표시는 로컬(KST).
 *
 * 이 테스트가 지키는 것은 숫자가 아니라 **날짜 경계**다. 경계가 틀리면 값 자체는 그럴듯해서
 * 눈으로는 안 보인다. 실제로 그렇게 틀려 있었다(2026-08-04): 00:00~09:00 KST 에 한 일이
 * 전날로 집계되고, 21시 스파이크가 12시 칸에 그려졌다.
 *
 * 검증하는 불변식:
 *   ① KST 하루는 UTC [D-1 15:00, D 15:00) — 그 경계가 정확한가
 *   ② 시간 라벨이 +9 로 옮겨지는가(자정 넘어가는 것 포함)
 *   ③ 주 경계는 **옮긴 뒤** 접히는가(월요일)
 *   ④ 조회 범위 넓히기 + 정확 필터가 짝으로 동작하는가(경계 행을 잃지도 더하지도 않는다)
 *   ⑤ 오프셋은 env 로 바뀌고, 이상값은 기본값으로 수렴한다
 */
const { test, describe } = require('node:test');
const assert = require('node:assert/strict');

const tz = require('../lib/tz');

describe('① 로컬 날짜 경계 — KST 하루는 UTC 15:00 에 시작한다', () => {
  test('심야 KST 가 전날로 밀리지 않는다', () => {
    // 2026-08-03T16:00Z = 2026-08-04 01:00 KST → **08-04** 여야 한다(종전엔 08-03 이었다).
    assert.equal(tz.localDay('2026-08-03T16:00:00.000Z'), '2026-08-04');
    assert.equal(tz.localDay('2026-08-03T23:59:59.000Z'), '2026-08-04');
  });

  test('경계 직전/직후가 정확히 갈린다', () => {
    assert.equal(tz.localDay('2026-08-03T14:59:59.999Z'), '2026-08-03', '15:00Z 직전은 아직 전날');
    assert.equal(tz.localDay('2026-08-03T15:00:00.000Z'), '2026-08-04', '15:00Z 부터 다음날');
  });

  test('낮 시간대는 UTC 날짜와 같다 — 바뀌지 않아야 하는 자리', () => {
    assert.equal(tz.localDay('2026-08-04T00:00:00.000Z'), '2026-08-04');
    assert.equal(tz.localDay('2026-08-04T05:30:00.000Z'), '2026-08-04');
  });

  test('월·연 경계도 넘어간다', () => {
    assert.equal(tz.localDay('2026-07-31T15:00:00.000Z'), '2026-08-01');
    assert.equal(tz.localDay('2026-12-31T15:00:00.000Z'), '2027-01-01');
  });

  test('형식이 아니면 지어내지 않는다', () => {
    assert.equal(tz.localDay(''), '');
    assert.equal(tz.localDay(null), '');
    assert.equal(tz.localDay('nope'), 'nope'.slice(0, 10));
  });
});

describe('② 시간 라벨 이동', () => {
  test('+9 로 옮긴다', () => {
    assert.equal(tz.localHour('2026-08-03T12'), '2026-08-03T21');
    assert.equal(tz.localHour('2026-08-03T00'), '2026-08-03T09');
  });

  test('자정을 넘으면 날짜도 함께 넘어간다 — 여기가 9시간 밀리던 자리', () => {
    assert.equal(tz.localHour('2026-08-03T15'), '2026-08-04T00');
    assert.equal(tz.localHour('2026-08-03T23'), '2026-08-04T08');
  });

  test('형식이 아니면 그대로 돌려준다', () => {
    assert.equal(tz.localHour('2026-08-03'), '2026-08-03');
    assert.equal(tz.localHour(''), '');
    assert.equal(tz.localHour(null), '');
  });
});

describe('③ 주 경계 — 옮긴 뒤에 접는다', () => {
  test('월요일로 접힌다', () => {
    for (const [day, want] of [
      ['2026-08-03', '2026-08-03'],   // 월
      ['2026-08-04', '2026-08-03'],   // 화
      ['2026-08-09', '2026-08-03'],   // 일 — 같은 주의 끝
      ['2026-08-10', '2026-08-10'],   // 다음 월
    ]) assert.equal(tz.weekStart(day), want, `${day} 의 주 시작이 틀렸다`);
  });

  test('연 경계를 넘어도 월요일이다', () => {
    const w = tz.weekStart('2026-01-01');
    assert.equal(new Date(`${w}T00:00:00Z`).getUTCDay(), 1);
  });

  test('형식이 아니면 그대로', () => {
    assert.equal(tz.weekStart('nope'), 'nope');
    assert.equal(tz.weekStart(''), '');
  });
});

describe('④ 조회 범위 넓히기 + 정확 필터', () => {
  test('양쪽으로 하루씩 넓힌다 — 경계 행을 잃지 않게', () => {
    assert.deepEqual(tz.widenUtcRange({ from: '2026-08-04', to: '2026-08-04' }),
      { from: '2026-08-03', to: '2026-08-05' });
  });

  test('범위가 없으면 넓히지 않는다', () => {
    assert.deepEqual(tz.widenUtcRange({}), { from: undefined, to: undefined });
    assert.deepEqual(tz.widenUtcRange({ from: '', to: '' }), { from: '', to: '' });
  });

  test('넓힌 만큼 정확히 되걸러낸다', () => {
    const r = { from: '2026-08-04', to: '2026-08-04' };
    assert.equal(tz.inRange('2026-08-04', r), true);
    assert.equal(tz.inRange('2026-08-03', r), false, '넓혀 뜬 앞날이 남으면 안 된다');
    assert.equal(tz.inRange('2026-08-05', r), false, '넓혀 뜬 뒷날이 남으면 안 된다');
  });

  test('시간 라벨도 앞 10자로 판정한다', () => {
    assert.equal(tz.inRange('2026-08-04T21', { from: '2026-08-04', to: '2026-08-04' }), true);
  });

  test('범위가 비면 전부 통과 — 필터가 없다는 뜻이지 전부 버린다는 뜻이 아니다', () => {
    assert.equal(tz.inRange('2026-08-04', {}), true);
    assert.equal(tz.inRange('2026-08-04', { from: '', to: '' }), true);
  });

  test('라벨이 형식이 아니면 버린다', () => {
    assert.equal(tz.inRange('nope', {}), false);
    assert.equal(tz.inRange('', {}), false);
  });
});

describe('⑤ 오프셋 설정', () => {
  test('기본은 KST(+540)', () => {
    assert.equal(tz.offsetMin({}), 540);
    assert.equal(tz.DEFAULT_OFFSET_MIN, 540);
  });

  test('env 로 바꾼다 — 팀이 다른 시간대로 옮길 때 이 값만 바꾸면 된다', () => {
    assert.equal(tz.offsetMin({ USAGE_TZ_OFFSET_MIN: '0' }), 0);
    assert.equal(tz.offsetMin({ USAGE_TZ_OFFSET_MIN: '-480' }), -480);
    assert.equal(tz.offsetMin({ USAGE_TZ_OFFSET_MIN: '330' }), 330, '30분 오프셋(인도)도 표현된다');
  });

  test('이상값은 기본값으로 수렴한다 — 설정 오류가 데이터를 망가뜨리지 않게', () => {
    for (const bad of ['abc', '', '9999', '-9999', undefined]) {
      assert.equal(tz.offsetMin({ USAGE_TZ_OFFSET_MIN: bad }), 540, `${String(bad)} 를 받아들였다`);
    }
  });

  test('오프셋 0 이면 UTC 와 같아진다 — 되돌릴 수 있다는 증거', () => {
    assert.equal(tz.localDay('2026-08-03T16:00:00.000Z', 0), '2026-08-03');
    assert.equal(tz.localHour('2026-08-03T15', 0), '2026-08-03T15');
  });

  test('라벨이 시간대를 말한다', () => {
    assert.equal(tz.label(540), 'KST', '아는 시간대는 이름으로 — UTC 라는 낱말이 오해를 만든다');
    assert.equal(tz.label(0), 'UTC');
    assert.equal(tz.label(-480), 'PST');
    assert.equal(tz.label(330), 'IST');
    assert.equal(tz.label(45), 'UTC+00:45', '모르는 오프셋은 정확한 표기로 떨어진다');
  });
});
