'use strict';
/*
 * 집계 시간대 — 저장은 UTC, **집계·표시는 로컬(기본 KST)**.
 *
 * ── 왜 필요한가 (2026-08-04 실측) ────────────────────────────────────────
 * 사용량 집계의 날짜·시간 라벨이 전부 UTC 였다. 한국 팀에게 이건 두 가지로 틀린다:
 *
 *   ① **날짜가 밀린다.** KST 하루는 UTC 로 [D-1 15:00, D 15:00) 이다. 그래서 00:00~09:00 KST
 *      에 한 일이 **전날**로 집계된다. 개발자가 가장 흔하게 일하는 심야가 통째로 어제로 간다.
 *   ② **시간대 그래프가 9시간 밀린다.** 21시 KST 스파이크가 12시 칸에 그려진다 —
 *      "몇 시에 몰리나" 를 보려고 만든 축이 정확히 그 질문에 거짓말을 한다.
 *
 * ── 왜 컨테이너 TZ 만으로는 안 고쳐지나 ─────────────────────────────────
 * 버킷 라벨을 만드는 것이 `toISOString()` 인데 이건 TZ 환경변수와 **무관하게 항상 UTC** 다.
 * TZ=Asia/Seoul 은 `date`·로그·toLocaleString 만 바꾼다. 그래서 코드로 옮겨야 한다.
 *
 * ── 왜 저장은 UTC 로 두나 ───────────────────────────────────────────────
 * 저장을 로컬로 바꾸면 ① 이미 쌓인 데이터와 새 데이터가 섞여 같은 컬럼에 두 시간대가 공존하고
 * ② 수집기(팀원 PC)까지 고쳐 재배포해야 하며 ③ 팀이 다른 시간대로 옮기면 과거가 통째로
 * 틀려진다. UTC 로 저장하고 **읽을 때 옮기는** 것이 되돌릴 수 있는 유일한 방향이다.
 * 그래서 이 모듈은 마이그레이션이 필요 없다 — 같은 원본을 다르게 읽을 뿐이다.
 *
 * 순수 함수만 둔다 — DB·설정 파일을 읽지 않는다(오프셋은 인자 또는 env).
 */

// 기본 +09:00(Asia/Seoul). 분 단위인 이유: 인도(+05:30) 같은 30분 오프셋 지역이 실재한다.
const DEFAULT_OFFSET_MIN = 540;

/*
 * 오프셋 결정 — env(USAGE_TZ_OFFSET_MIN) > 기본값.
 *
 * IANA 이름(Asia/Seoul)이 아니라 **고정 오프셋**을 쓰는 이유: 한국은 서머타임이 없어 오프셋이
 * 상수다. IANA 를 쓰려면 Intl 로 매 행마다 시간대 변환을 해야 하는데(수만 행) 그 비용을
 * 서머타임 없는 지역에 지불할 이유가 없다. 서머타임 지역으로 옮길 일이 생기면 그때 Intl 로
 * 바꾼다 — 그 전까지는 이 단순함이 옳다.
 */
function offsetMin(env = process.env) {
  const raw = env.USAGE_TZ_OFFSET_MIN;
  // ⚠ Number('') === 0 이다. 빈 값(미설정과 같다)을 UTC 로 읽으면 컨테이너에서 env 를
  //   빈 문자열로 넘긴 순간 집계가 조용히 UTC 로 되돌아간다.
  if (raw == null || String(raw).trim() === '') return DEFAULT_OFFSET_MIN;
  const v = Number(raw);
  // -14:00 ~ +14:00 이 실재하는 범위다. 밖의 값은 설정 오류이지 "그런 시간대" 가 아니다.
  return Number.isFinite(v) && v >= -840 && v <= 840 ? Math.trunc(v) : DEFAULT_OFFSET_MIN;
}

const MS_MIN = 60000;

/* 오프셋을 더한 Date. 내부 전용 — 이 값의 UTC 필드를 읽으면 곧 로컬 시각이다. */
function shifted(ms, off) {
  return new Date(ms + off * MS_MIN);
}

/*
 * ISO 타임스탬프 → 로컬 날짜 'YYYY-MM-DD'.
 * 형식이 아니면 **입력을 그대로 돌려준다** — 라벨이 없는 행을 지어내지 않는다.
 */
function localDay(iso, off = offsetMin()) {
  const ms = Date.parse(String(iso == null ? '' : iso));
  if (!Number.isFinite(ms)) return String(iso == null ? '' : iso).slice(0, 10);
  return shifted(ms, off).toISOString().slice(0, 10);
}

/*
 * UTC 시간 라벨('YYYY-MM-DDTHH') → 로컬 시간 라벨.
 * 수집기가 만든 라벨은 UTC 다(team-usage.js hourBucket). 그걸 읽을 때 옮긴다.
 */
const HOUR_RE = /^\d{4}-\d{2}-\d{2}T\d{2}$/;
function localHour(label, off = offsetMin()) {
  const s = String(label == null ? '' : label);
  if (!HOUR_RE.test(s)) return s;
  const ms = Date.parse(`${s}:00:00Z`);
  if (!Number.isFinite(ms)) return s;
  return shifted(ms, off).toISOString().slice(0, 13);
}

/*
 * 로컬 날짜 'YYYY-MM-DD' → 그 주의 **월요일**(ISO 8601 주 시작).
 *
 * 이미 로컬로 옮긴 날짜 라벨에만 적용한다 — UTC 라벨에 적용하면 주 경계가 9시간 밀린 채로
 * 굳는다. 월요일인 이유는 ISO 표준이고 팀 주간 회고가 주 초에 열려서다.
 */
const DAY_RE = /^\d{4}-\d{2}-\d{2}$/;
function weekStart(day) {
  const s = String(day == null ? '' : day);
  if (!DAY_RE.test(s)) return s;
  const d = new Date(`${s}T00:00:00Z`);
  if (Number.isNaN(d.getTime())) return s;
  d.setUTCDate(d.getUTCDate() - ((d.getUTCDay() + 6) % 7));
  return d.toISOString().slice(0, 10);
}

/*
 * 로컬 날짜 범위 → **UTC 조회 범위**.
 *
 * SQL 필터는 UTC 라벨 위에서 돈다(started_at·hour 가 UTC). 로컬 'YYYY-MM-DD' 를 그대로
 * 넘기면 경계가 오프셋만큼 어긋나 하루의 앞뒤가 잘린다. 그래서 **하루씩 넓혀** 뜬 다음
 * 옮긴 라벨로 정확히 거른다 — 넓히지 않으면 경계 행이 조용히 사라진다.
 *
 * 넓히는 방향이 한쪽이 아닌 이유: 오프셋이 양수면(KST) 로컬 D 의 시작이 UTC D-1 이고,
 * 음수면(미주) 로컬 D 의 끝이 UTC D+1 이다. 부호를 따지지 않고 양쪽을 넓히는 편이 안전하다.
 */
function widenUtcRange({ from, to } = {}) {
  const shiftDay = (d, delta) => {
    if (!DAY_RE.test(String(d || ''))) return d;
    const x = new Date(`${d}T00:00:00Z`);
    x.setUTCDate(x.getUTCDate() + delta);
    return x.toISOString().slice(0, 10);
  };
  return { from: shiftDay(from, -1), to: shiftDay(to, 1) };
}

/* 로컬 라벨이 요청 범위 안인가 — widenUtcRange 로 넓혀 뜬 뒤 이걸로 정확히 거른다. */
function inRange(localLabel, { from, to } = {}) {
  const d = String(localLabel || '').slice(0, 10);
  if (!DAY_RE.test(d)) return false;
  if (DAY_RE.test(String(from || '')) && d < from) return false;
  if (DAY_RE.test(String(to || '')) && d > to) return false;
  return true;
}

/*
 * 화면이 "이 숫자가 어느 시간대 기준인가" 를 말할 수 있게 라벨을 함께 내보낸다.
 *
 * 아는 시간대는 **이름**으로 낸다("KST"). 'UTC+09:00' 은 정확하지만 화면에서 읽는 사람에게는
 * UTC 라는 낱말이 먼저 들어와 "아직 UTC 인가?" 로 오해된다 — 실제로 그 오해가 있었다.
 * 모르는 오프셋만 UTC±HH:MM 으로 떨어진다(그때는 이름을 지어낼 수 없으므로 정확한 편이 낫다).
 */
const NAMED = Object.freeze({ 540: 'KST', 0: 'UTC', '-480': 'PST', 480: 'CST', 330: 'IST', 60: 'CET' });

function label(off = offsetMin()) {
  const named = NAMED[String(off)];
  if (named) return named;
  const sign = off < 0 ? '-' : '+';
  const a = Math.abs(off);
  const hh = String(Math.floor(a / 60)).padStart(2, '0');
  const mm = String(a % 60).padStart(2, '0');
  return `UTC${sign}${hh}:${mm}`;
}

module.exports = {
  DEFAULT_OFFSET_MIN,
  offsetMin,
  localDay,
  localHour,
  weekStart,
  widenUtcRange,
  inRange,
  label,
};
