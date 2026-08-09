#!/usr/bin/env node
/*
 * 빌드 후처리 — **산출물에서 인라인 <script> 를 없앤다.**
 *
 * 왜 필요한가. 서버는 CSP 를 `script-src 'self'` 로 잠그고 있다(server.js:289~293, Go 포팅본도
 * 같은 헤더를 옮긴다). 그런데 Next 는 정적 export 에서도 RSC 페이로드를 **인라인 스크립트**로
 * 남긴다(`self.__next_f.push(...)`). 그대로 두면 셋 중 하나를 골라야 한다:
 *
 *   ① CSP 에 'unsafe-inline' 을 연다        → 스크립트 주입 방어를 통째로 포기한다
 *   ② 인라인마다 sha256 해시를 CSP 에 넣는다 → 빌드마다 헤더가 바뀐다(Go 쪽이 산출물을 파싱해야 한다)
 *   ③ 인라인을 파일로 뽑는다                 → CSP 는 'self' 하나로 끝난다
 *
 * ③ 을 고른다. 현행 셸이 테마 선반영까지 굳이 파일(js/theme-boot.js)로 뺀 이유와 같다 —
 * 인라인이 하나라도 있으면 그 방어가 성립하지 않는다.
 *
 * 실행 의미는 보존된다: 인라인 스크립트는 문서 순서대로 동기 실행되고, `src` 를 단 고전
 * 스크립트(async·defer 없음)도 문서 순서대로 동기 실행된다. 순서가 바뀌지 않는다.
 *
 * 덤으로 `embed-manifest.json` 을 남긴다 — Go 쪽 정적 서빙이 **경로 화이트리스트**라
 * (go/CONTRACT.md) 무엇을 열어야 하는지 목록이 필요하다. 해시 파일명은 빌드마다 바뀌므로
 * 사람이 손으로 적는 목록은 반드시 낡는다.
 */
import { createHash } from 'node:crypto';
import { mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.resolve(HERE, '..', 'out');
const INLINE_DIR = path.join(OUT, '_next', 'static', 'inline');
const INLINE_URL = '/_next/static/inline';

/** src 가 **없는** <script> 만 고른다. 여는 태그의 속성은 그대로 살린다(type=module 등). */
const INLINE_RE = /<script(?![^>]*\ssrc=)([^>]*)>([\s\S]*?)<\/script>/g;

async function walk(dir) {
  const out = [];
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) out.push(...await walk(p));
    else out.push(p);
  }
  return out;
}

async function main() {
  await mkdir(INLINE_DIR, { recursive: true });

  const pending = [];
  const files = await walk(OUT);
  const htmls = files.filter((f) => f.endsWith('.html'));
  let extracted = 0;
  const written = new Set();

  for (const file of htmls) {
    const src = await readFile(file, 'utf8');
    let changed = false;

    const next = src.replace(INLINE_RE, (whole, attrs, body) => {
      // 내용 없는 <script></script> 는 굳이 파일로 만들 이유가 없다.
      if (!body.trim()) return whole;

      /*
       * React 는 인라인 스크립트 안의 `</script>` 만 `<\/script>` 로 접는다.
       * 파일로 옮기면 그 이스케이프가 불필요하고, 남겨 두면 정규식 리터럴 안에서 뜻이 달라진다.
       */
      const code = body.replaceAll(String.raw`<\/script>`, '</script>');
      const hash = createHash('sha256').update(code).digest('hex').slice(0, 16);
      const name = `${hash}.js`;
      if (!written.has(name)) written.add(name);
      pending.push([name, code]);

      extracted += 1;
      changed = true;
      return `<script${attrs} src="${INLINE_URL}/${name}"></script>`;
    });

    if (changed) await writeFile(file, next, 'utf8');
  }

  for (const [name, code] of pending) {
    await writeFile(path.join(INLINE_DIR, name), code, 'utf8');
  }

  /* ── 검증: 남은 인라인이 하나라도 있으면 실패다 ────────────────────────
   * "대체로 없앴다"는 CSP 에서 아무 의미가 없다. 하나만 남아도 그 스크립트가 죽고,
   * 그것이 하이드레이션 부트스트랩이면 화면은 **빈 채로** 뜬다(에러 없이).
   */
  const leftovers = [];
  for (const file of htmls) {
    const src = await readFile(file, 'utf8');
    for (const m of src.matchAll(INLINE_RE)) {
      if (m[2].trim()) leftovers.push(`${path.relative(OUT, file)}: ${m[0].slice(0, 80)}`);
    }
  }
  if (leftovers.length) {
    console.error('[externalize] 인라인 스크립트가 남았다 — CSP script-src \'self\' 에서 죽는다:');
    for (const l of leftovers) console.error('  ' + l);
    process.exit(1);
  }

  // 화이트리스트용 매니페스트. 경로는 URL 형태(선행 / 포함)로 낸다.
  const all = (await walk(OUT))
    .map((f) => '/' + path.relative(OUT, f).split(path.sep).join('/'))
    .sort();
  await writeFile(
    path.join(OUT, 'embed-manifest.json'),
    `${JSON.stringify({ generatedBy: 'web/scripts/externalize-inline-scripts.mjs', paths: all }, null, 2)}\n`,
    'utf8',
  );

  console.log(`[externalize] 인라인 ${extracted}개를 파일 ${written.size}개로 뽑았다 · html ${htmls.length}장 · 산출물 ${all.length}개`);
}

await main();
