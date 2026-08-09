import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

/*
 * 서버 호출구는 `lib/api.ts` 하나다(go/CONTRACT.md web/ 절).
 *
 * 왜 테스트로까지 잡는가: 이건 리뷰로 지켜지지 않는 종류의 규율이다. 컴포넌트 하나가 급할 때
 * fetch 를 직접 부르면 그 순간에는 아무것도 깨지지 않고, **SSO·프록시로 갈아탈 때** 비로소
 * 전면 수정으로 돌아온다. 그 시점에는 누가 언제 넣었는지도 남아 있지 않다.
 *
 * eslint 규칙(no-restricted-globals)도 같은 것을 막지만, 린트는 설정이 바뀌면 조용히 꺼진다.
 * 이 테스트는 소스를 직접 읽으므로 설정과 무관하다.
 */
const HERE = path.dirname(fileURLToPath(import.meta.url));
const WEB = path.resolve(HERE, '..');
const SCAN = ['app', 'components', 'hooks', 'lib'];
const ALLOWED = new Set([path.join('lib', 'api.ts')]);

function sources(dir: string): string[] {
  const abs = path.join(WEB, dir);
  const out: string[] = [];
  for (const name of readdirSync(abs)) {
    const p = path.join(abs, name);
    if (statSync(p).isDirectory()) out.push(...sources(path.join(dir, name)));
    else if (/\.tsx?$/.test(name)) out.push(path.join(dir, name));
  }
  return out;
}

describe('서버 호출구는 하나다', () => {
  const files = SCAN.flatMap(sources);

  it('스캔 대상이 비어 있지 않다 (경로가 바뀌면 이 테스트가 무력해진다)', () => {
    expect(files.length).toBeGreaterThan(10);
  });

  it.each(files.filter((f) => !ALLOWED.has(f)))('%s 는 fetch 를 직접 부르지 않는다', (rel) => {
    const src = readFileSync(path.join(WEB, rel), 'utf8');
    expect(src).not.toMatch(/(?<![\w.])fetch\s*\(/);
    expect(src).not.toMatch(/(?:window|globalThis|self)\.fetch/);
    expect(src).not.toMatch(/XMLHttpRequest|EventSource|navigator\.sendBeacon/);
  });

  it('lib/api.ts 만 fetch 를 부른다', () => {
    const src = readFileSync(path.join(WEB, 'lib', 'api.ts'), 'utf8');
    expect(src).toMatch(/(?<![\w.])fetch\s*\(/);
    expect((src.match(/(?<![\w.])fetch\s*\(/g) ?? []).length).toBe(1);
  });

  it('dangerouslySetInnerHTML 은 어디에도 없다', () => {
    for (const rel of files) {
      expect(readFileSync(path.join(WEB, rel), 'utf8')).not.toContain('dangerouslySetInnerHTML');
    }
  });
});
