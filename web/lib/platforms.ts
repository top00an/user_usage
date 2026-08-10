/*
 * ── 플랫폼 축의 단일 출처 ─────────────────────────────────────────────────
 *
 * 이 파일이 답하는 질문은 하나다: **"그 플랫폼은 이 지표를 기록하는가?"**
 *
 * 왜 필요한가. 플랫폼마다 수집 가능한 축이 다르다. 그런데 API 응답은 숫자 하나로만 오므로
 * `0` 이 세 가지 서로 다른 사실을 뭉친다:
 *
 *   ① 실제 0    — 수집됐고 값이 0 이다("이번 기간에 안 썼다")
 *   ② 미수집    — 그 플랫폼이 애초에 기록하지 않아 수집이 불가능하다("모른다")
 *   ③ 해당 없음 — 개념 자체가 없다("그런 값은 존재하지 않는다")
 *
 * ②③ 을 `0` 으로 그리면 화면은 "안 썼다"고 말하게 된다. 그건 우리가 모르는 것을 아는 것처럼
 * 말하는 것이고, 이 레포가 반복해서 값을 치른 사고 유형이다(캐시읽기 오독·$0 단가 미등록).
 * 그래서 렌더 직전에 이 표를 거친다 — `components/platform/SupportBadge.tsx` 의 MetricValue.
 *
 * ── 이 표는 정적 상수다(응답이 아니다) ────────────────────────────────────
 * "어떤 플랫폼이 화면에 나오는가"는 절대 여기서 정하지 않는다 — 그건 /api/usage/platforms
 * 응답이 정한다. 여기 있는 것은 "나온 플랫폼이 무엇을 기록하는가"라는 **사실표**뿐이다.
 *
 * ── 근거(2026-08-10 기준) ─────────────────────────────────────────────────
 *   · 필터로 보낼 수 있는 값의 목록 = go/internal/store/platform.go 의
 *     PlatformFilterValues() = Platforms(claude·codex·gemini·antigravity) + other.
 *     **여기 없는 값을 보내면 서버가 400 을 낸다.** 그래서 UI 는 반드시 이 목록으로 좁힌다.
 *   · codex 의 slash·skill·agent: Codex 세션 로그에 그 개념의 기록 자체가 남지 않는다 →
 *     수집기가 보낼 것이 없다(미수집).
 *   · codex 의 cacheCreate: OpenAI 는 캐시 **쓰기**에 과금하지 않는다 → 청구/토큰 축으로서의
 *     '캐시 생성'이라는 값이 존재하지 않는다(해당 없음). 응답에 0 이 와도 그건 관측이 아니다.
 *   · gemini: 수집기가 아직 없다. 목록에 나타나더라도 값은 올라오지 않는다(준비 중).
 *   · antigravity: **gemini 와 다른 도구다.** 사람들이 "Gemini CLI" 라 부르는 것에는 오픈소스
 *     google-gemini/gemini-cli 와 Google Antigravity CLI(agy) 두 가지가 섞여 있다. 모델이 같아
 *     model 로는 안 갈리지만 수집 가능 범위가 다르다 — gemini 는 세션 파일에서 도구·MCP·
 *     subagent·skill·LOC 까지 나오는 반면, antigravity 에서 얻을 수 있는 것은 statusLine 의
 *     토큰·모델·세션·프로젝트가 전부다(훅 protobuf 스키마·대화 DB·transcript 를 전수 확인한
 *     결과 usage 외의 축이 없다). 그래서 행동 축은 '준비 중'이 아니라 **미수집**이다 —
 *     수집기를 더 만들면 언젠가 온다는 뜻이 아니라, 그 도구가 기록하지 않아 올 수 없다는 뜻이다.
 *   · antigravity 의 cacheCreate: Gemini 의 암시적 캐싱에는 캐시 **쓰기** 과금이라는 개념이
 *     없다 → 값이 존재하지 않는다(해당 없음). codex 와 같은 이유, 다른 근거다.
 *   · other: 허용목록 밖 이름으로 보고된 세션의 자리(서버가 claude 로 접지 않고 남긴다).
 *     무엇을 기록하는 도구인지 우리는 모른다(미상).
 *
 * 사실이 바뀌면 **여기만** 고친다. 화면 여러 곳에 같은 판단을 복사해 두면 한 곳만 고쳐지는
 * 날이 오고, 그때 어느 화면이 맞는지 아무도 모른다.
 */

/** 서버가 조회 필터로 받는 값. 이 목록 밖을 보내면 400 이다. */
export const PLATFORM_FILTER_VALUES = ['claude', 'codex', 'gemini', 'antigravity', 'other'] as const;

export type PlatformId = (typeof PLATFORM_FILTER_VALUES)[number];

/** 필터로 실어 보내도 되는 값인가. 응답에 모르는 이름이 와도 질의로는 되돌려 보내지 않는다. */
export function isPlatformId(v: unknown): v is PlatformId {
  return typeof v === 'string' && (PLATFORM_FILTER_VALUES as readonly string[]).includes(v);
}

/* ── 지원 상태 ───────────────────────────────────────────────────────── */

export type SupportState =
  /** 수집된다 — 값이 0 이면 그건 관측된 0 이다. */
  | 'yes'
  /** 그 플랫폼이 기록하지 않아 수집할 수 없다 — 값이 아니라 공백이다. */
  | 'unmeasured'
  /** 개념 자체가 없다 — 0 도 아니고 공백도 아니다. */
  | 'na'
  /** 수집기가 아직 없다. */
  | 'planned'
  /** 우리가 모르는 플랫폼이다 — 단정하지 않는다. */
  | 'unknown';

export const SUPPORT_LABEL: Record<SupportState, string> = {
  yes: '수집됨',
  unmeasured: '미수집',
  na: '해당 없음',
  planned: '준비 중',
  unknown: '미상',
};

export interface Support {
  state: SupportState;
  /** 짧은 사유. 배지 툴팁에 그대로 실린다 — 배지만 보고 "버그인가?" 하지 않도록. */
  why: string;
}

/* ── 지표 축 ─────────────────────────────────────────────────────────── */

/**
 * 지표 id. 앞 7개는 `components/usagetrack/labels.ts` 의 AXES id 와 **같은 이름**이다
 * (그래야 축 패널이 축 id 로 바로 지원 여부를 물을 수 있다 — 매핑 표를 하나 더 두지 않는다).
 */
export const METRICS = [
  'sessions', 'input', 'output', 'cacheRead', 'cacheCreate', 'cost',
  'tool', 'bash', 'mcp', 'keyword', 'slash', 'skill', 'agent',
  'loc', 'edits',
] as const;

export type MetricId = (typeof METRICS)[number];

export const METRIC_LABEL: Record<MetricId, string> = {
  sessions: '세션',
  input: '입력(비캐시)',
  output: '출력',
  cacheRead: '캐시읽기',
  cacheCreate: '캐시생성',
  cost: 'API 환산 비용',
  tool: '내장 도구',
  bash: '개발 명령',
  mcp: 'MCP 도구',
  keyword: '키워드',
  slash: '슬래시 명령',
  skill: '스킬',
  agent: '서브에이전트',
  loc: 'LOC(추가·삭제)',
  edits: '편집 수락·거부',
};

/**
 * 공통 코어 — 세 플랫폼이 **같은 방식으로** 기록하는 축. 플랫폼 비교는 이것만으로 한다.
 * 한쪽만 기록하는 축을 비교에 넣으면 "안 쓴 것"과 "못 재는 것"이 같은 막대가 된다.
 *
 * 토큰 축을 하나로 합치지 않는 이유는 `usagetrack/labels.ts` 에 적힌 것과 같다 —
 * 합계는 산술적으로 맞지만 `입력` 이라는 이름 아래 있으면 거짓말이다.
 */
export const COMMON_CORE: readonly MetricId[] = ['sessions', 'input', 'output', 'cacheRead', 'cost'];

/* ── 지원표 ──────────────────────────────────────────────────────────── */

/**
 * 값 축 — /api/usage/platforms 가 **숫자로 답하는** 축이다.
 *
 * 이 축들은 원칙적으로 `yes` 다. 서버가 실제 세션 행에서 집계한 값이라, 행이 있으면 그 숫자는
 * 관측이다. 여기에 '준비 중'·'미상'을 씌우면 이번엔 **수집된 값을 화면이 숨기게 된다** —
 * 없는 값을 0 으로 그리는 것과 정확히 같은 종류의 거짓말이고, 방향만 반대다.
 * (예외는 codex 의 cacheCreate 뿐이다: 열은 합산되지만 그 개념 자체가 존재하지 않는다.)
 */
const VALUE_AXES: readonly MetricId[] = ['sessions', 'input', 'output', 'cacheRead', 'cacheCreate', 'cost'];

const VALUE_WHY = '세션 롤업에서 서버가 실제 행으로 집계한 값입니다.';

/** 값 축은 yes 로 두고, 행동 축(도구·LOC 등)만 다른 상태로 채운다. */
function fill(behavior: SupportState, why: string): Record<MetricId, Support> {
  const out = {} as Record<MetricId, Support>;
  for (const m of METRICS) {
    out[m] = VALUE_AXES.includes(m) ? { state: 'yes', why: VALUE_WHY } : { state: behavior, why };
  }
  return out;
}

const UNKNOWN: Support = {
  state: 'unknown',
  why: '이 화면이 모르는 플랫폼입니다 — 어떤 축을 기록하는지 단정할 수 없습니다.',
};

const SUPPORT: Record<PlatformId, Record<MetricId, Support>> = {
  claude: fill('yes', 'Claude Code 수집기가 전 축을 보고합니다.'),
  codex: {
    ...fill('yes', 'Codex 수집기가 보고합니다.'),
    slash: {
      state: 'unmeasured',
      why: 'Codex 세션 로그에 슬래시 명령 기록이 남지 않습니다 — 수집기가 보낼 값이 없습니다.',
    },
    skill: {
      state: 'unmeasured',
      why: 'Codex 세션 로그에 스킬 호출 기록이 남지 않습니다 — 수집기가 보낼 값이 없습니다.',
    },
    agent: {
      state: 'unmeasured',
      why: 'Codex 세션 로그에 서브에이전트 위임 기록이 남지 않습니다 — 수집기가 보낼 값이 없습니다.',
    },
    cacheCreate: {
      state: 'na',
      why: 'OpenAI 는 캐시 쓰기에 과금하지 않습니다 — 캐시 생성이라는 값 자체가 없습니다(0 이 아닙니다).',
    },
  },
  gemini: fill('planned', '수집기가 아직 없습니다 — 이 축의 값은 올라오지 않습니다.'),
  /*
   * antigravity — 행동 축이 '준비 중'이 아니라 '미수집'인 것이 핵심이다.
   * 준비 중은 "언젠가 온다"이고 미수집은 "그 도구가 안 남겨서 올 수 없다"다. 여기서 '준비 중'을
   * 쓰면 우리는 오지 않을 값을 기다리게 하고, 화면은 빈칸의 이유를 틀리게 말한다.
   */
  antigravity: {
    ...fill(
      'unmeasured',
      'Antigravity 는 이 축을 기록하지 않습니다 — statusLine 의 사용량(토큰·모델·세션·프로젝트) 외에 '
      + '수집기가 읽을 수 있는 근거가 없습니다.',
    ),
    cacheCreate: {
      state: 'na',
      why: 'Gemini 의 암시적 캐싱에는 캐시 쓰기 과금이라는 개념이 없습니다 — 캐시 생성이라는 값 자체가 없습니다(0 이 아닙니다).',
    },
  },
  other: fill('unknown', UNKNOWN.why),
};

/**
 * 그 플랫폼이 그 지표를 기록하는가.
 *
 * 모르는 플랫폼(서버가 우리보다 새로울 때)은 **other 와 같이** 다룬다: 값 축은 그대로 보여주고
 * (그 숫자는 서버가 집계한 관측이다), 행동 축만 '미상'이라고 말한다. 전부 '미상'으로 접으면
 * 새 플랫폼의 실제 사용량이 화면에서 사라진다 — 지원표를 안 고친 날 조용히 그렇게 된다.
 */
export function supportOf(platform: string, metric: MetricId): Support {
  const row = (isPlatformId(platform) ? SUPPORT[platform] : undefined) ?? SUPPORT.other;
  return row[metric] ?? UNKNOWN;
}

/* ── 표시용 메타 ─────────────────────────────────────────────────────── */

export interface PlatformMeta {
  id: string;
  label: string;
  /** 계열색. globals.css 의 --series-* 토큰이라 라이트/다크가 함께 따라온다. */
  color: string;
  /** 목록에 나타났을 때 함께 말해야 하는 사실(없으면 빈 문자열). */
  note: string;
}

const META: Record<PlatformId, Omit<PlatformMeta, 'id'>> = {
  claude: { label: 'Claude', color: 'var(--series-2)', note: '' },
  codex: { label: 'Codex', color: 'var(--series-3)', note: '' },
  gemini: { label: 'Gemini', color: 'var(--series-1)', note: '수집기 준비 중' },
  // 표시명은 제품명 그대로다. gemini 와 나란히 서는 자리라 색까지 갈라 둔다 —
  // 같은 색이면 사람은 두 행을 한 도구의 분해로 읽는다.
  antigravity: { label: 'Antigravity', color: 'var(--series-4)', note: '토큰·비용 축만 기록' },
  other: { label: '기타(미식별)', color: 'var(--series-5)', note: '허용목록 밖 이름으로 보고된 세션' },
};

/**
 * 표시 메타. **모르는 id 도 그대로 그린다** — 서버가 우리보다 새로울 수 있고, 그때 화면에서
 * 통째로 사라지는 것보다 "모르는 플랫폼이 있다"가 보이는 편이 낫다(서버의 other 규율과 같다).
 */
export function platformMeta(id: string): PlatformMeta {
  const m = isPlatformId(id) ? META[id] : undefined;
  return {
    id,
    label: m?.label ?? id,
    color: m?.color ?? 'var(--fg-faint)',
    note: m?.note ?? '이 화면이 모르는 플랫폼입니다',
  };
}
