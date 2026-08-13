package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/tscorp/user-usage/internal/identity"
	"github.com/tscorp/user-usage/internal/intake"
	"github.com/tscorp/user-usage/internal/store"
)

/*
 * routes/usage.js 의 포팅 — `/api/usage` 접두사를 통째로 소유한다(안 걸리면 404 를 직접 낸다).
 *
 * 두 계층으로 갈린다:
 *   PUBLIC  POST /api/usage          팀원 PC 수집기가 보고. 자체 게이트를 진다.
 *   AUTHED  GET  /api/usage/summary  관제 화면이 읽는다.
 *
 * 왜 조회에 admin 을 요구하나: 이 데이터는 **사람별 사용량**이다. 누가 얼마나 썼는지가 보이므로
 * 열람 권한을 넓히지 않는다.
 */

// bodyLimit — 본문 상한. 없으면 한 요청이 프로세스 메모리를 통째로 먹을 수 있다.
const bodyLimit = 5_000_000

// actor 는 감사 로그에 남는 주체다. 이 도구의 사용자는 토큰을 가진 한 사람이다 —
// 역할 체계를 여기서 흉내내지 않는다(조회 도구에 인증 체계를 두 벌 만들면 한 벌만 고쳐진다).
const actor = "usage-admin"

type intakeResponse struct {
	OK       bool `json:"ok"`
	Sessions int  `json:"sessions"`
	Counters int  `json:"counters"`
	Buckets  int  `json:"buckets"`
}

/*
 * PUBLIC — 팀원 PC 보고 인테이크.
 *
 * 등록 판정은 이미 호스트(server.go 의 게이트)가 끝냈다: 여기 닿는 요청은 admin 또는 intake
 * 스코프이고, **브라우저 세션은 근거가 아니다.** 이건 쓰기 경로라 세션을 인정하면 로그인한
 * 사람의 브라우저를 꾀어 임의 사용량을 밀어 넣을 자리가 된다.
 */
func (s *server) routeIntake(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	if r.Method != http.MethodPost || c.path != "/api/usage" {
		return false, nil
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil || len(raw) > bodyLimit {
		// 본문을 못 읽은 것은 보고자 쪽 문제다. 인테이크는 절대 죽지 않는다 —
		// 빈 페이로드로 접어 0건 응답을 낸다(현행 JS 의 `catch { body = null }` 와 같다).
		raw = nil
	}
	// NormPayload 는 깨진 JSON 에 error 를 내지만, 인테이크는 그것을 사고로 취급하지 않는다.
	// 수집기는 best-effort 경로라 여기서 400 을 내면 그 사람 사용량이 통째로 사라진다.
	payload, _ := intake.NormPayload(raw)

	/*
	 * 계단 분리분의 불변식(0 <= long <= 총량) 위반은 **조용히 넘기지 않는다.**
	 *
	 * 인테이크가 이미 접어서 저장은 안전하지만, 접었다는 사실이 어디에도 안 남으면 수집기의
	 * 계산 버그가 서버에서 정상값으로 둔갑한다 — 그 뒤로는 비용이 틀렸다는 것을 아무도 모른다.
	 * 400 을 내지는 않는다(인테이크는 best-effort 경로다). 로그가 그 사실의 유일한 흔적이다.
	 */
	if payload.LongClamped > 0 {
		logf("인테이크 롱컨텍스트 불변식 위반 %d건 — 총량을 넘거나 음수인 분리분을 접었다(세션 %d건)",
			payload.LongClamped, len(payload.Sessions))
	}

	resp := s.storeSessions(r.Context(), payload.Sessions, c.keyUser)
	writeJSON(w, http.StatusOK, resp, nil)
	return true, nil
}

/*
 * storeSessions 는 정규화된 세션들을 store 에 쓴다(서버 권위 귀속·멱등 포함). 퍼스트파티
 * 인테이크(`POST /api/usage`)가 유일한 호출부다 — 수집 경로가 하나라 저장 규율도 한 벌이다.
 *
 * keyUser 는 **이 요청을 인증한 인제스트 키에 묶인 사용자**다(게이트가 정한다). 비어 있지
 * 않으면 귀속에서 무조건 이긴다 — 아래 주석 참조.
 */
func (s *server) storeSessions(ctx context.Context, sessions []intake.Session, keyUser string) intakeResponse {
	stored, counters, buckets := 0, 0, 0
	for _, sess := range sessions {
		/*
		 * 서버 권위 귀속 — 우선순위가 계약이다(동결 ①):
		 *
		 *   ① 키에 묶인 username   ← 있으면 무조건 이긴다
		 *   ② machine_identity 매핑 ← 관리자가 손으로 고친 값
		 *   ③ payload.user          ← 클라이언트 주장(최후)
		 *
		 * ①이 ②를 이기는 근거: ①은 "그 사용자에게 발급된 키를 실제로 갖고 있음"이 증명된
		 * 사실이고, ②는 사용자에 묶이지 않은 키(레거시·org 공용)를 관리자가 뒤늦게 교정하는
		 * 수단이다. 증명된 사실이 교정보다 약할 이유가 없다. **①이 설 때 ②는 아예 타지 않는다** —
		 * 매핑을 조용히 덮어쓰는 것이 아니라 애초에 조회하지 않는다.
		 *
		 * 하위호환: username 이 없는 기존 키는 keyUser 가 빈 문자열이라 종전대로 ②→③ 을 탄다.
		 * 클라이언트가 보고하는 이름은 기본이 OS 계정명이라 팀 계정명과 어긋날 수 있고, 그것을
		 * 수집기 재배포로 고치는 방식은 반복 비용이 크다 — ②가 그 자리다.
		 */
		username := deref(sess.Username)
		machine := deref(sess.Machine)
		if keyUser != "" {
			username = keyUser
		} else if mapped, err := identity.Resolve(ctx, machine, username); err == nil {
			username = mapped
		}

		// 한 세션이 실패해도 나머지는 넣는다 — 텔레메트리가 전부-아니면-전무일 이유가 없다.
		err := store.SessionUpsert(ctx, store.SessionInput{
			SessionID: sess.SessionID,
			Machine:   machine,
			Username:  username,
			Project:   deref(sess.Project),
			Model:     deref(sess.Model),
			// 선택적 필드다 — 안 보내면 빈 값이고, 저장 계층이 claude 로 채운다
			// (허용목록 밖은 other). 그 규칙의 단일 출처는 store.NormalizePlatform 이다.
			Platform: deref(sess.Platform),
			Input:    sess.Input, Output: sess.Output,
			CacheRead: sess.CacheRead, CacheCreate: sess.CacheCreate,
			// 계단 분리분 — 없으면 0 이고 그것이 현행 동작이다(전부 표준 구간).
			InputLong: sess.InputLong, OutputLong: sess.OutputLong,
			CacheReadLong: sess.CacheReadLong,
			WebSearch:     sess.WebSearch, WebFetch: sess.WebFetch, Turns: sess.Turns,
			StartedAt: deref(sess.StartedAt), EndedAt: deref(sess.EndedAt),
			NoTsTurns:     sess.NoTsTurns,
			LinesAdded:    sess.LinesAdded,
			LinesRemoved:  sess.LinesRemoved,
			EditsAccepted: sess.EditsAccepted,
			EditsRejected: sess.EditsRejected,
		})
		switch {
		case err == nil:
			stored++
		case errors.Is(err, store.ErrEmptySessionID):
			// 세션 id 가 없으면 저장할 자리가 없다. 세지 않되 나머지 축은 계속 시도한다
			// (현행 JS 의 sessionUpsert → false 경로와 같다).
		default:
			logf("인테이크 세션 저장 실패(%s): %v", sess.SessionID, err)
			continue
		}

		n, err := store.CountersUpsertN(ctx, store.CountersInput{
			SessionID: sess.SessionID, Username: username, Machine: machine,
			StartedAt: deref(sess.StartedAt),
			Rows:      toCounterRows(sess.Counters),
		})
		if err != nil {
			logf("인테이크 카운터 저장 실패(%s): %v", sess.SessionID, err)
			continue
		}
		counters += n

		/*
		 * 시간 버킷은 신버전 수집기만 보낸다. 없으면 아무 일도 일어나지 않는다 —
		 * 구버전 수집기를 쓰는 팀원의 보고가 거절되면 그동안 그 사람 사용량이 통째로 사라진다.
		 * 귀속(username)은 위에서 서버가 정한 값을 쓴다. 세션 행과 갈라지면 안 된다.
		 */
		if len(sess.Series) > 0 {
			nb, err := store.SeriesUpsertN(ctx, store.SeriesInput{
				SessionID: sess.SessionID, Username: username, Machine: machine,
				Project: deref(sess.Project),
				Rows:    toSeriesRows(sess.Series),
			})
			if err != nil {
				logf("인테이크 버킷 저장 실패(%s): %v", sess.SessionID, err)
				continue
			}
			buckets += nb
		}
	}

	return intakeResponse{OK: true, Sessions: stored, Counters: counters, Buckets: buckets}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func toCounterRows(cs []intake.Counter) []store.CounterRow {
	out := make([]store.CounterRow, 0, len(cs))
	for _, c := range cs {
		out = append(out, store.CounterRow{Kind: c.Kind, Key: c.Key, Count: c.Count})
	}
	return out
}

func toSeriesRows(bs []intake.Bucket) []store.SeriesRow {
	out := make([]store.SeriesRow, 0, len(bs))
	for _, b := range bs {
		out = append(out, store.SeriesRow{
			Hour: b.Hour, Model: b.Model,
			Input: b.Input, Output: b.Output,
			CacheRead: b.CacheRead, CacheCreate: b.CacheCreate,
			InputLong: b.InputLong, OutputLong: b.OutputLong,
			CacheReadLong: b.CacheReadLong,
			CC5m:          b.CC5m, CC1h: b.CC1h, Turns: b.Turns,
			ToolErrors: b.ToolErrors, StopMaxTokens: b.StopMaxTokens, StopRefusal: b.StopRefusal,
			LatencyMsSum: b.LatencyMsSum, LatencyMsMax: b.LatencyMsMax, LatencyTurns: b.LatencyTurns,
		})
	}
	return out
}

/* ── AUTHED — 관제 화면 ──────────────────────────────────────────────── */

type summaryTopDTO struct {
	Tool    []keyDTO `json:"tool"`
	Bash    []keyDTO `json:"bash"`
	Slash   []keyDTO `json:"slash"`
	Skill   []keyDTO `json:"skill"`
	Agent   []keyDTO `json:"agent"`
	MCP     []keyDTO `json:"mcp"`
	Keyword []keyDTO `json:"keyword"`
}

type summaryResponse struct {
	Totals         totalsDTO         `json:"totals"`
	ByDay          []dayDTO          `json:"byDay"`
	ByUser         []userDTO         `json:"byUser"`
	ByModel        []modelDTO        `json:"byModel"`
	ModelAxis      modelAxisDTO      `json:"modelAxis"`
	Top            summaryTopDTO     `json:"top"`
	Recommendation recommendationDTO `json:"recommendation"`
	Retention      retentionDTO      `json:"retention"`
}

type identityListResponse struct {
	Items    []mappingDTO  `json:"items"`
	Unmapped []unmappedDTO `json:"unmapped"`
}

type identitySetResponse struct {
	OK       bool     `json:"ok"`
	Machine  string   `json:"machine"`
	Username string   `json:"username"`
	Moved    movedDTO `json:"moved"`
}

type identityRemoveResponse struct {
	OK bool `json:"ok"`
}

type identitySetRequest struct {
	Machine  string `json:"machine"`
	Username string `json:"username"`
	Note     string `json:"note"`
}

/*
 * routeAdmin — `/api/usage` 접두사의 주인이다. **안 걸리면 404 를 직접 낸다**(다른 모듈로
 * 흘려보내지 않는다). 그래서 라우트 체인에서 analytics 보다 **뒤**에 와야 한다 — 앞에 두면
 * 관측 화면이 통째로 404 가 된다.
 */
func (s *server) routeAdmin(w http.ResponseWriter, r *http.Request, c *rctx) (bool, error) {
	if !hasPrefix(c.path, "/api/usage") {
		return false, nil
	}
	ctx := r.Context()

	if r.Method == http.MethodGet && c.path == "/api/usage/summary" {
		days := int(numOr(c.query.Get("days"), 30))
		topN := int(numOr(c.query.Get("top"), 20))

		/*
		 * platform 필터 — 판정 규칙은 analytics.go 의 platformParam 이 단일 출처다
		 * (이 라우트만 다른 파일에 있을 뿐 계약은 같다). 미지정이면 조건을 걸지 않는다.
		 *
		 * ⚠ 이 화면의 카드들은 **서로 다른 표**에서 온다(세션·시간 버킷·카운터). 하나라도
		 *   안 걸면 같은 화면 안에서 두 카드가 서로 다른 플랫폼을 그리고, 그 사실이 어디에도
		 *   표시되지 않는다. 그래서 아래는 전부 …WithFilter 로 간다.
		 */
		platform, ok := platformParam(w, c)
		if !ok {
			return true, nil
		}
		/*
		 * user 필터 — 사용 추적 화면이 "이 사람만" 볼 때 싣는다. 파라미터 이름은 이미 쓰이는
		 * `user` 로 맞춘다(analytics.go 의 sessions·platforms 갈래와 한 벌).
		 *
		 * platform 과 달리 **허용목록이 없다.** 사용자명은 자유 문자열이라 400 을 낼 근거가
		 * 없고, 없는 이름은 빈 집계로 돌아온다(그것이 사실이다 — 그 사람의 보고가 없다).
		 * 화면은 선택지를 응답의 byUser 에서만 만들고, 목록에 없는 선택은 전체로 되돌린다.
		 *
		 * 미지정이면 조건이 하나도 안 붙는다 = 현행과 같은 SQL·같은 응답(골든 44개 무회귀).
		 */
		f := store.Filter{Platform: platform, Username: c.query.Get("user")}

		totals, err := store.TotalsWithFilter(ctx, f)
		if err != nil {
			return true, err
		}
		byDay, err := store.UsageByDayWithFilter(ctx, days, f)
		if err != nil {
			return true, err
		}
		byUser, err := store.UsageByUserWithFilter(ctx, f)
		if err != nil {
			return true, err
		}
		byModel, err := store.UsageByModelWithFilter(ctx, f)
		if err != nil {
			return true, err
		}
		/*
		 * 모델 축의 근거 커버리지. byModel 행의 fromSeries/fromSession 이 "이 값이 어디서
		 * 왔나"를 말하고, 이쪽이 "누구의 값이 근사인가"를 말한다 — 둘이 붙어야 화면이
		 * 오귀속을 정확한 값으로 위장하지 않는다.
		 */
		modelAxis, err := store.UsageModelAxisWithFilter(ctx, f)
		if err != nil {
			return true, err
		}

		top := summaryTopDTO{}
		for _, spec := range []struct {
			kind string
			dst  *[]keyDTO
		}{
			{"tool", &top.Tool}, {"bash", &top.Bash}, {"slash", &top.Slash},
			{"skill", &top.Skill}, {"agent", &top.Agent}, {"mcp", &top.MCP},
			{"keyword", &top.Keyword},
		} {
			rows, err := store.TopKeysWithFilter(ctx, spec.kind, topN, f)
			if err != nil {
				return true, err
			}
			*spec.dst = toKeyDTOs(rows)
		}

		/*
		 * ⚠ 추천 관측(usage_recommendations)은 **플랫폼 축이 없다.** 세션에 매달려 있지 않고
		 * (session_id 컬럼이 없다) 목표 문장·점수만 남는 별도 경로라, 어느 도구에서 나온
		 * 호출인지 되짚을 근거가 이 표에 존재하지 않는다.
		 *
		 * 그래서 플랫폼 필터는 걸지 않는다. 빈 값으로 접지도 않는다 — 그러면 "codex 에서는 추천
		 * 공백이 없었다"는 **없는 사실**을 만들어 낸다. 축이 없는 것과 값이 0 인 것은 다르다.
		 * (응답 shape 를 못 바꾸므로 이 한계는 코드와 보고에만 남는다 — 잔여 항목.)
		 *
		 * **사용자 축은 다르다.** 이 표에는 username 컬럼이 있어서 사람으로는 정직하게 걸린다 —
		 * f 를 그대로 넘기고, store 쪽이 자기가 볼 수 있는 축(Username)만 집어 쓴다.
		 */
		gaps, err := store.RecommendationGapsAtWithFilter(ctx, 1, topN, f)
		if err != nil {
			return true, err
		}
		reco, err := store.RecommendationSummaryWithFilter(ctx, f)
		if err != nil {
			return true, err
		}

		writeJSON(w, http.StatusOK, summaryResponse{
			Totals: toTotalsDTO(totals), ByDay: toDayDTOs(byDay), ByUser: toUserDTOs(byUser),
			ByModel: toModelDTOs(byModel), ModelAxis: toModelAxisDTO(modelAxis),
			Top: top,
			Recommendation: recommendationDTO{
				Total: reco.Total, Miss: reco.Miss, Gaps: toGapDTOs(gaps),
			},
			/*
			 * 보존 정책을 응답에 실어 화면이 말하게 한다 — 데이터가 언젠가 사라진다는 사실은
			 * 보는 사람이 알아야 하고(추세가 끊기는 이유), 팀에게도 공개돼야 하는 약속이다.
			 */
			Retention: retentionDTO{KeywordDays: s.cfg.KeywordRetentionDays},
		}, nil)
		return true, nil
	}

	/*
	 * 머신 → 계정 매핑 관리. 사람별 사용량을 다루는 화면과 같은 권한 경계를 쓴다 —
	 * 귀속을 바꾸는 일이라 더 낮출 이유가 없다.
	 */
	if c.path == "/api/usage/identity" {
		switch r.Method {
		case http.MethodGet:
			items, err := identity.List(ctx)
			if err != nil {
				return true, err
			}
			pending, err := identity.Unmapped(ctx)
			if err != nil {
				return true, err
			}
			writeJSON(w, http.StatusOK, identityListResponse{
				Items: toMappingDTOs(items), Unmapped: toUnmappedDTOs(pending),
			}, nil)
			return true, nil

		case http.MethodPut, http.MethodPost:
			var body identitySetRequest
			// 본문이 깨졌으면 빈 값으로 간다 — identity.Set 이 그것을 "machine 이 필요하다"로
			// 거부하고, 그 문구가 사용자가 고칠 수 있는 안내다(JSON 파싱 원문보다 낫다).
			_ = decodeJSONBody(r, &body)
			res, err := identity.Set(ctx, identity.SetInput{
				Machine: body.Machine, Username: body.Username, Note: body.Note, Actor: actor,
			})
			if err != nil {
				// 의도한 400 이다 — 검증 메시지는 그대로 남긴다(failRequest 경로가 아니다).
				sendError(w, http.StatusBadRequest, err.Error())
				return true, nil
			}
			// 감사 기록은 오류를 전파하지 않는다 — 기록 실패가 귀속 교정을 되돌리면
			// 사람이 그 기능을 피하게 된다.
			identity.AuditLog(ctx, actor, "usage.identity.set", res.Machine, map[string]any{
				"username": res.Username,
				"moved":    map[string]int{"sessions": res.Moved.Sessions, "counters": res.Moved.Counters},
			})
			writeJSON(w, http.StatusOK, identitySetResponse{
				OK: true, Machine: res.Machine, Username: res.Username,
				Moved: movedDTO{Sessions: res.Moved.Sessions, Counters: res.Moved.Counters},
			}, nil)
			return true, nil

		case http.MethodDelete:
			machine := c.query.Get("machine")
			ok, err := identity.Remove(ctx, machine)
			if err != nil {
				return true, err
			}
			if ok {
				identity.AuditLog(ctx, actor, "usage.identity.remove", machine, map[string]any{})
			}
			writeJSON(w, http.StatusOK, identityRemoveResponse{OK: ok}, nil)
			return true, nil
		}
	}

	// 접두사를 소유하므로 여기까지 오면 404 를 직접 낸다.
	sendError(w, http.StatusNotFound, "not found")
	return true, nil
}
