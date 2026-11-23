/**
 * KlipForge Cloudflare Edge Worker (klip.breakthematrix.id) — Hardened Team Edition
 * Security Controls:
 * - MANDATORY ACCESS_KEY (Master Owner) OR Personal Team AccessKey (synced in KV team_keys)
 * - Per-Member Scoped X-Agent-Key (Owner AGENT_KEY OR Team Member personal Muse AgentKey)
 * - Per-IP Brute-Force Rate Limiting via Workers KV (max 10 failed attempts / 5 min -> 429)
 * - ZERO open-proxy header override (X-KlipForge-Engine completely removed)
 * - Strips Authorization & forged X-Engine-Token / X-Access-Key / X-Agent-Key before upstream proxy
 * - Master Owner -> injects trusted X-Engine-Token; Team Member Web -> forwards ONLY verified X-Access-Key;
 *   Muse Agent -> forwards ONLY verified X-Agent-Key (Go Engine strictly enforces RBAC + createdBy attribution!)
 * - Dynamic Engine Bridge via Workers KV (POST /edge-register & POST /edge-sync-team with X-Bridge-Secret)
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

async function loadTeamKeys(env) {
  if (!env.STATE) return { access: {}, agent: {} };
  try {
    const raw = await env.STATE.get('team_keys');
    if (!raw) return { access: {}, agent: {} };
    const parsed = JSON.parse(raw);
    return {
      access: parsed && typeof parsed.access === 'object' && parsed.access ? parsed.access : {},
      agent: parsed && typeof parsed.agent === 'object' && parsed.agent ? parsed.agent : {},
    };
  } catch {
    return { access: {}, agent: {} };
  }
}

function matchKeyInMap(keyMap, provided) {
  const p = String(provided || '').trim();
  if (!p || !keyMap) return null;
  let matched = null;
  for (const [k, info] of Object.entries(keyMap)) {
    if (timingSafeEqual(p, k)) {
      matched = { key: k, info };
    }
  }
  return matched;
}

async function checkAuth(request, env, teamKeys) {
  const masterKey = (env.ACCESS_KEY || '').trim();
  if (!masterKey) {
    return {
      response: new Response(
        'KlipForge Locked: ACCESS_KEY belum dikonfigurasi di Cloudflare Worker secrets. Jalankan scripts/set-access-key.ps1.',
        { status: 503, headers: { 'Content-Type': 'text/plain; charset=utf-8', 'Cache-Control': 'no-store' } }
      ),
    };
  }

  const ip = getClientIP(request);
  if (await isRateLimited(env, 'auth', ip)) {
    return {
      response: new Response('Terlalu banyak percobaan login gagal. Coba lagi dalam 5 menit.', {
        status: 429,
        headers: {
          'Content-Type': 'text/plain; charset=utf-8',
          'Retry-After': String(AUTH_WINDOW_SEC),
          'Cache-Control': 'no-store',
        },
      }),
    };
  }

  const auth = request.headers.get('Authorization') || '';
  let provided = '';
  if (auth.startsWith('Basic ')) {
    try {
      const decoded = atob(auth.slice(6));
      const idx = decoded.indexOf(':');
      provided = (idx >= 0 ? decoded.slice(idx + 1) : decoded).trim();
    } catch {}
  } else if (auth.startsWith('Bearer ')) {
    provided = auth.slice(7).trim();
  }

  if (provided) {
    if (timingSafeEqual(provided, masterKey)) {
      return { ok: true, isMaster: true, accessKey: '' };
    }
    const teamMatch = matchKeyInMap(teamKeys.access, provided);
    if (teamMatch) {
      return { ok: true, isMaster: false, accessKey: teamMatch.key, member: teamMatch.info };
    }
    await recordAuthFailure(env, 'auth', ip);
  }

  return {
    response: new Response('KlipForge Protected — Masukkan Access Key (username bebas, password = Access Key kamu)', {
      status: 401,
      headers: {
        'WWW-Authenticate': 'Basic realm="KlipForge Studio", charset="UTF-8"',
        'Content-Type': 'text/plain; charset=utf-8',
        'Cache-Control': 'no-store',
      },
    }),
  };
}

async function resolveEngineOrigin(env) {
  if (env.STATE) {
    const kvOrigin = await env.STATE.get('engine_origin');
    if (kvOrigin && /^https:\/\/[a-z0-9.-]+(:\d+)?$/i.test(kvOrigin)) {
      return kvOrigin.replace(/\/+$/, '');
    }
  }
  const fallback = (env.ENGINE_ORIGIN || '').trim();
  if (/^https:\/\/[a-z0-9.-]+(:\d+)?$/i.test(fallback)) {
    return fallback.replace(/\/+$/, '');
  }
  return '';
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const ip = getClientIP(request);

    // 1. Bridge registration endpoint (PC Quick Tunnel or VPS HTTPS origin)
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
        if (!/^https:\/\/[a-z0-9.-]+(:\d+)?$/i.test(origin)) {
          return Response.json({ error: 'Origin HTTPS tidak valid' }, { status: 400 });
        }
        await env.STATE.put('engine_origin', origin);
        await env.STATE.put('engine_updated_at', new Date().toISOString());
        return Response.json({ ok: true, origin });
      } catch {
        return Response.json({ error: 'Invalid JSON body' }, { status: 400 });
      }
    }

    // 2. Team Keys Sync endpoint (called automatically by Go Engine when owner adds/edits/revokes team members)
    if (url.pathname === '/edge-sync-team' && request.method === 'POST') {
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
        const clean = {
          access: body && typeof body.access === 'object' && body.access ? body.access : {},
          agent: body && typeof body.agent === 'object' && body.agent ? body.agent : {},
        };
        await env.STATE.put('team_keys', JSON.stringify(clean));
        await env.STATE.put('team_updated_at', new Date().toISOString());
        return Response.json({
          ok: true,
          accessCount: Object.keys(clean.access).length,
          agentCount: Object.keys(clean.agent).length,
        });
      } catch {
        return Response.json({ error: 'Invalid JSON body' }, { status: 400 });
      }
    }

    const teamKeys = await loadTeamKeys(env);

    // 3. Scoped Agent Key shortcut for /api/* (Owner's Muse OR any Team Member's personal Muse!)
    // Strictly least-privilege: Worker NEVER injects admin X-Engine-Token for agent calls!
    let isScopedAgent = false;
    const providedAgentKey = (request.headers.get('X-Agent-Key') || '').trim();
    if (url.pathname.startsWith('/api/') && providedAgentKey) {
      if (await isRateLimited(env, 'agent', ip)) {
        return Response.json({ error: 'Rate limit exceeded' }, { status: 429, headers: { 'Retry-After': String(AUTH_WINDOW_SEC) } });
      }
      const expectedMasterAgent = (env.AGENT_KEY || '').trim();
      const masterOk = Boolean(expectedMasterAgent) && timingSafeEqual(providedAgentKey, expectedMasterAgent);
      const memberAgentMatch = !masterOk ? matchKeyInMap(teamKeys.agent, providedAgentKey) : null;
      if (!masterOk && !memberAgentMatch) {
        await recordAuthFailure(env, 'agent', ip);
        return Response.json({ error: 'Unauthorized X-Agent-Key' }, { status: 401 });
      }
      isScopedAgent = true;
    }

    // 4. Mandatory Access Key Gate (Master Owner OR Team Member personal AccessKey)
    let authResult = null;
    if (!isScopedAgent) {
      authResult = await checkAuth(request, env, teamKeys);
      if (authResult.response) return authResult.response;
    }

    // Health probe for edge worker itself (authenticated)
    if (url.pathname === '/edge-health') {
      const origin = await resolveEngineOrigin(env);
      const updatedAt = env.STATE ? await env.STATE.get('engine_updated_at') : null;
      const teamUpdatedAt = env.STATE ? await env.STATE.get('team_updated_at') : null;
      return Response.json({
        ok: true,
        app: env.APP_NAME || 'KlipForge',
        host: url.host,
        engineConfigured: Boolean(origin),
        engineTokenConfigured: Boolean(env.ENGINE_TOKEN),
        agentKeyConfigured: Boolean(env.AGENT_KEY),
        teamAccessCount: Object.keys(teamKeys.access).length,
        teamAgentCount: Object.keys(teamKeys.agent).length,
        engineUpdatedAt: updatedAt,
        teamUpdatedAt,
      });
    }

    // 5. Proxy all /api/* traffic to the Go+FFmpeg+yt-dlp Engine (PC Tunnel or VPS)
    if (url.pathname.startsWith('/api/')) {
      const engineOrigin = await resolveEngineOrigin(env);
      if (!engineOrigin) {
        return Response.json(
          {
            error: 'Go Engine belum terhubung. Jalankan start-online.bat di PC atau Docker di VPS kamu!',
            engineOffline: true,
          },
          { status: 503, headers: { 'Cache-Control': 'no-store' } }
        );
      }

      const targetUrl = `${engineOrigin}${url.pathname}${url.search}`;
      const headers = new Headers(request.headers);
      // Security: NEVER leak Basic Auth credentials or client-forged internal tokens upstream!
      headers.delete('authorization');
      headers.delete('x-klipforge-engine');
      headers.delete('x-engine-token');
      headers.delete('x-access-key');
      headers.delete('x-agent-key');
      headers.delete('host');
      headers.delete('cf-connecting-ip');
      headers.delete('cf-ray');
      headers.set('X-Forwarded-Host', url.host);
      headers.set('X-Forwarded-Proto', 'https');

      if (isScopedAgent) {
        // Scoped agent path: forward ONLY verified X-Agent-Key (Go Engine enforces agent RBAC + member attribution!)
        headers.set('X-Agent-Key', providedAgentKey);
      } else if (authResult && authResult.isMaster) {
        // Master Owner path: inject trusted Worker -> Engine admin token
        if (env.ENGINE_TOKEN) {
          headers.set('X-Engine-Token', env.ENGINE_TOKEN);
        }
      } else if (authResult && authResult.accessKey) {
        // Team Member web path: forward ONLY verified personal X-Access-Key (NEVER admin X-Engine-Token!)
        // Go Engine verifies against data/team.json for instant revocation & role enforcement!
        headers.set('X-Access-Key', authResult.accessKey);
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
            error: 'Tidak dapat terhubung ke Go Engine. Pastikan start-online.bat atau VPS Docker sedang berjalan!',
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
