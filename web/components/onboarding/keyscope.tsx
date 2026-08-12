'use client';

/*
 * ── 인제스트 키가 무엇을 열고 무엇을 못 여는가 ────────────────────────────
 *
 * 운영자는 이 키를 개발자 PC 에 심는다. 그 순간 "이걸로 대시보드도 보이나?", "해지하면 언제
 * 끊기나?" 를 화면이 대신 답해 주지 않으면, 사람은 가장 안전한 쪽이 아니라 **가장 넓은 쪽으로
 * 가정한다** — 열람 권한이 있다고 믿고 키를 아끼거나, 반대로 유출을 방치한다.
 *
 * 아래 사실은 전부 서버 코드에서 확인된 것만 적는다(추정·계획 금지):
 *   · go/internal/httpapi/server.go — 인테이크 스코프는 `POST /api/usage` 하나만 통과시키고
 *     그 밖의 경로는 403 이다. 즉 이 키로는 대시보드를 열 수 없다.
 *   · go/internal/httpapi/agent.go — `GET /api/agent/collector` 는 인제스트 키로 통과한다.
 *   · go/internal/org/org.go — 키 해석은 요청마다 DB 조회이고 `revoked_at IS NULL` 조건이라
 *     해지가 즉시 반영된다(캐시 없음). 해지된 키는 보고·다운로드 모두 401.
 *   · 저장은 sha256 해시뿐이다(HashKey) — 그래서 평문은 발급 응답 그 1회가 전부다.
 *   · go/internal/httpapi/usage.go — 키에 사용자가 묶여 있으면 그 보고의 귀속은 **키 주인**이다.
 *     `payload.user` 도 machine 매핑도 조회되지 않는다(동결 ① 우선순위).
 *
 * 접근성: 상태를 **색이 아니라 글자**로 말한다(허용/차단/주의 배지 텍스트). 색만으로 구분하면
 * 색각 이상 사용자에게 '허용'과 '차단'이 같은 회색 칩이 된다 — SupportBadge 와 같은 규율.
 */

/**
 * 동결된 원라인 설치 명령. **이 형태 그대로** 만든다 — 서버가 install.sh 를 이 인자 계약으로 받는다.
 * 순수 함수로 떼어 테스트가 문자열을 직접 잡을 수 있게 한다(화면 렌더에 의존하지 않는다).
 */
export function installCommand(origin: string, key: string): string {
  return `curl -fsSL ${origin}/install.sh | sh -s -- --key ${key} --server ${origin}`;
}

interface ScopeFact { kind: string; cls: string; term: string; body: React.ReactNode }

/**
 * 귀속은 **그 키마다 다른 사실**이라 상수로 둘 수 없다. 셀프서비스 키는 나에게 묶이고, 관리자
 * 대리발급은 남에게 묶이고, 결속 없는 키는 아무에게도 묶이지 않는다 — 한 문장으로 뭉치면 세
 * 경우 중 둘에서 화면이 거짓을 말한다. `owner` 로 갈라 그 키의 사실만 적는다.
 *
 *   undefined → 셀프서비스(내 키)   string → 그 사람에게 묶인 키   null → 결속 없음
 */
function attributionFact(owner: string | null | undefined): ScopeFact {
  if (owner === null) {
    return {
      kind: '주의', cls: 'warn', term: '사용자에 묶여 있지 않습니다',
      body: <>이 키로 들어온 사용량은 <b>보고한 PC 가 주장하는 이름</b>으로 잡힙니다 — 사람 계정과 일치한다는 보장이 없습니다.</>,
    };
  }
  if (owner === undefined) {
    return {
      kind: '귀속', cls: 'ok', term: '이 키는 나에게 묶입니다',
      body: <>이 키로 들어온 사용량은 <b>내 이름</b>으로 잡힙니다 — PC 가 보내는 이름보다 우선합니다.</>,
    };
  }
  return {
    kind: '귀속', cls: 'ok', term: `이 키는 ${owner} 에게 묶입니다`,
    body: <>이 키로 들어온 사용량은 <b>{owner}</b> 로 잡힙니다 — PC 가 보내는 이름보다 우선합니다.</>,
  };
}

function scopeFacts(owner: string | null | undefined): ScopeFact[] {
  return [
    {
      kind: '허용', cls: 'ok', term: '사용량 보고',
      body: <>이 키는 <code className="mono">POST /api/usage</code> <b>하나만</b> 엽니다.</>,
    },
    {
      kind: '허용', cls: 'ok', term: '수집기 다운로드',
      body: <><code className="mono">GET /api/agent/collector</code> 로 수집기 바이너리를 받습니다.</>,
    },
    {
      kind: '차단', cls: 'mute', term: '대시보드 열람 불가',
      body: <>이 키로 조회를 시도하면 <b>403</b> 입니다. 열람은 로그인 계정으로 합니다.</>,
    },
    attributionFact(owner),
    {
      kind: '해지', cls: '', term: '즉시 반영',
      body: <>해지하면 그 키의 보고·다운로드가 <b>바로 401</b> 이 됩니다.</>,
    },
    {
      kind: '보관', cls: '', term: '평문은 1회만',
      body: <>서버는 <b>sha256 해시만</b> 저장합니다 — 평문은 발급 시 한 번만 표시됩니다.</>,
    },
    {
      kind: '주의', cls: 'warn', term: '팀원 PC 마다 복제되는 자격',
      body: <>유출되면 그 배포에 임의 사용량이 들어올 수 있습니다. 의심되면 해지하고 재발급하세요.</>,
    },
  ];
}

/**
 * 키 스코프 명세. 발급 모달과 키 목록 **양쪽**에 같은 사실을 둔다 — 한쪽만 보고 판단하는 자리를
 * 남기지 않기 위해서다. 두 인스턴스가 한 화면에 동시에 뜰 수 있으므로 aria-label 은 서로 달라야
 * 한다(같은 이름의 목록이 둘이면 스크린리더 사용자는 어느 쪽을 듣고 있는지 알 수 없다).
 */
export function KeyScope({ label, owner }: { label: string; owner?: string | null }) {
  return (
    <div className="key-scope" role="list" aria-label={label}>
      {scopeFacts(owner).map((f) => (
        <div className="key-scope-item" role="listitem" key={f.term}>
          <span className={`badge ${f.cls}`}>{f.kind}</span>
          <span className="key-scope-txt">
            <b>{f.term}</b> — {f.body}
          </span>
        </div>
      ))}
    </div>
  );
}

/** 클립보드 복사 — 최신 API 를 쓰되, 없거나 막힌 환경에서도 죽지 않게 textarea 로 되짚는다. */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* 아래 폴백으로 */
  }
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}
