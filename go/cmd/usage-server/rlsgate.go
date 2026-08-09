package main

import "github.com/tscorp/user-usage/internal/db"

/*
 * rlsGate — RLS 판정을 부팅 행동으로 옮긴다.
 *
 * 왜 함수로 꺼냈나: 이 세 갈래를 부팅 코드에 인라인으로 두면 **실행 검증이 불가능하다**
 * (pg 클러스터와 슈퍼유저 계정이 있어야 밟힌다). 여기로 꺼내면 판정 → 행동의 대응을
 * 단위 테스트로 못박을 수 있고, 그게 이 배선에서 유일하게 값싼 확신이다.
 *
 * ⚠ **거부는 Verdict.Rejects() 로만 판단한다.** `!v.OK` 로 분기하면 판정 불가(터널 미개통·DB
 *   다운)에서 부팅이 막혀, "터널을 먼저 뚫는다"는 정상 절차가 부팅 실패로 보인다. 붙지 못한
 *   DB 는 노출도 없으니 거부할 이유가 없다 — 대신 조용하지도 않게 stderr 로 크게 남긴다.
 */
type rlsAction int

const (
	rlsProceed rlsAction = iota // 격리 성립 — 그대로 뜬다
	rlsWarn                     // 판정 불가 — 뜨지만 stderr 로 크게 남긴다
	rlsReject                   // 위반 — 뜨지 않는다(뜨고 나면 증상이 없는 사고다)
)

func rlsGate(v db.Verdict) rlsAction {
	switch {
	case v.Rejects():
		return rlsReject
	case v.Inconclusive:
		return rlsWarn
	default:
		return rlsProceed
	}
}
