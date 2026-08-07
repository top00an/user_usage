'use strict';
/*
 * keyword 축 시크릿·PII 필터(lib/intake.js 의 safeKeyword).
 *
 * 왜 이 축만인가: 7개 축 중 keyword 만 **사람이 입력한 말**에서 나온다. 나머지는 도구명·명령어·
 * 에이전트명이라 어휘가 닫혀 있다. 그래서 프롬프트에 섞인 API 키·이메일·사번이 집계로 들어갈 수
 * 있는 자리는 여기 하나뿐이고, 한 번 저장되면 토큰을 가진 전원이 상위 키워드 화면에서 본다.
 *
 * 이 파일의 규율: **버리는 쪽으로만 실패해야 한다.** 평범한 낱말이 잘못 버려지면 집계가 조금
 * 부정확해지지만, 시크릿이 통과하면 되돌릴 수 없다. 그래서 음성 대조(평범한 말은 남는가)를
 * 양성 대조만큼 촘촘히 둔다 — 필터가 과하게 잡아 축이 통째로 비는 것도 조용한 고장이라서다.
 */
const { test, describe } = require('node:test');
const assert = require('node:assert/strict');

const intake = require('../lib/intake');

const SID = 'a1b2c3d4-0000-4000-8000-000000000001';

/** counters.keyword 만 담은 최소 페이로드 → 통과한 keyword 키 목록. */
function keywords(bucket) {
  const [r] = intake.normPayload({
    machine: 'pc-1',
    user: 'user-a',
    sessions: [{
      id: SID,
      startedAt: '2026-08-03T09:00:00.000Z',
      counters: { keyword: bucket },
    }],
  });
  return (r ? r.rows : []).filter((x) => x.kind === 'keyword').map((x) => x.key);
}

describe('① 버려야 하는 것 — 시크릿 모양', () => {
  const secrets = [
    ['sk-ant-api03-abcdefghijklmnop', 'Anthropic 키 접두사'],
    ['ghp_abcdefghijklmnopqrstuvwxyz0123', 'GitHub PAT'],
    ['github_pat_11ABCDEFG0abcdefghijkl', 'GitHub 세분화 PAT'],
    ['AKIAIOSFODNN7EXAMPLE', 'AWS 액세스 키'],
    ['xoxb-123456789012-abcdefgh', 'Slack 봇 토큰'],
    ['glpat-abcdefghijklmnopqrst', 'GitLab PAT'],
    ['eyJhbGciOiJIUzI1NiIsInR5cCI6', 'JWT 헤더 조각'],
    ['npm_abcdefghijklmnopqrstuvwxyz01', 'npm 토큰'],
    ['token=abcdef123456', '값이 붙은 라벨'],
    ['user:hunter2', '콜론으로 붙은 값'],
  ];
  for (const [k, why] of secrets) {
    test(`${why}: ${k.slice(0, 16)}… 는 버린다`, () => {
      assert.deepEqual(keywords({ [k]: 5 }), [], `${k} 가 집계에 남았다`);
    });
  }

  test('32자 이상 hex(해시·랜덤 키)는 버린다', () => {
    assert.deepEqual(keywords({ d41d8cd98f00b204e9800998ecf8427e: 3 }), []);
  });

  /*
   * 벤더 접두사에 걸리지 않는 임의 키. normKey 가 소문자로 접기 **전**을 봐야 잡힌다 —
   * 이 검사가 정규화된 값만 보면 영원히 통과시킨다(회귀 시 조용히 뚫린다).
   */
  test('대소문자+숫자가 섞인 24자 이상 문자열은 버린다(키의 일반형)', () => {
    assert.deepEqual(keywords({ aB3dEfGh1jKlMn0pQrStUvWx9z: 3 }), []);
  });

  test('40자를 넘으면 무엇이든 버린다 — 사람이 쓰는 낱말이 아니다', () => {
    assert.deepEqual(keywords({ ['refactoring-plan-' + 'z'.repeat(30)]: 3 }), []);
  });
});

describe('② 버려야 하는 것 — PII 모양', () => {
  test('이메일은 버린다', () => {
    assert.deepEqual(keywords({ 'someone@example.com': 7 }), []);
  });

  test('접속 문자열 조각은 버린다', () => {
    assert.deepEqual(keywords({ 'postgres://u:p@host/db': 2 }), []);
  });

  test('10자리 이상 연속 숫자(전화·카드·사번)는 버린다', () => {
    assert.deepEqual(keywords({ '01012345678': 4 }), []);
    assert.deepEqual(keywords({ 'emp-2026001234567': 4 }), []);
  });
});

describe('③ 남아야 하는 것 — 평범한 말', () => {
  /*
   * 이쪽이 무너지면 축이 조용히 빈다. 실제로 쓰이는 어휘(한글·영문 소문자·하이픈·버전 숫자)를
   * 표본으로 둔다.
   */
  const ok = ['리팩터', '배포', '테스트코드', 'refactor', 'deploy', 'test', 'usage-dashboard',
    'postgres', 'sqlite', 'node22', 'v2', 'ci', 'rls', '마이그레이션'];
  for (const k of ok) {
    test(`'${k}' 는 남는다`, () => {
      assert.deepEqual(keywords({ [k]: 3 }), [k.toLowerCase()]);
    });
  }

  test('짧은 숫자는 남는다 — 10자리 미만은 식별자가 아니다', () => {
    assert.deepEqual(keywords({ 2026: 3 }), ['2026']);
  });

  test('40자 경계는 남는다(상한은 초과부터 버린다)', () => {
    const k = 'refactoring-plan-' + 'z'.repeat(23);   // 정확히 40자, hex 도 무작위도 아니다
    assert.equal(k.length, intake.KEYWORD_MAX);
    assert.deepEqual(keywords({ [k]: 3 }), [k]);
  });

  /*
   * 라벨 낱말은 값이 아니다 — 버려도 얻는 보안이 없고, 이 대시보드에서는 정상 어휘다.
   * (값이 붙은 `token=abc` 는 ① 에서 버려지는 것을 확인한다.)
   */
  test("'token'·'password' 같은 라벨 낱말 자체는 남는다", () => {
    assert.deepEqual(keywords({ token: 3 }), ['token']);
    assert.deepEqual(keywords({ password: 3 }), ['password']);
  });

  test('한글은 길이가 길어도 무작위 판정에 걸리지 않는다', () => {
    assert.deepEqual(keywords({ 사용량관측대시보드: 3 }), ['사용량관측대시보드']);
  });
});

describe('④ 다른 축은 이 필터가 건드리지 않는다', () => {
  /*
   * bash·tool 축은 어휘가 닫혀 있어 필터가 필요 없고, 걸면 정상 도구명을 잡는다.
   * (예: 'password' 라는 이름의 CLI 나 긴 MCP 서버명.)
   */
  test('bash 축의 password 는 그대로 센다', () => {
    const [r] = intake.normPayload({
      machine: 'pc-1',
      sessions: [{ id: SID, counters: { bash: { password: 2 }, tool: { Bash: 1 } } }],
    });
    assert.deepEqual(r.rows.find((x) => x.kind === 'bash'), { kind: 'bash', key: 'password', count: 2 });
  });
});

describe('⑤ safeKeyword 는 단독으로도 쓸 수 있다(순수)', () => {
  test('공개 함수로 노출된다 — 수집기 쪽과 규칙을 맞출 때 참조점이 된다', () => {
    assert.equal(typeof intake.safeKeyword, 'function');
    assert.equal(intake.safeKeyword('배포'), true);
    assert.equal(intake.safeKeyword('sk-ant-abcdefghijklmnop'), false);
    assert.equal(intake.safeKeyword(''), false);
  });

  test('원본 인자를 주면 대소문자 정보를 함께 본다', () => {
    const raw = 'aB3dEfGh1jKlMn0pQrStUvWx9z';
    assert.equal(intake.safeKeyword(raw.toLowerCase()), true, '정규화된 값만으로는 판별할 수 없다');
    assert.equal(intake.safeKeyword(raw.toLowerCase(), raw), false, '원본을 주면 잡아야 한다');
  });

  test('KEYWORD_MAX 가 축 상한(KEY_MAX 120)보다 낮다', () => {
    assert.ok(intake.KEYWORD_MAX < 120, '키워드 상한이 일반 키 상한과 같으면 좁힌 의미가 없다');
  });
});
