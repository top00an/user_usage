/*
 * ── runtime 축의 단일 출처 ────────────────────────────────────────────────
 *
 * runtime 은 **이 세션이 어디서 돌았나**다(cloud|local). platform 과 직교한다 — platform 은
 * "어느 도구"이고, 같은 도구가 클라우드 모델도 로컬 모델도 문다.
 *
 * 왜 별도 축인가: 로컬 모델은 API 과금이 $0 이고 단가표에 없어 비용에서 빠진다. 그런데
 * 데이터에 "로컬이었다"가 적힌 자리가 없으면 "우리 작업의 얼마가 로컬로 옮겨갔나"에 답할 수
 * 없다. `go/internal/cost/seed_openai.go` 가 gpt-oss-120b 를 미등록으로 둔 이유가 정확히
 * 그것이다 — "어디서 돌렸는지를 사용량 레코드가 말해 주지 않는다."
 *
 * ── platforms.ts 와 다른 점: 여기엔 지원표가 없다 ─────────────────────────
 *
 * `platforms.ts` 는 "그 플랫폼이 이 지표를 기록하는가"라는 사실표를 갖는다. runtime 에는 그
 * 표를 두지 않는다 — 수집 가능 범위는 **도구**의 성질이고 runtime 은 실행 위치다. 행을
 * (platform × runtime) 으로 늘리면 지원표가 8행이 되어 "플랫폼별 범위 차이"라는 그 표의
 * 목적이 흐려진다(docs/PLAN-local-llm.md D1 의 결정).
 *
 * 로컬 엔드포인트가 캐시 축을 주지 않는다는 사실은 별개로 남아 있다 — 로컬 세션이 실제로
 * 쌓인 뒤 그 표기를 정한다.
 *
 * ── 허용목록은 서버가 정한다 ──────────────────────────────────────────────
 *
 * 필터로 보낼 수 있는 값 = `go/internal/store/runtime.go` 의 `Runtimes`(cloud·local).
 * **여기 없는 값을 보내면 서버가 400 을 낸다.** 그래서 UI 는 반드시 이 목록으로 좁힌다.
 *
 * platform 과 달리 `other` 같은 제3의 값이 없다 — runtime 은 이분법이라 늘어날 이유가 없고,
 * 서버도 허용목록 밖 값을 cloud 로 접는다(제3의 값을 만들지 않는다).
 */

/** 서버 허용목록과 한 벌이다(store.Runtimes). */
export const RUNTIME_IDS = ['cloud', 'local'] as const;

export type RuntimeId = (typeof RUNTIME_IDS)[number];

export function isRuntimeId(v: string): v is RuntimeId {
  return (RUNTIME_IDS as readonly string[]).includes(v);
}

/*
 * 라벨. `cloud` 를 "클라우드"라고만 적지 않는 이유가 있다 —
 * 저장된 `cloud` 는 **"확인된 클라우드"가 아니라 "로컬이라는 표시가 없다"** 는 뜻이다
 * (migrations/pg/0042_runtime.sql 의 DEFAULT 주석). 압도적 다수가 실제로 클라우드지만,
 * 화면이 이 값을 확정 사실로 말하면 과장이다. 그래서 도움말이 그 구분을 진다.
 */
export function runtimeLabel(id: string): string {
  switch (id) {
    case 'local':
      return '로컬';
    case 'cloud':
      return '클라우드';
    default:
      return id;
  }
}

export function runtimeHint(id: string): string {
  switch (id) {
    case 'local':
      return '로컬 엔드포인트로 돌린 세션. API 과금이 없어 비용 합계에서 빠집니다(모델이 단가표에 없습니다).';
    case 'cloud':
      return '로컬이라는 표시가 없는 세션. 대부분 실제 클라우드이지만, 수집기가 판정하지 못한 세션도 여기 들어갑니다.';
    default:
      return '';
  }
}
