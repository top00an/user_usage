package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/tscorp/user-usage/internal/db"
)

/*
 * 감사 로그 — 누가·언제·무엇을 바꿨나.
 *
 * 이 서비스에서 사람이 **데이터를 바꾸는** 경로는 하나뿐이다: 귀속 교정
 * (PUT/DELETE /api/usage/identity). 그 한 동작이 과거 행 수천 개의 username 을 재스탬프하므로,
 * "왜 어제 보던 이름이 오늘 다르지"를 나중에 답할 수 있어야 한다. 그게 이 테이블의 전부다.
 *
 * ── 설계 결정 세 가지 ──────────────────────────────────────────────
 *
 *	① **자기 테이블을 쓴다.** 다른 시스템의 감사 스키마에 얹지 않는다 — 그 스키마가 바뀌면
 *	   사용량과 무관한 이유로 여기가 깨진다.
 *	② **절대 실패를 전파하지 않는다.** 감사 기록 실패가 본 동작(귀속 교정)을 되돌리게 하면,
 *	   사람은 로그를 남기지 않으려고 기능을 피하게 된다. 기록에 실패하면 stderr 로 흘려
 *	   최소한 흔적은 남긴다.
 *	③ **기한을 두지 않는다.** 키워드 축과 달리 이 표는 "어제 보던 이름이 왜 오늘 다른가"에
 *	   답하는 표다. 그 질문에는 시효가 없다.
 */

// AuditRecord 는 기록 1건이다(Detail 은 JSON 문자열, 없으면 nil).
type AuditRecord struct {
	At     string
	Actor  string
	Action string
	Target *string
	Detail *string
}

// AuditEntry 는 조회 결과다. Detail 은 다시 값으로 펼친다.
type AuditEntry struct {
	At     string
	Actor  string
	Action string
	Target *string
	Detail any
}

var auditDDL = []string{
	`CREATE TABLE IF NOT EXISTS usage_audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		at TEXT NOT NULL,
		actor TEXT,
		action TEXT NOT NULL,
		target TEXT,
		detail TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_usage_audit_at ON usage_audit(at)`,
}

// AuditInit 은 감사 표를 만든다. pg 는 스키마가 migrations 소유라 아무것도 하지 않는다.
func AuditInit(ctx context.Context, d db.DB) error {
	if d == nil || d.Dialect() != db.DialectSQLite {
		return nil
	}
	for _, stmt := range auditDDL {
		if err := d.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("identity: 감사 DDL 실패: %w", err)
		}
	}
	return nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

/*
 * AuditLog 는 1건을 기록한다.
 *
 * extra 는 구조화 필드(예: {username, moved})이고 JSON 문자열로 접어 넣는다 — 컬럼으로 펴면
 * 새 동작이 생길 때마다 마이그레이션이 필요해진다.
 *
 * 반환은 기록한 레코드다(호출부가 응답에 실을 수 있게). **오류를 돌려주지 않는다** — 위 ②.
 */
func AuditLog(ctx context.Context, actor, action, target string, extra any) AuditRecord {
	a := clip(actor, 200)
	if a == "" {
		a = "system"
	}
	rec := AuditRecord{
		At:     nowISO(),
		Actor:  a,
		Action: clip(action, 120),
		Target: strPtr(clip(target, 400)),
	}
	if extra != nil {
		if b, err := json.Marshal(extra); err == nil {
			rec.Detail = strPtr(clip(string(b), 4000))
		}
	}

	d, err := conn()
	if err == nil {
		var target any
		if rec.Target != nil {
			target = *rec.Target
		}
		var detail any
		if rec.Detail != nil {
			detail = *rec.Detail
		}
		err = d.Exec(ctx, "INSERT INTO usage_audit(at,actor,action,target,detail) VALUES(?,?,?,?,?)",
			rec.At, rec.Actor, rec.Action, target, detail)
	}
	if err != nil {
		/*
		 * 기록 실패가 본 동작을 되돌리지 않는다 — 다만 조용히 사라지게 두지도 않는다.
		 *
		 * ⚠ `detail` 은 찍지 않는다. 거기에 귀속 대상 계정명이 들어가는데, stderr 는 이 서비스의
		 *   보존 정책이 닿지 않는 곳이다(컨테이너 로그 수집기가 그대로 퍼간다). 사고 추적에
		 *   필요한 넷(언제·누가·무엇을·어디에)은 아래로 충분하고, 그 이상은 DB 가 살아났을 때
		 *   다시 남기면 된다.
		 */
		t := "-"
		if rec.Target != nil {
			t = *rec.Target
		}
		fmt.Fprintf(os.Stderr, "audit: 기록 실패 — %v at=%s actor=%s action=%s target=%s\n",
			err, rec.At, rec.Actor, rec.Action, t)
	}
	return rec
}

// AuditRecent 는 최근 기록이다.
// 조회 실패는 빈 슬라이스 — 감사 화면이 없다고 서비스가 죽을 이유는 없다.
func AuditRecent(ctx context.Context, n int) []AuditEntry {
	out := []AuditEntry{}
	lim := n
	if lim == 0 {
		lim = 200
	}
	if lim > 1000 {
		lim = 1000
	}
	if lim < 1 {
		lim = 1
	}
	d, err := conn()
	if err != nil {
		return out
	}
	rows, err := d.Query(ctx,
		"SELECT at, actor, action, target, detail FROM usage_audit ORDER BY id DESC LIMIT ?", lim)
	if err != nil {
		return out
	}
	for _, r := range rows {
		e := AuditEntry{
			At:     r.Str("at"),
			Actor:  r.Str("actor"),
			Action: r.Str("action"),
			Target: strPtr(r.Str("target")),
		}
		if s := r.Str("detail"); s != "" {
			var v any
			if err := json.Unmarshal([]byte(s), &v); err == nil {
				e.Detail = v
			}
		}
		out = append(out, e)
	}
	return out
}
