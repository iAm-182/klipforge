/**
 * KlipForge Cloudflare Edge Worker (klip.breakthematrix.id) — Hardened Edition
 * Security Controls:
 * - MANDATORY ACCESS_KEY Basic/Bearer Auth (fail-closed if ACCESS_KEY secret is unset)
 * - Per-IP Brute-Force Rate Limiting via Workers KV (max 10 failed attempts / 5 min -> 429)
 * - ZERO open-proxy header override (X-KlipForge-Engine completely removed)
 * - Strips Authorization header before forwarding to upstream (prevents credential leak)
 * - Injects X-Engine-Token secret to upstream Go Engine (blocks direct trycloudflare.com bypass!)
 * - Dynamic Engine Bridge via Workers KV (POST /edge-register with X-Bridge-Secret + rate limit)
 */

const AUTH_MAX_FAILS = 10;
const AUTH_WINDOW_SEC = 300; // 5 minutes

function getClientIP(request) {
  return request.headers.get('CF-Connecting-IP') || 'unknown';
}

function timingSafeEqual(a, b) {
  const sa = String(a || '');
  const sb = String(b || '');
  const len = Math.max(sa.length, sb.length);
  let diff = sa.length ^ sb.length;
  for (let i = 0; i < len; i++) {
    diff |= (sa.charCodeAt(i) || 0) ^ (sb.charCodeAt(i) || 0);
  }
  return diff === 0;
}

async function isRateLimited(env, scope, ip) {
  if (!env.STATE || !ip || ip === 'unknown') return false;
  const key = `rl:${scope}:${ip}`;
  const raw = await env.STATE.get(key);
  const count = parseInt(raw || '0', 10);
  return Number.isFinite(count) && count >= AUTH_MAX_FAILS;
}

async function recordAuthFailure(env, scope, ip) {
  if (!env.STATE || !ip || ip === 'unknown') return;
  const key = `rl:${scope}:${ip}`;
  const raw = await env.STATE.get(key);
  const next = (parseInt(raw || '0', 10) || 0) + 1;
  await env.STATE.put(key, String(next), { expirationTtl: AUTH_WINDOW_SEC });
}

async function clearAuthFailures(env, scope, ip) {
  if (!env.STATE || !ip || ip === 'unknown') return;
  const key = `rl:${scope}:${ip}`;
  await env.STATE.delete(key);
}

async function checkAuth(request, env) {
  const key = (env.ACCESS_KEY || '').trim();
  // Fail-closed: refuse public access if ACCESS_KEY secret is not configured!
  if (!key) {
    return new Response(
      'KlipForge Locked: ACCESS_KEY belum dikonfigurasi di Cloudflare Worker secrets. Jalankan scripts/set-access-key.ps1.',
      { status: 503, headers: { 'Content-Type': 'text/plain; charset=utf-8', 'Cache-Control': 'no-store' } }
    );
  }

  const ip = getClientIP(request);
  if (await isRateLimited(env, 'auth', ip)) {
    return new Response('Terlalu banyak percobaan login gagal. Coba lagi dalam 5 menit.', {
      status: 429,
      headers: {
        'Content-Type': 'text/plain; charset=utf-8',
        'Retry-After': String(AUTH_WINDOW_SEC),
        'Cache-Control': 'no-store',
      },
    });
  }

  const auth = request.headers.get('Authorization') || '';
  let ok = false;
  if (auth.startsWith('Basic ')) {
    try {
      const decoded = atob(auth.slice(6));
      const idx = decoded.indexOf(':');
      const pass = idx >= 0 ? decoded.slice(idx + 1) : decoded;
      if (timingSafeEqual(pass, key)) ok = true;
    } catch {}
  } else if (auth.startsWith('Bearer ')) {
    if (timingSafeEqual(auth.slice(7).trim(), key)) ok = true;
  }

  if (ok) {
    // Only clear if there were prior failures (keep KV write ops low)
    return null;
  }

  // Only count as failure when caller actually attempted credentials (avoid browser initial 401 prompt eating quota)
  if (auth) {
    await recordAuthFailure(env, 'auth', ip);
  }

  return new Response('KlipForge Protected — Masukkan Access Key (username bebas, password = ACCESS_KEY)', {
    status: 401,
    headers: {
      'WWW-Authenticate': 'Basic realm="KlipForge Studio", charset="UTF-8"',
      'Content-Type': 'text/plain; charset=utf-8',
      'Cache-Control': 'no-store',
    },
  });
}

async function resolveEngineOrigin(env) {
  // Strictly resolve ONLY from trusted KV STATE or env.ENGINE_ORIGIN (NO client header override!)
  if (env.STATE) {
    const kvOrigin = await env.STATE.get('engine_origin');
    if (kvOrigin && /^https:\/\/[a-z0-9.-]+$/i.test(kvOrigin)) {
      return kvOrigin.replace(/\/+$/, '');
    }
  }
  const fallback = (env.ENGINE_ORIGIN || '').trim();
  if (/^https:\/\/[a-z0-9.-]+$/i.test(fallback)) {
    return fallback.replace(/\/+$/, '');
  }
  return '';
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const ip = getClientIP(request);

    // Bridge registration endpoint (called automatically by scripts/online.ps1 with X-Bridge-Secret)
    if (url.pathname === '/edge-register' && request.method === 'POST') {
      if (await isRateLimited(env, 'reg', ip)) {
        return Response.json({ error: 'Rate limit exceeded' }, { status: 429, headers: { 'Retry-After': String(AUTH_WINDOW_SEC) } });
      }
      const expected = (env.BRIDGE_SECRET || '').trim();
      const provided = (request.headers.get('X-Bridge-Secret') || '').trim();
      if (!expected || !timingSafeEqual(provided, expected)) {
        await recordAuthFailure(env, 'reg', ip);
        return Response.json({ error: 'Unauthorized bridge secret' }, { status: 401 });
      }
      await clearAuthFailures(env, 'reg', ip);
      if (!env.STATE) {
        return Response.json({ error: 'KV STATE binding belum terpasang' }, { status: 500 });
      }
      try {
        const body = await request.json();
        const origin = String(body.origin || '').trim().replace(/\/+$/, '');
        if (!/^https:\/\/[a-z0-9-]+\.trycloudflare\.com$/i.test(origin) && !/^https:\/\/[a-z0-9.-]+$/i.test(origin)) {
          return Response.json({ error: 'Origin HTTPS tidak valid' }, { status: 400 });
        }
        await env.STATE.put('engine_origin', origin);
        await env.STATE.put('engine_updated_at', new Date().toISOString());
        return Response.json({ ok: true, origin });
      } catch {
        return Response.json({ error: 'Invalid JSON body' }, { status: 400 });
      }
    }

    // Optional scoped Agent Key shortcut for /api/* (lets assistant agents like Muse hit klip.breakthematrix.id/api/*
    // directly with X-Agent-Key — strictly least-privilege, Worker NEVER injects admin X-Engine-Token for agent calls!)
    let isScopedAgent = false;
    const providedAgentKey = (request.headers.get('X-Agent-Key') || '').trim();
    if (url.pathname.startsWith('/api/') && providedAgentKey) {
      if (await isRateLimited(env, 'agent', ip)) {
        return Response.json({ error: 'Rate limit exceeded' }, { status: 429, headers: { 'Retry-After': String(AUTH_WINDOW_SEC) } });
      }
      const expectedAgent = (env.AGENT_KEY || '').trim();
      if (!expectedAgent || !timingSafeEqual(providedAgentKey, expectedAgent)) {
        await recordAuthFailure(env, 'agent', ip);
        return Response.json({ error: 'Unauthorized X-Agent-Key' }, { status: 401 });
      }
      isScopedAgent = true;
    }

    // Mandatory Access Key Gate for ALL UI, Health, and non-agent API routes
    if (!isScopedAgent) {
      const authErr = await checkAuth(request, env);
      if (authErr) return authErr;
    }

    // Health probe for edge worker itself (authenticated)
    if (url.pathname === '/edge-health') {
      const origin = await resolveEngineOrigin(env);
      const updatedAt = env.STATE ? await env.STATE.get('engine_updated_at') : null;
      return Response.json({
        ok: true,
        app: env.APP_NAME || 'KlipForge',
        host: url.host,
        engineConfigured: Boolean(origin),
        engineTokenConfigured: Boolean(env.ENGINE_TOKEN),
        agentKeyConfigured: Boolean(env.AGENT_KEY),
        engineUpdatedAt: updatedAt,
      });
    }

    // Proxy all /api/* traffic to the Go+FFmpeg+yt-dlp Engine
    if (url.pathname.startsWith('/api/')) {
      const engineOrigin = await resolveEngineOrigin(env);
      if (!engineOrigin) {
        return Response.json(
          {
            error: 'Go Engine belum terhubung. Jalankan start-online.bat di PC kamu!',
            engineOffline: true,
          },
          { status: 503, headers: { 'Cache-Control': 'no-store' } }
        );
      }

      const targetUrl = `${engineOrigin}${url.pathname}${url.search}`;
      const headers = new Headers(request.headers);
      // Security: NEVER leak Basic Auth credentials, forged tokens, or allow client origin overrides upstream!
      headers.delete('authorization');
      headers.delete('x-klipforge-engine');
      headers.delete('x-engine-token');
      headers.delete('host');
      headers.delete('cf-connecting-ip');
      headers.delete('cf-ray');
      headers.set('X-Forwarded-Host', url.host);
      headers.set('X-Forwarded-Proto', 'https');
      if (isScopedAgent) {
        // Scoped agent path: forward ONLY verified X-Agent-Key (Go Engine enforces read/search/render-only 403 guard!)
        headers.set('X-Agent-Key', providedAgentKey);
      } else {
        // Full admin path (Basic Auth verified): strip any agent key and inject trusted Worker -> Engine admin token
        headers.delete('x-agent-key');
        if (env.ENGINE_TOKEN) {
          headers.set('X-Engine-Token', env.ENGINE_TOKEN);
        }
      }

      const isBodyMethod = request.method !== 'GET' && request.method !== 'HEAD';
      try {
        const upstream = await fetch(targetUrl, {
          method: request.method,
          headers,
          body: isBodyMethod ? request.body : undefined,
          redirect: 'manual',
        });

        const ct = upstream.headers.get('content-type') || '';
        if ((upstream.status === 530 || upstream.status === 502 || upstream.status === 503) && ct.includes('text/html')) {
          return Response.json(
            {
              error: 'Go Engine (FFmpeg + yt-dlp) sedang offline. Jalankan start-online.bat di PC atau Docker di VPS kamu!',
              engineOffline: true,
            },
            { status: 503, headers: { 'Cache-Control': 'no-store' } }
          );
        }

        const respHeaders = new Headers(upstream.headers);
        respHeaders.set('X-KlipForge-Edge', 'cloudflare');
        if (url.pathname === '/api/events') {
          respHeaders.set('Cache-Control', 'no-cache');
        }
        return new Response(upstream.body, {
          status: upstream.status,
          statusText: upstream.statusText,
          headers: respHeaders,
        });
      } catch (err) {
        return Response.json(
          {
            error: 'Tidak dapat terhubung ke Go Engine. Pastikan start-online.bat sedang berjalan!',
            engineOffline: true,
            detail: String(err && err.message ? err.message : err),
          },
          { status: 503, headers: { 'Cache-Control': 'no-store' } }
        );
      }
    }

    // Serve static Dashboard UI from Cloudflare Edge Assets
    if (env.ASSETS) {
      return env.ASSETS.fetch(request);
    }
    return new Response('KlipForge Assets not bound', { status: 500 });
  },
};
