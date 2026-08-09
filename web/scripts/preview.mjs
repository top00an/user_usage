#!/usr/bin/env node
/*
 * 미리보기 서버 — **배포와 같은 조건**에서 정적 산출물을 확인하기 위한 개발 도구다.
 *
 * 왜 필요한가. 배포 형태는 "Go 바이너리 하나가 정적 파일과 /api 를 **같은 오리진**에서 서빙"이다.
 * 그런데 개발 중에는 API 가 다른 포트(Node 서버)에 있다. 다른 오리진에 붙이면 쿠키 자격증명이
 * 실리지 않아 **로그인 자체가 성립하지 않는다** — 즉 "다른 포트로 열어 봤다"는 검증이 아니다.
 * 그래서 이 서버가 정적 파일과 /api 프록시를 한 오리진에 모은다.
 *
 * 보안 헤더는 현행 server.js 의 것을 **그대로** 쓴다. CSP 를 느슨하게 두고 확인하면
 * "여기서는 됐는데 배포하면 화면이 빈다"가 된다 — 그 사고를 여기서 미리 낸다.
 *
 *   node scripts/preview.mjs --port 4300 --api http://127.0.0.1:4191
 */
import { createServer } from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const OUT = path.resolve(HERE, '..', 'out');

function arg(name, dflt) {
  const i = process.argv.indexOf(`--${name}`);
  return i >= 0 && process.argv[i + 1] ? process.argv[i + 1] : dflt;
}

const PORT = Number(arg('port', '4300'));
const API = arg('api', 'http://127.0.0.1:4191').replace(/\/$/, '');

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon',
  '.txt': 'text/plain; charset=utf-8',
  '.woff2': 'font/woff2',
};

// server.js:289~293 과 같은 값이다. 여기서 느슨하게 두면 검증이 검증이 아니게 된다.
const CSP = [
  "default-src 'none'", "script-src 'self'", "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data:", "font-src 'self'", "connect-src 'self'",
  "form-action 'self'", "base-uri 'none'", "object-src 'none'", "frame-ancestors 'none'",
].join('; ');

const SECURITY = {
  'X-Frame-Options': 'DENY',
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'same-origin',
  'Content-Security-Policy': CSP,
  'Cache-Control': 'no-cache',
};

async function serveStatic(req, res, urlPath) {
  const rel = urlPath === '/' ? 'index.html' : urlPath.replace(/^\/+/, '');
  const abs = path.resolve(OUT, rel);
  // 경로 탈출 방어. 산출물 디렉터리 밖은 존재하지 않는 것으로 친다.
  if (abs !== OUT && !abs.startsWith(OUT + path.sep)) {
    res.writeHead(404, SECURITY).end('not found');
    return;
  }
  try {
    const st = await stat(abs);
    if (!st.isFile()) throw new Error('not a file');
    const buf = await readFile(abs);
    res.writeHead(200, {
      ...SECURITY,
      'Content-Type': MIME[path.extname(abs).toLowerCase()] || 'application/octet-stream',
      'Content-Length': buf.length,
    });
    res.end(req.method === 'HEAD' ? undefined : buf);
  } catch {
    res.writeHead(404, SECURITY).end('not found');
  }
}

async function proxy(req, res, urlPath) {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  const body = chunks.length ? Buffer.concat(chunks) : undefined;

  const headers = {};
  // 쿠키가 이 프록시의 존재 이유다 — 여기서 떨어뜨리면 인증이 통째로 사라진다.
  for (const k of ['cookie', 'authorization', 'content-type', 'accept']) {
    if (req.headers[k]) headers[k] = req.headers[k];
  }

  let upstream;
  try {
    upstream = await fetch(`${API}${urlPath}`, { method: req.method, headers, body });
  } catch (e) {
    res.writeHead(502, { 'Content-Type': 'application/json; charset=utf-8' });
    res.end(JSON.stringify({ error: `업스트림(${API})에 붙지 못했습니다: ${e.message}` }));
    return;
  }

  const out = {};
  upstream.headers.forEach((v, k) => {
    if (k === 'content-encoding' || k === 'content-length' || k === 'transfer-encoding') return;
    out[k] = v;
  });
  const buf = Buffer.from(await upstream.arrayBuffer());
  res.writeHead(upstream.status, { ...out, 'Content-Length': buf.length });
  res.end(buf);
}

const server = createServer((req, res) => {
  const urlPath = new URL(req.url, 'http://x').pathname;
  const p = urlPath.startsWith('/api/') || urlPath === '/healthz'
    ? proxy(req, res, req.url)
    : serveStatic(req, res, urlPath);
  p.catch((e) => {
    if (!res.headersSent) res.writeHead(500, { 'Content-Type': 'text/plain' });
    res.end(String(e && e.message));
  });
});

server.listen(PORT, '127.0.0.1', () => {
  console.log(`[preview] http://127.0.0.1:${PORT}  (정적: ${OUT} · API 프록시: ${API})`);
});
