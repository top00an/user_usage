# 사용량 대시보드 — Go 단일 바이너리 이미지.
#
# 최종 이미지에는 **Node 런타임이 없다.** Next.js 는 빌드 타임에만 존재하고, 그 산출물은
# go:embed 로 바이너리 안에 박혀 나간다(scripts/build.sh ②③). 런타임에 남는 것은
# 정적 링크된 실행 파일 하나 + tini + busybox 뿐이다.
#
#   docker build -t user-usage:local .
#   docker run --rm -p 127.0.0.1:4191:4191 --env-file .env -v usage-data:/data user-usage:local
#
# ── 왜 빌더가 하나인가 ───────────────────────────────────────────────────────
# "stage1 node 로 web 빌드 → stage2 golang 으로 go build" 로 쪼개면 **webroot 동기화를 여기서
# 다시 구현하게 된다.** scripts/build.sh 는 그 동기화가 사람 손을 타면 반드시 어긋난다는 이유로
# 만들어진 유일한 빌드 경로이고(그 파일 주석 참조), Dockerfile 이 두 번째 구현이 되는 순간
# "로컬 빌드와 이미지 빌드가 다르다"는 가장 찾기 힘든 종류의 사고가 생긴다.
#
# 그래서 빌더는 하나이고, 거기에 **두 툴체인을 다 넣은 뒤 build.sh 를 그대로 호출한다.**
# SKIP_WEB 은 쓰지 않는다 — web 은 이 빌드 안에서 소스로부터 새로 만들어져야 한다.
# 레이어 캐시는 스테이지를 쪼개는 대신 COPY 순서(의존성 먼저, 소스 나중)로 얻는다.

# ── 툴체인 고정 ─────────────────────────────────────────────────────────────
# go.mod 는 go 1.25.0 을 요구한다. 아래 GOTOOLCHAIN=local 과 짝이다 — 태그를 올릴 때 둘을
# 같이 본다.
FROM golang:1.25-alpine AS gotoolchain

# ── ① 빌더 ──────────────────────────────────────────────────────────────────
FROM node:22-alpine AS builder

# Go 를 이미지로 가져온다. `apk add go` 를 쓰지 않는 이유는 alpine 저장소의 go 버전이 base
# 이미지 갱신에 따라 조용히 움직이기 때문이다 — go.mod 의 요구 버전과 어긋나는 날이 온다.
COPY --from=gotoolchain /usr/local/go /usr/local/go

# build.sh 는 bash 스크립트다(`set -euo pipefail`, `[[ ]]`). alpine 의 기본 셸은 ash 라 없다.
RUN apk add --no-cache bash

ENV PATH=/usr/local/go/bin:$PATH \
    # 요구 버전이 안 맞을 때 go 가 네트워크에서 다른 툴체인을 조용히 받아오는 것을 막는다.
    # 받아오면 "빌드는 됐는데 어느 컴파일러로 됐는지 모르는" 산출물이 나온다.
    GOTOOLCHAIN=local \
    # 정적 링크. 최종 이미지에 libc 를 맞춰 줄 필요가 없어지고, 런타임 base 를 바꿔도 깨지지
    # 않는다. 이 프로젝트의 sqlite(modernc)·pg(pgx)는 둘 다 순수 Go 라 잃는 것이 없다.
    CGO_ENABLED=0 \
    # 빌드 경로가 바이너리에 박히지 않게 한다(재현성 + 빌더 경로 노출 방지).
    GOFLAGS=-trimpath \
    NEXT_TELEMETRY_DISABLED=1 \
    NODE_ENV=production

WORKDIR /src

# ── 의존성 레이어를 먼저 굳힌다 ─────────────────────────────────────────────
# 소스만 바뀌는 대부분의 빌드에서 npm ci 와 go mod download 는 캐시에서 재사용된다.
# build.sh 는 web/node_modules 가 이미 있으면 npm ci 를 건너뛴다 — 그래서 이 순서가 먹는다.
# (.dockerignore 가 web/node_modules 를 빼 두지 않으면 아래 `COPY . .` 가 이 레이어를
#  호스트의 node_modules 로 덮어써 버린다. 두 파일은 한 쌍이다.)
COPY web/package.json web/package-lock.json ./web/
RUN cd web && npm ci

COPY go/go.mod go/go.sum ./go/
RUN cd go && go mod download

# ── 소스 + 유일한 빌드 경로 ─────────────────────────────────────────────────
COPY . .

# next build → web/out → webroot/ 동기화(먼저 비움) → go build.
# 이 한 줄이 로컬에서 사람이 치는 것과 **같은 명령**이라는 점이 이 Dockerfile 의 요점이다.
RUN bash scripts/build.sh

# ── ② 런타임 ────────────────────────────────────────────────────────────────
# alpine 을 쓴다(distroless/static 이 아니라). 이유는 하나뿐이다: **HEALTHCHECK.**
# 이 바이너리에는 healthcheck 서브커맨드가 없고(cmd/usage-server 는 인자를 읽지 않는다),
# distroless 에는 HTTP 를 두드릴 실행 파일이 하나도 없다. busybox 의 wget 이 그 자리를 메운다.
# 대가는 이미지에 셸이 남는다는 것 — read_only + no-new-privileges + 비루트로 상쇄한다
# (docker-compose.yml). 나중에 헬스체크를 오케스트레이터 쪽으로 올리면 이 스테이지의 base 만
# gcr.io/distroless/static 으로 바꾸면 된다. 바이너리가 정적이라 그 교체는 한 줄이다.
FROM alpine:3.22

# tini — PID 1 로 도는 프로세스는 기본 시그널 처리가 없다. main.go 가 SIGTERM/SIGINT 를
# 명시적으로 잡고 있어서 Go 바이너리는 tini 없이도 종료가 도는 편이지만, 그 성립 조건이
# "애플리케이션 코드가 계속 그렇게 되어 있는 것"에 달려 있다. 20KB 로 그 의존을 없앤다.
RUN apk add --no-cache tini

# 비루트. uid/gid 를 **숫자로 고정한다** — 이름만 쓰면 base 이미지가 바뀔 때 번호가 움직이고,
# 그러면 기존 /data 볼륨의 파일 소유자가 안 맞아 sqlite 가 열리지 않는다(증상: 기동 실패).
RUN addgroup -g 10001 -S usage \
 && adduser -u 10001 -S -G usage -H -s /sbin/nologin usage

# 데이터 디렉터리. **볼륨으로 마운트하지 않으면 컨테이너와 함께 사라진다** —
# local(sqlite) 모드로 쓸 거라면 반드시 마운트한다(docker-compose.yml 이 그렇게 해 둔다).
# remote 모드는 여기에 쓰지 않는다(읽기 전용).
RUN mkdir -p /data && chown 10001:10001 /data

COPY --from=builder --chown=root:root --chmod=0555 /src/go/usage-server /usr/local/bin/usage-server

# 빌드 타임 스모크 — **바이너리가 이 이미지 안에서 실제로 실행되는가.**
# 무설정으로 돌리면 config 게이트가 거부하며 exit 2 로 나간다(go/internal/config/config.go).
# 즉 2 가 나왔다는 것은 로더가 붙고 Go 런타임이 올라와 main 까지 갔다는 뜻이다 — 정적 링크가
# 깨졌거나 아키텍처가 어긋났으면 여기서 127/126 이 나며 빌드가 선다.
# 이 검사가 없으면 같은 사고가 `docker run` 시점에 "no such file or directory" 로 나타나는데,
# 그 메시지는 파일이 없다고 말하므로 아무도 링크를 의심하지 않는다.
RUN /usr/local/bin/usage-server >/dev/null 2>&1; \
    [ $? -eq 2 ] || { echo "usage-server 가 이 런타임에서 실행되지 않는다"; exit 1; }

ENV USAGE_DATA_DIR=/data \
    # 컨테이너 안에서 127.0.0.1 바인드는 곧 "아무도 못 붙음"이다. 노출 범위는 이미지가 아니라
    # 포트 매핑(-p 127.0.0.1:4191:4191)이 정한다 — docker-compose.yml 참조.
    USAGE_HOST=0.0.0.0 \
    USAGE_PORT=4191 \
    # read_only 파일시스템에서 HOME 이 없는 곳을 가리키면 임시 파일을 쓰는 라이브러리가
    # 조용히 실패한다. tmpfs 로 마운트되는 /tmp 로 보낸다.
    HOME=/tmp \
    TMPDIR=/tmp

EXPOSE 4191

USER 10001:10001

# /healthz 는 무인증·무DB 경로다(게이트 **위**에 있다 — go/internal/httpapi/server.go).
# 그래서 토큰 없이 프로브할 수 있고, DB 가 죽어도 "프로세스는 살아 있다"를 정확히 답한다.
#
# shell form 을 쓴다 — exec form 은 ${USAGE_PORT} 를 확장하지 못해 포트를 바꾸면 헬스체크만
# 조용히 옛 포트를 두드린다(컨테이너가 unhealthy 로 뜨는데 서비스는 멀쩡한 상태).
# docker-compose.yml 에 같은 프로브가 한 벌 더 있다 — **한쪽을 고치면 다른 쪽도 고쳐라.**
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -T 3 -O /dev/null "http://127.0.0.1:${USAGE_PORT:-4191}/healthz" || exit 1

ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/usage-server"]
