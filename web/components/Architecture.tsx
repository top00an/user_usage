'use client';

/*
 * 아키텍처(작동 방식) — 관리자 전용, **정적 설명 페이지**다(데이터 fetch 없음).
 *
 * 운영자가 "인제스트 키 하나로 데이터가 어떻게 수집·연동·수신되는지"를 한눈에 이해하도록,
 * 좌→우 데이터 흐름 구성도 + ①~⑥ 단계 설명을 담는다. 여기 적힌 사실은 실제 시스템 계약이다 —
 * 없는 기능(SSO·미터링·실시간 스트리밍 등)을 그리지 않는다.
 *
 * ── 이 화면이 **한 경로가 아니라 네 경로**를 그리는 이유 ──────────────────
 * 예전 도식은 Claude Code 단일 경로만 그렸다. 그동안 수집 대상이 4개 플랫폼·2가지 방식으로
 * 늘었고, 도식은 그대로였다. 그러면 운영자는 "Antigravity 도구·MCP 칸이 왜 비어 있나 — 버그인가"
 * 를 이 화면에서 답받지 못한다. 도식이 현실보다 좁으면 그 차이는 사용자의 오해로 정산된다.
 *
 *   파일 기반 수집(3종)  세션 파일을 읽어 집계 후 전송
 *     · Claude Code   ~/.claude/projects/**\/*.jsonl   (SessionEnd 훅이 트리거)
 *     · Codex         ~/.codex/sessions/**\/*.jsonl
 *     · Gemini CLI    ~/.gemini/tmp/<slug>/chats/*.jsonl (오픈소스 google-gemini/gemini-cli)
 *   런타임 캡처(1종)   파일에 토큰이 안 남아 실행 중에 잡는다
 *     · Antigravity CLI  statusLine 이 사용량을 로컬 스풀에 기록 → Stop 훅이 서버로 플러시
 *
 * 이후 경로는 넷이 같다: 아웃바운드 HTTPS `POST /api/usage` → ALB(TLS) → 서버(키→tenant→RLS)
 * → Postgres → 대시보드.
 *
 * ── 수집 범위 표는 **파생이지 사본이 아니다** ────────────────────────────
 * 플랫폼별로 무엇을 기록하는가는 `lib/platforms.ts` 의 지원표가 단일 출처다. 여기서 그 판정을
 * 손으로 옮겨 적으면 사실이 바뀐 날 한 곳만 고쳐지고, 그때 어느 화면이 맞는지 아무도 모른다.
 * 그래서 이 파일은 `supportOf()` 를 호출해 렌더할 뿐 판정을 갖지 않는다.
 *
 * 접근성: 구성도는 하나의 그림(role="img")으로 요약 aria-label 을 달고, 도식을 못 봐도
 * 아래 ①~⑥ 단계 설명만으로 같은 내용을 이해할 수 있게 한다(도식은 보조, 텍스트가 정본).
 * 수집 범위는 도식이 아니라 **표**로 둔다 — 배지는 색이 아니라 글자로 상태를 말한다.
 */

import { SupportChip } from '@/components/platform/SupportBadge';
import { METRIC_LABEL, platformMeta, type MetricId, type PlatformId } from '@/lib/platforms';

/** 작은 강조 칩. 색만으로 말하지 않도록 아이콘 글리프를 함께 둔다(색각 이상 대비). */
function Chip({ tone = 'default', glyph, children }: { tone?: 'policy' | 'net' | 'sec' | 'default'; glyph?: string; children: React.ReactNode }) {
  return (
    <span className={`arch-chip chip-${tone}`}>
      {glyph ? <span aria-hidden="true">{glyph}</span> : null}
      {children}
    </span>
  );
}

/** 노드 사이 연결 화살표. 세로(zone 내부) / 가로(zone 사이) 두 방향. 장식이라 스크린리더에서 숨긴다. */
function Arrow({ dir, label }: { dir: 'down' | 'right'; label?: string }) {
  return (
    <div className={`arch-conn arch-conn-${dir}`} aria-hidden="true">
      {label ? <span className="arch-conn-label">{label}</span> : null}
      <svg className="arch-conn-svg" viewBox="0 0 24 24" role="presentation">
        {dir === 'down' ? (
          <path d="M12 3v14m0 0l-5-5m5 5l5-5" />
        ) : (
          <path d="M3 12h14m0 0l-5-5m5 5l-5 5" />
        )}
      </svg>
    </div>
  );
}

/* 한 개의 노드 카드 — 제목 · 부연(모노) · 칩들. */
function Node({
  step, title, sub, children,
}: {
  step?: string;
  title: string;
  sub?: React.ReactNode;
  children?: React.ReactNode;
}) {
  return (
    <div className="arch-node">
      <div className="arch-node-head">
        {step ? <span className="arch-node-step" aria-hidden="true">{step}</span> : null}
        <span className="arch-node-title">{title}</span>
      </div>
      {sub ? <div className="arch-node-sub mono">{sub}</div> : null}
      {children}
    </div>
  );
}

/*
 * 구성도 전체를 한 문장으로 요약한 텍스트 대체. role="img" 의 aria-label 로 쓰인다 —
 * 도식을 못 보는 사용자는 이 요약 + 아래 ①~⑥ 단계로 전체 흐름을 파악한다.
 */
const DIAGRAM_ALT =
  '데이터 흐름 구성도. 왼쪽 개발자 머신에는 수집 경로가 두 가지다. ' +
  '파일 기반 수집은 Claude Code · Codex · Gemini CLI 가 남긴 세션 파일(jsonl)을 수집기가 읽어 ' +
  '집계한다(원문 미수집). 런타임 캡처는 Antigravity CLI 처럼 파일에 토큰이 남지 않는 도구용으로, ' +
  'statusLine 이 사용량을 로컬 스풀에 기록하고 Stop 훅이 서버로 플러시한다. ' +
  '이후 경로는 네 플랫폼이 같다 — 아웃바운드 HTTPS 로 POST /api/usage(인제스트 키 Bearer)로 전송하고, ' +
  '클라우드에서는 ALB(443·TLS 종단)를 지나 프라이빗 서브넷의 ECS Fargate 서버가 인제스트 키로 ' +
  '조직을 판정하고 Postgres RLS 로 조직을 격리해 저장한다. 운영자는 세션 쿠키 로그인으로 ' +
  '자기 조직 데이터만 열람한다.';

/*
 * ── 수집 소스 ────────────────────────────────────────────────────────────
 * 표시 라벨은 `platformMeta` 에서 가져온다(색·주석까지 한 곳에서). 여기서 갖는 것은 이 화면에만
 * 있는 사실 — **어디를 읽는가**(경로)와 **무엇이 트리거인가**(훅) 둘뿐이다.
 */
interface Source {
  id: PlatformId;
  /** 읽는 자리. 파일 기반은 glob, 런타임 캡처는 캡처 지점. */
  where: string;
  /** 서버로 나가는 계기. */
  trigger: string;
}

const FILE_SOURCES: Source[] = [
  { id: 'claude', where: '~/.claude/projects/**/*.jsonl', trigger: 'SessionEnd 훅이 트리거' },
  { id: 'codex', where: '~/.codex/sessions/**/*.jsonl', trigger: '세션 파일을 읽어 집계' },
  { id: 'gemini', where: '~/.gemini/tmp/<slug>/chats/*.jsonl', trigger: '오픈소스 gemini-cli · 세션 파일을 읽어 집계' },
];

const RUNTIME_SOURCES: Source[] = [
  { id: 'antigravity', where: 'statusLine → 로컬 스풀', trigger: 'Stop 훅이 서버로 플러시' },
];

const ALL_SOURCES = [...FILE_SOURCES, ...RUNTIME_SOURCES];

/*
 * 수집 범위 표의 행. **판정은 여기 없다** — id 만 갖고, 각 칸은 supportOf(platform, metric) 이
 * 그린다(lib/platforms.ts 가 단일 출처). 순서만 이 화면의 결정이다: 값 축 → 행동 축 → 코드 축.
 */
const SCOPE_ROWS: MetricId[] = [
  'sessions', 'input', 'output', 'cacheRead', 'cacheCreate', 'cost',
  'tool', 'bash', 'mcp', 'keyword', 'slash', 'skill', 'agent',
  'loc', 'edits',
];

/** 소스 카드 한 장 — 플랫폼 이름 · 읽는 자리 · 트리거. 계열색은 좌측 바로만 쓰고 글자로도 말한다. */
function SourceNode({ src }: { src: Source }) {
  const meta = platformMeta(src.id);
  return (
    <div className="arch-node arch-src" style={{ borderLeftColor: meta.color }}>
      <div className="arch-node-head">
        <span className="arch-node-title">{meta.label}</span>
        {meta.note ? <span className="badge mute">{meta.note}</span> : null}
      </div>
      <div className="arch-node-sub mono">{src.where}</div>
      <div className="arch-node-note help">{src.trigger}</div>
    </div>
  );
}

export default function Architecture() {
  return (
    <>
      <p className="lead">
        인제스트 키 하나로 개발자 머신의 사용 기록이 어떻게 <b>수집 · 연동 · 수신</b>되는지를 그린 구성도입니다.
        수집 대상은 <b>4개 플랫폼 · 2가지 수집 방식</b>이고, 전송 이후 경로는 넷이 같습니다.
        아래 도식은 왼쪽(개발자 머신) → 오른쪽(클라우드)으로 데이터가 흐르는 순서입니다.
      </p>

      {/* ── 데이터 정책 배지 — 이 시스템의 핵심 5가지를 먼저 눈에 띄게 ── */}
      <div className="arch-policy" role="list" aria-label="핵심 데이터 정책">
        <div className="arch-policy-item" role="listitem">
          <span className="arch-policy-ico" aria-hidden="true">▤</span>
          <div>
            <b>집계만 수집</b>
            <span className="help">프롬프트 원문 · 파일 경로 · 명령 인자는 수집하지 않습니다.</span>
          </div>
        </div>
        <div className="arch-policy-item" role="listitem">
          <span className="arch-policy-ico" aria-hidden="true">↑</span>
          <div>
            <b>아웃바운드 HTTPS</b>
            <span className="help">클라이언트 인바운드 방화벽 개방 불필요(NAT · 사내망 OK).</span>
          </div>
        </div>
        <div className="arch-policy-item" role="listitem">
          <span className="arch-policy-ico" aria-hidden="true">▦</span>
          <div>
            <b>RLS 조직 격리</b>
            <span className="help">Postgres 행 수준 보안으로 조직 간 교차 조회 0.</span>
          </div>
        </div>
        <div className="arch-policy-item" role="listitem">
          <span className="arch-policy-ico" aria-hidden="true">⚿</span>
          <div>
            <b>키 해시 저장</b>
            <span className="help">인제스트 키는 sha256 해시로만 저장(평문 미저장 · 해지 가능).</span>
          </div>
        </div>
        {/*
          다섯 번째. 앞 넷은 "우리가 무엇을 지키는가"이고 이것만 "무엇을 못 받는가"다 —
          그런데 빈칸의 이유를 여기서 말해 두지 않으면 사람은 빈칸을 버그로 읽는다.
        */}
        <div className="arch-policy-item" role="listitem">
          <span className="arch-policy-ico" aria-hidden="true">◐</span>
          <div>
            <b>플랫폼마다 수집 범위가 다르다</b>
            <span className="help">도구 · MCP · LOC 는 플랫폼에 따라 미수집입니다 — 아래 표가 축별로 밝힙니다.</span>
          </div>
        </div>
      </div>

      {/* ── 구성도 ── */}
      <section className="card glass arch-diagram-card mt">
        <div className="between mb">
          <h2 className="arch-h">수집 · 연동 · 수신 구성도</h2>
          <ul className="arch-legend" aria-label="영역 범례">
            <li><span className="arch-legend-dot zone-client" aria-hidden="true" />개발자 머신</li>
            <li><span className="arch-legend-dot zone-net" aria-hidden="true" />인터넷(HTTPS)</li>
            <li><span className="arch-legend-dot zone-cloud" aria-hidden="true" />클라우드</li>
          </ul>
        </div>

        {/* 넓은 도식은 본문을 밀지 않고 자기 안에서만 가로 스크롤한다(.table-wrap 과 같은 원칙). */}
        <div className="arch-diagram-scroll">
          <div className="arch-diagram" role="img" aria-label={DIAGRAM_ALT}>

            {/* ① 개발자 머신 — 수집 방식 두 갈래(파일 기반 3종 · 런타임 캡처 1종) */}
            <div className="arch-zone zone-client">
              <div className="arch-zone-h">
                <span className="arch-zone-badge" aria-hidden="true">①</span> 개발자 머신 (클라이언트)
              </div>
              <div className="arch-zone-body">
                <div className="arch-srcs">
                  <div className="arch-srcs-h">
                    파일 기반 수집 <span className="help">· 3종 — 세션 파일을 읽어 집계</span>
                  </div>
                  {FILE_SOURCES.map((s) => <SourceNode key={s.id} src={s} />)}
                </div>

                <div className="arch-srcs">
                  <div className="arch-srcs-h">
                    런타임 캡처 <span className="help">· 1종 — 파일에 토큰이 안 남아 실행 중에 잡는다</span>
                  </div>
                  {RUNTIME_SOURCES.map((s) => <SourceNode key={s.id} src={s} />)}
                </div>

                <Arrow dir="down" label="집계" />

                <Node title="수집기 · usage-collector">
                  <div className="arch-chips">
                    <Chip tone="net" glyph="⚡">훅 트리거 · 실시간 푸시</Chip>
                    <Chip tone="default" glyph="↺">백필 -all · 증분 · 체크포인트 · 멱등</Chip>
                  </div>
                  <div className="arch-node-note help">
                    토큰 I/O · 캐시(read·create) · 세션 · 비용을 <b>집계</b>합니다.
                    도구 · MCP · LOC 같은 행동 축은 <b>플랫폼마다 수집 범위가 다릅니다</b>(아래 표).
                  </div>
                  <div className="arch-chips">
                    <Chip tone="policy" glyph="▤">집계만 — 원문 · 경로 · 인자 미수집</Chip>
                  </div>
                </Node>
              </div>
            </div>

            {/* ② 전송 — 인터넷(HTTPS) */}
            <Arrow dir="right" label="전송" />

            <div className="arch-zone zone-net">
              <div className="arch-zone-h">
                <span className="arch-zone-badge" aria-hidden="true">②</span> 전송 · 인터넷(HTTPS)
              </div>
              <div className="arch-zone-body">
                <Node title="POST /api/usage" sub="Authorization: Bearer uu_ing_…">
                  <div className="arch-chips">
                    <Chip tone="net" glyph="↑">아웃바운드 HTTPS만</Chip>
                    <Chip tone="net" glyph="↺">멱등 UPSERT · 재전송 안전</Chip>
                  </div>
                  <div className="arch-node-note help">
                    인바운드 방화벽 개방이 필요 없어 NAT · 사내망 뒤에서도 그대로 동작합니다.
                  </div>
                </Node>
              </div>
            </div>

            <Arrow dir="right" label="수신" />

            {/* ③④⑤ 클라우드 */}
            <div className="arch-zone zone-cloud">
              <div className="arch-zone-h">
                <span className="arch-zone-badge" aria-hidden="true">③④⑤</span> 클라우드 (AWS)
              </div>
              <div className="arch-zone-body">
                <Node step="③" title="ALB · 443" sub="TLS 종단">
                  <div className="arch-node-note help">서버는 ALB 443 만 공개합니다.</div>
                </Node>

                <Arrow dir="down" label="프라이빗 서브넷" />

                <Node step="④" title="ECS Fargate · 서버 (수신 · 격리)">
                  <ol className="arch-flow">
                    <li>인제스트 키 → <code className="mono">org.Resolve</code></li>
                    <li>tenant(조직) 판정</li>
                    <li><code className="mono">SET LOCAL app.tenant_id</code></li>
                    <li>Postgres RLS 로 조직 격리</li>
                    <li>집계 정규화 → store UPSERT</li>
                  </ol>
                  <div className="arch-chips">
                    <Chip tone="sec" glyph="⚿">인제스트 키 sha256 해시 저장</Chip>
                  </div>
                </Node>

                <Arrow dir="down" label="UPSERT" />

                <Node step="⑤" title="RDS PostgreSQL" sub="프라이빗 · 암호화">
                  <div className="arch-chips">
                    <Chip tone="sec" glyph="▦">RLS org 격리 · 교차 조회 0</Chip>
                  </div>
                </Node>

                <Arrow dir="down" label="열람 (RLS · 세션 쿠키)" />

                <Node title="운영자 · 대시보드 열람">
                  <div className="arch-node-note help">
                    ID/PW 로그인 → 세션 쿠키로 <b>자기 조직 데이터만</b> 조회합니다.
                  </div>
                </Node>
              </div>
            </div>

          </div>
        </div>

        <p className="help mt">
          도식을 보기 어려우면 아래 <b>①~⑥ 단계 설명</b>으로 같은 내용을 순서대로 확인할 수 있습니다.
        </p>
      </section>

      {/* ── 플랫폼별 수집 범위 — 판정은 전부 lib/platforms.ts 에서 파생한다 ── */}
      <section className="card glass mt">
        <div className="between mb">
          <h2 className="arch-h">플랫폼별 수집 범위</h2>
          <span className="help">판정 출처: 지원표(lib/platforms.ts)</span>
        </div>
        <p className="help">
          같은 <b>0</b> 이라도 뜻이 다릅니다 — <b>수집됨</b>은 관측된 값이고, <b>미수집</b>은 그 도구가
          기록하지 않아 올 수 없는 값이며, <b>해당 없음</b>은 개념 자체가 없는 값입니다.
          <b>조건부 수집</b>은 조건이 맞을 때만 오는 값이라, 비어 있어도 고장이 아닙니다
          (Antigravity 의 슬래시 · 키워드는 대화형 세션에서만 쌓입니다).
          그래서 화면은 빈칸을 0 으로 그리지 않고 이 표대로 말합니다.
        </p>
        {/* 넓은 표는 본문을 밀지 않고 자기 안에서만 가로 스크롤한다(.table-wrap 과 같은 원칙). */}
        <div className="table-wrap mt">
          <table className="arch-scope">
            <caption className="sr-only">플랫폼별 지표 수집 범위</caption>
            <thead>
              <tr>
                <th scope="col">지표</th>
                {ALL_SOURCES.map((s) => (
                  <th scope="col" key={s.id}>{platformMeta(s.id).label}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {SCOPE_ROWS.map((m) => (
                <tr key={m}>
                  <th scope="row">{METRIC_LABEL[m]}</th>
                  {/*
                    SupportChip 은 '수집됨'만 강조 배지로, 나머지는 점선 회색으로 그린다 —
                    그래야 60칸을 훑을 때 **구멍이 먼저 보인다**. 상태는 여전히 글자로도 있으므로
                    색을 못 봐도 정보는 남는다(색은 스캔을 돕는 역할만 한다).
                  */}
                  {ALL_SOURCES.map((s) => (
                    <td key={s.id}><SupportChip platform={s.id} metric={m} /></td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      {/* ── ①~⑥ 흐름 설명 ── */}
      <section className="arch-steps mt">
        <h2 className="arch-h">데이터 흐름 — 6단계</h2>
        <ol className="arch-step-list">
          <li className="arch-step">
            <span className="arch-step-num" aria-hidden="true">①</span>
            <div className="arch-step-body">
              <h3>개발자 머신에서 기록 · 집계 — 4개 플랫폼 · 2가지 방식</h3>
              <p className="help">
                <b>파일 기반 수집(3종)</b> — 세션 파일을 읽어 집계합니다.
                Claude Code 는 <code className="mono">~/.claude/projects/**/*.jsonl</code>(SessionEnd 훅이 트리거),
                Codex 는 <code className="mono">~/.codex/sessions/**/*.jsonl</code>,
                Gemini CLI(오픈소스)는 <code className="mono">~/.gemini/tmp/&lt;slug&gt;/chats/*.jsonl</code> 입니다.
              </p>
              <p className="help">
                <b>런타임 캡처(1종)</b> — <b>Antigravity CLI</b> 는 파일에 토큰이 남지 않아 실행 중에 잡습니다.
                <code className="mono">statusLine</code> 이 사용량을 로컬 스풀에 기록하고, <b>Stop 훅</b>이 서버로 플러시합니다.
              </p>
              <p className="help">
                수집기는 토큰 I/O · 캐시(read·create) · 세션 · 비용을 집계합니다.
                도구 · MCP · LOC 같은 행동 축은 플랫폼마다 다릅니다 — 위 <b>플랫폼별 수집 범위</b> 표가 축별로 밝힙니다.
              </p>
              <div className="arch-chips">
                <Chip tone="net" glyph="⚡">훅 트리거(실시간)</Chip>
                <Chip tone="default" glyph="↺">백필 -all(증분 · 멱등)</Chip>
                <Chip tone="policy" glyph="▤">집계만 수집 · 원문 미수집</Chip>
                <Chip tone="default" glyph="◐">플랫폼마다 수집 범위 상이</Chip>
              </div>
            </div>
          </li>

          <li className="arch-step">
            <span className="arch-step-num" aria-hidden="true">②</span>
            <div className="arch-step-body">
              <h3>전송 — 아웃바운드 HTTPS (네 플랫폼 공통)</h3>
              <p className="help">
                여기서부터는 <b>4개 플랫폼이 같은 경로</b>입니다.
                <code className="mono">POST /api/usage</code> 에 헤더 <code className="mono">Authorization: Bearer uu_ing_…</code>(인제스트 키)로 보냅니다.
                아웃바운드 HTTPS 만 쓰므로 클라이언트 인바운드 방화벽을 열 필요가 없고(NAT · 사내망 OK),
                세션 지문 UPSERT 라 재전송해도 안전합니다.
              </p>
              <div className="arch-chips">
                <Chip tone="net" glyph="↑">아웃바운드 HTTPS만</Chip>
                <Chip tone="net" glyph="↺">멱등 · 재전송 안전</Chip>
              </div>
            </div>
          </li>

          <li className="arch-step">
            <span className="arch-step-num" aria-hidden="true">③</span>
            <div className="arch-step-body">
              <h3>클라우드 엣지 — ALB</h3>
              <p className="help">
                인터넷 → <b>ALB(443 · TLS 종단)</b> → 프라이빗 서브넷의 <b>ECS Fargate</b>.
                서버는 ALB 443 만 공개하고 그 외 포트는 외부에 노출하지 않습니다.
              </p>
            </div>
          </li>

          <li className="arch-step">
            <span className="arch-step-num" aria-hidden="true">④</span>
            <div className="arch-step-body">
              <h3>서버 — 수신 · 조직 격리</h3>
              <p className="help">
                인제스트 키 → <code className="mono">org.Resolve</code> 로 조직(tenant)을 판정하고,
                <code className="mono">SET LOCAL app.tenant_id</code> 후 <b>Postgres RLS</b> 로 조직을 완전 격리한 채
                집계를 정규화해 store 에 UPSERT 합니다. 인제스트 키는 sha256 해시로만 저장하며(평문 미저장),
                발급 시 1회만 노출되고 언제든 해지할 수 있습니다.
              </p>
              <div className="arch-chips">
                <Chip tone="sec" glyph="▦">RLS 조직 격리</Chip>
                <Chip tone="sec" glyph="⚿">키 sha256 해시 저장</Chip>
              </div>
            </div>
          </li>

          <li className="arch-step">
            <span className="arch-step-num" aria-hidden="true">⑤</span>
            <div className="arch-step-body">
              <h3>저장 · 열람</h3>
              <p className="help">
                <b>RDS PostgreSQL</b>(프라이빗 · 암호화)에 저장하고, RLS 로 조직 간 데이터를 격리합니다(교차 조회 0).
                운영자는 ID/PW 로그인 → 세션 쿠키로 대시보드에서 <b>자기 조직 데이터만</b> 조회합니다.
              </p>
            </div>
          </li>

          <li className="arch-step">
            <span className="arch-step-num" aria-hidden="true">⑥</span>
            <div className="arch-step-body">
              <h3>키 발급 · 연동 (운영자 관점)</h3>
              <p className="help">
                운영자가 <b>&ldquo;연동&rdquo;</b> 탭에서 인제스트 키를 발급해 개발자에게 전달하면,
                개발자는 <b>원커맨드</b>(<code className="mono">curl … | sh</code>)로 수집기와 훅을 자동 설치합니다.
                이후에는 세션마다 자동으로 수집됩니다.
              </p>
              <div className="arch-chips">
                <Chip tone="default" glyph="⧗">보존: keyword 축은 90일 후 삭제 · 나머지 집계 · 사용량은 계속 보관</Chip>
              </div>
            </div>
          </li>
        </ol>
      </section>
    </>
  );
}
