# 사용량 대시보드 — 단일 스테이지. 빌드 산출물이 없어서(번들러·트랜스파일 없음) 나눌 것이 없다.
#
# 런타임 의존은 pg 하나뿐이고, 그마저도 remote 모드에서만 실제로 로드된다(lib/db/pg.js 의 지연
# require). local 모드로만 쓸 거면 이미지에서 npm ci 단계를 빼도 동작한다 — 다만 두 모드를
# 같은 이미지로 굴리는 편이 운영이 단순해서 넣어 둔다.
FROM node:22-alpine

# tini — PID 1 로 도는 node 는 SIGTERM 을 받아도 자식·시그널 정리를 못 한다.
# server.js 는 SIGTERM 에서 타이머를 걷고 커넥션을 닫는데, 그 경로가 실제로 돌게 하려면 필요하다.
RUN apk add --no-cache tini

ENV NODE_ENV=production
WORKDIR /app

# 의존성 레이어를 먼저 굳힌다 — 소스만 바뀌면 이 레이어는 캐시에서 재사용된다.
COPY package.json package-lock.json* ./
RUN npm ci --omit=dev || npm install --omit=dev

COPY . .

# 데이터 디렉터리. **볼륨으로 마운트하지 않으면 컨테이너와 함께 사라진다** —
# local 모드로 쓸 거라면 반드시 마운트한다(docker-compose.yml 이 그렇게 해 둔다).
RUN mkdir -p /data && chown -R node:node /data /app
ENV USAGE_DATA_DIR=/data

# 컨테이너 안에서는 루프백 바인드가 곧 "아무도 못 붙음"이다. 노출 범위는 이미지가 아니라
# 포트 매핑(-p 127.0.0.1:4191:4191)으로 정한다.
ENV USAGE_HOST=0.0.0.0
ENV USAGE_PORT=4191
EXPOSE 4191

# root 로 돌리지 않는다. 이 서비스가 쓰는 파일은 /data 뿐이라 비용이 0 이다.
USER node

# /healthz 는 무인증·무DB 경로다(토큰 없이 프로브할 수 있게 게이트 위에 둔다).
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD node -e "fetch('http://127.0.0.1:'+(process.env.USAGE_PORT||4191)+'/healthz').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["node", "server.js"]
