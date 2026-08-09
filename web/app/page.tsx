import Dashboard from '@/components/Dashboard';

/*
 * 유일한 라우트. 탭 전환은 해시(#/usage · #/usageobs)로 하므로 산출물은 index.html 한 장이다.
 *
 * 서버 컴포넌트에서 데이터를 가져오지 않는다 — 정적 export 라 그 값은 **빌드 시각에 굳고**,
 * 화면은 낡은 숫자를 아무 신호 없이 보여주게 된다. 데이터는 전부 클라이언트에서 온다.
 */
export default function Page() {
  return <Dashboard />;
}
