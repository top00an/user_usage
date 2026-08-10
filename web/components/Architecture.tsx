'use client';

/*
 * 아키텍처(작동 방식) — 관리자 전용, **정적 설명 페이지**다(데이터 fetch 없음).
 *
 * 운영자가 "인제스트 키 하나로 데이터가 어떻게 수집·연동·수신되는지"를 한눈에 이해하도록,
 * 좌→우 데이터 흐름 구성도 + ①~⑥ 단계 설명을 담는다. 여기 적힌 사실은 실제 시스템 계약이다 —
 * 없는 기능(SSO·미터링·실시간 스트리밍 등)을 그리지 않는다.
 *
 * 접근성: 구성도는 하나의 그림(role="img")으로 요약 aria-label 을 달고, 도식을 못 봐도
 * 아래 ①~⑥ 단계 설명만으로 같은 내용을 이해할 수 있게 한다(도식은 보조, 텍스트가 정본).
 */

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
  '데이터 흐름 구성도. 왼쪽 개발자 머신에서 Claude Code 가 남긴 세션 기록(jsonl)을 수집기가 ' +
  '집계(원문 미수집)해, 아웃바운드 HTTPS 로 POST /api/usage(인제스트 키 Bearer)로 전송한다. ' +
  '클라우드에서는 ALB(443·TLS 종단)를 지나 프라이빗 서브넷의 ECS Fargate 서버가 인제스트 키로 ' +
  '조직을 판정하고 Postgres RLS 로 조직을 격리해 저장한다. 운영자는 세션 쿠키 로그인으로 ' +
  '자기 조직 데이터만 열람한다.';

export default function Architecture() {
  return (
    <>
      <p className="lead">
        인제스트 키 하나로 개발자 머신의 사용 기록이 어떻게 <b>수집 · 연동 · 수신</b>되는지를 그린 구성도입니다.
        아래 도식은 왼쪽(개발자 머신) → 오른쪽(클라우드)으로 데이터가 흐르는 순서입니다.
      </p>

      {/* ── 데이터 정책 배지 — 이 시스템의 핵심 4가지를 먼저 눈에 띄게 ── */}
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

            {/* ① 개발자 머신 */}
            <div className="arch-zone zone-client">
              <div className="arch-zone-h">
                <span className="arch-zone-badge" aria-hidden="true">①</span> 개발자 머신 (클라이언트)
              </div>
              <div className="arch-zone-body">
                <Node
                  title="Claude Code"
                  sub="~/.claude/projects/**/*.jsonl"
                >
                  <div className="arch-node-note help">세션마다 기록을 로컬에 남깁니다.</div>
                </Node>

                <Arrow dir="down" label="jsonl 읽기" />

                <Node title="수집기 · usage-collector">
                  <div className="arch-chips">
                    <Chip tone="net" glyph="⚡">SessionEnd 훅 · 실시간 푸시</Chip>
                    <Chip tone="default" glyph="↺">백필 -all · 증분 · 체크포인트 · 멱등</Chip>
                  </div>
                  <div className="arch-node-note help">
                    토큰 I/O · 캐시(read·create) · 세션 · 비용, 그리고 7축(tool · bash · slash · skill · agent · mcp · keyword)으로 <b>집계</b>합니다.
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

      {/* ── ①~⑥ 흐름 설명 ── */}
      <section className="arch-steps mt">
        <h2 className="arch-h">데이터 흐름 — 6단계</h2>
        <ol className="arch-step-list">
          <li className="arch-step">
            <span className="arch-step-num" aria-hidden="true">①</span>
            <div className="arch-step-body">
              <h3>개발자 머신에서 기록 · 집계</h3>
              <p className="help">
                Claude Code 가 세션 기록을 <code className="mono">~/.claude/projects/**/*.jsonl</code> 에 남깁니다.
                수집기가 이를 읽어 토큰 I/O · 캐시(read·create) · 세션 · 비용과 7축(tool · bash · slash · skill · agent · mcp · keyword)으로 집계합니다.
              </p>
              <div className="arch-chips">
                <Chip tone="net" glyph="⚡">SessionEnd 훅(실시간)</Chip>
                <Chip tone="default" glyph="↺">백필 -all(증분 · 멱등)</Chip>
                <Chip tone="policy" glyph="▤">집계만 수집 · 원문 미수집</Chip>
              </div>
            </div>
          </li>

          <li className="arch-step">
            <span className="arch-step-num" aria-hidden="true">②</span>
            <div className="arch-step-body">
              <h3>전송 — 아웃바운드 HTTPS</h3>
              <p className="help">
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
