'use strict';
/*
 * 뷰 전환 토큰 — 늦게 도착한 응답이 새 화면을 덮어쓰지 못하게 하는 장치.
 *
 * 뷰는 조회를 시작하기 전에 받은 seq 를 들고 있다가, 응답이 돌아오면 `isStale(seq)` 로 "이 렌더가
 * 아직 최신인가"를 묻는다. 셸(app.js)이 탭을 바꿀 때마다 seq 가 올라가므로, 앞 탭의 늦은 응답은
 * stale 로 판정되어 조용히 빠져나간다.
 *
 * ⚠ 항상 false 를 돌려주는 더미로 만들면 탭을 빠르게 오갈 때 이전 탭의 응답이 현재 탭을
 *   덮어쓴다 — 화면이 틀린 값을 보여주는데 아무 에러도 나지 않는 조용한 오표시다.
 */

// 뷰 전환 토큰. 셸(app.js)이 탭을 바꿀 때마다 올린다.
export let NAV_SEQ = 0;
export const isStale = (seq) => seq !== NAV_SEQ;

/** 새 렌더의 seq 를 발급한다. 셸이 탭 전환마다 1회 호출하고 그 값을 뷰에 넘긴다. */
export function nextSeq() {
  NAV_SEQ += 1;
  return NAV_SEQ;
}
