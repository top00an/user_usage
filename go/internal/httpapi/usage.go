package httpapi

import (
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

	ctx := r.Context()
	stored, counters, buckets := 0, 0, 0
	for _, sess := range payload.Sessions {
		/*
		 * 서버 권위 귀속 — 머신 매핑이 걸려 있으면 클라이언트가 보낸 사용자명을 **덮어쓴다.**
		 *
		 * 클라이언트가 보고하는 이름은 기본이 OS 계정명이라 팀 계정명과 어긋날 수 있고, 그것을
		 * 수집기 재배포로 고치는 방식은 반복 비용이 크다. 인테이크가 유일한 진입점이라 여기서
		 * 덮으면 클라이언트 경로가 몇 개든 결과가 하나로 모인다.
		 */
		username := deref(sess.Username)
		machine := deref(sess.Machine)
		if mapped, err := identity.Resolve(ctx, machine, username); err == nil {
			username = mapped
		}

		// 한 세션이 실패해도 나머지는 넣는다 — 텔레메트리가 전부-아니면-전무일 이유가 없다.
		err := store.SessionUpsert(ctx, store.SessionInput{
			SessionID: sess.SessionID,
			Machine:   machine,
			Username:  username,
			Project:   deref(sess.Project),
			Model:     deref(sess.Model),
			Input:     sess.Input, Output: sess.Output,
			CacheRead: sess.CacheRead, CacheCreate: sess.CacheCreate,
			WebSearch: sess.WebSearch, WebFetch: sess.WebFetch, Turns: sess.Turns,
			StartedAt: deref(sess.StartedAt), EndedAt: deref(sess.EndedAt),
			NoTsTurns: sess.NoTsTurns,
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

	writeJSON(w, http.StatusOK, intakeResponse{
		OK: true, Sessions: stored, Counters: counters, Buckets: buckets,
	}, nil)
	return true, nil
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
			CC5m: b.CC5m, CC1h: b.CC1h, Turns: b.Turns,
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

		totals, err := store.Totals(ctx)
		if err != nil {
			return true, err
		}
		byDay, err := store.UsageByDay(ctx, days)
		if err != nil {
			return true, err
		}
		byUser, err := store.UsageByUser(ctx)
		if err != nil {
			return true, err
		}
		byModel, err := store.UsageByModel(ctx)
		if err != nil {
			return true, err
		}
		/*
		 * 모델 축의 근거 커버리지. byModel 행의 fromSeries/fromSession 이 "이 값이 어디서
		 * 왔나"를 말하고, 이쪽이 "누구의 값이 근사인가"를 말한다 — 둘이 붙어야 화면이
		 * 오귀속을 정확한 값으로 위장하지 않는다.
		 */
		modelAxis, err := store.UsageModelAxis(ctx)
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
			rows, err := store.TopKeys(ctx, spec.kind, topN)
			if err != nil {
				return true, err
			}
			*spec.dst = toKeyDTOs(rows)
		}

		gaps, err := store.RecommendationGaps(ctx, topN)
		if err != nil {
			return true, err
		}
		reco, err := store.RecommendationSummary(ctx)
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
