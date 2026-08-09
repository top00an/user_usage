# 수집 설치 — Claude Code 훅 + 백그라운드 에이전트

각 개발자 머신에서 사용량을 SaaS 로 올리는 두 경로다. **훅**은 신선도(세션 끝나면 바로),
**백그라운드 에이전트**는 완전성(훅 미설치 기간·과거 세션 백필)을 맡는다. 둘은 같은 수집기·같은
org 인제스트 키를 쓴다. 서버가 멱등이라 둘이 겹쳐도 중복 집계되지 않는다.

## 1. 수집기 설치

```sh
cd collector && go build -o /usr/local/bin/usage-collector ./cmd/usage-collector
```

## 2. org 키 발급 (운영자, 서버 호스트에서)

```sh
usage-server org create --name "Acme"          # → org 생성됨: id=org_xxxx ...
usage-server key issue  --org org_xxxx          # → uu_ing_...  (이 평문은 1회만 보인다)
```

## 3. 설정 파일 (개발자 머신)

`usage-collector.env.example` 을 복사해 값을 채운다:

```sh
cp deploy/hooks/usage-collector.env.example ~/.config/usage-collector.env
chmod 600 ~/.config/usage-collector.env      # 키가 들어가므로 권한을 좁힌다
# USAGE_SERVER_URL, USAGE_INTAKE_TOKEN 을 채운다
```

## 4. SessionEnd 훅 (신선도 경로)

`settings.snippet.json` 을 Claude Code `settings.json` 의 `hooks` 에 병합하고, `command` 의
절대경로를 `session-end.sh` 실제 위치로 바꾼다. 세션이 끝날 때마다 수집기가 델타를 올린다.
설정(env)이 없으면 훅은 조용히 나간다 — 세션 흐름을 막지 않는다.

## 5. 백그라운드 에이전트 (완전성 경로, 선택)

주기 실행(cron·launchd·systemd timer)으로 수집기를 돌려 훅이 놓친 것을 백필한다:

```sh
*/15 * * * *  . $HOME/.config/usage-collector.env; usage-collector >/dev/null 2>&1
```

## 동작 요약

- 수집기는 `~/.claude/projects/**/<sessionId>.jsonl` 을 읽어 **집계만** 보낸다(프롬프트 원문·경로·
  인자 미수집, keyword 축은 시크릿·PII 필터). 증분 체크포인트로 델타만 전송.
- `USAGE_INTAKE_TOKEN` 이 곧 org 인제스트 키다. 멀티테넌트 서버가 이 키를 tenant 로 해석해
  RLS 로 격리한다 — 다른 org 의 데이터와 절대 섞이지 않는다.
