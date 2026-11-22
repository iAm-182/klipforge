/**
 * KlipForge Cloudflare Edge Worker (klip.breakthematrix.id)
 * - Serves the KlipForge Studio Dashboard 24/7 globally from Cloudflare Assets
 * - Dynamic Engine Bridge via Workers KV (POST /edge-register with X-Bridge-Secret)
 *   so PC/VPS Quick Tunnel connects to klip.breakthematrix.id automatically in 1 click!
 * - Proxies /api/* (REST, multipart uploads, SSE stream, and Range video streaming)
 *   straight to the active Go+FFmpeg+yt-dlp Engine
 * - Optional Basic Auth protection if ACCESS_KEY secret is configured
 */

function checkAuth(request, env) {
  const key = env.ACCESS_KEY;
  if (!key) return null;

  const auth = request.headers.get('Authorization') || '';
  if (auth.startsWith('Basic ')) {
    try {
      const decoded = atob(auth.slice(6));
      const idx = decoded.indexOf(':');
      const pass = idx >= 0 ? decoded.slice(idx + 1) : decoded;
      if (pass === key) return null;
    } catch {}
  }
  if (auth === `Bearer ${key}`) return null;

  return new Response('KlipForge Protected — Masukkan Access Key', {
    status: 401,
    headers: {
      'WWW-Authenticate': 'Basic realm="KlipForge Studio", charset="UTF-8"',
      'Content-Type': 'text/plain; charset=utf-8',
    },
  });
}

async function resolveEngineOrigin(request, env) {
  const headerOrigin = request.headers.get('X-KlipForge-Engine');
  if (headerOrigin && /^https?:\/\//i.test(headerOrigin)) {
    return headerOrigin.replace(/\/+$/, '');
  }
  if (env.STATE) {
    const kvOrigin = await env.STATE.get('engine_origin');
    if (kvOrigin && /^https?:\/\//i.test(kvOrigin)) {
      return kvOrigin.replace(/\/+$/, '');
    }
  }
  return (env.ENGINE_ORIGIN || '').replace(/\/+$/, '');
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);

    // Bridge registration endpoint (called automatically by start-online.bat)
    if (url.pathname === '/edge-register' && request.method === 'POST') {
      const expected = env.BRIDGE_SECRET || '';
      const provided = request.headers.get('X-Bridge-Secret') || '';
      if (!expected || provided !== expected) {
        return Response.json({ error: 'Unauthorized bridge secret' }, { status: 401 });
      }
      if (!env.STATE) {
        return Response.json({ error: 'KV STATE binding belum terpasang' }, { status: 500 });
      }
      try {
        const body = await request.json();
        const origin = String(body.origin || '').trim().replace(/\/+$/, '');
        if (!/^https:\/\/[a-z0-9.-]+$/i.test(origin)) {
          return Response.json({ error: 'Origin HTTPS tidak valid' }, { status: 400 });
        }
        await env.STATE.put('engine_origin', origin);
        await env.STATE.put('engine_updated_at', new Date().toISOString());
        return Response.json({ ok: true, origin });
      } catch (e) {
        return Response.json({ error: 'Invalid JSON body' }, { status: 400 });
      }
    }

    // Optional Basic Auth gate for UI & API
    const authErr = checkAuth(request, env);
    if (authErr) return authErr;

    // Health probe for edge worker itself
    if (url.pathname === '/edge-health') {
      const origin = await resolveEngineOrigin(request, env);
      const updatedAt = env.STATE ? await env.STATE.get('engine_updated_at') : null;
      return Response.json({
        ok: true,
        app: env.APP_NAME || 'KlipForge',
        host: url.host,
        engineConfigured: Boolean(origin),
        engineUpdatedAt: updatedAt,
      });
    }

    // Proxy all /api/* traffic to the Go+FFmpeg+yt-dlp Engine
    if (url.pathname.startsWith('/api/')) {
      const engineOrigin = await resolveEngineOrigin(request, env);
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
      headers.delete('host');
      headers.delete('cf-connecting-ip');
      headers.delete('cf-ray');
      headers.set('X-Forwarded-Host', url.host);
      headers.set('X-Forwarded-Proto', 'https');

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
