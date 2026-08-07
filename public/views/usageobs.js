'use strict';
/*
 * 사용 관측 — 비용·분포·드릴다운.
 *
 * 기존 '사용 추적'(usagetrack.js)이 합계를 보여준다면 이 화면은 **"왜 그 숫자인가"** 를 보여준다.
 * 만든 이유는 구체적이다: 같은 사용량을 두고 도구마다 176배 차이 나는 숫자가 나왔다.
 * 원인은 화면이 가장 크게 띄운 값이 **출력 토큰 하나**였다는 것이다 —
 * 실제로는 캐시읽기가 비용의 64.5%, 출력은 11% 였다.
 * 그래서 이 화면의 첫 카드는 합계가 아니라 **비용의 축별 분해**다.
 *
 * '언제 튀었나'(시간축 추이)는 대시보드 추이 그래프·'사용 추적' 화면이 담당한다 — 여기서는
 * 중복을 걷어내고 "왜"(축별 분해·분포·상위 세션)에 집중한다.
 */
// core·router 가 절대경로인 이유는 usagetrack.js 상단 주석 참조(URL 깊이 ≠ FS 깊이, 별칭 쓰면 모듈 2인스턴스).
import { api, esc, fmtTime, openModal, toast } from '/js/core.js';
import { isStale } from '/js/router.js';

function n(v) { return (Number(v) || 0).toLocaleString('ko-KR'); }
function usd(v) {
  const x = Number(v) || 0;
  if (x === 0) return '$0';
  return x < 1 ? `$${x.toFixed(3)}` : `$${x.toLocaleString('en-US', { maximumFractionDigits: 2 })}`;
}
function short(v) {
  const x = Number(v) || 0;
  if (x >= 1e9) return `${(x / 1e9).toFixed(1)}B`;
  if (x >= 1e6) return `${(x / 1e6).toFixed(1)}M`;
  if (x >= 1e3) return `${(x / 1e3).toFixed(1)}K`;
  return String(x);
}
function pct(part, whole) {
  const w = Number(whole) || 0;
  return w > 0 ? ((Number(part) || 0) / w) * 100 : 0;
}
/* 값에 비례하는 인라인 막대. 폭만 쓰므로 테마 변수를 그대로 탄다. */
function bar(ratio) {
  const w = Math.max(0, Math.min(100, Number(ratio) || 0));
  return `<span class="ubar" style="display:inline-block;width:${w.toFixed(1)}%;height:.55rem;
    border-radius:.28rem;background:currentColor;opacity:.35"></span>`;
}

/*
 * 비용 카드 — 이 화면의 존재 이유.
 *
 * 축을 **비중 내림차순**으로 세운다. 토큰 수 순서로 세우면 화면이 다시 "제일 큰 축이 제일 안
 * 비싼 축"을 보여주게 된다(그게 애초의 사고였다).
 */
function costCard(sum, sample) {
  const axes = [
    { k: 'cacheRead', label: '캐시 읽기', hint: '입력가의 0.1배' },
    {
      k: 'cacheCreate',
      label: '캐시 생성',
      // TTL 을 아는 행만 있으면 "가정"이라 말하지 않는다 — 아는 것을 모른다고 말하는 것도 거짓이다.
      hint: sum.ttlUnknownRows ? '5분 1.25배 · 1시간 2배(일부 TTL 미상)' : '5분 1.25배 · 1시간 2배',
    },
    { k: 'output', label: '출력', hint: '' },
    { k: 'input', label: '입력', hint: '' },
  ].map((a) => ({ ...a, v: Number(sum.byAxis[a.k]) || 0 }))
    .sort((a, b) => b.v - a.v);

  const rows = axes.map((a) => `<tr>
    <td>${esc(a.label)}${a.hint ? ` <span class="help">${esc(a.hint)}</span>` : ''}</td>
    <td style="width:45%">${bar(pct(a.v, sum.usd))}</td>
    <td class="mono" style="text-align:right">${esc(usd(a.v))}</td>
    <td class="mono" style="text-align:right">${pct(a.v, sum.usd).toFixed(1)}%</td>
  </tr>`).join('');

  return `<div class="card glass">
    <div class="between"><h3>API 환산액</h3>
      <span class="help">단가 기준일 ${esc(sum.pricedAt || '-')}</span></div>
    <div class="v" style="font-size:1.6rem">${esc(usd(sum.usd))}</div>
    <p class="help mt-sm"><b>실제 결제액이 아닙니다.</b>
      팀은 구독(Claude Max·Team)으로 쓰므로 결제액은 <b>정액 구독료</b>이고, 이 숫자는 같은 사용량을
      <b>API 종량제로 썼다면</b> 나왔을 금액입니다 — 즉 구독이 얼마나 절약하고 있는지를 보여줍니다.
      사용자·모델 간 <b>상대 비교</b>와 어느 축이 비싼지를 보는 용도입니다.</p>
    <div class="table-wrap mt-sm"><table><tbody>${rows}</tbody></table></div>
    ${sample && sample.truncated ? `<p class="help">표본이 최근 ${n(sample.rows)}건으로 잘렸습니다 —
      더 넓게 보려면 기간을 좁히세요.</p>` : ''}
  </div>`;
}

/* 분포 — 합계가 감춘 이상치를 끌어올린다. */
function distCard(d) {
  const defs = [
    { k: 'cacheReadPerTurn', label: '턴당 캐시읽기', fmt: short,
      why: '한 요청이 실어 보낸 컨텍스트 크기. 캐시읽기가 큰 이유가 여기 있다.' },
    { k: 'sessionCostUsd', label: '세션당 비용', fmt: usd, why: '비싼 세션이 어디쯤인지.' },
    { k: 'turnsPerSession', label: '세션당 턴 수', fmt: n, why: '' },
    { k: 'cacheHitRate', label: '캐시 적중률', fmt: (v) => `${(Number(v) * 100).toFixed(1)}%`,
      why: '낮아지면 접두사가 깨지고 있다는 뜻.' },
  ];
  const rows = defs.map(({ k, label, fmt, why }) => {
    const s = d[k] || { p: {} };
    if (!s.n) return `<tr><td>${esc(label)}</td><td colspan="4" class="muted">표본 없음</td></tr>`;
    return `<tr>
      <td>${esc(label)}${why ? `<br><span class="help">${esc(why)}</span>` : ''}</td>
      <td class="mono" style="text-align:right">${esc(fmt(s.p.p50))}</td>
      <td class="mono" style="text-align:right">${esc(fmt(s.p.p95))}</td>
      <td class="mono" style="text-align:right">${esc(fmt(s.p.p99))}</td>
      <td class="mono" style="text-align:right">${esc(fmt(s.max))}</td>
    </tr>`;
  }).join('');
  return `<div class="card glass mt">
    <h3>분포</h3>
    <div class="table-wrap mt-sm"><table>
      <thead><tr><th>지표</th><th style="text-align:right">p50</th><th style="text-align:right">p95</th>
        <th style="text-align:right">p99</th><th style="text-align:right">최대</th></tr></thead>
      <tbody>${rows}</tbody></table></div>
  </div>`;
}

/* 상위 세션 — 메트릭에서 개별 세션으로 내려가는 입구. */
function sessionsCard(d) {
  const rows = d.sessions.map((s) => `<tr data-sid="${esc(s.sessionId)}" style="cursor:pointer">
    <td class="mono">${esc(String(s.startedAt || '').slice(0, 16).replace('T', ' '))}</td>
    <td>${esc(s.username || '(미상)')}</td>
    <td class="mono">${esc(s.model || '(미상)')}</td>
    <td>${esc(s.project || '')}</td>
    <td class="mono" style="text-align:right">${n(s.turns)}</td>
    <td class="mono" style="text-align:right">${esc(short(s.cacheRead))}</td>
    <td class="mono" style="text-align:right">${s.priced ? esc(usd(s.usd)) : '<span class="help">단가 미등록</span>'}</td>
  </tr>`).join('');
  return `<div class="card glass mt">
    <h3>상위 세션 <span class="help">비용순 · 행을 누르면 상세</span></h3>
    <div class="table-wrap mt-sm"><table>
      <thead><tr><th>시작</th><th>사용자</th><th>모델</th><th>프로젝트</th>
        <th style="text-align:right">턴</th><th style="text-align:right">캐시읽기</th>
        <th style="text-align:right">비용</th></tr></thead>
      <tbody>${rows}</tbody></table></div>
    <div id="obsDetail" class="mt-sm"></div>
  </div>`;
}

function detailHtml(d) {
  const kinds = Object.entries(d.counters || {});
  const body = kinds.length ? kinds.map(([kind, items]) => `<div class="mt-sm">
    <b>${esc(kind)}</b>
    <div class="help">${items.slice(0, 12).map((i) => `${esc(i.key)} ${n(i.count)}`).join(' · ')}</div>
  </div>`).join('') : '<div class="muted">이 세션의 도구 사용 기록이 없습니다.</div>';
  const c = d.cost || { byAxis: {} };

  /*
   * 시간 버킷이 있으면 그것이 이 세션의 진짜 모양이다 — 몇 시에, 어느 모델로, 얼마나 느렸고
   * 몇 번 실패했나. 세션 한 줄로는 답할 수 없던 질문이 여기서 끝난다.
   */
  const bk = Array.isArray(d.series) ? d.series : [];
  const hours = bk.length ? `<div class="table-wrap mt-sm"><table>
    <thead><tr><th>시각</th><th>모델</th><th style="text-align:right">턴</th>
      <th style="text-align:right">캐시읽기</th><th style="text-align:right">평균지연</th>
      <th style="text-align:right">오류</th></tr></thead>
    <tbody>${bk.map((b) => `<tr>
      <td class="mono">${esc(String(b.hour).slice(8, 10))}일 ${esc(String(b.hour).slice(11, 13))}시</td>
      <td class="mono">${esc(b.model)}</td>
      <td class="mono" style="text-align:right">${n(b.turns)}</td>
      <td class="mono" style="text-align:right">${esc(short(b.cacheRead))}</td>
      <td class="mono" style="text-align:right">${b.latencyTurns
    ? `${(b.latencyMsSum / b.latencyTurns / 1000).toFixed(1)}s` : '-'}</td>
      <td class="mono" style="text-align:right">${b.toolErrors ? n(b.toolErrors) : '-'}</td>
    </tr>`).join('')}</tbody></table></div>` : '';

  const dur = (d.session.startedAt && d.session.endedAt)
    ? `${Math.round((Date.parse(d.session.endedAt) - Date.parse(d.session.startedAt)) / 60000)}분`
    : null;

  return `<div class="card glass">
    <div class="between"><h4 class="mono">${esc(d.session.sessionId)}</h4>
      <span class="mono">${c.priced ? esc(usd(c.usd)) : '단가 미등록'}</span></div>
    <div class="help">캐시읽기 ${esc(usd(c.byAxis.cacheRead))} · 캐시생성 ${esc(usd(c.byAxis.cacheCreate))}
      · 출력 ${esc(usd(c.byAxis.output))} · 입력 ${esc(usd(c.byAxis.input))}
      ${dur ? ` · 지속 ${esc(dur)}` : ''}
      ${c.exact ? '' : ' · <b>근사</b>(세션 최빈 모델 1개로 계산 — 시간 버킷이 없습니다)'}</div>
    ${hours}
    ${body}
    <p class="help mt-sm">${esc(d.note || '')}</p>
  </div>`;
}

/* 사람별 리더보드 — byUser 에 비용·효율(캐시적중·세션당비용)을 얹어 팀 관점으로 세운다. */
function leaderboardCard(d) {
  const users = (d && d.users) || [];
  if (!users.length) return '';
  const rows = users.slice(0, 15).map((u) => {
    const name = u.username || '(미상)';
    return `<tr>
    <td>${esc(name)}</td>
    <td class="mono" style="text-align:right">${n(u.sessions)}</td>
    <td class="mono" style="text-align:right">${n(u.turns)}</td>
    <td class="mono" style="text-align:right">${esc(short(u.tokens))}</td>
    <td class="mono" style="text-align:right">${(Number(u.cacheHitRate) * 100).toFixed(0)}%</td>
    <td class="mono" style="text-align:right">${u.priced ? esc(usd(u.usdPerSession)) : '-'}</td>
    <td class="mono" style="text-align:right">${u.priced ? esc(usd(u.usd)) : '<span class="help">단가 미등록</span>'}</td>
    <td style="text-align:right"><button type="button" class="ghost sm" data-detail="${esc(name)}">상세</button></td>
  </tr>`;
  }).join('');
  return `<div class="card glass mt">
    <h3>사용자별 <span class="help">API 환산액순 · 이 기간 전체</span></h3>
    <div class="table-wrap mt-sm"><table>
      <thead><tr><th>사용자</th><th style="text-align:right">세션</th><th style="text-align:right">턴</th>
        <th style="text-align:right">토큰</th><th style="text-align:right">캐시적중</th>
        <th style="text-align:right">세션당</th><th style="text-align:right">환산액</th>
        <th style="text-align:right"></th></tr></thead>
      <tbody>${rows}</tbody></table></div>
    ${users.length > 15 ? `<p class="help">상위 15명 표시(전체 ${n(users.length)}명).</p>` : ''}
  </div>`;
}

/*
 * ── 사용자별 상세 ─────────────────────────────────────────────────────────
 *
 * "이 사람이 무엇을 얼마나 쓰는가" 를 **모델별 × 시간축**으로 편다. 리더보드는 기간 전체 합계라
 * "요즘 늘었나 줄었나", "어느 모델로 옮겨갔나" 를 답하지 못한다.
 *
 * 두 축을 함께 보여주는 이유: 일별 7일은 최근 추세를, 주간은 그보다 긴 흐름을 본다. 한쪽만
 * 두면 "이번 주가 유난한가" 를 판단할 기준이 없다.
 *
 * 데이터는 기존 /api/usage/series 를 그대로 쓴다(group_by=model · user 필터). 새 엔드포인트를
 * 만들지 않는다 — 같은 질문에 두 개의 집계 경로가 생기면 값이 갈릴 자리가 된다.
 */
const DETAIL_DAYS = 7;
const DETAIL_WEEKS = 8;

/*
 * n일 전의 **로컬** 날짜. UTC 로 계산하면 00:00~09:00 KST 사이에 화면을 열었을 때 하루가
 * 밀린 범위를 요청하게 된다(서버가 로컬 라벨로 거르므로 그만큼 빈 칸이 생긴다).
 */
function dayShift(days) {
  const d = new Date();
  d.setDate(d.getDate() - days);
  const p = (x) => String(x).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/*
 * 시계열 → 모델(행) × 시각(열) 표. 값이 0 인 칸은 비워 둔다 — 0 을 채우면 눈이 그리로 간다.
 *
 * 값 포맷을 인자로 받는 이유: 이 표는 **토큰 소모량**을 보여준다(금액이 아니다). 팀이 구독제라
 * 금액은 실제 결제액이 아니고, 알고 싶은 것은 "누가 얼마나 태우는가" 이기 때문이다.
 */
function pivot(series, label, fmt = short) {
  const cols = [...new Set((series || []).flatMap((s) => (s.points || []).map((p) => p.t)))]
    .sort((a, b) => a.localeCompare(b));
  if (!cols.length) return `<p class="help">${esc(label)} 구간에 데이터가 없습니다.</p>`;

  const totalOf = (t) => (series || []).reduce(
    (sum, s) => sum + ((s.points || []).find((p) => p.t === t)?.v || 0), 0,
  );
  const head = cols.map((t) => `<th style="text-align:right">${esc(t.slice(5))}</th>`).join('');
  const body = (series || []).map((s) => {
    const cells = cols.map((t) => {
      const v = (s.points || []).find((p) => p.t === t)?.v || 0;
      // title 에 원본값 — short() 는 자릿수를 접으므로 정확한 값이 필요한 사람이 볼 곳이 있어야 한다.
      return `<td class="mono" style="text-align:right"${v ? ` title="${esc(n(v))}"` : ''}>${v ? esc(fmt(v)) : '<span class="help">·</span>'}</td>`;
    }).join('');
    return `<tr><td class="mono">${esc(s.label || '-')}</td>${cells}
      <td class="mono" style="text-align:right"><b>${esc(fmt(s.total))}</b></td></tr>`;
  }).join('');
  const totals = cols.map((t) => `<td class="mono" style="text-align:right"><b>${esc(fmt(totalOf(t)))}</b></td>`).join('');
  const grand = (series || []).reduce((a, s) => a + (Number(s.total) || 0), 0);

  return `<div class="table-wrap"><table>
    <thead><tr><th>모델</th>${head}<th style="text-align:right">합계</th></tr></thead>
    <tbody>${body}</tbody>
    <tfoot><tr><td><b>합계</b></td>${totals}
      <td class="mono" style="text-align:right"><b>${esc(fmt(grand))}</b></td></tr></tfoot>
  </table></div>`;
}

// export 하는 이유: 테스트가 버튼 핸들러의 **실제 동작**(일별·주간 두 축을 부르는가)을
// 호출해서 확인한다. 마크업만 보면 핸들러가 죽어 있어도 통과한다.
export async function openUserDetail(username) {
  const qs = (from, interval) => `/api/usage/series?metric=tokens&interval=${interval}`
    + `&group_by=model&user=${encodeURIComponent(username)}&from=${from}`;
  let daily; let weekly;
  try {
    // 두 요청은 서로 독립이다 — 순차로 기다릴 이유가 없다.
    [daily, weekly] = await Promise.all([
      api(qs(dayShift(DETAIL_DAYS - 1), 'day')),
      api(qs(dayShift(DETAIL_WEEKS * 7), 'week')),
    ]);
  } catch (e) {
    toast(`상세를 불러오지 못했습니다: ${String((e && e.message) || e).slice(0, 120)}`, 'err');
    return;
  }

  openModal({
    title: `${username} — 사용 상세`,
    okLabel: null,          // 읽기 전용 — 저장할 것이 없다
    cancelLabel: '닫기',
    maxWidth: 880,
    body: `
      <p class="help">모델별 <b>토큰 소모량</b>입니다(입력·출력·캐시읽기·캐시생성 합계).
        숫자에 마우스를 올리면 정확한 값이 나옵니다.
        시각 기준 <span class="mono">${esc((daily && daily.timezone) || (weekly && weekly.timezone) || '-')}</span>.</p>
      <h4 class="mt">최근 ${DETAIL_DAYS}일 <span class="help">일별</span></h4>
      ${pivot(daily && daily.series, `최근 ${DETAIL_DAYS}일`)}
      <h4 class="mt">최근 ${DETAIL_WEEKS}주 <span class="help">주간 · 월요일 시작</span></h4>
      ${pivot(weekly && weekly.series, `최근 ${DETAIL_WEEKS}주`)}`,
  });
}

/* 품질축 — usage_series 에 이미 쌓이는 오류·거부·지연을 꺼낸다. 신수집기만 보내므로 커버리지를 함께 밝힌다. */
function qualityCard(d) {
  if (!d) return '';
  const cov = d.coverage || {};
  const pctS = (v) => `${(Number(v) * 100).toFixed(1)}%`;
  if (!d.turns) {
    return `<div class="card glass mt"><h3>품질</h3>
      <p class="help">아직 시간 버킷(신수집기)이 없어 품질 지표를 낼 수 없습니다 —
        팀원 PC 가 수집기를 갱신하면 도구 오류율·거부율·지연이 여기 쌓입니다.</p></div>`;
  }
  const rows = [
    ['도구 오류율', pctS(d.toolErrorRate), 'tool_result 가 오류로 돌아온 비율'],
    ['응답 거부율', pctS(d.refusalRate), '모델이 거부(refusal)로 종료한 비율'],
    ['최대토큰 중단율', pctS(d.stopMaxRate), '길이 상한에 걸려 잘린 비율'],
    ['평균 지연', `${(Number(d.latencyAvgMs) / 1000).toFixed(1)}s`, '턴당 응답 대기(평균)'],
    ['최대 지연', `${(Number(d.latencyMaxMs) / 1000).toFixed(1)}s`, '가장 느렸던 턴'],
  ].map(([k, v, why]) => `<tr><td>${esc(k)}<br><span class="help">${esc(why)}</span></td>
    <td class="mono" style="text-align:right">${esc(v)}</td></tr>`).join('');
  return `<div class="card glass mt">
    <div class="between"><h3>품질</h3>
      <span class="help">신수집기 세션 ${n(cov.sessionsWithSeries)}/${n(cov.sessionsTotal)} 커버 · 측정 턴 ${n(d.latencyTurns)}</span></div>
    <div class="table-wrap mt-sm"><table><tbody>${rows}</tbody></table></div>
  </div>`;
}

/* 상대 시각 — 응답의 now 를 기준으로(클라이언트 시계 오차를 타지 않게). */
function rel(nowIso, iso) {
  if (!iso) return '—';
  const ms = Date.parse(nowIso) - Date.parse(iso);
  if (!Number.isFinite(ms)) return '—';
  const m = Math.max(0, Math.floor(ms / 60000));
  if (m < 60) return `${m}분 전`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}시간 전`;
  return `${Math.floor(h / 24)}일 전`;
}

/* 수집 상태 — 발신처별 마지막 보고. "왜 데이터가 없나"에 화면이 직접 답한다. */
function coverageCard(d) {
  const reps = (d && d.reporters) || [];
  if (!reps.length) return '';
  const now = (d && d.now) || '';
  const STALE_MS = 2 * 24 * 3600 * 1000;
  const rows = reps.map((r) => {
    const staleMs = r.lastReportedAt ? (Date.parse(now) - Date.parse(r.lastReportedAt)) : Infinity;
    const stale = !(staleMs < STALE_MS);
    return `<tr>
      <td class="mono">${esc(r.machine)}</td>
      <td>${esc(r.username || '(미상)')}</td>
      <td class="mono" style="text-align:right">${n(r.sessions)}</td>
      <td class="mono" style="text-align:right" title="${esc(fmtTime(r.lastReportedAt))}">
        ${stale ? '<span class="badge warn">' : ''}${esc(rel(now, r.lastReportedAt))}${stale ? '</span>' : ''}</td>
      <td style="text-align:right">${r.sendsSeries ? '<span class="badge ok">신</span>' : '<span class="help">구</span>'}</td>
    </tr>`;
  }).join('');
  return `<div class="card glass mt">
    <div class="between"><h3>수집 상태</h3>
      <span class="help">발신처별 마지막 보고 — "왜 데이터가 없나"에 답합니다</span></div>
    <div class="table-wrap mt-sm"><table>
      <thead><tr><th>발신처</th><th>사용자</th><th style="text-align:right">세션</th>
        <th style="text-align:right">마지막 보고</th><th style="text-align:right">수집기</th></tr></thead>
      <tbody>${rows}</tbody></table></div>
    <p class="help">2일 이상 보고 없으면 <b>주황</b> — 그 PC 의 수집기가 꺼졌거나 네트워크가 막힌 신호입니다.
      '신'=시간 버킷까지 보내는 신수집기(품질 지표 기여), '구'=세션 합계만.</p>
  </div>`;
}

export async function renderUsageObs(pane, seq) {
  pane.innerHTML = '<div class="muted">불러오는 중…</div>';

  let dist; let sessions; let leaderboard; let quality; let coverage;
  try {
    [dist, sessions, leaderboard, quality, coverage] = await Promise.all([
      api('/api/usage/distribution'),
      api('/api/usage/sessions?sort=cost&top=25'),
      api('/api/usage/leaderboard').catch(() => ({ users: [] })),
      api('/api/usage/quality').catch(() => null),
      api('/api/usage/coverage').catch(() => ({ reporters: [] })),
    ]);
  } catch (e) {
    const msg = String((e && e.message) || e);
    // 403 은 오류가 아니라 정책이다 — 사람별 비용이라 admin 만 본다(usagetrack.js 와 같은 처리).
    pane.innerHTML = /403|forbidden|권한/i.test(msg)
      ? `<div class="card glass"><h3>권한이 필요합니다</h3>
         <p class="help">사용 관측은 <b>사용자별 사용량과 비용</b>을 담고 있어 관리자만 열람합니다.</p></div>`
      : `<div class="card glass"><div class="muted">사용 관측을 불러오지 못했습니다.</div>
         <div class="help mt-sm">${esc(msg)}</div></div>`;
    return;
  }
  if (isStale(seq) || !pane.isConnected) return;

  // 비용 합계·축별 분해는 세션 목록 엔드포인트가 **전 윈도우**로 집계해 내려준다(top-N 이전의
  // 전 rows 합). usd 와 byAxis 가 같은 소스(costOf 합)라 헤드라인 총액과 축별 분해가 정합한다.
  const sum = {
    usd: sessions.usd || 0,
    byAxis: sessions.byAxis || { input: 0, output: 0, cacheRead: 0, cacheCreate: 0 },
    pricedAt: sessions.pricedAt,
    unpriced: sessions.unpriced || [],
    ttlUnknownRows: sessions.ttlUnknownRows || 0,
  };

  if (!sessions.sessions.length) {
    pane.innerHTML = `<div class="card glass"><h3>아직 보고가 없습니다</h3>
      <p class="help">팀원 PC 가 수집기를 갱신한 뒤 세션을 한 번 열면 집계가 올라옵니다.</p></div>`;
    return;
  }

  pane.innerHTML = `
    <div class="lead">토큰이 아니라 <b>비용 비중</b>으로 봅니다. 축마다 단가 배수가 달라
      토큰 수가 큰 축과 비용이 큰 축이 다릅니다.</div>
    ${costCard(sum, sessions.sample)}
    ${leaderboardCard(leaderboard)}
    ${qualityCard(quality)}
    ${distCard(dist.distributions || {})}
    ${sessionsCard(sessions)}
    ${coverageCard(coverage)}`;

  /*
   * 사용자별 [상세] — 버튼마다 직접 건다.
   *
   * ⚠ 핸들러 안에서 **렌더 함수의 지역 변수를 참조하지 않는다.** 필요한 값(username)은
   *   data 속성에서 읽는다. 지역 변수를 참조하면 클릭 시점에 ReferenceError 로 조용히 죽어
   *   "버튼을 눌러도 아무 반응이 없는" 증상이 된다(2026-08-04 기획 화면에서 실제로 겪은 사고).
   */
  pane.querySelectorAll('button[data-detail]').forEach((b) => {
    b.onclick = (ev) => {
      ev.stopPropagation();   // 상위 행의 세션 드릴다운이 함께 열리지 않게
      openUserDetail(b.dataset.detail);
    };
  });

  // 드릴다운 — 행 위임(행이 다시 그려져도 핸들러가 살아 있게 tbody 가 아니라 pane 에 건다).
  const detail = pane.querySelector('#obsDetail');
  pane.querySelectorAll('tr[data-sid]').forEach((tr) => {
    tr.onclick = async () => {
      if (!detail) return;
      detail.innerHTML = '<div class="muted">불러오는 중…</div>';
      try {
        const d = await api(`/api/usage/sessions/${encodeURIComponent(tr.dataset.sid)}`);
        if (!pane.isConnected) return;
        detail.innerHTML = detailHtml(d);
      } catch (e) {
        detail.innerHTML = `<div class="help">${esc(String((e && e.message) || e))}</div>`;
      }
    };
  });
}
