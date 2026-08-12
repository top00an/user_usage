---
type: decision
tags: [이력, otlp, 아키텍처]
updated: 2026-08-12
sources: ["docs/PLAN-saas-ingestion.md", "README.md", "docs/PLAN-phase1-multitenant.md"]
---

# 결정 — OTLP 경로 제거 (2026-08-10, PR #16)

## 무엇을 지웠나

| 제거된 것 |
|---|
| `POST /v1/logs` 수신구 (OTel Collector/SDK 호환) |
| `GET /api/usage/export/otlp` (역방향 export) |
| `go/internal/otlp` 패키지 |
| `docs/SPEC-otlp-claude.md` |

## 왜

**공식 Claude Code OTel 규약은 `claude_code.*` 인데, 이 레포가 만든 것은 `claude.*` 였다.**

표준 속성으로 담을 수 없는 것이 있었기 때문이다:

- `cacheRead` · `cacheCreate` · `cc1h`(ephemeral_1h)
- 7축(tool · bash · slash · skill · agent · mcp · keyword)

표준은 `gen_ai.usage.input_tokens`·`output_tokens`·`gen_ai.request.model` 까지만 준다.
나머지를 `claude.*` 네임스페이스로 확장했고, **그 확장이 비호환의 원인이 됐다.**

결과: **"표준처럼 보이지만 표준이 아닌" 반쪽 자산.** 표준 파이프라인 호환이라는 원래 목적을
달성하지 못하는데, 유지 비용은 그대로 든다.

## 리스크가 현실화된 사례

`docs/PLAN-saas-ingestion.md` §8 의 리스크 목록에 **"OTLP 매핑의 표현력"** 이 처음부터 열려
있었다. 그 리스크가 그대로 실현됐고, 문서가 그것을 숨기지 않고 기록한다:

> **이 리스크가 현실화되어 OTLP 를 제거했다(2026-08-10).** 캐시·cc1h·7축을 표준 속성으로 못
> 담아 `claude.*` 확장이 필요했고, 그 확장이 공식 `claude_code.*` 와 어긋나 "표준 호환"이라는
> 목적 자체가 성립하지 않았다.

## 대신 무엇을 했나

**퍼스트파티 수집기 단일 경로 + 멀티플랫폼 확장.**

원안은 인제스트 표면을 둘로 두려 했다 — "표준 관측 파이프라인(OTel)을 쓰는 고객과 아무것도
없는 고객 둘 다". 제거 후 **온보딩 경로가 하나가 됐다**: 원커맨드 설치기([[installer]]).

> **결론: 퍼스트파티 경로가 1급이 아니라 유일하다.**

Phase 2 의 목표가 "OTLP 호환"에서 **"멀티플랫폼 수집"** 으로 바뀌었고,
**지금 4개 플랫폼이 그 결과다** → [[platform-coverage]].

## 이 결정의 성질

**되돌리기 어렵지 않다** — 제거된 것은 표면이지 데이터가 아니고, 필요해지면 공식
`claude_code.*` 규약에 맞춰 다시 만들면 된다. 다만 그때는 "우리 규약"이 아니라 **공식 규약을
따라야** 한다는 것이 이 결정의 교훈이다.

문서에서 관련 서술은 **지우지 않고 이력으로 남겼다**(`PLAN-saas-ingestion.md` §3.1·§4·§7·§8).
왜 그런 시도를 했는지 알 수 있어야 하기 때문이다.

## 관련

[[platform-coverage]] · [[collector]] · [[installer]] · [[overview]]
