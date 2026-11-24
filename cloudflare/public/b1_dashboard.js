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
  };

  function getTok() {
    return (localStorage.getItem('kf_engine_token') || '').trim();
  }

  async function apiB1(path, opts = {}) {
    const hdrs = new Headers(opts.headers || {});
    const tok = getTok();
    if (tok && !hdrs.has('X-Engine-Token')) hdrs.set('X-Engine-Token', tok);
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

  // ==================== Navigation (Left Rail + Mobile Bottom Bar + Contextual) ====================
  function openTab(tab) {
    b1.activeTab = tab;
    document.querySelectorAll('.tab-panel').forEach((p) => {
      p.classList.toggle('active', p.id === `tab-${tab}`);
    });
    document.querySelectorAll('[data-tab]').forEach((btn) => {
      btn.classList.toggle('active', btn.dataset.tab === tab);
    });
    if (tab === 'home') refreshHome();
    if (tab === 'board') refreshBoard();
    if (tab === 'review') refreshReviewEnhancements();
    if (tab === 'queue') enhanceLibraryCards();
    if (tab === 'team') refreshTeamAndActivity();
    if (tab === 'studio') window.dispatchEvent(new Event('resize'));
  }
  window.__kfSwitchTab = openTab;

  function wireNavigation() {
    document.querySelectorAll('.rail-nav-btn, .mob-nav-btn').forEach((btn) => {
      btn.addEventListener('click', () => {
        if (btn.dataset.tab) openTab(btn.dataset.tab);
      });
    });
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
      } else if (b1.statusRaw && b1.statusRaw.deps) {
        pill.className = 'engine-pill awake';
        txt.textContent = 'Bangun';
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
    if (selProj && selProj.options.length <= 1) {
      apiB1('/api/projects').then((projs) => {
        selProj.innerHTML = '<option value="all">Semua Project</option>';
        (projs || []).forEach((p) => {
          const opt = document.createElement('option');
          opt.value = p.id;
          opt.textContent = p.title || p.id;
          selProj.appendChild(opt);
        });
        selProj.value = b1.boardFilter.projectId;
      }).catch(() => {});
    }
    const selMem = $('boardFilterMember');
    if (selMem && selMem.options.length <= 1) {
      apiB1('/api/team/metrics').then((tm) => {
        selMem.innerHTML = '<option value="all">Semua Anggota</option>';
        (tm.members || []).forEach((m) => {
          const opt = document.createElement('option');
          opt.value = m.id;
          opt.textContent = `${m.name} (${m.role})`;
          selMem.appendChild(opt);
        });
        selMem.value = b1.boardFilter.member;
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
            <div style="display:flex;gap:4px;">
              <button class="btn btn-2xs btn-ghost btn-k-edit" title="Buka di Studio">Edit</button>
              ${c.jobId && col.id === 'review' ? '<button class="btn btn-2xs btn-secondary btn-k-rev">Review</button>' : ''}
              ${c.jobId && (col.id === 'siap_posting' || col.id === 'diposting') ? '<button class="btn btn-2xs btn-secondary btn-k-kit">Kit</button>' : ''}
            </div>
          </div>
        `;

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
    if (sel && sel.options.length <= 1) {
      sel.innerHTML = '<option value="all">Semua Aktivitas</option>';
      members.forEach((m) => {
        const o = document.createElement('option');
        o.value = m.id;
        o.textContent = m.name;
        sel.appendChild(o);
      });
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
  async function enhanceLibraryCards() {
    const box = $('jobsList');
    if (!box) return;
    try {
      const jobs = await apiB1('/api/jobs');
      const byTitleOrIdx = new Map((jobs || []).map((j) => [j.id, j]));
      const cards = box.querySelectorAll('.job-card');
      cards.forEach((card) => {
        if (card.dataset.b2Enhanced) return;
        const vid = card.querySelector('video.job-video');
        const dl = card.querySelector('a[href*="/api/jobs/"]');
        const href = vid?.getAttribute('src') || dl?.getAttribute('href') || '';
        const m = href.match(/\/api\/jobs\/([^/]+)\/video/);
        if (!m) return;
        const job = byTitleOrIdx.get(decodeURIComponent(m[1]));
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
          <div style="display:flex;align-items:center;justify-content:space-between;margin-top:6px;">
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
        }
      });
    } catch {}
  }

  async function openPublishKitModalById(jobId) {
    try {
      const res = await apiB1(`/api/jobs/${encodeURIComponent(jobId)}/publish-kit`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
      });
      b1.publishKitJob = jobId;
      const kit = res.publishKit;
      $('pkTikTok').value = kit.tiktok?.caption || '';
      $('pkShortsTitle').value = kit.shorts?.title || '';
      $('pkShortsDesc').value = kit.shorts?.description || '';
      $('pkReels').value = kit.reels?.caption || '';
      $('pkChecklist').innerHTML = `
        <span class="${kit.checklist?.readyToPost ? 'sop-ready-pill' : 'sop-notready-pill'}">
          ${kit.checklist?.readyToPost ? 'SEMUA 4 CEK SOP LOLOS — SIAP POSTING' : 'BELUM LENGKAP: ' + esc((kit.checklist?.missing || []).join(', '))}
        </span>
      `;
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

    // Review Sheet events
    if ($('btnCloseReviewSheet')) $('btnCloseReviewSheet').addEventListener('click', () => $('b1ReviewSheet').classList.add('hidden'));
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
          $('b1ReviewSheet').classList.add('hidden');
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
          $('b1ReviewSheet').classList.add('hidden');
          showToast('Dikembalikan ke Kandidat dengan alasan', 'info');
          refreshReviewEnhancements();
          refreshHome();
        } catch (e) {
          showToast(e.message, 'err');
        }
      });
    }

    // Batch Review Approve
    if ($('btnB1BatchApprove')) {
      $('btnB1BatchApprove').addEventListener('click', async () => {
        const ids = Array.from(b1.selectedBatchReview);
        if (ids.length === 0) return;
        try {
          const res = await apiB1('/api/jobs/review/batch', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ jobIds: ids, reviewStatus: 'approved' }),
          });
          b1.selectedBatchReview.clear();
          showToast(`Batch approve selesai (${(res.updated || []).length} klip disetujui)`, 'ok');
          refreshReviewEnhancements();
          refreshHome();
        } catch (e) {
          showToast(e.message, 'err');
        }
      });
    }

    // Publish Kit modal copy & close
    if ($('btnClosePublishKit')) $('btnClosePublishKit').addEventListener('click', () => $('b1PublishKitModal').classList.add('hidden'));
    const copyVal = (id, label) => {
      const txt = $(id)?.value || '';
      if (!txt) return;
      navigator.clipboard.writeText(txt).then(() => showToast(`${label} disalin`, 'ok')).catch(() => {});
    };
    if ($('btnCopyPkTikTok')) $('btnCopyPkTikTok').addEventListener('click', () => copyVal('pkTikTok', 'Caption TikTok'));
    if ($('btnCopyPkShorts')) $('btnCopyPkShorts').addEventListener('click', () => copyVal('pkShortsTitle', 'Judul Shorts'));
    if ($('btnCopyPkReels')) $('btnCopyPkReels').addEventListener('click', () => copyVal('pkReels', 'Caption Reels'));
    if ($('btnPkPublishTikTok')) {
      $('btnPkPublishTikTok').addEventListener('click', async () => {
        if (!b1.publishKitJob) return;
        try {
          await apiB1(`/api/jobs/${encodeURIComponent(b1.publishKitJob)}/publish`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ platform: 'tiktok', caption: $('pkTikTok').value }),
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
            body: JSON.stringify({ platform: 'youtube', title: $('pkShortsTitle').value, caption: $('pkShortsDesc').value }),
          });
          showToast('Upload YouTube Shorts dimulai', 'ok');
        } catch (e) {
          showToast(e.message, 'err');
        }
      });
    }

    // Observe Library re-renders from app.js so SOP checklist + Draft/Kit buttons stay attached
    const jobsBox = $('jobsList');
    if (jobsBox && window.MutationObserver) {
      new MutationObserver(() => enhanceLibraryCards()).observe(jobsBox, { childList: true });
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
