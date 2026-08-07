'use strict';
/*
 * 멀티테넌트 컨텍스트 — AsyncLocalStorage 기반(node 내장, 제로 의존).
 *
 * 계약:
 *   const { tenantStore, currentTenant } = require('./tenant');
 *   tenantStore.run(tenantId, fn)   // 이 스코프 안의 모든 쿼리/tx 가 tenantId 로 격리된다
 *   currentTenant()                 // 현재 tenant(미설정 시 'default')
 *
 * 저장값은 tenantId 문자열 그 자체다(AsyncLocalStorage.run(store, cb) 의 store = 문자열).
 * pg 어댑터가 매 쿼리/tx 에서 currentTenant() 를 읽어 `SET LOCAL app.tenant_id` 로 주입하고,
 * 그 값을 RLS 정책이 본다. sqlite 백엔드는 단일 테넌트라 이 값을 무시한다.
 *
 * ⚠ 요청 컨텍스트 **밖**(부팅 시드·타이머 워커·CLI)에서 DB 를 건드릴 때도 감싸야 한다.
 *   감싸지 않으면 'default' 로 흐르는데, 그게 맞는다는 보장은 아무 데도 없다.
 */
const { AsyncLocalStorage } = require('node:async_hooks');

const DEFAULT_TENANT = 'default';
const tenantStore = new AsyncLocalStorage();

function currentTenant() {
  const t = tenantStore.getStore();
  return (typeof t === 'string' && t) ? t : DEFAULT_TENANT;
}

// 편의 래퍼 — run 은 tenantStore.run 과 동일하나 비-문자열/빈 값을 'default' 로 정규화한다.
function runWithTenant(tenantId, fn) {
  const t = (typeof tenantId === 'string' && tenantId) ? tenantId : DEFAULT_TENANT;
  return tenantStore.run(t, fn);
}

module.exports = { tenantStore, currentTenant, runWithTenant, DEFAULT_TENANT };
