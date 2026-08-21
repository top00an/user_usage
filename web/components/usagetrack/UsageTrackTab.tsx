'use client';

import { useCallback, useState } from 'react';
import { getDispatch, getLeaderboard, getPlatforms, getSummary } from '@/lib/api';
import { softly, useResource } from '@/hooks/useResource';
import type { Dispatch, Leaderboard, PlatformsResponse, Summary, UserRow } from '@/lib/types';
import { n, shortTokens } from '@/lib/format';
import { Card, Empty, ErrorState, Loading, TableWrap, TokenCount } from '@/components/ui';
import PlatformFilter from '@/components/platform/PlatformFilter';
import RuntimeFilter from '@/components/platform/RuntimeFilter';
import { useScope } from '@/lib/scope';
import ModelTable from './ModelTable';
import AxisExplorer from './AxisExplorer';
import UserFilter from './UserFilter';
import { setBuilderPrefill, type GroupBy, type Metric } from '@/lib/customPanels';

/* '그래프로 추가' — 대시보드 빌더에 프리필을 넘기고 대시보드 탭으로 이동한다. */
function PinButton({ metric, groupBy, title }: { metric: Metric; groupBy: GroupBy; title: string }) {
  return (
    <button
      type="button"
      className="pin-btn"
      onClick={() => { setBuilderPrefill({ metric, groupBy, type: groupBy === 'none' ? 'line' : 'bar', days: 30, title }); location.hash = '#/overview'; }}
    >
      ＋ 그래프로 추가
    </button>
  );
}
import { AX_CACHE_CREATE, AX_CACHE_READ, IN_HINT, IN_LABEL, TURNS_HINT } from './labels';

/* ================================================================
   사용 추적 — 동기화된 PC 들이 무엇을 얼마나 썼는가.

   세 가지를 한 화면에서 본다:
     ① 규모   토큰 사용량(입력·출력·캐시읽기·캐시생성)을 사람·모델·날짜로
     ② 행동   실제로 부른 도구·명령·스킬·에이전트·MCP
     ③ 공백   추천이 매칭에 실패한 목표의 토큰 — **새 에이전트를 만들 자리**

   ③ 이 이 화면의 존재 이유다. ①②만 보면 비용 대시보드에 그친다.

   **사용자 축이 이 화면의 1급 축이다**(UserFilter). 한 사람을 고르면 위 세 가지가 전부 그
   사람 기준으로 다시 조회된다 — 전사 합계 안에서는 개인의 사용 패턴이 평균에 묻힌다(실측:
   같은 지침이 모든 세션에 실렸는데 역할 에이전트 사용은 사람마다 25 대 0 으로 갈렸다).

   캐시 읽기를 항상 따로 세운다 — 입력에 합치면 비용이 수십 배로 과대 표시된다(실측 71887 vs 2).
   ================================================================ */

interface TrackData {
  summary: Summary;
  dispatch: Dispatch | null;
}

/*
 * 보존 정책 문구 — 키워드 축만 기한이 있다.
 * 화면이 이 사실을 말하지 않으면 두 가지가 생긴다: 추세가 끊긴 이유를 아무도 모르고,
 * 팀은 자기 발화 데이터가 얼마나 남는지 모른다. 둘 다 화면이 답할 일이다.
 */
function RetentionNote({ retention }: { retention?: Summary['retention'] }) {
  const days = retention?.keywordDays;
  return days
    ? <span className="help">키워드는 <b>{n(days)}일</b> 보관 후 자동 삭제됩니다(다른 축과 사용량은 계속 보관).</span>
    : <span className="help">키워드 보존 기한이 설정돼 있지 않습니다 — 무기한 보관됩니다.</span>;
}

function TotalsTiles({ t }: { t: Summary['totals'] }) {
  return (
    <div className="grid cols-2">
      <div className="tile glass">
        <div className="k">보고된 세션</div>
        <div className="v">{n(t.sessions)}</div>
        <div className="s">{n(t.users)}명 · {n(t.machines)}대</div>
      </div>
      <div className="tile glass">
        <div className="k">출력 토큰</div>
        <div className="v" title={n(t.output)}>{shortTokens(t.output)}</div>
        <div className="s" title={IN_HINT}>
          {IN_LABEL} {shortTokens(t.input)} · 캐시읽기 {shortTokens(t.cacheRead)} · 캐시생성 {shortTokens(t.cacheCreate)}
        </div>
      </div>
    </div>
  );
}


function UserTable({ rows }: { rows: UserRow[] }) {
  if (!rows.length) return <Empty>아직 사용량 보고가 없습니다.</Empty>;
  return (
    <TableWrap>
      <table>
        <thead>
          <tr>
            <th>사용자</th>
            <th className="num" title={IN_HINT}>{IN_LABEL}</th>
            <th className="num" title={AX_CACHE_READ}>캐시읽기</th>
            <th className="num" title={AX_CACHE_CREATE}>캐시생성</th>
            <th className="num">출력</th>
            <th className="num" title={TURNS_HINT}>턴</th>
            <th className="num">세션</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.username}>
              <td className="mono">{r.username}</td>
              <td className="num"><TokenCount v={r.input} /></td>
              <td className="num"><TokenCount v={r.cacheRead} /></td>
              <td className="num"><TokenCount v={r.cacheCreate} /></td>
              <td className="num"><TokenCount v={r.output} /></td>
              <td className="num">{n(r.turns)}</td>
              <td className="num">{n(r.sessions)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </TableWrap>
  );
}

export default function UsageTrackTab() {
  /*
   * 사용자 선택 — **이 탭 안에서만** 산다(전역 스토어에 넣지 않는다). 탭을 떠나면 풀리고,
   * 사용 관측·대시보드는 전사 기준을 유지한다. 그쪽 화면들은 자기 조회·빈 상태 문구를
   * 사람 기준으로 다시 쓰지 않았으므로, 선택을 공유하면 "전사"라고 말하면서 한 사람의 값을
   * 그리게 된다.
   */
  const [user, setUser] = useState('');

  /*
   * 두 조회를 한 로더에 묶는다 — 같은 signal 을 공유하므로 탭을 떠나면 **둘 다** 끊긴다.
   * '사람별 활용'(dispatch)은 fail-soft 다: 실패해도 위의 전체 집계는 그대로 보여야 한다.
   *
   * user 는 summary·dispatch **양쪽에** 같은 값으로 싣는다. 한쪽만 걸면 같은 화면의 두 카드가
   * 서로 다른 모집단을 그리면서 그 사실을 말하지 않는다.
   * (platforms 는 "이 서버에 어떤 플랫폼 데이터가 있나"라 사람 축과 무관하다 — 안 싣는다.)
   */
  /*
   * 의존성으로 쓸 **원시값**을 먼저 뽑는다 — useResource 는 deps 를 문자열로 이어 키를 만들어서
   * 객체를 넣으면 키가 굳고 재조회가 안 된다(lib/scope.ts 의 경고).
   */
  const scope = useScope(user || undefined);
  const { platform, runtime } = scope;

  const load = useCallback(async ({ signal }: { signal: AbortSignal }): Promise<TrackData> => {
    const s = { platform, runtime, user: user || undefined };
    const [summary, dispatch] = await Promise.all([
      getSummary(s, { signal }),
      softly(getDispatch(s, { signal }), null as Dispatch | null),
    ]);
    return { summary, dispatch };
  }, [platform, runtime, user]);

  // 세 축이 키에 들어가므로 선택이 바뀌면 재조회된다(낡은 응답은 훅이 키 대조로 버린다).
  const { state, reload } = useResource(load, [platform, runtime, user]);

  /*
   * 명단(roster) — 셀렉트의 선택지다. **필터가 걸리지 않은 응답만** 근거로 삼는다.
   * 걸린 응답의 byUser 에는 고른 사람만 남으므로, 그것으로 목록을 만들면 한 번 고른 뒤
   * 다른 사람으로 갈아탈 방법이 사라진다.
   */
  /*
   * 명단은 **필터를 절대 싣지 않는 별도 조회**로 받는다(deps 가 [] 이므로 마운트당 한 번).
   * 걸린 summary 의 byUser 로 만들면 한 사람을 고른 순간 목록에 그 사람만 남아 다른 사람으로
   * 갈아탈 방법이 사라진다 — 자기 자신을 좁히는 목록은 만들지 않는다.
   *
   * leaderboard 를 쓰는 이유: "사용량을 보고한 사람"이 곧 이 응답의 users 다(summary 를 한 번
   * 더 부르면 이 화면에서 가장 무거운 질의를 같은 값으로 두 번 받는다).
   * fail-soft 다 — 실패하면 셀렉트만 안 그려지고 전체 기준 화면은 그대로 산다.
   */
  const rosterLoad = useCallback(
    ({ signal }: { signal: AbortSignal }) =>
      softly(getLeaderboard({}, { signal }), null as Leaderboard | null),
    [],
  );
  const rosterRes = useResource(rosterLoad, []);
  const roster = rosterRes.state.status === 'ready'
    ? (rosterRes.state.data?.users ?? [])
        .map((u) => u.username)
        .filter((u): u is string => !!u)
    : [];

  /*
   * 이 화면은 스코프 세 축을 **전부 싣는다**(2026-08-21 배선). 그래서 applies={true} 다.
   *
   * 서버는 처음부터 summary·dispatch 를 platform·runtime 으로 걸렀다(실측: ?platform=codex 로
   * 본문 16,399→4,949B, ?runtime=local 로 5,610B. 오타는 400 이다). 못 하는 쪽은 서버가 아니라
   * 화면이었고, 그 오해의 출처는 "platform 축은 받지 않는다"고 적어 둔 낡은 주석이었다.
   *
   * 예전에 이 배선을 미뤄 둔 단서 하나("추천 공백 축은 못 거른다")는 **해소됐다** —
   * `Summary.recommendation` 은 응답에 오지만 이 화면이 렌더하지 않는다(그리는 컴포넌트가
   * 없다). 걸러지지 않는 값을 화면에 놓고 안 밝히는 상태가 애초에 아니었다.
   *
   * 축 패널은 여전히 **축마다** 어느 플랫폼이 그것을 기록하는지 말한다(AxisExplorer).
   */
  /*
   * 플랫폼 목록은 **따로 싣는다**(deps []). 조회 조건과 무관한 선택지의 원천이기 때문이다.
   *
   * ⚠ 같은 로더에 묶으면 플랫폼을 고를 때마다 목록까지 다시 받아오고, 그동안 rows 가 null 이
   *   되어 **셀렉트가 사라진다.** 그러면 PlatformFilter 의 "목록에 없는 선택은 되돌린다"
   *   이펙트가 걸려 방금 고른 값이 즉시 초기화된다 — 고를 수 없는 필터가 된다.
   *   이 탭이 축을 싣게 된 순간(2026-08-21) 실제로 그 상태가 됐고 테스트가 잡았다.
   *   UsageObsTab 이 같은 이유로 이미 따로 싣고 있다.
   */
  const loadPlatforms = useCallback(
    ({ signal }: { signal: AbortSignal }) => softly(getPlatforms({ signal }), null as PlatformsResponse | null),
    [],
  );
  const platformsRes = useResource(loadPlatforms, []);
  const platformRows = platformsRes.state.status === 'ready'
    ? platformsRes.state.data?.platforms ?? null
    : null;
  // `.pf-bars` 로 감싸 한 줄에 세운다(globals.css 의 `.pf-bars` 주석).
  const bar = (
    <div className="pf-bars">
      <PlatformFilter rows={platformRows} applies what="이 화면의 집계는 이 플랫폼만 셉니다" />
      <RuntimeFilter />
      <UserFilter users={roster} value={user} onChange={setUser} />
    </div>
  );

  /*
   * 로딩·오류에도 **필터 바를 남긴다.** 조회할 때마다 셀렉트가 사라지면 방금 고른 사람을
   * 되돌리거나 다른 사람으로 갈아탈 수 없고, 실패한 선택에 갇힌다.
   */
  if (state.status === 'loading') return <>{bar}<Loading /></>;
  if (state.status === 'error') {
    return <>{bar}<ErrorState what="사용 추적을 불러오지 못했습니다." error={state.error} onRetry={reload} /></>;
  }

  const d = state.data.summary;
  const t = d.totals ?? { sessions: 0, users: 0, machines: 0, input: 0, output: 0, cacheRead: 0, cacheCreate: 0 };
  const empty = !t.sessions;

  return (
    <>
      {bar}
      <p className="lead">
        {user
          ? <><b>{user}</b> 의 사용량입니다 — 아래 모든 수치가 이 사람 기준입니다.{' '}</>
          : <>동기화된 PC 들이 <b>무엇을 얼마나 썼는지</b>를 모읍니다.{' '}</>}
        집계만 수집하며 <b>프롬프트 원문·파일 경로·명령 인자는 저장하지 않습니다.</b>{' '}
        <RetentionNote retention={d.retention} />
      </p>

      <TotalsTiles t={t} />

      {/*
        * 빈 화면의 이유가 둘이다 — 보고가 아예 없는 것과, 고른 사람의 보고가 없는 것.
        * 후자에 설치 안내를 띄우면 "수집기를 갱신하라"는 틀린 처방이 된다(다른 사람의 데이터는
        * 이미 올라와 있다). 그래서 문구를 갈라 쓴다.
        */}
      {empty && user && (
        <Card title={`${user} 의 보고가 없습니다`} className="mt">
          <p className="help">
            이 기간에 <b>{user}</b> 로 귀속된 세션이 없습니다. 사용자를 <b>전체</b>로 되돌리면
            팀 전체 집계를 볼 수 있습니다.
          </p>
          <p className="help mt-sm">
            이름이 실제 담당자와 다르게 올라왔다면 <b>귀속 교정</b>으로 머신을 사람에게 묶습니다.
          </p>
        </Card>
      )}

      {empty && !user && (
        <Card title="아직 보고가 없습니다" className="mt">
          <p className="help">
            팀원 PC 가 <b>수집기를 갱신한 뒤 세션을 한 번 열면</b> 직전 세션들의 집계가 올라옵니다
            (수집기는 하루 1회 자동 갱신되므로 재설치는 필요 없습니다).
          </p>
          <p className="help mt-sm">
            각 PC 에서 끄려면 <span className="mono">TEAM_USAGE_DISABLE=1</span>,
            키워드 축만 빼려면 <span className="mono">TEAM_USAGE_NO_KEYWORDS=1</span> 입니다.
          </p>
        </Card>
      )}

      <div className="mt">

        <Card
          title="사용자별"
          className="mb"
          aside={<span className="aside-row"><span className="help">이름이 실제 담당자와 다르면 <b>귀속 교정</b>(/api/usage/identity)으로 묶습니다</span><PinButton metric="cost" groupBy="user" title="사용자별 API 환산 비용" /></span>}
        >
          <UserTable rows={d.byUser ?? []} />
        </Card>

        <Card
          title="모델별"
          className="mb"
          aside={<span className="aside-row"><span className="help">series 가 있는 세션은 모델별 정확값 · 없는 세션은 세션 최빈 모델 기준</span><PinButton metric="tokens" groupBy="model" title="모델별 토큰" /></span>}
        >
          <ModelTable rows={d.byModel ?? []} axis={d.modelAxis} />
        </Card>

        <AxisExplorer
          top={d.top ?? {}}
          dispatch={state.data.dispatch}
          dispatchFailed={state.data.dispatch === null}
          platforms={(platformRows ?? []).map((p) => p.platform)}
        />
      </div>
    </>
  );
}
