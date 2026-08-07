'use strict';
/*
 * core·router 는 **절대경로**로 부른다. 정적 루트가 public/ 이라 FS 경로(public/js/core.js)와
 * URL(/js/core.js)의 깊이가 다르고, 상대경로로 쓰면 브라우저와 노드 테스트 중 한쪽이 반드시 깨진다.
 * 별칭(/public/js/core.js)을 하나 더 여는 안은 더 나쁘다 — URL 이 곧 모듈 식별자라 core·router 가
 * 두 인스턴스로 뜨고, router 의 NAV_SEQ 가 두 벌이 되어 isStale 이 남의 카운터를 본다(조용한 레이스).
 */
import { api, esc } from '/js/core.js';
import { isStale } from '/js/router.js';
/* ================================================================
   대시보드 · 사용 추적 탭 — 동기화된 PC 들이 무엇을 얼마나 썼는가.

   세 가지를 한 화면에서 본다:
     ① 규모   토큰 사용량(입력·출력·캐시읽기·캐시생성)을 사람·모델·날짜로
     ② 행동   실제로 부른 도구·명령·스킬·에이전트·MCP
     ③ 공백   추천이 매칭에 실패한 목표의 토큰 — **새 에이전트를 만들 자리**

   ③ 이 이 화면의 존재 이유다. ①②만 보면 비용 대시보드에 그치는데, 학습 플랫폼이 스스로
   카탈로그를 넓히려면 "무엇을 찾다가 못 찾았나"가 보여야 한다.

   캐시 읽기를 항상 따로 세운다 — 입력에 합치면 비용이 수십 배로 과대 표시된다(실측 71887 vs 2).
   ================================================================ */

// 축 정의. label 은 표 제목, hint 는 그 축이 무엇을 말하는지(오해를 미리 막는 자리).
const AXES = [
  { id: 'bash', label: '개발 명령', hint: '선두 실행파일만 집계합니다. 인자와 경로는 수집하지 않습니다' },
  { id: 'tool', label: '내장 도구', hint: 'Claude Code 도구 호출 분포' },
  { id: 'mcp', label: 'MCP 도구', hint: '서버가 직접 계측한 값과 교차 검증됩니다' },
  { id: 'skill', label: '스킬', hint: '호출 0 인 표준 스킬은 은퇴 또는 발견 실패 후보입니다' },
  { id: 'agent', label: '서브에이전트', hint: '실제로 위임된 에이전트 타입' },
  { id: 'slash', label: '슬래시 명령', hint: '슬래시 명령 사용 분포' },
  { id: 'keyword', label: '키워드', hint: '프롬프트의 정규화 토큰(2회 이상 반복분). 원문은 저장하지 않습니다' },
];

/*
 * 서브에이전트·스킬 활용 — **사람별로** 나눠 본다.
 *
 * 왜(실측): 같은 지침이 모든 세션에 실리는데 실제 사용은 사람마다 갈렸다 —
 * 한 사람은 역할 에이전트를 65회 썼고 다른 사람은 0회에 general-purpose 만 25회였다.
 * 전체 합(위 막대)으로는 이 차이가 보이지 않아 아무도 몰랐다.
 *
 * 강제하지 않는다. 보이면 사람이 판단한다 — 훅 넛지는 "실질 작업"을 정확히 가릴 수 없어
 * 오판정 잔소리가 되고, 그건 무시당한다.
 */
let dispatchData = null;
function dispatchHtml(axis) {
  if (axis !== 'agent' && axis !== 'skill') return '';
  if (!dispatchData) return '<div class="help mt">사람별 활용을 불러오는 중…</div>';
  const rows = (axis === 'agent' ? dispatchData.agents : dispatchData.skills) || [];
  if (!rows.length) {
    return `<div class="help mt">사람별 데이터가 아직 없습니다 — 세션 보고가 도착하면 채워집니다.</div>`;
  }
  const head = axis === 'agent'
    ? '<tr><th>사용자</th><th class="num">역할</th><th class="num">범용</th><th class="num">합</th><th>상위</th></tr>'
    : '<tr><th>사용자</th><th class="num">합</th><th>상위</th></tr>';
  const body = rows.map((u) => {
    const top = u.items.slice(0, 4).map((i) => `${esc(i.key)} ${i.count}`).join(' · ');
    if (axis !== 'agent') {
      return `<tr><td>${esc(u.username)}</td><td class="num">${u.total}</td><td class="help">${top}</td></tr>`;
    }
    // 역할 0 은 지침이 닿지 않은 자리다 — 눈에 띄게 표시한다.
    const warn = u.role === 0 ? ' class="txt-err"' : '';
    return `<tr><td>${esc(u.username)}</td><td class="num"${warn}>${u.role}</td>`
      + `<td class="num">${u.generic}</td><td class="num">${u.total}</td>`
      + `<td class="help" style="min-width:0;overflow-wrap:anywhere">${top}</td></tr>`;
  }).join('');
  return `<div class="mt" style="border-top:1px solid var(--border-soft);padding-top:8px">
    <div class="row"><b style="font-size:13px">사람별 활용</b>
      <span class="help">${axis === 'agent'
    ? `역할 에이전트 vs 범용(${(dispatchData.generic || []).join(', ')}) — 역할 0 은 팬아웃이 일어나지 않은 것입니다`
    : '스킬을 명시적으로 지시했는가'}</span></div>
    <div class="table-wrap mt"><table class="tbl"><thead>${head}</thead><tbody>${body}</tbody></table></div>
  </div>`;
}


let axis = 'bash';

function n(v) { return (Number(v) || 0).toLocaleString('ko-KR'); }

// 토큰 수는 자릿수가 커서 원본만 보면 비교가 안 된다. 천/백만 단위로 접어 보여주되
// title 에 원본을 남긴다(정확한 값이 필요한 사람은 마우스를 올린다).
function short(v) {
  const x = Number(v) || 0;
  if (x >= 1e8) return `${(x / 1e8).toFixed(1)}억`;
  if (x >= 1e4) return `${(x / 1e4).toFixed(1)}만`;
  return String(x);
}
function tok(v) { return `<span title="${esc(n(v))}">${esc(short(v))}</span>`; }

// 가로 막대 — 축별 상위 키를 한눈에. 외부 차트 라이브러리 없이 div 폭으로 그린다(무의존성).
function bars(rows) {
  if (!rows || !rows.length) return '<div class="muted">아직 데이터가 없습니다.</div>';
  const max = Math.max(...rows.map((r) => Number(r.count) || 0), 1);
  return `<div class="ubars">${rows.map((r) => {
    const w = Math.max(2, Math.round(((Number(r.count) || 0) / max) * 100));
    return `<div class="ubar-row">
      <div class="ubar-k mono" title="${esc(r.key)}">${esc(r.key)}</div>
      <div class="ubar-track"><div class="ubar-fill" style="width:${w}%"></div></div>
      <div class="ubar-v">${esc(n(r.count))}</div>
      <div class="ubar-s help">${esc(String(r.users || 0))}명 · ${esc(String(r.sessions || 0))}세션</div>
    </div>`;
  }).join('')}</div>`;
}

/*
 * 보존 정책 문구 — 키워드 축만 기한이 있다.
 * 화면이 이 사실을 말하지 않으면 두 가지가 생긴다: 추세가 끊긴 이유를 아무도 모르고,
 * 팀은 자기 발화 데이터가 얼마나 남는지 모른다. 둘 다 화면이 답할 일이다.
 */
function retentionNote(r) {
  const days = r && r.keywordDays;
  return days
    ? `<span class="help">키워드는 <b>${esc(String(days))}일</b> 보관 후 자동 삭제됩니다(다른 축과 사용량은 계속 보관).</span>`
    : '<span class="help">키워드 보존 기한이 설정돼 있지 않습니다 — 무기한 보관됩니다.</span>';
}

/*
 * `입력` 이라는 이름이 "받은 프롬프트 전체" 로 읽힌다. 실제 `input_tokens` 는 **캐시에 걸리지
 * 않은 새 입력만** 센다 — 캐싱이 잘 도는 사람일수록 이 값이 작아져서, 열심히 안 쓴 것처럼
 * 보인다(사용자가 실제로 그렇게 물었다). 그래서 열 이름에 조건을 박고 뜻을 title 에 적는다.
 */
const IN_LABEL = '입력(비캐시)';
const IN_HINT = '캐시에 걸리지 않은 새 입력 토큰만 셉니다. 캐시로 처리된 입력은 캐시읽기·캐시생성에 있습니다'
  + ' — 캐싱이 잘 돌수록 이 값은 작아집니다.';
/* IN_TH(조각용 열 머리)는 지웠다 — 두 표 모두 입력을 **합계**로 내므로 쓸 곳이 없다.
   IN_LABEL·IN_HINT 는 남긴다: 총계 타일이 세 축을 분해해 보여주는 자리에서 조각의 이름으로 쓴다. */

function totalsCard(t) {
  return `<div class="grid cols-2">
    <div class="tile glass"><div class="k">보고된 세션</div><div class="v">${esc(n(t.sessions))}</div>
      <div class="s">${esc(n(t.users))}명 · ${esc(n(t.machines))}대</div></div>
    <div class="tile glass"><div class="k">출력 토큰</div><div class="v">${short(t.output)}</div>
      <div class="s" title="${IN_HINT}">${IN_LABEL} ${short(t.input)} · 캐시읽기 ${short(t.cacheRead)} · 캐시생성 ${short(t.cacheCreate)}</div></div>
  </div>`;
}

// 세 입력 축의 관계를 한 줄로. 이걸 안 쓰면 '입력(비캐시)' 라는 이름만 남고 왜 작은지는 여전히 모른다.
function inputNote() {
  return '<div class="help mt-sm">입력(비캐시) + 캐시읽기 + 캐시생성 = 그 세션이 실제로 넣은 입력 전부입니다.'
    + ' 같은 맥락을 다시 보낼 때는 캐시읽기로 잡히므로, 캐싱이 잘 도는 사람일수록 입력(비캐시)만 작아집니다.</div>';
}

/*
 * ── 입력 축은 **합치지 않는다**. 세 축을 각자의 열로 낸다 ────────────────────
 *
 * 이 자리는 하루에 세 번 오독됐다(2026-08-05). 세 번 모두 같은 뿌리다 — 이름 하나에 성질이
 * 다른 숫자를 뭉쳤다.
 *
 *   ① 조각을 `입력` 이라 불렀을 때:  "이 계정의 입력이 이상한 거 같은데?"
 *   ② 그대로 나눠 봤을 때:          "실제값이 진짜 6.9만이야? 입력대비 출력이 엄청난데"
 *      → 그 표대로면 출력이 입력의 328배. 분모가 입력측의 0.001% 짜리 조각이었다.
 *   ③ 그래서 세 축 합을 `입력` 으로 냈을 때:  "입력 161억 출력 7683만인데 이게 말이되냐
 *      내가 어떻게 입력을 161억을 해"
 *
 * ③ 이 결정적이었다. 합계는 **산술적으로 맞지만** `입력` 이라는 이름 아래 있으면 거짓말이다 —
 * 그 161억의 98% 는 사람이 타이핑한 것이 아니라 매 턴 다시 보낸 맥락(캐시읽기)이고, 요금도
 * 비캐시의 0.1 배다. 성질이 다른 셋을 한 이름으로 부르는 동안, 어떤 라벨을 붙여도 사람은
 * 그 열을 "내가 넣은 입력"으로 읽는다. 그래서 합계 열을 **없앴다**.
 *
 * 덤으로 ③ 을 파다가 수집기 결함이 잡혔다: 한 응답이
 * content 블록마다 여러 줄로 기록되고 그 줄들이 같은 usage 를 들고 있어 토큰·턴이 ~2.1배로
 * 보고되고 있었다. 화면의 161억은 실제 79.8억이었다. 표기와 집계가 **둘 다** 틀렸던 것이다.
 *
 * 세 축의 관계는 inputNote() 가 표 아래에 한 줄로 말한다. 합계가 필요한 곳(비용)은 서버가
 * 축별 단가로 계산한다(lib/usage/cost.js) — 화면이 합계를 만들 이유가 없다.
 */
const AX_CACHE_READ = '같은 맥락을 다시 보낼 때 캐시에서 읽힌 입력입니다. 새로 타이핑한 양이 아니라'
  + ' 매 턴 다시 보낸 맥락이고, 요금은 비캐시의 0.1배입니다. 에이전트 코딩에서는 보통 이 축이 가장 큽니다.';
const AX_CACHE_CREATE = '맥락을 캐시에 올릴 때 든 입력입니다(TTL 5분·1시간). 한 번 올리면 그 뒤로는 캐시읽기로 잡힙니다.';

function userTable(rows) {
  if (!rows.length) return '<div class="muted">아직 사용량 보고가 없습니다.</div>';
  return `<div class="table-wrap"><table><thead><tr>
      <th>사용자</th>
      <th style="text-align:right" title="${IN_HINT}">${IN_LABEL}</th>
      <th style="text-align:right" title="${AX_CACHE_READ}">캐시읽기</th>
      <th style="text-align:right" title="${AX_CACHE_CREATE}">캐시생성</th>
      <th style="text-align:right">출력</th>
      <th style="text-align:right" title="응답 수입니다(트랜스크립트 줄 수가 아닙니다)">턴</th>
      <th style="text-align:right">세션</th>
    </tr></thead><tbody>${rows.map((r) => `<tr>
      <td class="mono">${esc(r.username)}</td>
      <td style="text-align:right">${tok(r.input)}</td>
      <td style="text-align:right">${tok(r.cacheRead)}</td>
      <td style="text-align:right">${tok(r.cacheCreate)}</td>
      <td style="text-align:right">${tok(r.output)}</td>
      <td style="text-align:right">${esc(n(r.turns))}</td>
      <td style="text-align:right">${esc(n(r.sessions))}</td>
    </tr>`).join('')}</tbody></table></div>`;
}

/*
 * ── 모델별 표: 값의 **근거**를 같이 낸다 ──────────────────────────────
 *
 * 서버는 두 축을 더해 이 표를 만든다. series(시간×모델 버킷)가 있는 세션은 모델별 정확값,
 * 없는 세션은 **세션 최빈 모델 1개**에 통째로 귀속된 근사값이다. 둘을 구분 없이 보여주면
 * 근사를 정확한 값으로 위장하게 된다 — 이번 결함(모델 간 상호 오귀속)의 본질이 그것이었다.
 *
 * 새 서버는 행마다 fromSeries/fromSession 을 싣는다. 없으면(구 서버·구 스텁) 그 열을 아예
 * 그리지 않고 종전 표 그대로 나간다 — 화면이 새 필드를 요구하지 않는다.
 */
function fallbackShare(r) {
  const fs = r && r.fromSession;
  if (!fs) return null;
  // 출력 기준. 출력이 0 인 행(토큰 0 세션 등)은 네 축 합으로 대신한다 — NaN 을 만들지 않는다.
  const num = r.output ? fs.output : (fs.input + fs.output + fs.cacheRead + fs.cacheCreate);
  const den = r.output ? r.output : (r.input + r.output + r.cacheRead + r.cacheCreate);
  if (!den) return { pct: 0, num: 0, den: 0 };
  return { pct: Math.round((num / den) * 1000) / 10, num, den };
}

function modelTable(rows) {
  if (!rows.length) return '';
  const hasBasis = rows.some((r) => r && r.fromSession);
  const basisTh = hasBasis
    ? '<th title="이 행의 값 중 세션 최빈 모델에 귀속된 몫(출력 기준). series 가 없는 세션과,'
      + ' series 가 덮지 못한 잔여입니다">근거</th>'
    : '';
  return `<div class="table-wrap"><table><thead><tr>
      <th>모델</th><th style="text-align:right">출력</th>
      ${/* 여기는 `입력`(세 축 합)과 `캐시읽기` 를 나란히 두고 있었다 — 같은 토큰이 두 열에
           걸쳐 보여 "어느 쪽이 진짜냐"를 만들던 자리다. 사용자별 표와 같은 축으로 맞춘다. */''}
      <th style="text-align:right" title="${IN_HINT}">${IN_LABEL}</th>
      <th style="text-align:right" title="${AX_CACHE_READ}">캐시읽기</th>
      <th style="text-align:right" title="${AX_CACHE_CREATE}">캐시생성</th>
      <th style="text-align:right" title="그 모델이 등장한 세션 수입니다. 모델이 섞인 세션은 모델마다 세어지므로 이 열의 합은 총 세션 수보다 클 수 있습니다">세션</th>
      ${basisTh}
    </tr></thead><tbody>${rows.map((r) => {
    const s = hasBasis ? fallbackShare(r) : null;
    const basisTd = !hasBasis ? '' : `<td class="help">${!s || !s.den
      ? '—'
      : s.pct <= 0.05
        ? 'series — 모델별 정확'
        : `<span class="${s.pct >= 50 ? 'txt-warn' : ''}" title="세션 최빈 모델 기준 ${esc(n(s.num))} / ${esc(n(s.den))}">세션 최빈 기준 ${s.pct}%</span>`}</td>`;
    return `<tr>
      <td class="mono">${esc(r.model)}</td>
      <td style="text-align:right">${tok(r.output)}</td>
      <td style="text-align:right">${tok(r.input)}</td>
      <td style="text-align:right">${tok(r.cacheRead)}</td>
      <td style="text-align:right">${tok(r.cacheCreate)}</td>
      <td style="text-align:right">${esc(n(r.sessions))}</td>
      ${basisTd}
    </tr>`;
  }).join('')}</tbody></table></div>`;
}

/*
 * 근거 커버리지 — 사람별로 낸다.
 *
 * 왜 사람별인가(실측 2026-08-05): 커버리지가 사람마다 갈린다. 어떤 사람은 91%, 어떤 사람은
 * 2.2% 다. 2.2% 인 사람의 모델별 값은 사실상 전부 세션 최빈 모델 기준인데, 지금은 그 사실이
 * DB 를 열어야만 보인다. 전체 비율 하나로는 그 사람 행을 볼 때 알 수가 없다.
 *
 * **원인은 말하지 않는다.** 왜 낮은지는 그 PC 쪽 사실이고 서버는 모른다 —
 * 화면이 추측하면 사람이 엉뚱한 데를 판다(이 레포가 반복한 실수다).
 */
function coverageCard(ax, rows) {
  if (!ax || !Array.isArray(ax.users) || !ax.users.length) return '';
  const tot = (rows || []).reduce((a, r) => {
    const s = r && r.fromSession;
    return s ? { num: a.num + s.output, den: a.den + (Number(r.output) || 0) } : a;
  }, { num: 0, den: 0 });
  const totPct = tot.den ? Math.round((tot.num / tot.den) * 1000) / 10 : null;

  const body = ax.users.map((u) => {
    const pct = u.sessions ? Math.round((u.withSeries / u.sessions) * 1000) / 10 : 0;
    const basis = pct >= 99.95 ? 'series — 모델별 정확'
      : (pct <= 0.05 ? '전부 세션 최빈 모델 기준' : '섞여 있음');
    /*
     * 커버리지가 낮은 **이유**를 한 칸으로 말한다(D3).
     *
     * 커버리지만 보면 사람은 "보고가 안 온다" 로 읽는다 — 실제로는 보고가 오고 있고 턴에 **시각이
     * 없어** series 버킷을 만들지 못한 것이다(수집기: `hour ? bucketOf(...) : null`).
     * 그 구분이 없으면 엉뚱한 데(토큰·서버 주소·수집기 설치)를 판다.
     *
     * ⚠ 원인을 여기서 단정하지 않는다. 서버가 아는 것은 "시각 없는 턴이 N개였다" 까지다.
     *   왜 시각이 없는지는 그 PC 의 트랜스크립트를 봐야 알고, 그건 이 화면 밖의 사실이다.
     * ⚠ noTsUnknown(구버전 수집기라 이 값을 안 보낸 세션)은 0 과 갈라 말한다 — 뭉치면
     *   구버전 PC 가 "시각 문제 없음" 으로 단정된다.
     */
    const nts = Number(u.noTsTurns) || 0;
    const unknown = Number(u.noTsUnknown) || 0;
    const why = pct >= 99.95 ? ''
      : (nts > 0
        ? `<span class="help" title="이 턴들은 시각이 없어 시간×모델 버킷에 올릴 자리가 없었습니다. 세션 합계에는 그대로 남아 있습니다 — 총량은 맞고 모델별 분해만 못 합니다. 왜 시각이 없는지는 그 PC 의 기록을 봐야 압니다.">시각 없는 턴 ${esc(n(nts))}</span>`
        : (unknown >= u.sessions
          ? '<span class="help" title="이 사용자의 세션은 전부 구버전 수집기가 보낸 것이라 시각 유무를 알 수 없습니다. 수집기가 갱신되면 채워집니다.">알 수 없음(구버전 수집기)</span>'
          : ''));
    return `<tr>
      <td class="mono">${esc(u.username)}</td>
      <td style="text-align:right">${esc(n(u.sessions))}</td>
      <td style="text-align:right">${esc(n(u.withSeries))}</td>
      <td style="text-align:right" class="${pct < 50 ? 'txt-warn' : ''}">${pct}%</td>
      <td class="help">${esc(basis)}</td>
      <td>${why}</td>
    </tr>`;
  }).join('');

  return `<div class="mt" style="border-top:1px solid var(--border-soft);padding-top:8px">
    <div class="help">모델별 값은 두 근거를 더한 것입니다 —
      <b>series</b>(시간×모델 버킷)가 있는 세션은 모델별 정확값,
      없는 세션은 <b>그 세션에서 가장 많이 쓴 모델 하나</b>에 통째로 귀속된 값입니다.
      ${totPct == null ? '' : `지금 이 표의 출력 <b>${totPct}%</b> 가 후자입니다.`}
      후자는 세션에 모델이 섞였다면 틀린 자리에 들어가 있습니다.</div>
    ${ax.overSessions ? `<div class="help txt-warn mt-sm">series 합이 세션 행보다 큰 세션
      ${esc(n(ax.overSessions))}건 — 그 초과분은 잔여 계산에서 0 으로 끊었습니다.</div>` : ''}
    <div class="table-wrap mt"><table class="tbl"><thead><tr>
      <th>사용자</th><th style="text-align:right">세션</th>
      <th style="text-align:right" title="usage_series(시간×모델 버킷)가 하나라도 있는 세션">series 있음</th>
      <th style="text-align:right">커버리지</th><th>모델별 값의 근거</th>
      <th title="커버리지가 낮은 이유. 서버가 아는 것은 '시각 없는 턴이 N개였다' 까지입니다 — 왜 없는지는 그 PC 의 기록을 봐야 압니다">왜 낮은가</th>
    </tr></thead><tbody>${body}</tbody></table></div>
  </div>`;
}

/*
 * 카탈로그 공백 카드 — 이 화면에서 가장 값어치 있는 자리.
 * 추천이 실패한 목표들이 공유하는 토큰을 보여준다. 여기 오래 남는 단어가 곧 만들어야 할 에이전트다.
 */
function gapCard(reco) {
  const gaps = (reco && reco.gaps) || [];
  const total = (reco && reco.total) || 0;
  const miss = (reco && reco.miss) || 0;
  const rate = total ? Math.round((miss / total) * 100) : 0;
  return `<div class="card glass mb"${gaps.length ? ' style="border-color:var(--accent-from)"' : ''}>
    <div class="between mb"><h3>카탈로그 공백</h3>
      <span class="count">추천 ${esc(n(total))}건 중 매칭 실패 ${esc(n(miss))}건 (${rate}%)</span></div>
    ${gaps.length
    ? `<p class="help mb"><b>매칭이 약했던</b> 목표들이 공유한 단어입니다(점수 1 이하 — 위 '실패'는 점수 0 만 센 것이라
         숫자가 다를 수 있습니다). <b>여기 반복해서 오르는 단어가 새 에이전트를 만들 자리</b>입니다.</p>
       <div class="row" style="gap:6px;flex-wrap:wrap">${gaps.map((g) => `<span class="badge">${esc(g.token)} <b>${esc(String(g.count))}</b></span>`).join('')}</div>`
    : `<div class="muted">${total ? '매칭이 약했던 목표가 아직 반복되지 않았습니다.' : '추천 호출 기록이 아직 없습니다.'}</div>
       <div class="help mt-sm">추천 API 를 호출하면 그 관측이 여기에 쌓입니다.</div>`}
  </div>`;
}

// 일별 추이 — 막대 하나가 하루. 캐시읽기는 자릿수가 달라 같은 축에 두지 않고 표에만 남긴다.
function dayCard(rows) {
  if (!rows.length) return '';
  const asc = rows.slice().reverse();
  const max = Math.max(...asc.map((r) => (r.output || 0) + (r.input || 0)), 1);
  return `<div class="card glass mb"><div class="between mb"><h3>일별 추이</h3>
      <span class="help">막대는 입력+출력 · 캐시읽기는 아래 숫자</span></div>
    <div class="udays">${asc.map((r) => {
    const h = Math.max(2, Math.round((((r.output || 0) + (r.input || 0)) / max) * 100));
    return `<div class="uday" title="${esc(r.day)} · 출력 ${n(r.output)} · 입력 ${n(r.input)} · 캐시읽기 ${n(r.cacheRead)}">
        <div class="uday-bar" style="height:${h}%"></div>
        <div class="uday-l">${esc(String(r.day || '').slice(5))}</div></div>`;
  }).join('')}</div></div>`;
}

export async function renderUsage(pane, seq) {
  pane.innerHTML = '<div class="muted">불러오는 중…</div>';
  let d = null;
  try {
    d = await api('/api/usage/summary');
  } catch (e) {
    const msg = String((e && e.message) || e);
    // 403 은 오류가 아니라 정책이다 — 사람별 사용량이라 admin 만 본다. 그 사실을 화면이 말한다.
    pane.innerHTML = /403|forbidden|권한/i.test(msg)
      ? `<div class="card glass"><h3>권한이 필요합니다</h3>
         <p class="help">사용 추적은 <b>사용자별 사용량</b>을 담고 있어 관리자만 열람합니다.</p></div>`
      : `<div class="card glass"><div class="muted">사용 추적을 불러오지 못했습니다.</div>
         <div class="help mt-sm">${esc(msg)}</div></div>`;
    return;
  }
  if (isStale(seq) || !pane.isConnected) return;

  const t = d.totals || {};
  const empty = !t.sessions;

  pane.innerHTML = `
    <div class="lead">동기화된 PC 들이 <b>무엇을 얼마나 썼는지</b>를 모읍니다.
      집계만 수집하며 <b>프롬프트 원문·파일 경로·명령 인자는 저장하지 않습니다.</b>
      ${retentionNote(d.retention)}</div>
    ${totalsCard(t)}
    ${inputNote()}
    ${empty ? `<div class="card glass mt"><h3>아직 보고가 없습니다</h3>
      <p class="help">팀원 PC 가 <b>수집기를 갱신한 뒤 세션을 한 번 열면</b> 직전 세션들의 집계가 올라옵니다
      (수집기는 하루 1회 자동 갱신되므로 재설치는 필요 없습니다).</p>
      <p class="help">각 PC 에서 끄려면 <span class="mono">TEAM_USAGE_DISABLE=1</span>,
      키워드 축만 빼려면 <span class="mono">TEAM_USAGE_NO_KEYWORDS=1</span> 입니다.</p></div>` : ''}
    ${gapCard(d.recommendation)}
    ${dayCard(d.byDay || [])}
    <div class="card glass mb"><div class="between mb"><h3>사용자별</h3>
        <span class="help">이름이 실제 담당자와 다르면 <b>귀속 교정</b>(/api/usage/identity)으로 묶습니다</span></div>
      ${userTable(d.byUser || [])}</div>
    <div class="card glass mb"><div class="between mb"><h3>모델별</h3>
        <span class="help">series 가 있는 세션은 모델별 정확값 · 없는 세션은 세션 최빈 모델 기준</span></div>
      ${modelTable(d.byModel || []) || '<div class="muted">아직 데이터가 없습니다.</div>'}
      ${coverageCard(d.modelAxis, d.byModel || [])}</div>
    <div class="card glass"><div class="between mb"><h3>사용 현황</h3>
        <span class="help" id="uxHint"></span></div>
      <div class="tabs" id="uxTabs" role="tablist">
        ${AXES.map((a) => `<button type="button" role="tab" data-axis="${a.id}"
          class="tab${a.id === axis ? ' active' : ''}" aria-selected="${a.id === axis}">${a.label}</button>`).join('')}
      </div>
      <div id="uxBody" class="mt"></div></div>`;

  const tabs = pane.querySelector('#uxTabs');
  const body = pane.querySelector('#uxBody');
  const hint = pane.querySelector('#uxHint');
  const paint = () => {
    tabs.querySelectorAll('[data-axis]').forEach((b) => {
      const on = b.dataset.axis === axis;
      b.className = 'tab' + (on ? ' active' : '');
      b.setAttribute('aria-selected', String(on));
    });
    const def = AXES.find((a) => a.id === axis) || AXES[0];
    hint.textContent = def.hint;
    body.innerHTML = bars((d.top || {})[axis] || []) + dispatchHtml(axis);
  };
  tabs.querySelectorAll('[data-axis]').forEach((b) => {
    b.onclick = () => { if (b.dataset.axis !== axis) { axis = b.dataset.axis; paint(); } };
  });
  paint();

  /*
   * 사람별 활용은 별도 조회다(fail-soft) — 실패해도 위의 전체 집계는 그대로 보인다.
   * 도착하면 지금 보고 있는 축이 agent·skill 일 때만 다시 그린다.
   */
  api('/api/usage/dispatch').then((r) => {
    dispatchData = r || { agents: [], skills: [], generic: [] };
    if (axis === 'agent' || axis === 'skill') paint();
  }).catch(() => {
    dispatchData = { agents: [], skills: [], generic: [], failed: true };
    if (axis === 'agent' || axis === 'skill') paint();
  });
}
