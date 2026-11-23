/**
 * KlipForge Cloudflare Edge Worker (klip.breakthematrix.id) — Hardened Team + Official InstaBox Auto-Wake Edition
 * Security & Reliability Controls:
 * - MANDATORY ACCESS_KEY (Master Owner) OR Personal Team AccessKey (synced in KV team_keys)
 * - Per-Member Scoped X-Agent-Key (Owner AGENT_KEY OR Team Member personal Muse AgentKey)
 * - Per-IP Brute-Force Rate Limiting via Workers KV (max 10 failed attempts / 5 min -> 429)
 * - Official InstaBox Scale-to-Zero Auto-Wake (GET/POST /v1/machines/{id}/resume|start) + Transparent Cooldown Countdown
 *   + Optional PC Fallback origin during InstaBox Free Plan cooldown windows!
 * - Strips Authorization & forged internal headers before upstream proxy
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

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
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

async function resolveFallbackOrigin(env) {
  if (!env.STATE) return '';
  const fb = await env.STATE.get('fallback_origin');
  if (fb && /^https:\/\/[a-z0-9.-]+(:\d+)?$/i.test(fb)) {
    return fb.replace(/\/+$/, '');
  }
  return '';
}

// Official InstaBox API helper (Scale-to-Zero Auto-Wake + Transparent Cooldown Detection)
async function getInstaboxMachine(env) {
  const apiKey = (env.INSTABOX_API_KEY || '').trim();
  const machineId = (env.INSTABOX_MACHINE_ID || '').trim();
  if (!apiKey || !machineId) return null;
  try {
    const resp = await fetch(`https://api.instabox.sh/v1/machines/${encodeURIComponent(machineId)}`, {
      headers: { 'X-API-Key': apiKey, Accept: 'application/json' },
    });
    if (!resp.ok) return null;
    const data = await resp.json();
    return data && data.machine ? data.machine : data;
  } catch {
    return null;
  }
}

async function wakeInstaboxOnDemand(env) {
  const apiKey = (env.INSTABOX_API_KEY || '').trim();
  const machineId = (env.INSTABOX_MACHINE_ID || '').trim();
  if (!apiKey || !machineId) return { configured: false };

  const machine = await getInstaboxMachine(env);
  if (!machine) return { configured: true, woke: false, state: 'unknown' };

  // Respect official Free Plan cooldown window transparently (never spam/bypass!)
  if (machine.cooldownUntil) {
    const untilMs = Date.parse(machine.cooldownUntil);
    const nowMs = Date.now();
    if (Number.isFinite(untilMs) && untilMs > nowMs) {
      const retryAfterSec = Math.max(30, Math.ceil((untilMs - nowMs) / 1000));
      return {
        configured: true,
        woke: false,
        inCooldown: true,
        state: machine.state || 'suspended',
        cooldownUntil: machine.cooldownUntil,
        retryAfterSec,
      };
    }
  }

  const st = String(machine.state || '').toLowerCase();
  if (st === 'running') {
    return { configured: true, woke: true, state: 'running' };
  }

  // Trigger official resume (from suspended) or start (from stopped)
  const action = st === 'stopped' ? 'start' : 'resume';
  try {
    await fetch(`https://api.instabox.sh/v1/machines/${encodeURIComponent(machineId)}/${action}`, {
      method: 'POST',
      headers: {
        'X-API-Key': apiKey,
        'Idempotency-Key': `kf-wake-${Math.floor(Date.now() / 15000)}`,
      },
    });
  } catch {}

  // Poll up to ~14s until InstaBox reaches running state (suspend resume usually takes 2-5s!)
  for (let i = 0; i < 10; i++) {
    await sleep(1400);
    const cur = await getInstaboxMachine(env);
    if (cur && String(cur.state || '').toLowerCase() === 'running' && cur.runtime?.ready !== false) {
      await sleep(800);
      return { configured: true, woke: true, state: 'running' };
    }
    if (cur && cur.cooldownUntil && Date.parse(cur.cooldownUntil) > Date.now()) {
      const retryAfterSec = Math.max(30, Math.ceil((Date.parse(cur.cooldownUntil) - Date.now()) / 1000));
      return {
        configured: true,
        woke: false,
        inCooldown: true,
        state: cur.state || 'suspended',
        cooldownUntil: cur.cooldownUntil,
        retryAfterSec,
      };
    }
  }

  return { configured: true, woke: false, waking: true, state: 'starting' };
}

function formatWIB(isoStr) {
  try {
    const d = new Date(isoStr);
    const wib = new Date(d.getTime() + 7 * 3600 * 1000);
    const hh = String(wib.getUTCHours()).padStart(2, '0');
    const mm = String(wib.getUTCMinutes()).padStart(2, '0');
    return `${hh}:${mm} WIB`;
  } catch {
    return isoStr;
  }
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const ip = getClientIP(request);

    // 1. Bridge registration endpoint (InstaBox permanent port, VPS HTTPS, or PC Quick Tunnel)
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
        const role = String(body.role || 'primary').trim().toLowerCase();
        if (role === 'fallback' || role === 'pc_fallback') {
          await env.STATE.put('fallback_origin', origin);
          return Response.json({ ok: true, role: 'fallback', origin });
        }
        await env.STATE.put('engine_origin', origin);
        await env.STATE.put('engine_updated_at', new Date().toISOString());
        return Response.json({ ok: true, role: 'primary', origin });
      } catch {
        return Response.json({ error: 'Invalid JSON body' }, { status: 400 });
      }
    }

    // 2. Team Keys Sync endpoint
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

    // 3. Scoped Agent Key shortcut for /api/* (Owner's Muse OR Team Member's personal Muse!)
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

    // 4. Mandatory Access Key Gate
    let authResult = null;
    if (!isScopedAgent) {
      authResult = await checkAuth(request, env, teamKeys);
      if (authResult.response) return authResult.response;
    }

    // Health probe (includes official InstaBox machine state when configured)
    if (url.pathname === '/edge-health') {
      const origin = await resolveEngineOrigin(env);
      const fallbackOrigin = await resolveFallbackOrigin(env);
      const updatedAt = env.STATE ? await env.STATE.get('engine_updated_at') : null;
      const teamUpdatedAt = env.STATE ? await env.STATE.get('team_updated_at') : null;
      const ib = await getInstaboxMachine(env);
      return Response.json({
        ok: true,
        app: env.APP_NAME || 'KlipForge',
        host: url.host,
        engineConfigured: Boolean(origin),
        fallbackConfigured: Boolean(fallbackOrigin),
        engineTokenConfigured: Boolean(env.ENGINE_TOKEN),
        agentKeyConfigured: Boolean(env.AGENT_KEY),
        instaboxAutoWakeConfigured: Boolean(env.INSTABOX_API_KEY && env.INSTABOX_MACHINE_ID),
        instabox: ib ? {
          id: ib.id,
          state: ib.state,
          ready: Boolean(ib.runtime?.ready),
          cooldownUntil: ib.cooldownUntil || null,
          maxRunMinutes: ib.maxRunMinutes,
        } : null,
        teamAccessCount: Object.keys(teamKeys.access).length,
        teamAgentCount: Object.keys(teamKeys.agent).length,
        engineUpdatedAt: updatedAt,
        teamUpdatedAt,
      });
    }

    // 5. Proxy /api/* to Go Engine (with Official InstaBox Auto-Wake + Optional PC Fallback)
    if (url.pathname.startsWith('/api/')) {
      const engineOrigin = await resolveEngineOrigin(env);
      if (!engineOrigin) {
        return Response.json(
          {
            error: 'Go Engine belum terhubung. Jalankan start-online.bat di PC atau Docker/InstaBox kamu!',
            engineOffline: true,
          },
          { status: 503, headers: { 'Cache-Control': 'no-store' } }
        );
      }

      const buildUpstreamHeaders = () => {
        const headers = new Headers(request.headers);
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
          headers.set('X-Agent-Key', providedAgentKey);
        } else if (authResult && authResult.isMaster) {
          if (env.ENGINE_TOKEN) headers.set('X-Engine-Token', env.ENGINE_TOKEN);
        } else if (authResult && authResult.accessKey) {
          headers.set('X-Access-Key', authResult.accessKey);
        }
        return headers;
      };

      const isBodyMethod = request.method !== 'GET' && request.method !== 'HEAD';
      // Buffer small JSON POST bodies so we can cleanly retry once if Auto-Wake wakes InstaBox!
      let bufferedBody = undefined;
      if (isBodyMethod) {
        const cl = parseInt(request.headers.get('content-length') || '0', 10);
        const ct = (request.headers.get('content-type') || '').toLowerCase();
        if (ct.includes('application/json') && cl >= 0 && cl <= 1024 * 1024) {
          bufferedBody = await request.arrayBuffer();
        }
      }

      const forwardTo = async (originUrl) => {
        const targetUrl = `${originUrl}${url.pathname}${url.search}`;
        return fetch(targetUrl, {
          method: request.method,
          headers: buildUpstreamHeaders(),
          body: isBodyMethod ? (bufferedBody !== undefined ? bufferedBody.slice(0) : request.body) : undefined,
          redirect: 'manual',
        });
      };

      const isUpstreamDown = (resp) => {
        if (!resp) return true;
        const ct = (resp.headers.get('content-type') || '').toLowerCase();
        if ((resp.status === 530 || resp.status === 502 || resp.status === 503 || resp.status === 504) && !ct.includes('application/json')) {
          return true;
        }
        return false;
      };

      const wrapResponse = (upstream, viaLabel) => {
        const respHeaders = new Headers(upstream.headers);
        respHeaders.set('X-KlipForge-Edge', viaLabel || 'cloudflare');
        if (url.pathname === '/api/events') {
          respHeaders.set('Cache-Control', 'no-cache');
        }
        return new Response(upstream.body, {
          status: upstream.status,
          statusText: upstream.statusText,
          headers: respHeaders,
        });
      };

      let upstream = null;
      let fetchErr = null;
      try {
        upstream = await forwardTo(engineOrigin);
      } catch (e) {
        fetchErr = e;
      }

      if (!fetchErr && !isUpstreamDown(upstream)) {
        return wrapResponse(upstream, 'cloudflare');
      }

      // Primary origin unreachable -> try Official InstaBox Auto-Wake (Scale-to-Zero!)
      const canRetryBody = !isBodyMethod || bufferedBody !== undefined;
      const wake = await wakeInstaboxOnDemand(env);
      if (wake.configured && wake.woke && canRetryBody) {
        try {
          const retryResp = await forwardTo(engineOrigin);
          if (!isUpstreamDown(retryResp)) {
            return wrapResponse(retryResp, 'cloudflare-autowake');
          }
        } catch {}
      }

      // If InstaBox is in official cooldown (or waking), optionally try fallback PC origin if configured!
      const fbOrigin = await resolveFallbackOrigin(env);
      if (fbOrigin && fbOrigin !== engineOrigin && canRetryBody) {
        try {
          const fbResp = await forwardTo(fbOrigin);
          if (!isUpstreamDown(fbResp)) {
            return wrapResponse(fbResp, 'cloudflare-fallback');
          }
        } catch {}
      }

      if (wake.configured && wake.inCooldown) {
        const mins = Math.max(1, Math.ceil(wake.retryAfterSec / 60));
        return Response.json(
          {
            error: `⏳ Server InstaBox sedang jeda pendinginan (Free Plan) — aktif otomatis jam ${formatWIB(wake.cooldownUntil)} (sisa ~${mins} menit).`,
            engineOffline: true,
            instaboxState: 'cooldown',
            cooldownUntil: wake.cooldownUntil,
            retryAfterSec: wake.retryAfterSec,
          },
          {
            status: 503,
            headers: {
              'Cache-Control': 'no-store',
              'Retry-After': String(wake.retryAfterSec),
            },
          }
        );
      }

      if (wake.configured && wake.waking) {
        return Response.json(
          {
            error: '⚡ Server InstaBox sedang dibangunkan otomatis (~5–10 detik). Refresh atau coba lagi sebentar lagi!',
            engineOffline: true,
            instaboxState: 'starting',
            retryAfterSec: 8,
          },
          {
            status: 503,
            headers: { 'Cache-Control': 'no-store', 'Retry-After': '8' },
          }
        );
      }

      return Response.json(
        {
          error: 'Go Engine (FFmpeg + yt-dlp) sedang offline. Pastikan InstaBox atau start-online.bat sedang berjalan!',
          engineOffline: true,
          detail: fetchErr ? String(fetchErr.message || fetchErr) : undefined,
        },
        { status: 503, headers: { 'Cache-Control': 'no-store' } }
      );
    }

    // Serve static Dashboard UI from Cloudflare Edge Assets
    if (env.ASSETS) {
      return env.ASSETS.fetch(request);
    }
    return new Response('KlipForge Assets not bound', { status: 500 });
  },
};
