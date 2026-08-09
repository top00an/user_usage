# syntax=docker/dockerfile:1
#
# 사용량 대시보드 — 멀티스테이지. 배포 형태는 **파일 하나짜리 Go 바이너리**다(web/next.config.mjs·
# go/CONTRACT.md 의 "정적 서빙"). 빌더에서 web 을 빌드해 go:embed 로 바이너리에 박고, 런타임
# 이미지에는 **Node 를 두지 않는다** — 최종 이미지에 남는 건 Go 바이너리·tini·CA 인증서뿐이다.
#
# 유일 빌드 경로는 scripts/build.sh 다(web build → webroot 동기화 → go build). 여기서도 그 스크립트를
# 그대로 부른다 — SKIP_WEB 을 쓰지 않는다. 그걸 쓰면 배포가 담을 산출물이 아니라 "지난 web/out 을
# 그대로 믿은" 산출물이 들어가고, go:embed 특성상 화면이 조용히 낡는다(scripts/build.sh 주석 참조).
#
# ⚠ 이 머신에 docker 가 없어 `docker build` 는 로컬에서 검증하지 못했다. 대신 같은 산출물을 만드는
#   scripts/smoke.sh(네이티브)로 기동·/healthz·/ 를 실증했다.

# ── 빌더 ──────────────────────────────────────────────────────────────────────
# Node 22 는 web 빌드(next build), Go 1.26 은 바이너리 빌드에 쓴다. 둘 다 공식 이미지에서
# 정확히 핀으로 가져온다. Go 트리(/usr/local/go)는 재배치 가능해서 Node 이미지로 통째 복사하면
# 되고, 두 쪽 다 glibc(bookworm)라 next 의 네이티브(turbopack/swc) 바이너리가 그대로 돈다
# (musl 알파인은 libc6-compat 를 얹어야 해서 빌더는 Debian 계열로 둔다).
FROM node:22-bookworm AS build
COPY --from=golang:1.26-bookworm /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"

# 순수 Go 정적 바이너리를 낸다. modernc.org/sqlite 는 cgo 가 없어 CGO 를 끄면 알파인·scratch 어디서든
# 도는 정적 바이너리가 나온다(런타임 이미지가 musl 이어도 안전). GOTOOLCHAIN=local 로 go.mod 의
# 1.25 선언 때문에 빌드 중 다른 toolchain 을 받아오지 않게 못박는다(재현성).
ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local

WORKDIR /src

# ── 의존성 레이어를 먼저 굳힌다 ──────────────────────────────────────────────
# 소스만 바뀌면 아래 두 레이어는 캐시에서 재사용된다. build.sh 는 web/node_modules 가 이미 있으면
# 자기 npm ci 를 건너뛰므로, 여기서 미리 깔아 두면 그대로 캐시가 산다.
COPY go/go.mod go/go.sum ./go/
RUN cd go && go mod download

COPY web/package.json web/package-lock.json ./web/
RUN cd web && npm ci

# ── 소스 → 유일 빌드 경로 ────────────────────────────────────────────────────
COPY . .
RUN OUT_BIN=/out/usage-server bash scripts/build.sh

# ── 런타임 ────────────────────────────────────────────────────────────────────
# Node 없음. Go 정적 바이너리 + tini + CA 인증서만. 알파인을 고른 이유는 두 가지다:
#   1) busybox wget 이 기본 내장이라 **컨테이너 자체 헬스체크**를 Node·별도 바이너리 없이 할 수 있다.
#   2) tini 를 apk 로 바로 얹을 수 있다.
FROM alpine:3.21

# tini — PID 1 이 시그널·좀비 자식을 정리하게 한다. 바이너리는 SIGTERM 에서 서버를 우아하게
#         닫는데(main.go 의 shutdownGrace), tini 없이는 그 시그널이 제대로 전달되지 않는다.
# ca-certificates — remote(pg) 모드에서 TLS 를 탈 때 필요하다. local(sqlite)만 쓸 거면 놀고 있는다.
RUN apk add --no-cache tini ca-certificates

# root 로 돌리지 않는다. 이 서비스가 쓰는 경로는 /data 하나뿐이다.
RUN addgroup -S app && adduser -S -G app -h /app app \
 && mkdir -p /data && chown -R app:app /data

# 컨테이너 안에서 루프백 바인드는 "아무도 못 붙음"이다. 노출 범위는 이미지가 아니라 포트
# 매핑(docker-compose.yml 의 127.0.0.1:4191:4191)으로 정한다.
ENV USAGE_HOST=0.0.0.0 \
    USAGE_PORT=4191 \
    USAGE_DATA_DIR=/data

WORKDIR /app
COPY --from=build /out/usage-server /usr/local/bin/usage-server

# 데이터 디렉터리. **볼륨으로 마운트하지 않으면 컨테이너와 함께 사라진다** — local 모드로 쓸 거면
# 반드시 마운트한다(docker-compose.yml 이 그렇게 해 둔다).
VOLUME ["/data"]
EXPOSE 4191
USER app

# /healthz 는 무인증·무DB 경로다(토큰 없이 프로브할 수 있게 게이트 위에 둔다 — go server.go).
# 프로브는 busybox wget 으로 한다. 바이너리에 별도 서브커맨드가 필요하지 않다.
#   ⚠ 최종 이미지가 wget 없는 베이스(scratch·distroless)로 바뀌면 이 프로브는 못 쓴다. 그때는
#     go 바이너리에 `healthcheck` 서브커맨드를 넣는 것이 정석인데, 그건 go source 변경이라 이
#     파일의 오너 영역 밖이다(별도 오너에 위임: "server main 에 healthcheck 서브커맨드 필요").
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:${USAGE_PORT}/healthz" || exit 1

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["usage-server"]
