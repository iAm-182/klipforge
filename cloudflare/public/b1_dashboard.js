(() => {
  'use strict';

  // ==================== B1 Dashboard + B2 Publish + B3 Draft Controller ====================
  // Complements existing Studio/Top-N/Campaign/Audio/Post-Edit engine in app.js with:
  // - Persistent top status strip (engine state + live cooldown countdown, disk gauge, fair queue)
  // - 5-item left rail + mobile bottom tab bar
  // - Beranda live state (4 KPIs w/ 7d delta, OpenCue Sedang Render w/ 30s frame peek, Butuh Kamu)
  // - Papan Kanban (6 columns, SortableJS drag-and-drop -> POST /api/board/move, WIP limit 3)
  // - Review queue sorted oldest-first w/ SLA aging + Review Sheet (Start/Peak frames side by side) + Batch Mode
  // - Tim screen (presence, turnaround, approval rate, 7d sparkline) + Persisted Activity Feed
  // - Library SOP 4-check Publish Checklist + Publish Kit modal + Jianying Draft export + Analytics

  const $ = (id) => document.getElementById(id);
  const esc = (s) => String(s || '').replace(/[&<>"']/g, (m) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[m]));

  const b1 = {
    activeTab: 'home',
    homeData: null,
    boardData: null,
    teamMetrics: null,
    activity: [],
    statusRaw: null,
    user: { id: 'owner', name: 'Owner', role: 'owner', via: 'web' },
    cooldownUntil: null,
    boardFilter: { member: 'all', projectId: 'all', momentType: 'all' },
    selectedBatchReview: new Set(),
    sortables: [],
    reviewSheetJob: null,
    selectedRejectReason: '',
    publishKitJob: null,
    publishModalJob: null,
    p0Inventory: null,
    p0Publishable: [],
    p0Ledger: { entries: [], summary: null },
    markPostedJob: null,
  };

  function getTok() {
    return (localStorage.getItem('kf_engine_token') || '').trim();
  }

  function applyAuthHeader(hdrs, tok) {
    if (!tok) return;
    if (tok.startsWith('kf_agt_')) {
      if (!hdrs.has('X-Agent-Key')) hdrs.set('X-Agent-Key', tok);
    } else if (tok.startsWith('kf_usr_')) {
      if (!hdrs.has('X-Access-Key')) hdrs.set('X-Access-Key', tok);
    } else if (!hdrs.has('X-Engine-Token')) {
      hdrs.set('X-Engine-Token', tok);
    }
  }

  async function apiB1(path, opts = {}) {
    const hdrs = new Headers(opts.headers || {});
    applyAuthHeader(hdrs, getTok());
    const res = await fetch(path, { ...opts, headers: hdrs });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
    return data;
  }

  function showToast(msg, type = 'info') {
    const box = $('toastBox');
    if (!box) return;
    const el = document.createElement('div');
    el.className = `toast ${type}`;
    el.textContent = msg;
    box.appendChild(el);
    setTimeout(() => el.remove(), 3600);
  }

  function fmtMMSS(sec) {
    const s = Math.max(0, Math.floor(sec || 0));
    const m = Math.floor(s / 60);
    const r = s % 60;
    return `${String(m).padStart(2, '0')}:${String(r).padStart(2, '0')}`;
  }

  function fmtAge(hours) {
    const h = Number(hours || 0);
    if (h < 1) return `${Math.max(1, Math.round(h * 60))}m`;
    if (h < 48) return `${Math.round(h)}j`;
    return `${Math.round(h / 24)}h`;
  }

  function initialsOf(name) {
    const parts = String(name || 'OW').trim().split(/\s+/);
    if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
    return String(name || 'OW').slice(0, 2).toUpperCase();
  }

  function isOwner() {
    const r = b1.user?.role || 'owner';
    return r === 'owner' || r === 'admin';
  }

  function canQC() {
    const r = b1.user?.role || 'owner';
    return r === 'owner' || r === 'admin' || r === 'qc';
  }

  // ==================== Navigation (Left Rail + Mobile Bottom Bar + Contextual Sheet) ====================
  const CONTEXTUAL_TABS = new Set(['studio', 'topn', 'campaigns', 'audio', 'autopilot', 'settings']);

  function openTab(tab) {
    b1.activeTab = tab;
    document.querySelectorAll('.tab-panel').forEach((p) => {
      p.classList.toggle('active', p.id === `tab-${tab}`);
    });
    document.querySelectorAll('[data-tab]').forEach((btn) => {
      btn.classList.toggle('active', btn.dataset.tab === tab);
    });
    if ($('btnMobMore')) {
      $('btnMobMore').classList.toggle('active', CONTEXTUAL_TABS.has(tab));
    }
    if ($('b1MobileMenuSheet')) {
      $('b1MobileMenuSheet').classList.add('hidden');
    }
    if (tab === 'home') refreshHome();
    if (tab === 'board') refreshBoard();
    if (tab === 'review') refreshReviewEnhancements();
    if (tab === 'queue') {
      enhanceLibraryCards();
      refreshP0Ops();
    }
    if (tab === 'team') refreshTeamAndActivity();
    if (tab === 'studio') window.dispatchEvent(new Event('resize'));
  }
  window.__kfSwitchTab = openTab;

  function wireNavigation() {
    document.querySelectorAll('.rail-nav-btn, .mob-nav-btn, .b1-mob-menu-item').forEach((btn) => {
      btn.addEventListener('click', () => {
        if (btn.dataset.tab) openTab(btn.dataset.tab);
      });
    });
    const mobMore = $('btnMobMore');
    const mobSheet = $('b1MobileMenuSheet');
    if (mobMore && mobSheet) {
      mobMore.addEventListener('click', () => mobSheet.classList.toggle('hidden'));
      mobSheet.addEventListener('click', (e) => {
        if (e.target === mobSheet) mobSheet.classList.add('hidden');
      });
    }
    if ($('btnCloseMobMenu') && mobSheet) {
      $('btnCloseMobMenu').addEventListener('click', () => mobSheet.classList.add('hidden'));
    }
  }

  // ==================== B1.1 Persistent Status Strip + Live Cooldown Countdown ====================
  function updateStatusStrip() {
    const pill = $('stripEnginePill');
    const txt = $('stripEngineText');
    if (pill && txt) {
      const now = Date.now();
      if (b1.cooldownUntil && b1.cooldownUntil > now) {
        const rem = Math.ceil((b1.cooldownUntil - now) / 1000);
        pill.className = 'engine-pill cooldown';
        txt.textContent = `Cooldown ${fmtMMSS(rem)}`;
      } else if (b1.statusRaw?.deps?.ready) {
        pill.className = 'engine-pill awake';
        txt.textContent = 'Bangun';
      } else if (b1.statusRaw && b1.statusRaw.deps) {
        pill.className = 'engine-pill cooldown';
        txt.textContent = 'Tools Kurang';
      } else {
        pill.className = 'engine-pill asleep';
        txt.textContent = 'Tidur';
      }
    }

    const disk = b1.statusRaw?.disk;
    if (disk && $('stripDiskText') && $('stripDiskFill') && $('stripDiskGauge')) {
      const free = Number(disk.freeGb || 0);
      const total = Math.max(1, Number(disk.totalGb || 1));
      const usedPct = Math.min(100, Math.max(2, Math.round(((total - free) / total) * 100)));
      $('stripDiskText').textContent = `${free.toFixed(1)} GB tersisa`;
      $('stripDiskFill').style.width = `${usedPct}%`;
      $('stripDiskGauge').classList.toggle('low', free < 5.0 || !!disk.lowDisk);
    }

    const ch = b1.statusRaw?.cookieHealth;
    const cPill = $('stripCookiePill');
    const cTxt = $('stripCookieText');
    if (ch && cPill && cTxt) {
      if (ch.state === 'healthy') {
        cPill.className = 'engine-pill awake';
        cTxt.textContent = 'Cookie YT Aktif';
      } else if (ch.state === 'expired' || ch.state === 'missing') {
        cPill.className = 'engine-pill cooldown';
        cTxt.textContent = ch.state === 'expired' ? 'Cookie Expired' : 'Cookie Kosong';
      } else {
        cPill.className = 'engine-pill asleep';
        cTxt.textContent = 'Cookie YT Cek...';
      }
      cPill.title = ch.message || 'Status YouTube cookie keep-alive di server';
    }

    const mq = b1.homeData?.myQueue;
    if (mq && $('stripQueueMeter')) {
      $('stripQueueMeter').innerHTML = `Antrean kamu <strong>${mq.active}/${mq.limit}</strong>`;
      $('stripQueueMeter').classList.toggle('full', mq.active >= mq.limit);
    }

    if (b1.user && $('stripMemberBadge')) {
      const roleLbl = b1.user.role || 'owner';
      $('stripMemberBadge').innerHTML = `${esc(b1.user.name || 'Owner')} <span class="strip-role">${esc(roleLbl)}</span>`;
    }
  }

  // Tick countdown & active render elapsed clocks every 1s
  setInterval(() => {
    updateStatusStrip();
    document.querySelectorAll('[data-elapsed-start]').forEach((el) => {
      const startMs = Number(el.dataset.elapsedStart || 0);
      if (startMs > 0) el.textContent = fmtMMSS((Date.now() - startMs) / 1000);
    });
  }, 1000);

  // Refresh live frame peek thumbnails every 30s on Beranda
  setInterval(() => {
    if (b1.activeTab === 'home' && b1.homeData?.activeJobs?.length > 0) {
      refreshHome();
    }
  }, 30000);

  // ==================== B1.5 Beranda (KPI + Sedang Render + Butuh Kamu) ====================
  async function refreshHome() {
    try {
      const [hm, st] = await Promise.all([
        apiB1('/api/metrics/home'),
        apiB1('/api/status'),
      ]);
      b1.homeData = hm;
      b1.statusRaw = st;
      if (st.user) b1.user = st.user;
      b1.cooldownUntil = hm.cooldownUntil ? Date.parse(hm.cooldownUntil) : null;
      updateStatusStrip();
      renderHomeLive();
    } catch (e) {
      updateStatusStrip();
    }
  }

  function renderDelta(val, unit = '') {
    const n = Number(val || 0);
    const sign = n > 0 ? '+' : '';
    const cls = n > 0 ? 'up' : (n < 0 ? 'down' : '');
    return `<span class="b1-kpi-delta ${cls}">${sign}${n}${unit} vs 7h lalu</span>`;
  }

  function renderHomeLive() {
    const hm = b1.homeData;
    if (!hm) return;
    const k = hm.kpis || {};
    if ($('kpiRendered7d')) $('kpiRendered7d').textContent = String(k.rendered7d?.value ?? 0);
    if ($('kpiRenderedDelta')) $('kpiRenderedDelta').innerHTML = renderDelta(k.rendered7d?.delta);
    if ($('kpiApproved7d')) $('kpiApproved7d').textContent = String(k.approved7d?.value ?? 0);
    if ($('kpiApprovedDelta')) $('kpiApprovedDelta').innerHTML = renderDelta(k.approved7d?.delta);
    if ($('kpiPosted7d')) $('kpiPosted7d').textContent = String(k.posted7d?.value ?? 0);
    if ($('kpiPostedDelta')) $('kpiPostedDelta').innerHTML = renderDelta(k.posted7d?.delta);
    if ($('kpiAvgRender')) $('kpiAvgRender').textContent = `${Math.round(k.avgRenderSec?.value || 0)}s`;
    if ($('kpiAvgDelta')) $('kpiAvgDelta').innerHTML = renderDelta(Math.round(k.avgRenderSec?.delta || 0), 's');

    const pendCount = (hm.pendingReviews || []).length;
    if ($('railReviewBadge')) {
      $('railReviewBadge').textContent = String(pendCount);
      $('railReviewBadge').classList.toggle('warn', pendCount > 0);
    }
    if ($('mobReviewDot')) {
      $('mobReviewDot').textContent = String(pendCount);
      $('mobReviewDot').classList.toggle('hidden', pendCount === 0);
    }

    // Sedang render panel (OpenCue model + 30s frame peek)
    const activeBox = $('homeActiveRenders');
    if (activeBox) {
      const list = hm.activeJobs || [];
      if (list.length === 0) {
        activeBox.innerHTML = '<div class="muted text-xs" style="padding:12px;">Tidak ada job yang sedang dirender saat ini.</div>';
      } else {
        const ts = Date.now();
        activeBox.innerHTML = list.map((j) => {
          const startedMs = Date.now() - Math.round((j.elapsedSec || 0) * 1000);
          return `
            <div class="peek-card">
              <div class="peek-thumb-wrap">
                <img src="${esc(j.peekUrl)}?t=${ts}" alt="Peek" onerror="this.style.display='none';this.parentElement.textContent='Menyiapkan frame';" />
              </div>
              <div>
                <div class="peek-title">${esc(j.title)}</div>
                <div class="peek-meta">
                  <span>W${j.workerId || 1}</span>
                  <span>Antrean #${j.queuePosition}</span>
                  <span>${esc(j.stage || j.status)}</span>
                  <span data-elapsed-start="${startedMs}">${fmtMMSS(j.elapsedSec)}</span>
                  <span>${Math.round(j.progress || 0)}%</span>
                </div>
                <div class="peek-bar"><div class="peek-bar-fill" style="width:${Math.round(j.progress || 0)}%"></div></div>
              </div>
            </div>
          `;
        }).join('');
      }
    }

    // Butuh kamu list (oldest pending review first, 1-tap Approve / Sheet)
    const butuhBox = $('homeButuhKamu');
    if (butuhBox) {
      const list = (hm.pendingReviews || []).slice(0, 8);
      if (list.length === 0) {
        butuhBox.innerHTML = '<div class="muted text-xs" style="padding:12px;">Antrean review bersih — tidak ada klip menunggu keputusan.</div>';
      } else {
        butuhBox.innerHTML = '';
        list.forEach((item) => {
          const selfBlocked = !isOwner() && item.createdById && item.createdById === b1.user?.id;
          const row = document.createElement('div');
          row.className = 'butuh-row';
          row.innerHTML = `
            <div style="min-width:0;">
              <div style="font-weight:600;font-size:12.5px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">${esc(item.title)}</div>
              <div class="text-xs muted" style="display:flex;gap:6px;align-items:center;margin-top:2px;">
                <span class="sla-badge ${item.slaLevel}">${fmtAge(item.ageHours)}</span>
                <span>${esc(item.createdBy || 'Owner')} (${esc(item.createdVia || 'web')})</span>
              </div>
            </div>
            <div style="display:flex;gap:5px;flex-shrink:0;">
              ${canQC() ? `<button class="btn btn-2xs btn-primary btn-quick-app" ${selfBlocked ? 'disabled title="Self-approve diblokir"' : ''}>Setujui</button>` : ''}
              <button class="btn btn-2xs btn-secondary btn-quick-sheet">Buka</button>
            </div>
          `;
          const appBtn = row.querySelector('.btn-quick-app');
          if (appBtn) {
            appBtn.addEventListener('click', async () => {
              try {
                await apiB1('/api/board/move', {
                  method: 'POST',
                  headers: { 'Content-Type': 'application/json' },
                  body: JSON.stringify({ jobId: item.id, toColumn: 'siap_posting' }),
                });
                showToast('Klip disetujui -> Siap Posting', 'ok');
                refreshHome();
              } catch (e) {
                showToast(e.message, 'err');
              }
            });
          }
          row.querySelector('.btn-quick-sheet').addEventListener('click', () => openReviewSheetById(item.id));
          butuhBox.appendChild(row);
        });
      }
    }
  }

  // ==================== B1.2 Papan (Kanban + SortableJS + WIP Limit 3) ====================
  async function refreshBoard() {
    const qs = new URLSearchParams();
    if (b1.boardFilter.member !== 'all') qs.set('member', b1.boardFilter.member);
    if (b1.boardFilter.projectId !== 'all') qs.set('projectId', b1.boardFilter.projectId);
    if (b1.boardFilter.momentType !== 'all') qs.set('momentType', b1.boardFilter.momentType);
    try {
      b1.boardData = await apiB1(`/api/board?${qs.toString()}`);
      renderBoardFilters();
      renderKanbanBoard();
    } catch (e) {
      showToast('Gagal memuat papan: ' + e.message, 'err');
    }
  }

  function renderBoardFilters() {
    const selProj = $('boardFilterProject');
    if (selProj) {
      apiB1('/api/projects').then((projs) => {
        selProj.innerHTML = '<option value="all">Semua Project</option>';
        (projs || []).forEach((p) => {
          const opt = document.createElement('option');
          opt.value = p.id;
          opt.textContent = p.title || p.id;
          selProj.appendChild(opt);
        });
        selProj.value = Array.from(selProj.options).some((o) => o.value === b1.boardFilter.projectId) ? b1.boardFilter.projectId : 'all';
      }).catch(() => {});
    }
    const selMem = $('boardFilterMember');
    if (selMem) {
      apiB1('/api/team/metrics').then((tm) => {
        selMem.innerHTML = '<option value="all">Semua Anggota</option>';
        (tm.members || []).forEach((m) => {
          const opt = document.createElement('option');
          opt.value = m.id;
          opt.textContent = `${m.name} (${m.role})`;
          selMem.appendChild(opt);
        });
        selMem.value = Array.from(selMem.options).some((o) => o.value === b1.boardFilter.member) ? b1.boardFilter.member : 'all';
      }).catch(() => {});
    }
  }

  function destroySortables() {
    b1.sortables.forEach((s) => { try { s.destroy(); } catch {} });
    b1.sortables = [];
  }

  function renderKanbanBoard() {
    const wrap = $('kanbanBoardWrap');
    if (!wrap || !b1.boardData) return;
    destroySortables();

    const data = b1.boardData;
    const myID = b1.user?.id || 'owner';
    const myActive = (data.activeByMember && data.activeByMember[myID]) || 0;
    const myLimit = (data.limitsByMember && data.limitsByMember[myID]) || (isOwner() ? 10 : 3);
    const wipFull = !isOwner() && myActive >= myLimit;

    wrap.innerHTML = '';
    (data.columns || []).forEach((col) => {
      const colCards = (data.cards || []).filter((c) => c.column === col.id);
      const colEl = document.createElement('div');
      const isRenderCol = col.id === 'render';
      colEl.className = 'kanban-col' + (isRenderCol && wipFull ? ' wip-locked' : '');
      colEl.innerHTML = `
        <div class="kanban-col-head">
          <span>${esc(col.label)}</span>
          <span class="kanban-col-count">${colCards.length}</span>
        </div>
        ${isRenderCol && wipFull ? `<div class="kanban-wip-banner">Batas antrean ${myActive}/${myLimit} aktif — tunggu 1 selesai</div>` : ''}
        <div class="kanban-dropzone" data-col="${esc(col.id)}"></div>
      `;

      const zone = colEl.querySelector('.kanban-dropzone');
      colCards.forEach((c) => {
        const card = document.createElement('div');
        card.className = 'kanban-card';
        card.dataset.cardKind = c.kind;
        card.dataset.jobId = c.jobId || '';
        card.dataset.candidateId = c.candidateId || '';
        card.dataset.projectId = c.projectId || '';
        card.dataset.fromCol = col.id;

        const vb = c.viralityBreakdown;
        const virHtml = (c.viralityScore || vb)
          ? `<div class="kanban-virality">
               <span>V ${Math.round(c.viralityScore || 0)}</span>
               ${vb ? `<span>H${Math.round(vb.hook)}</span><span>E${Math.round(vb.emotion)}</span><span>K${Math.round(vb.conflict)}</span><span>Q${Math.round(vb.quotable)}</span>` : ''}
             </div>`
          : '';

        card.innerHTML = `
          ${c.thumbnailUrl ? `<img class="kanban-thumb" src="${esc(c.thumbnailUrl)}" loading="lazy" alt="" onerror="this.style.display='none'" />` : ''}
          <div class="kanban-title">${esc(c.title)}</div>
          ${c.arcLabel || c.momentType ? `<div class="kanban-arc">${c.momentType ? `<strong>${esc(c.momentType)}</strong> · ` : ''}${esc(c.arcLabel || '')}</div>` : ''}
          ${virHtml}
          ${c.reason ? `<div class="kanban-reason">Catatan: ${esc(c.reason)}</div>` : ''}
          <div class="kanban-foot">
            <div style="display:flex;align-items:center;gap:5px;">
              <span class="owner-initials" title="${esc(c.ownerName || 'Owner')}">${initialsOf(c.ownerName)}</span>
              <span class="via-tag ${c.createdVia === 'agent' ? 'agent' : ''}">${esc(c.createdVia || 'web')}</span>
              <span class="kanban-age muted">${fmtAge(c.ageHours)}</span>
            </div>
            <div style="display:flex;gap:4px;align-items:center;flex-wrap:wrap;justify-content:flex-end;">
              <select class="input kanban-move-sel" title="Pindah kolom (ramah ponsel)">
                <option value="">Pindah...</option>
                ${(data.columns || []).filter((mc) => mc.id !== col.id).map((mc) => `<option value="${esc(mc.id)}">${esc(mc.label)}</option>`).join('')}
              </select>
              <button class="btn btn-2xs btn-ghost btn-k-edit" title="Buka di Studio">Edit</button>
              ${c.jobId && col.id === 'review' ? '<button class="btn btn-2xs btn-secondary btn-k-rev">Review</button>' : ''}
              ${c.jobId && (col.id === 'siap_posting' || col.id === 'diposting') ? '<button class="btn btn-2xs btn-secondary btn-k-kit">Kit</button>' : ''}
            </div>
          </div>
        `;

        const moveSel = card.querySelector('.kanban-move-sel');
        if (moveSel) {
          moveSel.addEventListener('click', (ev) => ev.stopPropagation());
          moveSel.addEventListener('change', async (ev) => {
            ev.stopPropagation();
            const toCol = ev.target.value;
            if (!toCol) return;
            let reason = '';
            if (col.id === 'review' && toCol === 'kandidat') {
              reason = window.prompt('Alasan penolakan (wajib):') || '';
              if (!reason.trim()) {
                ev.target.value = '';
                showToast('Penolakan butuh alasan', 'err');
                return;
              }
            }
            const payload = { toColumn: toCol, reason: reason.trim() };
            if (c.jobId) payload.jobId = c.jobId;
            else {
              payload.candidateId = c.candidateId;
              payload.projectId = c.projectId;
            }
            try {
              await apiB1('/api/board/move', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
              });
              showToast(`Kartu dipindah ke ${toCol}`, 'ok');
              refreshBoard();
              refreshHome();
            } catch (err) {
              showToast(err.message, 'err');
              ev.target.value = '';
            }
          });
        }

        card.querySelector('.btn-k-edit').addEventListener('click', (ev) => {
          ev.stopPropagation();
          if (c.projectId && $('projectHistorySelect')) {
            $('projectHistorySelect').value = c.projectId;
            $('projectHistorySelect').dispatchEvent(new Event('change'));
          }
          openTab('studio');
        });
        const revBtn = card.querySelector('.btn-k-rev');
        if (revBtn) revBtn.addEventListener('click', (ev) => { ev.stopPropagation(); openReviewSheetById(c.jobId); });
        const kitBtn = card.querySelector('.btn-k-kit');
        if (kitBtn) kitBtn.addEventListener('click', (ev) => { ev.stopPropagation(); openPublishKitModalById(c.jobId); });

        zone.appendChild(card);
      });

      if (window.Sortable) {
        const s = window.Sortable.create(zone, {
          group: 'klipforge-board',
          animation: 140,
          delayOnTouchOnly: true,
          delay: 120,
          ghostClass: 'sortable-ghost',
          onAdd: async (evt) => {
            const el = evt.item;
            const toCol = zone.dataset.col;
            const fromCol = el.dataset.fromCol;
            let reason = '';
            if (fromCol === 'review' && toCol === 'kandidat') {
              reason = window.prompt('Alasan penolakan (wajib: frame kosong / arc terpotong / watermark salah / kata terlarang):') || '';
              if (!reason.trim()) {
                showToast('Penolakan butuh alasan', 'err');
                refreshBoard();
                return;
              }
            }
            const payload = {
              toColumn: toCol,
              reason: reason.trim(),
            };
            if (el.dataset.jobId) payload.jobId = el.dataset.jobId;
            else {
              payload.candidateId = el.dataset.candidateId;
              payload.projectId = el.dataset.projectId;
            }
            try {
              await apiB1('/api/board/move', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload),
              });
              showToast(`Kartu dipindah ke ${col.label}`, 'ok');
              refreshBoard();
              refreshHome();
            } catch (err) {
              showToast(err.message, 'err');
              refreshBoard();
            }
          },
        });
        b1.sortables.push(s);
      }

      wrap.appendChild(colEl);
    });
  }

  // ==================== B1.3 Review Queue (Oldest First + SLA + Review Sheet + Batch Mode) ====================
  async function refreshReviewEnhancements() {
    try {
      const [jobs, projs] = await Promise.all([apiB1('/api/jobs'), apiB1('/api/projects')]);
      renderReviewB1Queue(jobs || [], projs || []);
    } catch {}
  }

  function renderReviewB1Queue(jobs, projects) {
    const box = $('b1ReviewList');
    if (!box) return;
    const filter = document.querySelector('#tab-review .review-filter.active')?.dataset.rfilter || 'pending_review';
    const memberF = $('reviewMemberFilter')?.value || 'all';

    let list = jobs.filter((j) => {
      if (filter === 'approved') return j.reviewStatus === 'approved';
      if (filter === 'rejected') return j.reviewStatus === 'rejected';
      return j.status === 'done' && (j.reviewStatus === 'pending_review' || !j.reviewStatus);
    });
    if (memberF !== 'all') {
      list = list.filter((j) => (j.createdById || 'owner') === memberF);
    }

    // Sort OLDEST FIRST for SLA aging!
    list.sort((a, b) => new Date(a.completedAt || a.createdAt) - new Date(b.completedAt || b.createdAt));

    if ($('cntPending')) $('cntPending').textContent = String(jobs.filter((j) => j.status === 'done' && (!j.reviewStatus || j.reviewStatus === 'pending_review')).length);
    if ($('cntApproved')) $('cntApproved').textContent = String(jobs.filter((j) => j.reviewStatus === 'approved').length);
    if ($('cntRejected')) $('cntRejected').textContent = String(jobs.filter((j) => j.reviewStatus === 'rejected').length);

    // Prune stale IDs that are no longer in current pending list
    const validIds = new Set(list.filter((j) => !(!isOwner() && j.createdById && j.createdById === b1.user?.id)).map((j) => j.id));
    b1.selectedBatchReview.forEach((id) => {
      if (!validIds.has(id)) b1.selectedBatchReview.delete(id);
    });
    b1.visibleBatchIds = Array.from(validIds);

    if ($('b1BatchReviewBar')) {
      $('b1BatchReviewBar').classList.toggle('hidden', !canQC() || filter !== 'pending_review' || list.length === 0);
      $('b1BatchCount').textContent = String(b1.selectedBatchReview.size);
    }

    if (list.length === 0) {
      box.innerHTML = '<div class="muted text-xs" style="padding:18px;">Tidak ada klip pada filter review ini.</div>';
      return;
    }

    const projMap = new Map(projects.map((p) => [p.id, p]));
    box.innerHTML = '';
    list.forEach((j) => {
      const refMs = Date.parse(j.completedAt || j.createdAt || Date.now());
      const ageH = Math.max(0, (Date.now() - refMs) / 3600000);
      const slaCls = ageH >= 24 ? 'red' : (ageH >= 6 ? 'amber' : '');
      const selfBlocked = !isOwner() && j.createdById && j.createdById === b1.user?.id;
      const pr = projMap.get(j.projectId);
      const cand = pr && j.options?.candidateId ? (pr.candidates || []).find((c) => c.id === j.options.candidateId) : null;

      const card = document.createElement('div');
      card.className = 'job-card';
      card.innerHTML = `
        <div class="job-head">
          <div style="display:flex;gap:8px;align-items:flex-start;">
            ${canQC() && filter === 'pending_review' && !selfBlocked ? `<input type="checkbox" class="b1-rev-chk" ${b1.selectedBatchReview.has(j.id) ? 'checked' : ''} style="margin-top:3px;" />` : ''}
            <div>
              <div class="job-title">${esc(j.title)}</div>
              <div class="text-xs muted">${esc(j.campaignName)} · ${esc(j.createdBy || 'Owner')} (${esc(j.createdVia || 'web')})</div>
            </div>
          </div>
          <span class="sla-badge ${slaCls}">${fmtAge(ageH)}</span>
        </div>
        ${cand && (cand.startFrameUrl || cand.peakFrameUrl) ? `
          <div class="review-frames-compare">
            ${cand.startFrameUrl ? `<figure><img src="${esc(cand.startFrameUrl)}" loading="lazy" /><figcaption>Start Frame</figcaption></figure>` : ''}
            ${cand.peakFrameUrl ? `<figure><img src="${esc(cand.peakFrameUrl)}" loading="lazy" /><figcaption>Peak Frame</figcaption></figure>` : ''}
          </div>` : ''}
        ${cand?.verdict ? `<div class="text-xs" style="color:#34d399;">Verdict: ${esc(cand.verdict.verdict.toUpperCase())} (${cand.verdict.funny || 0}/5) · ${esc(cand.verdict.verdictBy || '')}</div>` : ''}
        ${j.rejectReason ? `<div class="kanban-reason">Alasan ditolak: ${esc(j.rejectReason)}</div>` : ''}
        <div class="job-actions mt-8">
          <button class="btn btn-xs btn-primary btn-open-sheet">Buka Review Sheet</button>
          ${canQC() && filter === 'pending_review' ? `<button class="btn btn-xs btn-secondary btn-fast-app" ${selfBlocked ? 'disabled title="Self-approve diblokir"' : ''}>Setujui</button>` : ''}
        </div>
      `;

      const chk = card.querySelector('.b1-rev-chk');
      if (chk) {
        chk.addEventListener('change', (e) => {
          if (e.target.checked) b1.selectedBatchReview.add(j.id);
          else b1.selectedBatchReview.delete(j.id);
          if ($('b1BatchCount')) $('b1BatchCount').textContent = String(b1.selectedBatchReview.size);
        });
      }
      card.querySelector('.btn-open-sheet').addEventListener('click', () => openReviewSheet(j, cand));
      const fastApp = card.querySelector('.btn-fast-app');
      if (fastApp) {
        fastApp.addEventListener('click', async () => {
          try {
            await apiB1(`/api/jobs/${encodeURIComponent(j.id)}/review`, {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ reviewStatus: 'approved' }),
            });
            showToast('Klip disetujui -> Siap Posting', 'ok');
            refreshReviewEnhancements();
            refreshHome();
          } catch (e) {
            showToast(e.message, 'err');
          }
        });
      }
      box.appendChild(card);
    });
  }

  async function openReviewSheetById(jobId) {
    try {
      const job = await apiB1(`/api/jobs/${encodeURIComponent(jobId)}`);
      let cand = null;
      if (job.projectId && job.options?.candidateId) {
        const pr = await apiB1(`/api/projects/${encodeURIComponent(job.projectId)}?includeDropped=1`).catch(() => null);
        cand = pr ? (pr.candidates || []).find((c) => c.id === job.options.candidateId) : null;
      }
      openReviewSheet(job, cand);
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function evalH1B1(flags) {
    if (window.KFCutter && typeof window.KFCutter.evaluateH1Checklist === 'function') {
      return window.KFCutter.evaluateH1Checklist(flags);
    }
    const f = flags && typeof flags === 'object' ? flags : {};
    const items = [
      ['h1TawaFacecam', 'F1 Tawa keras di facecam'],
      ['h1VisualKejadian', 'F2 Kejadian/fisika terlihat di frame'],
      ['h1ArcUtuh', 'F3 Arc setup→payoff 20–55 dtk utuh'],
      ['h1HookMutePass', 'F4 Hook 1–3 dtk lolos mute test / 1,5 dtk wajah+event'],
    ];
    const missing = [];
    let passedCount = 0;
    items.forEach(([k, lbl]) => {
      if (Boolean(f[k])) passedCount += 1;
      else missing.push(lbl);
    });
    return { passedCount, total: 4, publishable: passedCount === 4, missing };
  }

  function openReviewSheet(job, cand) {
    b1.reviewSheetJob = job;
    b1.selectedRejectReason = '';
    const sheet = $('b1ReviewSheet');
    if (!sheet) return;

    $('rsTitle').textContent = job.title || job.id;
    $('rsMeta').textContent = `${job.campaignName || ''} · ${job.createdBy || 'Owner'} (${job.createdVia || 'web'}) · ${job.finalDuration ? job.finalDuration.toFixed(1) + 's' : ''}`;
    $('rsVideo').src = `/api/jobs/${encodeURIComponent(job.id)}/video`;

    const framesEl = $('rsFrames');
    if (cand && (cand.startFrameUrl || cand.peakFrameUrl)) {
      framesEl.innerHTML = `
        <figure><img src="${esc(cand.startFrameUrl || '')}" alt="Start" /><figcaption>Start Frame</figcaption></figure>
        <figure><img src="${esc(cand.peakFrameUrl || '')}" alt="Peak" /><figcaption>Peak Frame</figcaption></figure>
      `;
      framesEl.classList.remove('hidden');
    } else {
      framesEl.classList.add('hidden');
    }

    const vb = cand?.viralityBreakdown;
    $('rsVirality').innerHTML = vb
      ? `Virality <strong>${Math.round(cand.viralityScore || 0)}</strong> · Hook ${Math.round(vb.hook)} · Emosi ${Math.round(vb.emotion)} · Konflik ${Math.round(vb.conflict)} · Quotable ${Math.round(vb.quotable)}`
      : 'Virality breakdown tidak tersedia pada klip manual.';
    $('rsVerdict').innerHTML = cand?.verdict
      ? `Verdict Fase 4: <strong>${esc(cand.verdict.verdict.toUpperCase())}</strong> (${cand.verdict.funny}/5) — ${esc(cand.verdict.note || '')} (${esc(cand.verdict.verdictBy || '')})`
      : '';

    const vFlags = cand?.verdict || {};
    const h1 = evalH1B1(vFlags);
    if ($('rsH1Badge')) {
      $('rsH1Badge').textContent = `H1: ${h1.passedCount}/${h1.total}`;
      $('rsH1Badge').classList.toggle('pass', h1.publishable);
      $('rsH1Badge').title = h1.missing.length ? 'Belum lolos: ' + h1.missing.join(' · ') : 'Semua 4 pilar H1 lolos!';
    }
    const pillLabels = {
      h1TawaFacecam: 'F1 Tawa keras di facecam',
      h1VisualKejadian: 'F2 Kejadian/fisika terlihat di frame',
      h1ArcUtuh: 'F3 Arc setup→payoff 20–55 dtk utuh',
      h1HookMutePass: 'F4 Hook 1–3 dtk lolos mute test / 1,5 dtk wajah+event',
    };
    if ($('rsH1Pills')) {
      $('rsH1Pills').querySelectorAll('.h1-pill').forEach((pill) => {
        const flagKey = pill.dataset.flag;
        const ok = Boolean(vFlags[flagKey]);
        pill.classList.toggle('ok', ok);
        pill.textContent = `${ok ? '✓' : '○'} ${pillLabels[flagKey] || flagKey}`;
      });
    }
    if ($('rsH1HookLine')) {
      const hl = String(vFlags.hookLine || '').trim();
      if (hl) {
        $('rsH1HookLine').textContent = `Hook Line: “${hl}”`;
        $('rsH1HookLine').classList.remove('hidden');
      } else {
        $('rsH1HookLine').textContent = 'Pastikan 1,5–3 dtk pertama lolos mute test (wajah + kejadian terlihat) sebelum menyetujui.';
        $('rsH1HookLine').classList.remove('hidden');
      }
    }

    $('rsNote').value = job.reviewNote || '';
    document.querySelectorAll('.reject-chip').forEach((c) => c.classList.remove('active'));

    const selfBlocked = !isOwner() && job.createdById && job.createdById === b1.user?.id;
    const btnApp = $('btnRsApprove');
    const btnRej = $('btnRsReject');
    btnApp.disabled = !canQC() || selfBlocked;
    btnApp.textContent = selfBlocked ? 'Self-Approve Diblokir' : 'Setujui -> Siap Posting';
    btnRej.disabled = !canQC();

    sheet.classList.remove('hidden');
  }

  // ==================== B1.4 Tim Screen + Activity Feed ====================
  async function refreshTeamAndActivity() {
    try {
      const memFilter = $('activityMemberFilter')?.value || 'all';
      const [tm, act] = await Promise.all([
        apiB1('/api/team/metrics'),
        apiB1(`/api/activity?limit=60&member=${encodeURIComponent(memFilter)}`),
      ]);
      b1.teamMetrics = tm;
      b1.activity = act.items || [];
      renderTimCards();
      renderActivityFeed();
    } catch {}
  }

  function renderSparklineSVG(arr) {
    const vals = (arr && arr.length === 7) ? arr : [0, 0, 0, 0, 0, 0, 0];
    const maxV = Math.max(1, ...vals);
    const w = 120, h = 28;
    const pts = vals.map((v, i) => {
      const x = Math.round((i / 6) * (w - 8) + 4);
      const y = Math.round(h - 4 - (v / maxV) * (h - 8));
      return `${x},${y}`;
    }).join(' ');
    return `<svg width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" style="display:block;"><polyline fill="none" stroke="#10b981" stroke-width="2" points="${pts}" /></svg>`;
  }

  function renderTimCards() {
    const box = $('b1TimCards');
    if (!box || !b1.teamMetrics) return;
    const members = b1.teamMetrics.members || [];

    const sel = $('activityMemberFilter');
    if (sel) {
      const prev = sel.value || 'all';
      sel.innerHTML = '<option value="all">Semua Aktivitas</option>';
      members.forEach((m) => {
        const o = document.createElement('option');
        o.value = m.id;
        o.textContent = m.name;
        sel.appendChild(o);
      });
      sel.value = Array.from(sel.options).some((o) => o.value === prev) ? prev : 'all';
    }

    box.innerHTML = members.map((m) => {
      const actHtml = (m.activeJobs || []).length > 0
        ? m.activeJobs.map((aj) => `
            <div style="margin-top:4px;font-size:11px;">
              <div style="display:flex;justify-content:space-between;"><span>${esc(aj.title)}</span><span class="mono">${Math.round(aj.progress || 0)}%</span></div>
              <div class="peek-bar"><div class="peek-bar-fill" style="width:${Math.round(aj.progress || 0)}%"></div></div>
            </div>`).join('')
        : '<div class="text-xs muted">Tidak ada render aktif</div>';

      return `
        <div class="tim-member-card">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;">
            <div style="display:flex;align-items:center;gap:8px;">
              <span class="tim-presence-dot ${m.online ? 'online' : ''}" title="${m.online ? 'Online' : 'Offline'}"></span>
              <strong>${esc(m.name)}</strong>
              <span class="strip-role">${esc(m.role)}</span>
            </div>
            <span class="mono text-xs muted">${m.activeCount}/${m.maxActive} aktif</span>
          </div>
          <div class="tim-metrics-grid">
            <div class="tim-metric-box"><div class="lbl">Hari ini</div><div class="val">${m.jobsToday}</div></div>
            <div class="tim-metric-box"><div class="lbl">Approval</div><div class="val">${m.approvalRate}%</div></div>
            <div class="tim-metric-box"><div class="lbl">Turnaround</div><div class="val">${m.avgTurnaroundMin}m</div></div>
          </div>
          <div style="display:flex;align-items:center;justify-content:space-between;">
            <span class="text-xs muted">Output 7 hari (${(m.sparkline7d || []).reduce((a, b) => a + b, 0)} klip)</span>
            ${renderSparklineSVG(m.sparkline7d)}
          </div>
          <div>${actHtml}</div>
        </div>
      `;
    }).join('');
  }

  function renderActivityFeed() {
    const box = $('b1ActivityFeed');
    if (!box) return;
    if (b1.activity.length === 0) {
      box.innerHTML = '<div class="muted text-xs" style="padding:12px;">Belum ada aktivitas tercatat.</div>';
      return;
    }
    box.innerHTML = b1.activity.map((a) => {
      const ts = a.timestamp ? new Date(a.timestamp).toLocaleTimeString('id-ID', { hour: '2-digit', minute: '2-digit' }) : '';
      return `
        <div class="activity-row ${esc(a.level || 'info')}">
          <div>${esc(a.summary)}</div>
          <div class="activity-time">${esc(ts)}${a.actorVia ? ' · ' + esc(a.actorVia) : ''}${a.detail ? ' · ' + esc(a.detail) : ''}</div>
        </div>
      `;
    }).join('');
  }

  // ==================== B2.1 + B3.1 Library SOP Checklist, Publish Kit & Jianying Draft ====================
  function closeReviewSheet() {
    const vid = $('rsVideo');
    if (vid) {
      vid.pause();
      vid.removeAttribute('src');
      vid.load();
    }
    if ($('b1ReviewSheet')) $('b1ReviewSheet').classList.add('hidden');
    b1.reviewSheetJob = null;
  }

  function closePublishKitModal() {
    if ($('b1PublishKitModal')) $('b1PublishKitModal').classList.add('hidden');
    b1.publishKitJob = null;
    b1.publishModalJob = null;
  }

  async function enhanceLibraryCards(jobsOverride) {
    const box = $('jobsList');
    if (!box) return;
    try {
      let jobs = Array.isArray(jobsOverride) ? jobsOverride : (window.__kfGetJobs ? window.__kfGetJobs() : null);
      if (!jobs || jobs.length === 0) {
        jobs = await apiB1('/api/jobs?limit=100');
      }
      const byId = new Map((jobs || []).map((j) => [j.id, j]));
      const cards = box.querySelectorAll('.job-card');
      cards.forEach((card) => {
        if (card.dataset.b2Enhanced) return;
        let jid = card.dataset.jobId || '';
        if (!jid) {
          const vid = card.querySelector('video.job-video');
          const dl = card.querySelector('a[href*="/api/jobs/"]');
          const href = vid?.getAttribute('src') || dl?.getAttribute('href') || '';
          const m = href.match(/\/api\/jobs\/([^/]+)\/video/);
          if (m) jid = decodeURIComponent(m[1]);
        }
        if (!jid) return;
        const job = byId.get(jid);
        if (!job) return;
        card.dataset.b2Enhanced = '1';

        const ck = job.publishKit?.checklist;
        const wmOK = ck ? ck.watermarkOk : !!job.compliance?.watermarkApplied;
        const outroOK = ck ? ck.outroCleanOk : !!(job.compliance?.outroApplied && job.compliance?.outroClean);
        const forbOK = ck ? ck.forbiddenCleanOk : true;
        const hashOK = ck ? ck.creditHashtagsOk : !!job.compliance?.hashtagsReady;
        const ready = wmOK && outroOK && forbOK && hashOK;

        const sopDiv = document.createElement('div');
        sopDiv.innerHTML = `
          <div style="display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:6px;margin-top:6px;">
            <span class="${ready ? 'sop-ready-pill' : 'sop-notready-pill'}">${ready ? 'SIAP POSTING' : 'CEK SOP'}</span>
            ${job.analytics ? `<span class="analytics-chip">YT ${job.analytics.views} views · ${job.analytics.likes} likes · ${job.analytics.comments} komen</span>` : ''}
          </div>
          <div class="sop-checklist">
            <div class="sop-check-item ${wmOK ? 'ok' : 'fail'}">${wmOK ? '✓' : '✕'} Watermark menempel</div>
            <div class="sop-check-item ${outroOK ? 'ok' : 'fail'}">${outroOK ? '✓' : '✕'} Outro bersih</div>
            <div class="sop-check-item ${forbOK ? 'ok' : 'fail'}">${forbOK ? '✓' : '✕'} Kata terlarang lolos</div>
            <div class="sop-check-item ${hashOK ? 'ok' : 'fail'}">${hashOK ? '✓' : '✕'} Credit + hashtag</div>
          </div>
        `;
        const actions = card.querySelector('.job-actions');
        if (actions) {
          actions.parentNode.insertBefore(sopDiv, actions);
          const btnKit = document.createElement('button');
          btnKit.className = 'btn btn-xs btn-secondary';
          btnKit.textContent = 'Publish Kit';
          btnKit.addEventListener('click', () => openPublishKitModalById(job.id));

          const btnDraft = document.createElement('button');
          btnDraft.className = 'btn btn-xs btn-secondary';
          btnDraft.textContent = 'Draft CapCut';
          btnDraft.addEventListener('click', () => exportJianyingDraft(job.id));

          actions.appendChild(btnKit);
          actions.appendChild(btnDraft);
          if (job.coverFrameUrl) {
            const aCov = document.createElement('a');
            aCov.className = 'btn btn-xs btn-ghost';
            aCov.href = job.coverFrameUrl;
            aCov.download = `${job.id}_cover.jpg`;
            aCov.textContent = 'Cover JPG';
            actions.appendChild(aCov);
          }
        }
      });
    } catch {}
  }
  window.__kfEnhanceLibraryCards = enhanceLibraryCards;

  function renderPublishKitState(job, kit) {
    if ($('pkTikTok')) $('pkTikTok').value = kit.tiktok?.caption || '';
    if ($('pkShortsTitle')) $('pkShortsTitle').value = kit.shorts?.title || '';
    if ($('pkShortsDesc')) $('pkShortsDesc').value = kit.shorts?.description || '';
    if ($('pkReels')) $('pkReels').value = kit.reels?.caption || '';
    if ($('pkChecklist')) {
      $('pkChecklist').innerHTML = `
        <span class="${kit.checklist?.readyToPost ? 'sop-ready-pill' : 'sop-notready-pill'}">
          ${kit.checklist?.readyToPost ? 'SEMUA 4 CEK SOP LOLOS — SIAP POSTING' : 'BELUM LENGKAP: ' + esc((kit.checklist?.missing || []).join(', '))}
        </span>
      `;
    }

    const isApproved = job?.reviewStatus === 'approved';
    const gate = $('pkApprovalGateBanner');
    if (gate) {
      gate.classList.remove('hidden');
      gate.classList.toggle('approved', isApproved);
      gate.textContent = isApproved
        ? `Disetujui QC (${job.reviewedBy || 'Owner'}) — siap publish ke TikTok, YouTube Shorts, atau Instagram Reels.`
        : 'Klip belum disetujui QC (ReviewStatus != approved). Salin caption manual boleh, tetapi tombol Publish otomatis dikunci sampai disetujui di tab Review.';
    }
    ['btnPkPublishTikTok', 'btnPkPublishYT', 'btnPkPublishIG'].forEach((id) => {
      if ($(id)) $(id).disabled = !isApproved;
    });

    const covUrl = job?.coverFrameUrl || kit?.reels?.coverUrl || '';
    const covWrap = $('pkCoverWrap');
    if (covWrap) {
      covWrap.classList.toggle('hidden', !covUrl);
      if (covUrl) {
        if ($('pkCoverPreview')) $('pkCoverPreview').src = covUrl;
        if ($('pkCoverDownload')) {
          $('pkCoverDownload').href = covUrl;
          $('pkCoverDownload').download = `${job?.id || 'clip'}_cover.jpg`;
        }
      }
    }

    const recBox = $('pkPublishRecords');
    const recs = job?.publishRecords || [];
    if (recBox) {
      recBox.classList.toggle('hidden', recs.length === 0);
      recBox.innerHTML = recs.map((r) => {
        const lbl = `${(r.platform || '').toUpperCase()}: ${r.status}${r.accountName ? ' (' + r.accountName + ')' : ''}`;
        if (r.publishedUrl && /^https?:\/\//i.test(r.publishedUrl)) {
          return `<a class="pk-record-pill ${esc(r.status)}" href="${esc(r.publishedUrl)}" target="_blank" rel="noopener">${esc(lbl)} ↗</a>`;
        }
        return `<span class="pk-record-pill ${esc(r.status)}" title="${esc(r.error || '')}">${esc(lbl)}</span>`;
      }).join('');
    }
  }

  async function populatePublishAccountSelect() {
    const sel = $('pkAccountSelect');
    const wrap = $('pkAccountWrap');
    if (!sel) return;
    if (!isOwner()) {
      if (wrap) wrap.classList.add('hidden');
      return;
    }
    if (wrap) wrap.classList.remove('hidden');
    try {
      const res = await apiB1('/api/publish/accounts');
      sel.innerHTML = '<option value="">Default Engine / Otomatis</option>';
      (res.accounts || []).forEach((a) => {
        const opt = document.createElement('option');
        opt.value = a.id;
        opt.textContent = `[${a.platform}] ${a.name}`;
        sel.appendChild(opt);
      });
    } catch {}
  }

  async function openPublishKitModalById(jobId) {
    try {
      const [job, res] = await Promise.all([
        apiB1(`/api/jobs/${encodeURIComponent(jobId)}`),
        apiB1(`/api/jobs/${encodeURIComponent(jobId)}/publish-kit`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: '{}',
        }),
      ]);
      b1.publishKitJob = jobId;
      b1.publishModalJob = job;
      renderPublishKitState(job, res.publishKit);
      populatePublishAccountSelect();
      $('b1PublishKitModal').classList.remove('hidden');
    } catch (e) {
      showToast('Gagal membuat Publish Kit: ' + e.message, 'err');
    }
  }

  async function exportJianyingDraft(jobId) {
    try {
      showToast('Menyiapkan draft Jianying / CapCut...', 'info');
      const res = await apiB1(`/api/jobs/${encodeURIComponent(jobId)}/draft`, { method: 'POST' });
      showToast(`Draft siap (${res.schemaVersion}) — mengunduh ZIP!`, 'ok');
      window.location.href = res.draft.downloadUrl;
    } catch (e) {
      showToast('Gagal ekspor draft: ' + e.message, 'err');
    }
  }

  // ==================== P0 Operations: Inventory (I-02), Publishable Queue (I-03), Views Ledger (I-04) ====================
  function fmtBytesB1(b) {
    const n = Number(b || 0);
    if (!n) return '0 B';
    if (n >= 1024 * 1024) return (n / (1024 * 1024)).toFixed(2) + ' MB';
    return (n / 1024).toFixed(1) + ' KB';
  }

  function copyTextB1(txt, okMsg) {
    if (!txt) return;
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(txt).then(() => showToast(okMsg, 'ok')).catch(() => {
        const ta = document.createElement('textarea');
        ta.value = txt;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        ta.remove();
        showToast(okMsg, 'ok');
      });
      return;
    }
    const ta = document.createElement('textarea');
    ta.value = txt;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    ta.remove();
    showToast(okMsg, 'ok');
  }

  function openMarkPostedModal(item) {
    if (!item) return;
    const modal = $('p0MarkPostedModal');
    if (!modal) return;
    b1.markPostedJob = item;
    if ($('p0MpJobTitle')) $('p0MpJobTitle').textContent = item.title || item.id || item.jobId || '—';
    if ($('p0MpPlatform')) $('p0MpPlatform').value = 'tiktok';
    if ($('p0MpUrl')) $('p0MpUrl').value = '';
    if ($('p0MpViews')) $('p0MpViews').value = '';
    if ($('p0MpNote')) $('p0MpNote').value = '';
    modal.classList.remove('hidden');
  }

  function closeMarkPostedModal() {
    if ($('p0MarkPostedModal')) $('p0MarkPostedModal').classList.add('hidden');
    b1.markPostedJob = null;
  }

  async function submitMarkPosted() {
    if (!b1.markPostedJob) return;
    const jobId = b1.markPostedJob.id || b1.markPostedJob.jobId;
    if (!jobId) return;
    const platform = ($('p0MpPlatform')?.value || 'tiktok').trim().toLowerCase();
    const publishedUrl = ($('p0MpUrl')?.value || '').trim();
    const note = ($('p0MpNote')?.value || '').trim();
    const rawViews = ($('p0MpViews')?.value || '').trim();

    const payload = { platform, publishedUrl, note };
    if (rawViews !== '') {
      const v = parseInt(rawViews, 10);
      if (Number.isNaN(v) || v < 0) {
        showToast('Views awal harus angka >= 0', 'err');
        return;
      }
      payload.views = v;
    }

    const btn = $('btnSubmitMarkPosted');
    if (btn) btn.disabled = true;
    try {
      await apiB1(`/api/jobs/${encodeURIComponent(jobId)}/mark-posted`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      closeMarkPostedModal();
      showToast('✓ Klip ditandai Diposting Manual!', 'ok');
      refreshP0Ops();
      refreshHome();
      if ($('btnRefreshJobs')) $('btnRefreshJobs').click();
    } catch (e) {
      showToast('Gagal tandai diposting: ' + e.message, 'err');
    } finally {
      if (btn) btn.disabled = false;
    }
  }

  function renderP0Inventory() {
    const box = $('p0InvSummary');
    if (!box) return;
    const raw = b1.p0Inventory;
    if (!raw) {
      box.innerHTML = '<span class="p0-inv-pill">Inventory: <strong>—</strong></span>';
      return;
    }
    const inv = raw.summary || raw.counts || raw;
    const countOf = (...vals) => {
      for (const v of vals) {
        if (typeof v === 'number' && Number.isFinite(v)) return v;
        if (Array.isArray(v)) return v.length;
      }
      return 0;
    };
    const publishable = countOf(inv.publishableCount, inv.publishable, inv.readyToPost, raw.publishableCount, raw.publishable);
    const posted = countOf(inv.postedCount, inv.posted, inv.publishedCount, inv.published, raw.postedCount, raw.posted);
    const rejected = countOf(inv.rejectedCount, inv.rejected, raw.rejectedCount, raw.rejected);
    const missing = countOf(inv.missingCount, inv.missingFileCount, inv.missingFiles, inv.missing, raw.missingCount, raw.missing);
    const orphans = countOf(inv.orphanCount, inv.orphans, inv.orphanFiles, raw.orphanCount, raw.orphanFiles, raw.orphans);

    box.innerHTML = `
      <span class="p0-inv-pill ok" title="done + approved + file ada di disk">Publishable: <strong>${publishable}</strong></span>
      <span class="p0-inv-pill" title="Sudah ditandai diposting manual">Posted: <strong>${posted}</strong></span>
      <span class="p0-inv-pill" title="Ditolak QC">Rejected: <strong>${rejected}</strong></span>
      <span class="p0-inv-pill ${missing > 0 ? 'warn' : ''}" title="Job tanpa file MP4 di disk">Missing: <strong>${missing}</strong></span>
      ${orphans > 0 ? `<span class="p0-inv-pill warn" title="File MP4 orphan di folder outputs">Orphan: <strong>${orphans}</strong></span>` : ''}
    `;
  }

  function renderP0Publishable() {
    const box = $('p0PublishableList');
    const cnt = $('p0PubCountBadge');
    if (!box) return;
    const items = Array.isArray(b1.p0Publishable) ? b1.p0Publishable : [];
    if (cnt) cnt.textContent = `${items.length} klip`;

    if (items.length === 0) {
      box.innerHTML = '<div class="muted text-xs" style="padding:10px;">Belum ada klip siap posting (butuh status <code>done + approved</code> dengan file MP4 tersedia di disk).</div>';
      return;
    }

    box.innerHTML = '';
    items.forEach((item) => {
      const jid = item.id || item.jobId || '';
      const title = item.title || jid;
      const dur = Number(item.finalDuration ?? item.durationSec ?? 0);
      const size = Number(item.fileSizeBytes ?? item.sizeBytes ?? 0);
      const fmt = item.format || item.options?.layout?.aspectRatio || '';
      const thumb = item.coverFrameUrl || item.thumbnailUrl || item.peekFrameUrl || '';
      const dlUrl = item.downloadUrl || `/api/jobs/${encodeURIComponent(jid)}/video?download=1`;
      const caption = item.postCaption || item.caption || item.publishKit?.tiktok?.caption || [title, item.hashtagLine].filter(Boolean).join('\n\n');

      const row = document.createElement('div');
      row.className = 'p0-pub-item';
      row.innerHTML = `
        <div class="p0-pub-thumb">
          ${thumb ? `<img src="${esc(thumb)}" loading="lazy" alt="Cover" onerror="this.style.display='none';this.parentElement.textContent='MP4';" />` : 'MP4'}
        </div>
        <div style="min-width:0;">
          <div class="p0-pub-title">${esc(title)}</div>
          <div class="p0-pub-meta">${dur > 0 ? dur.toFixed(1) + 's' : '—'} · ${fmtBytesB1(size)}${fmt ? ' · ' + esc(fmt) : ''}${item.campaignName ? ' · ' + esc(item.campaignName) : ''}</div>
          <div class="p0-pub-acts">
            <a class="btn btn-2xs btn-secondary" href="${esc(dlUrl)}" download>⬇️ Download MP4</a>
            <button class="btn btn-2xs btn-secondary btn-p0-copy-cap" type="button">📋 Copy Caption</button>
            <button class="btn btn-2xs btn-primary btn-p0-mark-posted" type="button" ${canQC() ? '' : 'disabled title="Khusus Owner atau QC Reviewer"'}>✓ Tandai Diposting Manual</button>
            <button class="btn btn-2xs btn-ghost btn-p0-log-views" type="button">📊 Catat Views</button>
          </div>
        </div>
      `;

      row.querySelector('.btn-p0-copy-cap').addEventListener('click', () => {
        copyTextB1(caption, 'Caption siap tempel disalin!');
      });
      const mpBtn = row.querySelector('.btn-p0-mark-posted');
      if (mpBtn && canQC()) {
        mpBtn.addEventListener('click', () => openMarkPostedModal(item));
      }
      row.querySelector('.btn-p0-log-views').addEventListener('click', () => {
        const sel = $('p0LedgerJobSel');
        if (sel) {
          if (!Array.from(sel.options).some((o) => o.value === jid)) {
            const opt = document.createElement('option');
            opt.value = jid;
            opt.textContent = title;
            sel.appendChild(opt);
          }
          sel.value = jid;
        }
        if ($('p0LedgerViews')) {
          $('p0LedgerViews').focus();
          $('p0LedgerViews').scrollIntoView({ behavior: 'smooth', block: 'center' });
        }
      });
      box.appendChild(row);
    });
  }

  function populateP0LedgerJobSelect(allJobs) {
    const sel = $('p0LedgerJobSel');
    if (!sel) return;
    const prev = sel.value;
    const byId = new Map();
    (b1.p0Publishable || []).forEach((it) => {
      const id = it.id || it.jobId;
      if (id) byId.set(id, { id, title: it.title || id, tag: 'Siap Posting', projectId: it.projectId || '', format: it.format || it.options?.layout?.aspectRatio || '' });
    });
    (allJobs || []).forEach((j) => {
      if (!j || !j.id || j.status !== 'done') return;
      if (!byId.has(j.id)) {
        const tag = j.boardColumn === 'diposting' ? 'Diposting' : (j.reviewStatus === 'approved' ? 'Approved' : 'Done');
        byId.set(j.id, { id: j.id, title: j.title || j.id, tag, projectId: j.projectId || '', format: j.options?.layout?.aspectRatio || '' });
      }
    });
    b1.p0LedgerJobsMap = byId;

    sel.innerHTML = '<option value="">— Pilih klip selesai —</option>';
    byId.forEach((info) => {
      const opt = document.createElement('option');
      opt.value = info.id;
      opt.textContent = `[${info.tag}] ${info.title}`;
      sel.appendChild(opt);
    });
    if (prev && byId.has(prev)) sel.value = prev;
  }

  function readP0LedgerH1Flags() {
    return {
      h1TawaFacecam: Boolean($('p0LedgerF1')?.checked),
      h1VisualKejadian: Boolean($('p0LedgerF2')?.checked),
      h1ArcUtuh: Boolean($('p0LedgerF3')?.checked),
      h1HookMutePass: Boolean($('p0LedgerF4')?.checked),
    };
  }

  function updateP0LedgerH1Badge() {
    const badge = $('p0LedgerH1Badge');
    if (!badge) return;
    const res = evalH1B1(readP0LedgerH1Flags());
    badge.textContent = `H1: ${res.passedCount}/${res.total}`;
    badge.classList.toggle('pass', res.publishable);
    badge.title = res.missing.length ? 'Belum lolos: ' + res.missing.join(' · ') : 'Semua 4 pilar H1 lolos!';
  }

  function renderP0Ledger() {
    const sumBox = $('p0LedgerSummary');
    const listBox = $('p0LedgerList');
    const summary = b1.p0Ledger?.summary || {};
    const entries = Array.isArray(b1.p0Ledger?.entries) ? b1.p0Ledger.entries : [];

    if (sumBox) {
      const totalViews = Number(summary.totalViews || 0);
      const medianViews = Number(summary.medianViews || 0);
      const totalEntries = Number(summary.totalEntries ?? entries.length);
      const bySplit = summary.bySplit || {};
      const inStats = bySplit['IN-SAMPLE'] || { entries: 0, totalViews: 0 };
      const hoStats = bySplit['HELD-OUT'] || { entries: 0, totalViews: 0 };
      const scopeNote = summary.dataScopeNote || 'PERINGATAN DATA SCOPE: Metrik IN-SAMPLE hanya observasi historis/internal dan TIDAK BOLEH dianggap sebagai bukti performa HELD-OUT.';

      const ramaiThreshold = typeof summary.ramaiThreshold === 'number'
        ? summary.ramaiThreshold
        : (totalEntries >= 3 ? medianViews * 2 : 0);
      const ramaiStatus = String(summary.ramaiStatus || (
        totalEntries < 3
          ? 'PLACEHOLDER_DUGAAN_N_LT_3'
          : (totalEntries < 10 ? 'BASELINE_AWAL_DUGAAN_N_LT_10' : 'AKTIF_2X_MEDIAN_N_GE_10')
      ));
      const isRamaiActive = totalEntries >= 10 && ramaiStatus === 'AKTIF_2X_MEDIAN_N_GE_10';
      const ramaiLabel = isRamaiActive
        ? `Ambang Ramai (2× Median): ${ramaiThreshold.toLocaleString('id-ID')} views`
        : `PLACEHOLDER / DUGAAN (n=${totalEntries} < 10) · Ambang: ${ramaiThreshold > 0 ? ramaiThreshold.toLocaleString('id-ID') + ' views' : 'belum aktif'}`;
      const h1FullCount = typeof summary.h1FullPassCount === 'number'
        ? summary.h1FullPassCount
        : entries.filter((e) => (typeof e.h1PassCount === 'number' ? e.h1PassCount : evalH1B1(e).passedCount) === 4).length;
      const byCohort = summary.byCohort || {};
      const cohortPills = Object.keys(byCohort).map((k) => {
        const st = byCohort[k] || {};
        return `<span class="p0-meta-pill cohort">${esc(k)}: ${st.entries || 0} (${Number(st.totalViews || 0).toLocaleString('id-ID')}v)</span>`;
      }).join('');

      sumBox.innerHTML = `
        <div class="p0-ledger-kpis">
          <div class="tim-metric-box"><div class="lbl">Total Views</div><div class="val">${totalViews.toLocaleString('id-ID')}</div></div>
          <div class="tim-metric-box"><div class="lbl">Median Views</div><div class="val">${medianViews.toLocaleString('id-ID')}</div></div>
          <div class="tim-metric-box"><div class="lbl">Entri / H1 4/4</div><div class="val">${totalEntries} <span class="text-xs muted">(${h1FullCount})</span></div></div>
        </div>
        <div class="p0-ramai-banner ${isRamaiActive ? 'active' : 'placeholder'}" title="Status adaptif ambang ramai: ${esc(ramaiStatus)}">
          <span>${esc(ramaiLabel)}</span>
          <span>${esc(ramaiStatus)}</span>
        </div>
        <div class="p0-split-row">
          <span class="p0-split-badge">IN-SAMPLE: ${inStats.entries || 0} entri (${Number(inStats.totalViews || 0).toLocaleString('id-ID')} views)</span>
          <span class="p0-split-badge held-out">HELD-OUT: ${hoStats.entries || 0} entri (${Number(hoStats.totalViews || 0).toLocaleString('id-ID')} views)</span>
          ${cohortPills}
        </div>
        <div class="p0-scope-warn">${esc(scopeNote)}</div>
      `;
    }

    if (listBox) {
      if (entries.length === 0) {
        listBox.innerHTML = '<div class="muted text-xs" style="padding:8px;">Belum ada catatan views. Setelah posting manual, catat views &amp; split (IN-SAMPLE / HELD-OUT) di atas.</div>';
        return;
      }
      listBox.innerHTML = entries.slice(0, 12).map((e) => {
        const split = String(e.sampleSplit || 'IN-SAMPLE').toUpperCase();
        const isHeldOut = split === 'HELD-OUT';
        const cohort = String(e.cohortTag || 'POST-v1.2');
        const hookType = String(e.hookType || '').trim();
        const triggerSrc = String(e.triggerSource || '').trim();
        const h1Count = typeof e.h1PassCount === 'number' ? e.h1PassCount : evalH1B1(e).passedCount;
        const ts = e.recordedAt ? new Date(e.recordedAt).toLocaleString('id-ID', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' }) : '';
        const urlHtml = e.postUrl && /^https?:\/\//i.test(e.postUrl)
          ? ` · <a href="${esc(e.postUrl)}" target="_blank" rel="noopener" style="color:#38bdf8;">Link ↗</a>`
          : '';
        const extraViews = [];
        if (Number(e.views24h || 0) > 0) extraViews.push(`24h:${Number(e.views24h).toLocaleString('id-ID')}`);
        if (Number(e.views7d || 0) > 0) extraViews.push(`7d:${Number(e.views7d).toLocaleString('id-ID')}`);
        const confParts = [];
        if (e.channelState) confParts.push(`ch:${e.channelState}`);
        if (e.thumbnailTs) confParts.push(`thumb:${e.thumbnailTs}`);

        return `
          <div class="p0-ledger-row">
            <div style="min-width:0;">
              <div style="font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">${esc(e.title || e.jobId)}</div>
              <div class="text-xs muted" style="display:flex;flex-wrap:wrap;gap:4px;align-items:center;margin-top:3px;">
                <span class="strip-role">${esc((e.platform || '').toUpperCase())}</span>
                <span class="p0-split-badge ${isHeldOut ? 'held-out' : ''}">${esc(split)}</span>
                <span class="p0-meta-pill cohort">${esc(cohort)}</span>
                ${hookType ? `<span class="p0-meta-pill">hook:${esc(hookType)}</span>` : ''}
                ${triggerSrc ? `<span class="p0-meta-pill">src:${esc(triggerSrc)}</span>` : ''}
                <span class="p0-meta-pill ${h1Count === 4 ? 'h1-pass' : ''}">H1 ${h1Count}/4</span>
                ${e.cueSelectionUsed ? '<span class="p0-meta-pill cue-excl" title="Dipakai untuk seleksi cue — exclude dari ranker">cue-sel</span>' : ''}
                <span>${esc(ts)}${urlHtml}</span>
                ${confParts.length ? `<span>· ${esc(confParts.join(' · '))}</span>` : ''}
                ${e.note ? `<span>· ${esc(e.note)}</span>` : ''}
              </div>
            </div>
            <div class="mono" style="text-align:right;flex-shrink:0;">
              <div style="font-weight:700;color:var(--text);">${Number(e.views || 0).toLocaleString('id-ID')} views</div>
              <div class="text-xs muted">${Number(e.likes || 0).toLocaleString('id-ID')} likes${extraViews.length ? ' · ' + esc(extraViews.join(' ')) : ''}</div>
            </div>
          </div>
        `;
      }).join('');
    }
  }

  async function submitLedgerEntry() {
    const jobId = ($('p0LedgerJobSel')?.value || '').trim();
    if (!jobId) {
      showToast('Pilih klip (Job) terlebih dahulu!', 'err');
      return;
    }
    const platform = ($('p0LedgerPlatform')?.value || 'tiktok').trim().toLowerCase();
    const views = parseInt($('p0LedgerViews')?.value || '0', 10);
    const likes = parseInt($('p0LedgerLikes')?.value || '0', 10);
    const rawV24 = ($('p0LedgerViews24h')?.value || '').trim();
    const rawV7d = ($('p0LedgerViews7d')?.value || '').trim();
    const views24h = rawV24 === '' ? 0 : parseInt(rawV24, 10);
    const views7d = rawV7d === '' ? 0 : parseInt(rawV7d, 10);
    const sampleSplit = ($('p0LedgerSplit')?.value || 'IN-SAMPLE').trim().toUpperCase();
    const cohortTag = ($('p0LedgerCohort')?.value || 'POST-v1.2').trim();
    const hookType = ($('p0LedgerHookType')?.value || '').trim();
    const triggerSource = ($('p0LedgerTrigger')?.value || 'manual').trim();
    const channelState = ($('p0LedgerChannelState')?.value || '').trim();
    const thumbnailTs = ($('p0LedgerThumbTs')?.value || '').trim();
    const cueSelectionUsed = Boolean($('p0LedgerCueSel')?.checked);
    const h1Flags = readP0LedgerH1Flags();
    const postUrl = ($('p0LedgerUrl')?.value || '').trim();
    const note = ($('p0LedgerNote')?.value || '').trim();

    if (
      Number.isNaN(views) || views < 0 ||
      Number.isNaN(likes) || likes < 0 ||
      Number.isNaN(views24h) || views24h < 0 ||
      Number.isNaN(views7d) || views7d < 0
    ) {
      showToast('Views, Likes, Views 24h, dan Views 7d harus angka >= 0!', 'err');
      return;
    }

    const meta = b1.p0LedgerJobsMap?.get(jobId) || {};
    const btn = $('btnSaveLedgerEntry');
    if (btn) btn.disabled = true;
    try {
      await apiB1('/api/ledger/views', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          jobId,
          projectId: meta.projectId || '',
          title: meta.title || jobId,
          platform,
          views,
          likes,
          views24h,
          views7d,
          sampleSplit,
          cohortTag,
          hookType,
          triggerSource,
          channelState,
          thumbnailTs,
          cueSelectionUsed,
          h1TawaFacecam: h1Flags.h1TawaFacecam,
          h1VisualKejadian: h1Flags.h1VisualKejadian,
          h1ArcUtuh: h1Flags.h1ArcUtuh,
          h1HookMutePass: h1Flags.h1HookMutePass,
          postUrl,
          note,
          format: meta.format || '',
        }),
      });
      if ($('p0LedgerViews')) $('p0LedgerViews').value = '';
      if ($('p0LedgerLikes')) $('p0LedgerLikes').value = '';
      if ($('p0LedgerViews24h')) $('p0LedgerViews24h').value = '';
      if ($('p0LedgerViews7d')) $('p0LedgerViews7d').value = '';
      if ($('p0LedgerHookType')) $('p0LedgerHookType').value = '';
      if ($('p0LedgerChannelState')) $('p0LedgerChannelState').value = '';
      if ($('p0LedgerThumbTs')) $('p0LedgerThumbTs').value = '';
      if ($('p0LedgerCueSel')) $('p0LedgerCueSel').checked = false;
      ['p0LedgerF1', 'p0LedgerF2', 'p0LedgerF3', 'p0LedgerF4'].forEach((id) => {
        if ($(id)) $(id).checked = false;
      });
      updateP0LedgerH1Badge();
      if ($('p0LedgerUrl')) $('p0LedgerUrl').value = '';
      if ($('p0LedgerNote')) $('p0LedgerNote').value = '';
      const h1Res = evalH1B1(h1Flags);
      showToast(`📊 Catatan views (${sampleSplit} · ${cohortTag} · H1 ${h1Res.passedCount}/4) tersimpan!`, 'ok');
      refreshP0Ops();
    } catch (e) {
      showToast('Gagal simpan views ledger: ' + e.message, 'err');
    } finally {
      if (btn) btn.disabled = false;
    }
  }

  async function refreshP0Ops() {
    if (!$('p0PublishableCard')) return;
    const jobsKnown = window.__kfGetJobs ? window.__kfGetJobs() : [];
    const [invRes, pubRes, ledRes, jobsRes] = await Promise.allSettled([
      apiB1('/api/inventory'),
      apiB1('/api/publishable'),
      apiB1('/api/ledger/views'),
      jobsKnown && jobsKnown.length > 0 ? Promise.resolve(jobsKnown) : apiB1('/api/jobs?limit=150'),
    ]);

    if (invRes.status === 'fulfilled') {
      b1.p0Inventory = invRes.value;
    }
    if (pubRes.status === 'fulfilled') {
      const pv = pubRes.value;
      b1.p0Publishable = Array.isArray(pv) ? pv : (pv.items || pv.jobs || []);
    } else if ($('p0PublishableList')) {
      $('p0PublishableList').innerHTML = `<div class="muted text-xs" style="padding:10px;">Gagal memuat antrean siap posting: ${esc(pubRes.reason?.message || 'endpoint belum siap')}</div>`;
    }
    if (ledRes.status === 'fulfilled') {
      const lv = ledRes.value || {};
      b1.p0Ledger = {
        entries: Array.isArray(lv) ? lv : (lv.entries || lv.items || []),
        summary: lv.summary || null,
      };
    }

    renderP0Inventory();
    if (pubRes.status === 'fulfilled') renderP0Publishable();
    renderP0Ledger();
    populateP0LedgerJobSelect(jobsRes.status === 'fulfilled' ? jobsRes.value : jobsKnown);
  }
  window.__kfRefreshP0Ops = refreshP0Ops;

  // ==================== Real-Time SSE Bridge from app.js ====================
  let sseTimer = null;
  window.__kfOnSSE = (msg) => {
    if (!msg || !msg.type) return;
    if (msg.type === 'activity' && msg.payload) {
      b1.activity.unshift(msg.payload);
      if (b1.activity.length > 80) b1.activity.length = 80;
      if (b1.activeTab === 'team') renderActivityFeed();
      return;
    }
    if (msg.type === 'job_update' || msg.type === 'project_update') {
      if (sseTimer) clearTimeout(sseTimer);
      sseTimer = setTimeout(() => {
        if (b1.activeTab === 'home') refreshHome();
        else if (b1.activeTab === 'board') refreshBoard();
        else if (b1.activeTab === 'review') refreshReviewEnhancements();
        else if (b1.activeTab === 'queue') refreshP0Ops();
        else if (b1.activeTab === 'team') refreshTeamAndActivity();
      }, 500);
    }
  };
  window.addEventListener('kf:unlocked', () => {
    refreshHome();
    if (b1.activeTab === 'board') refreshBoard();
    if (b1.activeTab === 'review') refreshReviewEnhancements();
    if (b1.activeTab === 'queue') refreshP0Ops();
    if (b1.activeTab === 'team') refreshTeamAndActivity();
  });

  // ==================== Wire Modal & Filter Events ====================
  function wireEvents() {
    if ($('boardFilterProject')) $('boardFilterProject').addEventListener('change', (e) => { b1.boardFilter.projectId = e.target.value; refreshBoard(); });
    if ($('boardFilterMember')) $('boardFilterMember').addEventListener('change', (e) => { b1.boardFilter.member = e.target.value; refreshBoard(); });
    document.querySelectorAll('.board-moment-chip').forEach((btn) => {
      btn.addEventListener('click', () => {
        document.querySelectorAll('.board-moment-chip').forEach((x) => x.classList.remove('active'));
        btn.classList.add('active');
        b1.boardFilter.momentType = btn.dataset.moment || 'all';
        refreshBoard();
      });
    });
    if ($('btnRefreshBoard')) $('btnRefreshBoard').addEventListener('click', refreshBoard);
    if ($('activityMemberFilter')) $('activityMemberFilter').addEventListener('change', refreshTeamAndActivity);
    if ($('btnRefreshActivity')) $('btnRefreshActivity').addEventListener('click', refreshTeamAndActivity);

    // Review Sheet events (pause video cleanly + backdrop & Escape close!)
    if ($('btnCloseReviewSheet')) $('btnCloseReviewSheet').addEventListener('click', closeReviewSheet);
    if ($('b1ReviewSheet')) {
      $('b1ReviewSheet').addEventListener('click', (e) => {
        if (e.target === $('b1ReviewSheet')) closeReviewSheet();
      });
    }
    document.querySelectorAll('.reject-chip').forEach((chip) => {
      chip.addEventListener('click', () => {
        document.querySelectorAll('.reject-chip').forEach((c) => c.classList.remove('active'));
        chip.classList.add('active');
        b1.selectedRejectReason = chip.dataset.reason || '';
      });
    });
    if ($('btnRsApprove')) {
      $('btnRsApprove').addEventListener('click', async () => {
        if (!b1.reviewSheetJob) return;
        try {
          await apiB1(`/api/jobs/${encodeURIComponent(b1.reviewSheetJob.id)}/review`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ reviewStatus: 'approved', reviewNote: $('rsNote').value.trim() }),
          });
          closeReviewSheet();
          showToast('Disetujui -> Siap Posting', 'ok');
          refreshReviewEnhancements();
          refreshHome();
        } catch (e) {
          showToast(e.message, 'err');
        }
      });
    }
    if ($('btnRsReject')) {
      $('btnRsReject').addEventListener('click', async () => {
        if (!b1.reviewSheetJob) return;
        const note = $('rsNote').value.trim();
        const reason = [b1.selectedRejectReason, note].filter(Boolean).join(' — ');
        if (!reason) {
          showToast('Pilih alasan penolakan atau isi catatan terlebih dahulu', 'err');
          return;
        }
        try {
          await apiB1('/api/board/move', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ jobId: b1.reviewSheetJob.id, toColumn: 'kandidat', reason }),
          });
          closeReviewSheet();
          showToast('Dikembalikan ke Kandidat dengan alasan', 'info');
          refreshReviewEnhancements();
          refreshHome();
        } catch (e) {
          showToast(e.message, 'err');
        }
      });
    }

    // Batch Review (Select All / Reset / Approve / Reject with note)
    if ($('btnB1BatchSelectAll')) {
      $('btnB1BatchSelectAll').addEventListener('click', () => {
        (b1.visibleBatchIds || []).forEach((id) => b1.selectedBatchReview.add(id));
        document.querySelectorAll('#b1ReviewList .b1-rev-chk').forEach((c) => { c.checked = true; });
        if ($('b1BatchCount')) $('b1BatchCount').textContent = String(b1.selectedBatchReview.size);
      });
    }
    if ($('btnB1BatchClear')) {
      $('btnB1BatchClear').addEventListener('click', () => {
        b1.selectedBatchReview.clear();
        document.querySelectorAll('#b1ReviewList .b1-rev-chk').forEach((c) => { c.checked = false; });
        if ($('b1BatchCount')) $('b1BatchCount').textContent = '0';
      });
    }
    async function runBatchReview(status) {
      const ids = Array.from(b1.selectedBatchReview);
      if (ids.length === 0) {
        showToast('Pilih minimal 1 klip dulu', 'err');
        return;
      }
      const note = ($('inpB1BatchNote')?.value || '').trim();
      if (status === 'rejected' && !note) {
        showToast('Isi alasan penolakan batch terlebih dahulu', 'err');
        return;
      }
      try {
        const res = await apiB1('/api/jobs/review/batch', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ jobIds: ids, reviewStatus: status, reviewNote: note, rejectReason: status === 'rejected' ? note : '' }),
        });
        b1.selectedBatchReview.clear();
        if ($('inpB1BatchNote')) $('inpB1BatchNote').value = '';
        showToast(`Batch ${status === 'approved' ? 'approve' : 'reject'} selesai (${(res.updated || []).length} klip)`, status === 'approved' ? 'ok' : 'info');
        refreshReviewEnhancements();
        refreshHome();
      } catch (e) {
        showToast(e.message, 'err');
      }
    }
    if ($('btnB1BatchApprove')) $('btnB1BatchApprove').addEventListener('click', () => runBatchReview('approved'));
    if ($('btnB1BatchReject')) $('btnB1BatchReject').addEventListener('click', () => runBatchReview('rejected'));

    // Publish Kit modal: save/validate, analytics sync, copy, TikTok/YT/IG publish & close
    if ($('btnClosePublishKit')) $('btnClosePublishKit').addEventListener('click', closePublishKitModal);
    if ($('b1PublishKitModal')) {
      $('b1PublishKitModal').addEventListener('click', (e) => {
        if (e.target === $('b1PublishKitModal')) closePublishKitModal();
      });
    }
    document.addEventListener('keydown', (e) => {
      if (e.key !== 'Escape') return;
      if ($('b1ReviewSheet') && !$('b1ReviewSheet').classList.contains('hidden')) closeReviewSheet();
      if ($('b1PublishKitModal') && !$('b1PublishKitModal').classList.contains('hidden')) closePublishKitModal();
      if ($('p0MarkPostedModal') && !$('p0MarkPostedModal').classList.contains('hidden')) closeMarkPostedModal();
      if ($('b1MobileMenuSheet') && !$('b1MobileMenuSheet').classList.contains('hidden')) $('b1MobileMenuSheet').classList.add('hidden');
    });

    // P0 Operations (Publishable Queue + Manual Mark-Posted Modal + Views Ledger)
    if ($('btnRefreshP0Ops')) $('btnRefreshP0Ops').addEventListener('click', refreshP0Ops);
    if ($('btnToggleP0Ops') && $('p0OpsBody')) {
      $('btnToggleP0Ops').addEventListener('click', () => {
        const hidden = $('p0OpsBody').classList.toggle('hidden');
        $('btnToggleP0Ops').textContent = hidden ? 'Buka' : 'Tutup';
      });
    }
    if ($('btnSaveLedgerEntry')) $('btnSaveLedgerEntry').addEventListener('click', submitLedgerEntry);
    ['p0LedgerF1', 'p0LedgerF2', 'p0LedgerF3', 'p0LedgerF4'].forEach((id) => {
      if ($(id)) $(id).addEventListener('change', updateP0LedgerH1Badge);
    });
    updateP0LedgerH1Badge();
    if ($('btnCloseMarkPosted')) $('btnCloseMarkPosted').addEventListener('click', closeMarkPostedModal);
    if ($('btnCancelMarkPosted')) $('btnCancelMarkPosted').addEventListener('click', closeMarkPostedModal);
    if ($('btnSubmitMarkPosted')) $('btnSubmitMarkPosted').addEventListener('click', submitMarkPosted);
    if ($('p0MarkPostedModal')) {
      $('p0MarkPostedModal').addEventListener('click', (e) => {
        if (e.target === $('p0MarkPostedModal')) closeMarkPostedModal();
      });
    }

    const copyVal = (id, label) => {
      const txt = $(id)?.value || '';
      if (!txt) return;
      navigator.clipboard.writeText(txt).then(() => showToast(`${label} disalin`, 'ok')).catch(() => {});
    };
    if ($('btnCopyPkTikTok')) $('btnCopyPkTikTok').addEventListener('click', () => copyVal('pkTikTok', 'Caption TikTok'));
    if ($('btnCopyPkShorts')) $('btnCopyPkShorts').addEventListener('click', () => copyVal('pkShortsTitle', 'Judul Shorts'));
    if ($('btnCopyPkReels')) $('btnCopyPkReels').addEventListener('click', () => copyVal('pkReels', 'Caption Reels'));

    if ($('btnPkRegenerate')) {
      $('btnPkRegenerate').addEventListener('click', async () => {
        if (!b1.publishKitJob) return;
        try {
          const res = await apiB1(`/api/jobs/${encodeURIComponent(b1.publishKitJob)}/publish-kit`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              tiktokCaption: $('pkTikTok')?.value || '',
              shortsTitle: $('pkShortsTitle')?.value || '',
              shortsDescription: $('pkShortsDesc')?.value || '',
              reelsCaption: $('pkReels')?.value || '',
            }),
          });
          renderPublishKitState(b1.publishModalJob, res.publishKit);
          showToast('Caption disimpan & divalidasi SOP!', 'ok');
        } catch (e) {
          showToast(e.message, 'err');
        }
      });
    }
    if ($('btnPkSyncAnalytics')) {
      $('btnPkSyncAnalytics').addEventListener('click', async () => {
        if (!b1.publishKitJob) return;
        try {
          const res = await apiB1(`/api/jobs/${encodeURIComponent(b1.publishKitJob)}/analytics/sync`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ accountId: $('pkAccountSelect')?.value || '' }),
          });
          const a = res.analytics || {};
          showToast(`YouTube Stats: ${a.views || 0} views · ${a.likes || 0} likes · ${a.comments || 0} komen`, 'ok');
        } catch (e) {
          showToast(e.message, 'err');
        }
      });
    }

    function buildPublishCommonPayload() {
      const p = {
        accountId: $('pkAccountSelect')?.value || '',
        privacyStatus: $('pkPrivacyStatus')?.value || 'public',
      };
      const sched = ($('pkScheduledAt')?.value || '').trim();
      if (sched) {
        const dt = new Date(sched);
        if (!Number.isNaN(dt.getTime())) p.scheduledAt = dt.toISOString();
      }
      return p;
    }

    if ($('btnPkPublishTikTok')) {
      $('btnPkPublishTikTok').addEventListener('click', async () => {
        if (!b1.publishKitJob) return;
        try {
          await apiB1(`/api/jobs/${encodeURIComponent(b1.publishKitJob)}/publish`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ...buildPublishCommonPayload(), platform: 'tiktok', caption: $('pkTikTok').value }),
          });
          showToast('Publish TikTok (sau sidecar) dimulai', 'ok');
        } catch (e) {
          showToast(e.message, 'err');
        }
      });
    }
    if ($('btnPkPublishYT')) {
      $('btnPkPublishYT').addEventListener('click', async () => {
        if (!b1.publishKitJob) return;
        try {
          await apiB1(`/api/jobs/${encodeURIComponent(b1.publishKitJob)}/publish`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ...buildPublishCommonPayload(), platform: 'youtube', title: $('pkShortsTitle').value, caption: $('pkShortsDesc').value }),
          });
          showToast('Upload YouTube Shorts dimulai', 'ok');
        } catch (e) {
          showToast(e.message, 'err');
        }
      });
    }
    if ($('btnPkPublishIG')) {
      $('btnPkPublishIG').addEventListener('click', async () => {
        if (!b1.publishKitJob) return;
        try {
          await apiB1(`/api/jobs/${encodeURIComponent(b1.publishKitJob)}/publish`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ...buildPublishCommonPayload(), platform: 'instagram', caption: $('pkReels').value }),
          });
          showToast('Upload Instagram Reels dimulai', 'ok');
        } catch (e) {
          showToast(e.message, 'err');
        }
      });
    }

    // Re-filter review queue when filter tabs change
    document.querySelectorAll('#tab-review .review-filter').forEach((btn) => {
      btn.addEventListener('click', () => setTimeout(refreshReviewEnhancements, 40));
    });
    if ($('reviewMemberFilter')) $('reviewMemberFilter').addEventListener('change', () => setTimeout(refreshReviewEnhancements, 40));
  }

  // Bootstrap after DOM ready
  wireNavigation();
  wireEvents();
  refreshHome();
  setTimeout(enhanceLibraryCards, 900);
})();
