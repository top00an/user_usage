---
type: hub
tags: [원천, 메타]
updated: 2026-08-12
sources: ["README.md", "PORT-STATUS.md", "docs/", "go/CONTRACT.md", "contract/README.md"]
---

# 원천 — 무엇이 무엇의 단일 출처인가

이 위키가 읽는 원천의 목록. **각 원천이 무엇을 *소유*하는지**를 적는다 — 같은 사실이 두
문서에 적히면 한쪽만 고쳐지는 날이 오기 때문이다. 이 레포는 그 규율을 실제로 지키고 있고,
위키도 그것을 깨지 않는다([[CLAUDE|스키마]] 규칙 4).

## 문서 원천

| 문서 | 소유하는 것 | 성격 |
|---|---|---|
| `README.md` | **도구의 성격 · 데이터 정책 · API 계약 · 화면 구성** | 현행 사실 |
| `docs/OPERATIONS.md` | **운영 절차** — 기동 env·최초 관리자·키·설치/제거·문제해결·되돌리기·cleanup·사용자 관리 | 현행 사실 (전부 실측) |
| `docs/VERIFICATION.md` | **실측 검증 기록** — E2E·멀티테넌트 격리·설치기 | 실측 기록 |
| `go/CONTRACT.md` | **Go 패키지 경계와 시그니처** + 개정 이력 3건 | 역사 문서 + 유효 계약 |
| `contract/README.md` | **골든이 무엇을 대조하는가 · 시드 8세션의 함정** | 현행 사실 |
| `web/README.md` | **프런트 빌드·서빙·검증 · 근사를 말하는 6곳** | 현행 사실 |
| `deploy/README.md` | **AWS Terraform 배포 절차** | 현행 사실 |
| `PORT-STATUS.md` | **Node→Go 컷오버 경위와 잔여 리스크 9건** | 이력 + 열린 리스크 |
| `docs/PLAN-saas-ingestion.md` | SaaS 인제스트 **설계 의도**(+ §0 구현 현황) | 기획서 (미래형 서술 주의) |
| `docs/PLAN-phase1-multitenant.md` | Phase 1 **구현계획**(+ 슬라이스별 현황) | 계획서 (미래형 서술 주의) |
| `.env.example` | 환경변수 상세 설명 | 현행 사실 |
| `.github/workflows/ci.yml` | **상시 게이트** | 현행 사실 |

⚠ **기획서·계획서 두 개는 미래형으로 쓰여 있다.** 각 문서 머리에 "본문의 미래형 서술을
현행 사실로 읽지 말 것"이 명시돼 있고, §0 / 슬라이스 표가 실제 현황이다. 위키는 그 표만
사실로 취급한다.

## 코드 원천 — 주석이 단일 출처인 자리

이 레포는 결정의 근거를 **코드 주석에** 둔 곳이 여럿이다. 문서보다 코드가 이기는 자리다.

| 사실 | 단일 출처 |
|---|---|
| 시크릿·PII 판정 규칙 | `go/internal/intake/intake.go` 의 `safeKeyword` |
| 자리표시자 모델 판정 | `go/internal/intake/intake.go` 의 `placeholderModelRe` |
| `usage_series` 를 프루닝하지 않는 이유 | `go/internal/store` 의 주석 |
| 비용 라벨 문구 | `web/lib/costLabels.ts` |
| 골든이 접는 시점 의존 필드 | `contract/harness.mjs` 의 `VOLATILE` |
| 수집기 정책 상한이 서버와 같아야 함 | `collector/internal/policy/policy.go` + `drift_test.go` |
| 공급사별 토큰 정규화 책임 | `go/internal/cost/cost.go` 패키지 주석 |
| Antigravity 에 토큰이 statusLine 밖에 없다는 실측 | `collector/internal/antigravity/antigravity.go` 패키지 주석 |

## 원천의 물리적 위치

```
README.md  PORT-STATUS.md  .env.example  config.example.json
docs/          OPERATIONS.md  VERIFICATION.md  PLAN-*.md
go/            CONTRACT.md + cmd/ + internal/{httpapi,store,intake,cost,db,org,identity,stats,tz,tenant,config}
collector/     cmd/ + internal/{transcript,codex,gemini,antigravity,policy,payload,sender,state}
web/           README.md CLAUDE.md AGENTS.md + app/ components/ lib/ hooks/ test/
contract/      README.md + harness.mjs + fixtures.mjs + run.mjs + golden/(44개, 동결)
migrations/pg/ 0014 … 0038 (번호 공백은 의도)
deploy/        README.md + aws/(Terraform) + hooks/
scripts/       build.sh install.sh smoke.sh
.github/workflows/ci.yml
```

## 위키가 읽지 않는 것

- `go/internal/httpapi/webroot/` · `web/out/` · `web/.next/` — 빌드 산출물([[webroot-embed]])
- `data/` · `data-dev/` · `data-dev.bak-*` — 런타임 데이터
- `web/.verify/*.png` — 검증 스크린샷 (필요하면 개별로 본다)
- `node_modules/`
