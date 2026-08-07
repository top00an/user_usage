/*
 * 의존 그래프 검증 — 레포의 **모든 서버측 .js 를 실제로 require** 해 본다.
 *
 * 왜 별도 스크립트인가: 유닛 테스트는 자기가 부르는 모듈만 로드한다. 그래서 아무도 require 하지
 * 않는 파일에 끊어진 경로가 남아 있어도 스위트는 초록색이고, 그 파일을 처음 쓰는 날 런타임에서
 * 터진다. 레포를 다른 트리로 옮긴 직후에는 특히 그 위험이 크다(상대경로가 통째로 한 칸 움직인다).
 *
 * 검사 범위: server.js · index.js · lib/** · routes/**.
 *   · public/** 은 제외한다 — 브라우저 ESM 이고 document 를 잡는다(노드에서 로드하면 죽는 게 정상).
 *   · test/** 도 제외한다 — 테스트 러너가 이미 전부 로드한다.
 *
 * 사용: node test/require-all.mjs   (실패 시 exit 1 + 어느 파일의 무엇이 없는지)
 */
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.join(HERE, '..');
const require_ = createRequire(path.join(ROOT, 'package.json'));

// DB 파일을 레포에 만들지 않는다. 이 스크립트는 "로드되는가"만 묻는다.
process.env.USAGE_DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'usage-req-'));

function walk(dir) {
  const out = [];
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) out.push(...walk(p));
    else if (e.isFile() && e.name.endsWith('.js')) out.push(p);
  }
  return out;
}

const files = [
  path.join(ROOT, 'server.js'),
  path.join(ROOT, 'index.js'),
  ...walk(path.join(ROOT, 'lib')),
  ...walk(path.join(ROOT, 'routes')),
].filter((f) => fs.existsSync(f));

const failed = [];
for (const f of files) {
  try {
    require_(f);
  } catch (e) {
    failed.push({ file: path.relative(ROOT, f), err: String((e && e.message) || e) });
  }
}

for (const f of files) console.log(`  ok  ${path.relative(ROOT, f)}`);
if (failed.length) {
  console.error(`\n✖ ${failed.length}개 파일이 로드되지 않는다:`);
  for (const f of failed) console.error(`  · ${f.file}\n      ${f.err}`);
  process.exit(1);
}
console.log(`\n✔ ${files.length}개 파일 전부 로드됨 — MODULE_NOT_FOUND 없음`);
