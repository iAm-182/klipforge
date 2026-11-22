(() => {
  'use strict';

  // ==================== Global State ====================
  const state = {
    deps: null,
    settings: null,
    campaigns: [],
    autopilots: [],
    editingAutopilotId: '',
    reviewFilter: 'pending_review',
    topNSlots: [], // Ordered compilation slots (first plays as #N, last plays as #1 climax!)
    audioAssets: [],
    customSFXCues: [
      { assetId: 'sfx-whoosh', atSec: 0.1, atPeak: false, volume: 0.8 },
      { assetId: 'sfx-boom', atSec: 5.0, atPeak: true, volume: 1.0 },
    ],
    playingAudioId: '',
    activeCampaignId: '',
    editingCampaignId: '',
    projects: [],
    activeProject: null,
    jobs: [],
    selectedCandidateId: null,
    layout: {
      aspectRatio: '9:16',
      resolution: '1080p',
      cropMode: 'split_custom',
      splitRatio: 0.62,
      gameplayBox: { x: 0.14, y: 0.0, w: 0.72, h: 1.0 },
      facecamBox: { x: 0.72, y: 0.62, w: 0.28, h: 0.38 },
    },
    editingBox: 'gameplay', // 'gameplay' or 'facecam'
    previewImg: null,
    wmImg: null,
    currentSeekSec: 0,
  };

  const $ = (id) => document.getElementById(id);

  // ==================== API Helper ====================
  async function api(path, opts = {}) {
    const res = await fetch(path, opts);
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      throw new Error(data.error || `HTTP ${res.status}`);
    }
    return data;
  }

  function toast(msg, type = 'info') {
    const box = $('toastBox');
    const el = document.createElement('div');
    el.className = `toast ${type}`;
    el.textContent = msg;
    box.appendChild(el);
    setTimeout(() => el.remove(), 3800);
  }

  function fmtTime(sec) {
    const t = Math.max(0, Math.round(sec || 0));
    const h = Math.floor(t / 3600);
    const m = Math.floor((t % 3600) / 60);
    const s = t % 60;
    const pad = (n) => String(n).padStart(2, '0');
    return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`;
  }

  function fmtViews(n) {
    if (!n) return '';
    if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M views';
    if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K views';
    return n + ' views';
  }

  function fmtBytes(b) {
    if (!b) return '0 B';
    if (b >= 1024 * 1024) return (b / (1024 * 1024)).toFixed(2) + ' MB';
    return (b / 1024).toFixed(1) + ' KB';
  }

  function appendLog(source, level, message) {
    const box = $('liveConsole');
    const row = document.createElement('div');
    row.className = `log-line log-${level || 'info'}`;
    const ts = new Date().toLocaleTimeString('id-ID', { hour12: false });
    row.textContent = `[${ts}] [${source}] ${message}`;
    box.appendChild(row);
    box.scrollTop = box.scrollHeight;
  }

  // ==================== Navigation Tabs ====================
  function switchTab(tabName) {
    document.querySelectorAll('.nav-btn').forEach((b) => {
      b.classList.toggle('active', b.dataset.tab === tabName);
    });
    document.querySelectorAll('.tab-panel').forEach((p) => {
      p.classList.toggle('active', p.id === `tab-${tabName}`);
    });
    if (tabName === 'studio' && state.activeProject) {
      setTimeout(() => {
        drawHypeChart();
        drawCropCanvases();
      }, 40);
    }
  }

  document.querySelectorAll('.nav-btn').forEach((btn) => {
    btn.addEventListener('click', () => switchTab(btn.dataset.tab));
  });

  // ==================== SSE Real-Time Connection ====================
  function connectSSE() {
    const dot = $('sseDot');
    const es = new EventSource('/api/events');
    es.onopen = () => {
      dot.classList.add('online');
      appendLog('sse', 'success', 'Terhubung ke real-time Go engine stream.');
    };
    es.onerror = () => {
      dot.classList.remove('online');
    };
    es.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data);
        if (msg.type === 'log') {
          appendLog(msg.payload.source, msg.payload.level, msg.payload.message);
        } else if (msg.type === 'project_update') {
          onProjectUpdate(msg.payload);
        } else if (msg.type === 'job_update') {
          onJobUpdate(msg.payload);
        } else if (msg.type === 'autopilot_update') {
          onAutopilotUpdate(msg.payload);
        }
      } catch (e) {}
    };
  }

  function onProjectUpdate(proj) {
    const idx = state.projects.findIndex((p) => p.id === proj.id);
    if (idx >= 0) state.projects[idx] = proj;
    else state.projects.unshift(proj);
    renderProjectDropdown();

    if (state.activeProject && state.activeProject.id === proj.id) {
      const prevStatus = state.activeProject.status;
      state.activeProject = proj;
      renderActiveProject();
      if (prevStatus !== 'ready' && proj.status === 'ready') {
        toast(`Analisis selesai! ${proj.candidates?.length || 0} momen hits ditemukan.`, 'ok');
      } else if (proj.status === 'error') {
        toast(`Gagal analisis: ${proj.statusMessage}`, 'err');
      }
    }
  }

  function onJobUpdate(job) {
    const idx = state.jobs.findIndex((j) => j.id === job.id);
    if (idx >= 0) state.jobs[idx] = job;
    else state.jobs.unshift(job);
    updateJobBadge();
    renderJobs();
    renderReviewInbox();
    if (job.status === 'done') {
      toast(`Clip selesai: ${job.title}`, 'ok');
    } else if (job.status === 'error') {
      toast(`Render gagal: ${job.title}`, 'err');
    }
  }

  function onAutopilotUpdate(rule) {
    const idx = state.autopilots.findIndex((r) => r.id === rule.id);
    if (idx >= 0) state.autopilots[idx] = rule;
    else state.autopilots.push(rule);
    renderAutopilotList();
    if (state.editingAutopilotId === rule.id) {
      $('autoStatusLine').textContent = rule.lastStatus || '—';
    }
  }

  // ==================== Initial Data Load ====================
  async function init() {
    connectSSE();
    try {
      const [status, campaigns, projects, jobs, audioAssets, autopilots] = await Promise.all([
        api('/api/status'),
        api('/api/campaigns'),
        api('/api/projects'),
        api('/api/jobs'),
        api('/api/audio'),
        api('/api/autopilot'),
      ]);
      state.deps = status.deps;
      state.settings = status.settings;
      state.campaigns = campaigns || [];
      state.projects = projects || [];
      state.jobs = jobs || [];
      state.audioAssets = audioAssets || [];
      state.autopilots = autopilots || [];

      renderDepsAndSettings();
      renderAudioLibrary();
      if (state.campaigns.length > 0) {
        state.activeCampaignId = state.campaigns[0].id;
        state.editingCampaignId = state.campaigns[0].id;
      }
      renderCampaignSelectors();
      applyCampaignDefaultsToStudio();
      renderCampaignList();
      loadCampaignIntoEditor(state.editingCampaignId);

      if (state.autopilots.length > 0) {
        state.editingAutopilotId = state.autopilots[0].id;
      }
      renderAutopilotList();
      loadAutopilotIntoEditor(state.editingAutopilotId);

      renderProjectDropdown();
      if (state.projects.length > 0) {
        state.activeProject = state.projects[0];
        $('projectHistorySelect').value = state.activeProject.id;
        renderActiveProject();
      }
      updateJobBadge();
      renderJobs();
      renderReviewInbox();
      renderTopNBuilder();
    } catch (e) {
      const badge = $('depBadge');
      if (badge) {
        badge.textContent = 'Engine Offline';
        badge.className = 'dep-pill err';
      }
      const banner = $('projectStatusBanner');
      if (banner) {
        banner.classList.remove('hidden');
        $('statusBannerTitle').textContent = 'Go Engine (FFmpeg + yt-dlp) Sedang Offline';
        $('statusBannerMsg').textContent = 'Dashboard sudah online di Cloudflare Edge! Untuk mulai scan & render video, jalankan start-online.bat di PC kamu (atau Docker di VPS), lalu refresh halaman ini.';
      }
      toast('Engine offline — jalankan start-online.bat di PC kamu.', 'err');
    }
  }

  // ==================== Campaigns Management ====================
  function getActiveCampaign() {
    return state.campaigns.find((c) => c.id === state.activeCampaignId) || state.campaigns[0] || null;
  }

  function renderCampaignSelectors() {
    const sel = $('activeCampaignSelect');
    sel.innerHTML = '';
    state.campaigns.forEach((c) => {
      const opt = document.createElement('option');
      opt.value = c.id;
      opt.textContent = `${c.name} (${c.platform || 'Custom'})`;
      sel.appendChild(opt);
    });
    if (state.activeCampaignId) sel.value = state.activeCampaignId;
    renderCampaignStrip();
  }

  function renderCampaignStrip() {
    const camp = getActiveCampaign();
    if (!camp) return;
    $('stripCampaignName').textContent = `🏆 ${camp.name}`;

    const wmBadge = $('stripWmStatus');
    if (camp.watermark?.enabled && camp.watermark?.fileName) {
      wmBadge.textContent = `✅ Watermark: ${camp.watermark.position}`;
      wmBadge.className = 'strip-badge ok';
      loadWatermarkImage(`/api/assets/${encodeURIComponent(camp.watermark.fileName)}`);
    } else {
      wmBadge.textContent = '⚠️ Watermark: Belum Upload';
      wmBadge.className = 'strip-badge warn';
      state.wmImg = null;
    }

    const outroBadge = $('stripOutroStatus');
    if (camp.outro?.enabled && camp.outro?.fileName) {
      const dur = camp.outro.duration ? `${camp.outro.duration.toFixed(1)}s` : 'OK';
      outroBadge.textContent = `✅ Outro (${dur}, Bersih)`;
      outroBadge.className = 'strip-badge ok';
    } else {
      outroBadge.textContent = '⚠️ Outro 3s: Belum Upload';
      outroBadge.className = 'strip-badge warn';
    }

    $('stripDiscordTag').textContent = `Discord Tag: ${camp.discordTag || '—'}`;
    $('stripHashtags').textContent = (camp.hashtags || []).join(' ') || '(Belum ada hashtag)';
    drawCropCanvases();
  }

  function loadWatermarkImage(url) {
    const img = new Image();
    img.crossOrigin = 'anonymous';
    img.onload = () => {
      state.wmImg = img;
      drawCropCanvases();
    };
    img.src = url;
  }

  function applyCampaignDefaultsToStudio() {
    const camp = getActiveCampaign();
    if (!camp) return;
    if (camp.defaultLayout) {
      state.layout = JSON.parse(JSON.stringify(camp.defaultLayout));
      $('selCropMode').value = state.layout.cropMode || 'split_custom';
      $('selAspect').value = state.layout.aspectRatio || '9:16';
      $('selRes').value = state.layout.resolution || '1080p';
      const sr = Math.round((state.layout.splitRatio || 0.62) * 100);
      $('rngSplitRatio').value = sr;
      $('lblSplitRatio').textContent = sr + '%';
    }
    if (camp.defaultSubtitles) {
      $('chkSubtitles').checked = !!camp.defaultSubtitles.enabled;
      $('selSubPreset').value = camp.defaultSubtitles.preset || 'viral-yellow';
      $('selSubPos').value = camp.defaultSubtitles.position || 'split-seam';
      $('selSubFont').value = camp.defaultSubtitles.fontName || 'Impact';
    }
    $('chkAudioNorm').checked = camp.defaultAudioNorm !== false;
    $('chkUseWatermark').checked = !!camp.watermark?.enabled;
    $('chkUseOutro').checked = !!camp.outro?.enabled;
    if (camp.defaultAudioFx) {
      $('selBGM').value = camp.defaultAudioFx.bgmAssetId || '';
      const bv = Math.round((camp.defaultAudioFx.bgmVolume || 0.18) * 100);
      $('rngBgmVol').value = bv;
      $('lblBgmVol').textContent = bv + '%';
      $('chkAutoDuck').checked = camp.defaultAudioFx.autoDuck !== false;
      $('selSFXPreset').value = camp.defaultAudioFx.sfxAutoPreset || 'meme_punch';
      toggleCustomSFXUI();
    }
    if (camp.defaultVideoFx) {
      $('selColorPreset').value = camp.defaultVideoFx.colorPreset || 'vivid_gaming';
      $('selMotionPreset').value = camp.defaultVideoFx.motionPreset || 'peak_punch';
      $('selSpeed').value = String(camp.defaultVideoFx.speed || 1.0);
      $('chkFadeIn').checked = camp.defaultVideoFx.fadeIn !== false;
      $('chkSharpen').checked = camp.defaultVideoFx.sharpen !== false;
    }
    drawCropCanvases();
  }

  function renderCampaignList() {
    const box = $('campaignList');
    box.innerHTML = '';
    state.campaigns.forEach((c) => {
      const el = document.createElement('div');
      el.className = 'camp-item' + (c.id === state.editingCampaignId ? ' active' : '');
      const wmOk = c.watermark?.fileName ? '📍WM' : '⚠️WM';
      const outOk = c.outro?.fileName ? '🎞️Outro' : '⚠️Outro';
      el.innerHTML = `
        <div class="camp-item-name">${escapeHtml(c.name)}</div>
        <div class="camp-item-meta">${escapeHtml(c.platform || '')} · ${wmOk} · ${outOk}</div>
      `;
      el.addEventListener('click', () => {
        state.editingCampaignId = c.id;
        renderCampaignList();
        loadCampaignIntoEditor(c.id);
      });
      box.appendChild(el);
    });
  }

  function loadCampaignIntoEditor(id) {
    const c = state.campaigns.find((x) => x.id === id);
    if (!c) return;
    $('campEditorTitle').textContent = `Edit Campaign: ${c.name}`;
    $('cName').value = c.name || '';
    $('cPlatform').value = c.platform || 'MotionKlip';
    $('cCreator').value = c.creatorName || '';
    $('cChannelUrl').value = c.creatorChannelUrl || '';
    $('cDiscordTag').value = c.discordTag || 'MKLP';
    $('cEligibleDate').value = c.eligibleStartDate || '';
    $('cDriveUrl').value = c.referenceDriveUrl || '';
    $('cDriveOpenLink').href = c.referenceDriveUrl || '#';
    $('cHashtags').value = (c.hashtags || []).join(' ');
    $('cMentions').value = (c.socialMentions || []).join(' ');
    $('cBrief').value = c.briefSummary || '';
    $('cRules').value = (c.rules || []).join('\n');

    // Watermark fields
    const wm = c.watermark || {};
    $('cWmEnabled').checked = !!wm.enabled;
    $('cWmPos').value = wm.position || 'top-right';
    $('cWmMargin').value = wm.marginPx ?? 32;
    const sc = Math.round((wm.scale || 0.2) * 100);
    $('cWmScale').value = sc;
    $('lblWmScale').textContent = sc + '%';
    const op = Math.round((wm.opacity || 0.92) * 100);
    $('cWmOpacity').value = op;
    $('lblWmOpacity').textContent = op + '%';
    $('cWmCustomX').value = wm.customX ?? 0.72;
    $('cWmCustomY').value = wm.customY ?? 0.06;
    $('cWmOnOutro').checked = !!wm.onOutro;
    $('wmCustomXY').classList.toggle('hidden', $('cWmPos').value !== 'custom');

    const wmPrev = $('wmPreviewBox');
    if (wm.fileName) {
      const url = `/api/assets/${encodeURIComponent(wm.fileName)}?t=${Date.now()}`;
      if (/\.(mp4|mov|webm)$/i.test(wm.fileName)) {
        wmPrev.innerHTML = `<video src="${url}" autoplay loop muted></video>`;
      } else {
        wmPrev.innerHTML = `<img src="${url}" alt="Watermark" />`;
      }
    } else {
      wmPrev.innerHTML = `<span class="muted">Belum ada watermark di-upload</span>`;
    }

    // Outro fields
    const out = c.outro || {};
    $('cOutroEnabled').checked = !!out.enabled;
    const outPrev = $('outroPreviewBox');
    if (out.fileName) {
      const url = `/api/assets/${encodeURIComponent(out.fileName)}?t=${Date.now()}`;
      outPrev.innerHTML = `<video src="${url}" controls></video><div class="text-xs muted mt-4">${escapeHtml(out.fileName)} (${(out.duration || 0).toFixed(2)}s)</div>`;
    } else {
      outPrev.innerHTML = `<span class="muted">Upload video template 3 detik dari Google Drive di sini</span>`;
    }

    // Intro fields
    const intro = c.intro || {};
    $('cIntroEnabled').checked = !!intro.enabled;
    $('introFileInfo').textContent = intro.fileName
      ? `✅ ${intro.fileName} (${(intro.duration || 0).toFixed(2)}s)`
      : 'Belum ada intro';
  }

  function gatherCampaignFromEditor() {
    const orig = state.campaigns.find((x) => x.id === state.editingCampaignId) || {};
    const hashtags = $('cHashtags').value.trim().split(/\s+/).filter(Boolean);
    const mentions = $('cMentions').value.trim().split(/\s+/).filter(Boolean);
    const rules = $('cRules').value.split('\n').map((s) => s.trim()).filter(Boolean);

    return {
      ...orig,
      name: $('cName').value.trim() || 'Untitled Campaign',
      platform: $('cPlatform').value.trim(),
      creatorName: $('cCreator').value.trim(),
      creatorChannelUrl: $('cChannelUrl').value.trim(),
      discordTag: $('cDiscordTag').value.trim(),
      eligibleStartDate: $('cEligibleDate').value,
      referenceDriveUrl: $('cDriveUrl').value.trim(),
      hashtags,
      socialMentions: mentions,
      briefSummary: $('cBrief').value.trim(),
      rules,
      watermark: {
        ...(orig.watermark || {}),
        enabled: $('cWmEnabled').checked,
        position: $('cWmPos').value,
        marginPx: parseInt($('cWmMargin').value, 10) || 24,
        scale: (parseInt($('cWmScale').value, 10) || 20) / 100,
        opacity: (parseInt($('cWmOpacity').value, 10) || 92) / 100,
        customX: parseFloat($('cWmCustomX').value) || 0.72,
        customY: parseFloat($('cWmCustomY').value) || 0.06,
        onOutro: $('cWmOnOutro').checked,
      },
      outro: {
        ...(orig.outro || {}),
        enabled: $('cOutroEnabled').checked,
        keepRaw: true,
      },
      intro: {
        ...(orig.intro || {}),
        enabled: $('cIntroEnabled').checked,
        keepRaw: true,
      },
    };
  }

  async function saveEditedCampaign() {
    const payload = gatherCampaignFromEditor();
    try {
      const saved = await api(`/api/campaigns/${encodeURIComponent(payload.id)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      const idx = state.campaigns.findIndex((x) => x.id === saved.id);
      if (idx >= 0) state.campaigns[idx] = saved;
      renderCampaignSelectors();
      renderCampaignList();
      loadCampaignIntoEditor(saved.id);
      toast('Campaign berhasil disimpan!', 'ok');
    } catch (e) {
      toast('Gagal simpan campaign: ' + e.message, 'err');
    }
  }

  async function uploadCampaignAsset(assetType, file) {
    if (!file || !state.editingCampaignId) return;
    const fd = new FormData();
    fd.append('file', file);
    toast(`Mengunggah ${assetType}...`, 'info');
    try {
      const saved = await api(`/api/campaigns/${encodeURIComponent(state.editingCampaignId)}/upload/${assetType}`, {
        method: 'POST',
        body: fd,
      });
      const idx = state.campaigns.findIndex((x) => x.id === saved.id);
      if (idx >= 0) state.campaigns[idx] = saved;
      renderCampaignSelectors();
      renderCampaignList();
      loadCampaignIntoEditor(saved.id);
      toast(`Upload ${assetType} sukses!`, 'ok');
    } catch (e) {
      toast(`Upload ${assetType} gagal: ` + e.message, 'err');
    }
  }

  // ==================== Project Analysis & Rendering ====================
  function renderProjectDropdown() {
    const sel = $('projectHistorySelect');
    const cur = state.activeProject?.id || '';
    sel.innerHTML = '';
    if (state.projects.length === 0) {
      sel.innerHTML = '<option value="">— Belum ada project —</option>';
      return;
    }
    state.projects.forEach((p) => {
      const opt = document.createElement('option');
      opt.value = p.id;
      opt.textContent = `${p.title} (${fmtTime(p.durationSec)})`;
      sel.appendChild(opt);
    });
    if (cur) sel.value = cur;
  }

  function renderActiveProject() {
    const p = state.activeProject;
    if (!p) {
      $('studioEmpty').classList.remove('hidden');
      $('studioWorkspace').classList.add('hidden');
      $('projectStatusBanner').classList.add('hidden');
      return;
    }

    $('studioEmpty').classList.add('hidden');
    const busy = p.status === 'scanning' || p.status === 'downloading';
    $('projectStatusBanner').classList.toggle('hidden', !busy);
    if (busy) {
      $('statusBannerTitle').textContent = p.title || 'Memproses video...';
      $('statusBannerMsg').textContent = p.statusMessage || '';
    }

    if (p.status === 'scanning' && !p.durationSec) {
      $('studioWorkspace').classList.add('hidden');
      return;
    }
    $('studioWorkspace').classList.remove('hidden');

    $('projThumb').src = p.thumbnailUrl || '';
    $('projTitle').textContent = p.title || 'Untitled';
    $('projSub').textContent = `${p.channel || 'YouTube'} · Durasi ${fmtTime(p.durationSec)} ${p.viewCount ? '· ' + fmtViews(p.viewCount) : ''} ${p.localVideoPath ? '· ✅ Full MP4 Lokal' : '· ⚡ Quick Scan Mode'}`;

    const sig = p.signals || {};
    $('signalPills').innerHTML = `
      <span class="sig-pill ${sig.hasHeatmap ? '' : 'off'}">🔥 Heatmap: ${sig.hasHeatmap ? sig.heatmapPoints + ' pts' : 'N/A'}</span>
      <span class="sig-pill ${sig.hasTranscriptHype ? '' : 'off'}">🎙️ Transcript Hype: ${sig.hasTranscriptHype ? sig.subCueCount + ' baris' : 'N/A'}</span>
      <span class="sig-pill ${sig.hasLiveChat ? '' : 'off'}">💬 Live Chat: ${sig.hasLiveChat ? sig.chatMsgCount.toLocaleString() + ' msg' : 'N/A'}</span>
      <span class="sig-pill ${sig.hasAudio ? '' : 'off'}">🔊 Audio Peak: ${sig.hasAudio ? 'Aktif' : 'Belum'}</span>
    `;

    $('btnDownloadFullProj').classList.toggle('hidden', !!p.localVideoPath);

    // Load tuning sliders from project
    if (p.detectConfig) {
      const dc = p.detectConfig;
      $('rngWHeat').value = Math.round((dc.heatmapWeight || 0.45) * 100);
      $('lblWHeat').textContent = $('rngWHeat').value + '%';
      $('rngWChat').value = Math.round((dc.chatWeight || 0.35) * 100);
      $('lblWChat').textContent = $('rngWChat').value + '%';
      $('rngWAudio').value = Math.round((dc.audioWeight || 0.20) * 100);
      $('lblWAudio').textContent = $('rngWAudio').value + '%';
      $('rngMinScore').value = Math.round(dc.minScore || 35);
      $('lblMinScore').textContent = $('rngMinScore').value;
      $('inpMinDur').value = Math.round(dc.minDurationSec || 20);
      $('inpMaxDur').value = Math.round(dc.maxDurationSec || 55);
      $('inpPrePad').value = Math.round(dc.prePaddingSec || 12);
      $('inpPostPad').value = Math.round(dc.postPaddingSec || 22);
      $('inpMaxClips').value = dc.maxClips || 15;
      $('inpSkipIntro').value = Math.round(dc.skipIntroSec || 15);
    }

    // Load preview frame image into Cropper Canvas
    loadPreviewFrame(`/api/projects/${encodeURIComponent(p.id)}/preview-frame?t=${Date.now()}`);

    // Setup video player
    setupPlayer(p, p.candidates?.[0]?.startSec || 0, false);

    // Draw chart & candidates
    drawHypeChart();
    renderCandidates();
  }

  function loadPreviewFrame(url) {
    const img = new Image();
    img.crossOrigin = 'anonymous';
    img.onload = () => {
      state.previewImg = img;
      drawCropCanvases();
    };
    img.src = url;
  }

  function setupPlayer(proj, startSec, autoplay) {
    state.currentSeekSec = Math.max(0, Math.floor(startSec || 0));
    $('previewTimeBadge').textContent = fmtTime(state.currentSeekSec);
    const iframe = $('ytIframe');
    const localVid = $('localVideo');

    if (proj.localVideoPath) {
      iframe.classList.add('hidden');
      iframe.src = '';
      localVid.classList.remove('hidden');
      const vidUrl = `/api/projects/${encodeURIComponent(proj.id)}/video`;
      if (!localVid.src.endsWith(vidUrl)) {
        localVid.src = vidUrl;
      }
      localVid.currentTime = state.currentSeekSec;
      if (autoplay) localVid.play().catch(() => {});
    } else if (proj.videoId) {
      localVid.classList.add('hidden');
      localVid.pause();
      iframe.classList.remove('hidden');
      iframe.src = `https://www.youtube.com/embed/${encodeURIComponent(proj.videoId)}?start=${state.currentSeekSec}&autoplay=${autoplay ? 1 : 0}&rel=0`;
    }
  }

  // ==================== Interactive Multi-Signal Hype Chart ====================
  function drawHypeChart() {
    const canvas = $('hypeChart');
    const proj = state.activeProject;
    if (!canvas || !proj || !proj.timeline || proj.timeline.length === 0) return;

    const dpr = window.devicePixelRatio || 1;
    const rect = canvas.getBoundingClientRect();
    const w = Math.max(300, rect.width);
    const h = 175;
    canvas.width = w * dpr;
    canvas.height = h * dpr;

    const ctx = canvas.getContext('2d');
    ctx.scale(dpr, dpr);
    ctx.clearRect(0, 0, w, h);

    const padL = 10, padR = 10, padT = 16, padB = 24;
    const plotW = w - padL - padR;
    const plotH = h - padT - padB;
    const dur = Math.max(1, proj.durationSec || proj.timeline[proj.timeline.length - 1].timeSec);
    const xOf = (sec) => padL + (sec / dur) * plotW;
    const yOf = (val) => padT + (1 - Math.max(0, Math.min(1, val))) * plotH;

    // Subtle horizontal grid lines
    ctx.strokeStyle = 'rgba(255,255,255,0.06)';
    ctx.lineWidth = 1;
    for (let g = 0; g <= 4; g++) {
      const y = padT + (plotH / 4) * g;
      ctx.beginPath();
      ctx.moveTo(padL, y);
      ctx.lineTo(w - padR, y);
      ctx.stroke();
    }

    // Highlight candidate clip windows
    (proj.candidates || []).forEach((c) => {
      const x1 = xOf(c.startSec);
      const x2 = xOf(c.endSec);
      const isSel = c.id === state.selectedCandidateId;
      ctx.fillStyle = isSel ? 'rgba(168, 85, 247, 0.34)' : (c.selected ? 'rgba(16, 185, 129, 0.16)' : 'rgba(99, 102, 241, 0.10)');
      ctx.fillRect(x1, padT, Math.max(3, x2 - x1), plotH);

      // Rank tag on top
      ctx.fillStyle = isSel ? '#e9d5ff' : '#94a3b8';
      ctx.font = 'bold 10px Segoe UI';
      ctx.fillText(`#${c.rank}`, x1 + 2, padT + 10);
    });

    const pts = proj.timeline;
    const drawCurve = (field, stroke, fill, lineW) => {
      ctx.beginPath();
      pts.forEach((pt, idx) => {
        const x = xOf(pt.timeSec);
        const y = yOf(pt[field] || 0);
        if (idx === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
      });
      ctx.strokeStyle = stroke;
      ctx.lineWidth = lineW;
      ctx.stroke();
      if (fill) {
        ctx.lineTo(xOf(pts[pts.length - 1].timeSec), padT + plotH);
        ctx.lineTo(xOf(pts[0].timeSec), padT + plotH);
        ctx.closePath();
        ctx.fillStyle = fill;
        ctx.fill();
      }
    };

    if (proj.signals?.hasHeatmap) {
      drawCurve('heatmap', '#f59e0b', 'rgba(245, 158, 11, 0.14)', 1.6);
    }
    if (proj.signals?.hasLiveChat) {
      drawCurve('chatHype', '#10b981', 'rgba(16, 185, 129, 0.14)', 1.6);
    }
    if (proj.signals?.hasAudio) {
      drawCurve('audioPeak', '#f43f5e', 'rgba(244, 63, 94, 0.10)', 1.5);
    }
    if (proj.signals?.hasTranscriptHype) {
      drawCurve('transcriptHype', '#06b6d4', 'rgba(6, 182, 212, 0.12)', 1.5);
    }
    drawCurve('combined', '#c084fc', null, 2.2);

    // X-axis time ticks
    ctx.fillStyle = '#64748b';
    ctx.font = '10.5px Segoe UI';
    const ticks = 8;
    for (let i = 0; i <= ticks; i++) {
      const sec = (dur / ticks) * i;
      const x = xOf(sec);
      ctx.fillText(fmtTime(sec), Math.min(w - 42, Math.max(4, x - 14)), h - 6);
    }
  }

  function setupChartInteractions() {
    const canvas = $('hypeChart');
    const tip = $('chartTooltip');

    canvas.addEventListener('mousemove', (e) => {
      const proj = state.activeProject;
      if (!proj || !proj.timeline || proj.timeline.length === 0) return;
      const rect = canvas.getBoundingClientRect();
      const relX = Math.max(0, Math.min(1, (e.clientX - rect.left - 10) / (rect.width - 20)));
      const sec = relX * (proj.durationSec || 1);
      // Find nearest timeline point
      let best = proj.timeline[0];
      let bestDiff = Infinity;
      for (const pt of proj.timeline) {
        const d = Math.abs(pt.timeSec - sec);
        if (d < bestDiff) {
          bestDiff = d;
          best = pt;
        }
      }
      tip.classList.remove('hidden');
      tip.style.left = Math.min(rect.width - 170, Math.max(8, e.clientX - rect.left + 12)) + 'px';
      tip.style.top = '12px';
      tip.innerHTML = `
        <strong>⏱️ ${fmtTime(best.timeSec)}</strong><br/>
        ⚡ Combined: <b>${Math.round((best.combined || 0) * 100)}%</b><br/>
        🔊 Audio Keras: ${Math.round((best.audioPeak || 0) * 100)}%<br/>
        🎙️ Reaksi Indo: ${Math.round((best.transcriptHype || 0) * 100)}%<br/>
        🔥 Heatmap: ${Math.round((best.heatmap || 0) * 100)}%<br/>
        💬 Live Chat: ${Math.round((best.chatHype || 0) * 100)}%
      `;
    });

    canvas.addEventListener('mouseleave', () => tip.classList.add('hidden'));

    canvas.addEventListener('click', (e) => {
      const proj = state.activeProject;
      if (!proj || !proj.durationSec) return;
      const rect = canvas.getBoundingClientRect();
      const relX = Math.max(0, Math.min(1, (e.clientX - rect.left - 10) / (rect.width - 20)));
      const sec = Math.floor(relX * proj.durationSec);

      // If click lands inside a candidate, highlight it
      const hit = (proj.candidates || []).find((c) => sec >= c.startSec && sec <= c.endSec);
      if (hit) {
        state.selectedCandidateId = hit.id;
        drawHypeChart();
        renderCandidates();
      }
      $('manualStart').value = Math.max(0, sec - 10);
      $('manualEnd').value = Math.min(Math.floor(proj.durationSec), sec + 30);
      setupPlayer(proj, sec, true);
    });
  }

  // ==================== Candidates List ====================
  function renderCandidates() {
    const proj = state.activeProject;
    const box = $('candidatesList');
    const list = proj?.candidates || [];
    $('candCount').textContent = list.length;

    if (list.length === 0) {
      box.innerHTML = '<div class="muted text-xs" style="padding:16px;text-align:center;">Belum ada kandidat klip terdeteksi. Coba turunkan Skor Minimal di Tuning Deteksi atau gunakan Custom Range.</div>';
      updateSelectedCounter();
      return;
    }

    box.innerHTML = '';
    list.forEach((c) => {
      const el = document.createElement('div');
      el.className = 'cand-item' + (c.id === state.selectedCandidateId ? ' active' : '');

      const reasonsHtml = (c.reasons || []).map((r) => {
        let cls = 'reason-chip';
        if (r.includes('Chat')) cls += ' chat';
        else if (r.includes('Multi')) cls += ' viral';
        return `<span class="${cls}">${escapeHtml(r)}</span>`;
      }).join('');

      const chatsHtml = (c.sampleChats || []).length > 0
        ? `<div class="cand-chats">${c.sampleChats.map((m) => `<span class="chat-bubble">💬 ${escapeHtml(m)}</span>`).join('')}</div>`
        : '';

      const transHtml = c.transcript
        ? `<div class="cand-transcript">“${escapeHtml(c.transcript)}”</div>`
        : '';

      const scoreCls = c.score >= 70 ? 'cand-score' : 'cand-score mid';

      el.innerHTML = `
        <div class="cand-top">
          <input type="checkbox" class="cand-chk" ${c.selected ? 'checked' : ''} />
          <span class="cand-rank">#${c.rank}</span>
          <strong>${fmtTime(c.startSec)} – ${fmtTime(c.endSec)}</strong>
          <span class="muted text-xs">(${Math.round(c.endSec - c.startSec)}s)</span>
          <span class="${scoreCls}">⚡ ${Math.round(c.score)}</span>
        </div>
        <div class="cand-reasons">${reasonsHtml}</div>
        <div class="text-xs muted">
          🔥 Heat ${Math.round(c.heatmapScore)}% · 💬 Chat ${Math.round(c.chatScore)}% · 🔊 Audio ${Math.round(c.audioScore)}%
        </div>
        ${chatsHtml}
        ${transHtml}
        <div class="cand-times">
          <label class="text-xs">Start (s) <input type="number" class="input input-sm cand-start" value="${Math.round(c.startSec)}" min="0" /></label>
          <label class="text-xs">End (s) <input type="number" class="input input-sm cand-end" value="${Math.round(c.endSec)}" min="1" /></label>
          <label class="text-xs" style="flex:1">Hook Text (Banner Atas)
            <input type="text" class="input input-sm cand-hook-input" value="${escapeAttr(c.hookText || '')}" placeholder="Opsional: teks hook viral..." />
          </label>
        </div>
        <div class="cand-actions">
          <button class="btn btn-xs btn-secondary btn-preview-cand">▶ Preview (${fmtTime(c.peakSec)})</button>
          <div style="display:flex;gap:5px;">
            <button class="btn btn-xs btn-secondary btn-add-topn" title="Masukkan momen ini ke Top-N / Kompilasi Builder">➕ Top-N</button>
            <button class="btn btn-xs btn-primary btn-render-cand">🎬 Render</button>
          </div>
        </div>
      `;

      el.querySelector('.cand-chk').addEventListener('change', (ev) => {
        c.selected = ev.target.checked;
        updateSelectedCounter();
        drawHypeChart();
      });
      el.querySelector('.cand-start').addEventListener('change', (ev) => {
        c.startSec = Math.max(0, parseFloat(ev.target.value) || 0);
        c.durationSec = Math.max(1, c.endSec - c.startSec);
        drawHypeChart();
      });
      el.querySelector('.cand-end').addEventListener('change', (ev) => {
        c.endSec = Math.max(c.startSec + 1, parseFloat(ev.target.value) || c.startSec + 20);
        c.durationSec = c.endSec - c.startSec;
        drawHypeChart();
      });
      el.querySelector('.cand-hook-input').addEventListener('input', (ev) => {
        c.hookText = ev.target.value;
      });
      el.querySelector('.btn-preview-cand').addEventListener('click', () => {
        state.selectedCandidateId = c.id;
        drawHypeChart();
        renderCandidates();
        setupPlayer(proj, c.startSec, true);
      });
      el.querySelector('.btn-add-topn').addEventListener('click', () => {
        // Default tight 18s window around peak for snappy Top-N compilation!
        const s = Math.max(0, Math.round(c.peakSec - 7));
        const e = Math.min(Math.round(proj.durationSec || c.endSec), s + 18);
        addSlotToTopN({
          projectId: proj.id,
          startSec: s,
          endSec: e,
          label: (c.hookText || c.title || 'momen').slice(0, 26),
          endSfxId: 'sfx-metal-pipe',
          endSfxOffset: 1.0,
        });
      });
      el.querySelector('.btn-render-cand').addEventListener('click', () => {
        renderSingleCandidate(c);
      });

      box.appendChild(el);
    });
    updateSelectedCounter();
  }

  function updateSelectedCounter() {
    const cnt = (state.activeProject?.candidates || []).filter((c) => c.selected).length;
    $('selectedCount').textContent = cnt;
    $('btnAutoClipBatch').disabled = cnt === 0;
  }

  function buildRenderOptions(title, startSec, endSec, hookText, candidateId = '') {
    const camp = getActiveCampaign();
    return {
      projectId: state.activeProject.id,
      campaignId: camp ? camp.id : '',
      candidateId,
      title,
      startSec,
      endSec,
      hookText,
      layout: {
        ...state.layout,
        aspectRatio: $('selAspect').value,
        resolution: $('selRes').value,
        cropMode: $('selCropMode').value,
        splitRatio: (parseInt($('rngSplitRatio').value, 10) || 62) / 100,
      },
      subtitles: {
        enabled: $('chkSubtitles').checked,
        preset: $('selSubPreset').value,
        fontName: $('selSubFont').value,
        fontSize: 58,
        position: $('selSubPos').value,
        maxWordsPerRow: 4,
        uppercase: true,
      },
      audioFx: {
        bgmAssetId: $('selBGM').value || '',
        bgmVolume: (parseInt($('rngBgmVol').value, 10) || 18) / 100,
        autoDuck: $('chkAutoDuck').checked,
        sfxAutoPreset: $('selSFXPreset').value || 'none',
        sfxCues: $('selSFXPreset').value === 'custom' ? state.customSFXCues : [],
      },
      videoFx: {
        colorPreset: $('selColorPreset').value || 'none',
        motionPreset: $('selMotionPreset').value || 'none',
        fadeIn: $('chkFadeIn').checked,
        speed: parseFloat($('selSpeed').value) || 1.0,
        sharpen: $('chkSharpen').checked,
      },
      audioNorm: $('chkAudioNorm').checked,
      useWatermark: $('chkUseWatermark').checked,
      useOutro: $('chkUseOutro').checked,
      useIntro: !!(camp?.intro?.enabled && camp?.intro?.fileName),
    };
  }

  async function renderSingleCandidate(c) {
    if (!state.activeProject) return;
    try {
      const opts = buildRenderOptions(c.title, c.startSec, c.endSec, c.hookText, c.id);
      await api('/api/jobs/render', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(opts),
      });
      toast(`Ditambahkan ke antrean render: ${c.title}`, 'info');
      switchTab('queue');
    } catch (e) {
      toast('Gagal mulai render: ' + e.message, 'err');
    }
  }

  async function renderBatchSelected() {
    if (!state.activeProject) return;
    const selected = (state.activeProject.candidates || []).filter((c) => c.selected);
    if (selected.length === 0) {
      toast('Pilih minimal 1 kandidat klip dulu!', 'err');
      return;
    }
    const items = selected.map((c) => buildRenderOptions(c.title, c.startSec, c.endSec, c.hookText, c.id));
    try {
      await api('/api/jobs/batch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ items }),
      });
      toast(`⚡ Auto-Clip ${items.length} klip dimulai!`, 'ok');
      switchTab('queue');
    } catch (e) {
      toast('Batch render gagal: ' + e.message, 'err');
    }
  }

  // ==================== Visual Interactive Crop & 9:16 Live Preview ====================
  function getEffectiveBoxes() {
    const mode = $('selCropMode').value;
    if (mode === 'split_left') {
      return {
        gp: { x: 0.14, y: 0.0, w: 0.72, h: 1.0 },
        fc: { x: 0.0, y: 0.60, w: 0.30, h: 0.40 },
      };
    }
    if (mode === 'split_right') {
      return {
        gp: { x: 0.14, y: 0.0, w: 0.72, h: 1.0 },
        fc: { x: 0.70, y: 0.60, w: 0.30, h: 0.40 },
      };
    }
    if (mode === 'split_top_left') {
      return {
        gp: { x: 0.14, y: 0.0, w: 0.72, h: 1.0 },
        fc: { x: 0.0, y: 0.0, w: 0.30, h: 0.40 },
      };
    }
    if (mode === 'split_top_right') {
      return {
        gp: { x: 0.14, y: 0.0, w: 0.72, h: 1.0 },
        fc: { x: 0.70, y: 0.0, w: 0.30, h: 0.40 },
      };
    }
    return { gp: state.layout.gameplayBox, fc: state.layout.facecamBox };
  }

  function drawCropCanvases() {
    const srcCanvas = $('cropSourceCanvas');
    const outCanvas = $('cropOutputCanvas');
    if (!srcCanvas || !outCanvas) return;

    const sCtx = srcCanvas.getContext('2d');
    const sw = srcCanvas.width, sh = srcCanvas.height;
    sCtx.fillStyle = '#070a12';
    sCtx.fillRect(0, 0, sw, sh);

    if (state.previewImg) {
      sCtx.drawImage(state.previewImg, 0, 0, sw, sh);
    } else {
      sCtx.fillStyle = '#1e293b';
      sCtx.fillRect(0, 0, sw, sh);
      sCtx.fillStyle = '#64748b';
      sCtx.font = '14px Segoe UI';
      sCtx.fillText('Preview frame akan tampil setelah Scan URL', 90, sh / 2);
    }

    const mode = $('selCropMode').value;
    const isSplit = mode.startsWith('split_');
    const isFaceTop = mode === 'facecam_top_ribbon' || mode === 'facecam_top_clean';
    const isCinema = mode === 'gaming_cinema';
    const { gp, fc } = getEffectiveBoxes();

    if (isSplit || isFaceTop) {
      drawBoxOverlay(sCtx, gp, sw, sh, '#3b82f6', isFaceTop ? 'GAMEPLAY (BAWAH)' : 'GAMEPLAY (ATAS)', state.editingBox === 'gameplay');
      drawBoxOverlay(sCtx, fc, sw, sh, '#10b981', isFaceTop ? 'FACECAM (ATAS)' : 'FACECAM (BAWAH)', state.editingBox === 'facecam');
    } else if (isCinema) {
      drawBoxOverlay(sCtx, fc, sw, sh, '#10b981', 'FACECAM (FRAME)', true);
    }

    // Draw Live 9:16 Simulated Output
    const oCtx = outCanvas.getContext('2d');
    const ow = outCanvas.width, oh = outCanvas.height;
    oCtx.fillStyle = '#000';
    oCtx.fillRect(0, 0, ow, oh);

    if (state.previewImg) {
      const iw = state.previewImg.naturalWidth || state.previewImg.width;
      const ih = state.previewImg.naturalHeight || state.previewImg.height;
      if (mode === 'facecam_top_ribbon') {
        const fcH = Math.round(oh * 0.33);
        const ribH = Math.round(oh * 0.085);
        const gpH = oh - fcH - ribH;
        drawCoverSlice(oCtx, state.previewImg, fc.x * iw, fc.y * ih, fc.w * iw, fc.h * ih, 0, 0, ow, fcH);
        oCtx.fillStyle = '#ffffff';
        oCtx.fillRect(0, fcH, ow, ribH);
        oCtx.fillStyle = '#111111';
        oCtx.font = 'bold 9.5px Arial, Segoe UI';
        oCtx.textAlign = 'center';
        oCtx.fillText('JUDUL PITA PUTIH', ow / 2, fcH + ribH / 2 + 3.5);
        oCtx.textAlign = 'left';
        drawCoverSlice(oCtx, state.previewImg, gp.x * iw, gp.y * ih, gp.w * iw, gp.h * ih, 0, fcH + ribH, ow, gpH);
      } else if (mode === 'facecam_top_clean') {
        const fcH = Math.round(oh * 0.33);
        const gpH = oh - fcH;
        drawCoverSlice(oCtx, state.previewImg, fc.x * iw, fc.y * ih, fc.w * iw, fc.h * ih, 0, 0, ow, fcH);
        drawCoverSlice(oCtx, state.previewImg, gp.x * iw, gp.y * ih, gp.w * iw, gp.h * ih, 0, fcH, ow, gpH);
        oCtx.fillStyle = '#000';
        oCtx.fillRect(0, fcH - 1, ow, 2);
      } else if (isCinema) {
        oCtx.save();
        oCtx.filter = 'blur(8px) brightness(0.75)';
        drawCoverSlice(oCtx, state.previewImg, 0, 0, iw, ih, 0, 0, ow, oh);
        oCtx.restore();
        const gpH = Math.round(ow * (9 / 16));
        const gpY = Math.round(oh * 0.16);
        oCtx.drawImage(state.previewImg, 0, gpY, ow, gpH);
        const fcW = Math.round(ow * 0.56);
        const fcH = Math.round(oh * 0.27);
        const fcX = Math.round((ow - fcW) / 2);
        const fcY = Math.min(gpY + gpH + Math.round(oh * 0.03), oh - fcH - Math.round(oh * 0.16));
        drawCoverSlice(oCtx, state.previewImg, fc.x * iw, fc.y * ih, fc.w * iw, fc.h * ih, fcX, fcY, fcW, fcH);
        oCtx.strokeStyle = 'rgba(255,255,255,0.7)';
        oCtx.lineWidth = 1.5;
        oCtx.strokeRect(fcX, fcY, fcW, fcH);
      } else if (isSplit) {
        const splitR = (parseInt($('rngSplitRatio').value, 10) || 64) / 100;
        const topH = Math.round(oh * splitR);
        const botH = oh - topH;
        drawCoverSlice(oCtx, state.previewImg, gp.x * iw, gp.y * ih, gp.w * iw, gp.h * ih, 0, 0, ow, topH);
        drawCoverSlice(oCtx, state.previewImg, fc.x * iw, fc.y * ih, fc.w * iw, fc.h * ih, 0, topH, ow, botH);
        oCtx.fillStyle = '#ffe600';
        oCtx.fillRect(0, topH - 1, ow, 2);
      } else if (mode === 'blur_bg') {
        oCtx.save();
        oCtx.filter = 'blur(8px)';
        drawCoverSlice(oCtx, state.previewImg, 0, 0, iw, ih, 0, 0, ow, oh);
        oCtx.restore();
        const fitH = Math.round(ow * (ih / iw));
        oCtx.drawImage(state.previewImg, 0, (oh - fitH) / 2, ow, fitH);
      } else if (mode === 'original') {
        const fitH = Math.round(ow * (ih / iw));
        oCtx.drawImage(state.previewImg, 0, (oh - fitH) / 2, ow, fitH);
      } else {
        drawCoverSlice(oCtx, state.previewImg, 0, 0, iw, ih, 0, 0, ow, oh);
      }
    }

    // Simulated Watermark on 9:16 preview
    const camp = getActiveCampaign();
    if ($('chkUseWatermark').checked && camp?.watermark?.enabled) {
      const wm = camp.watermark;
      oCtx.save();
      oCtx.globalAlpha = wm.opacity || 0.96;
      if (wm.position === 'full-frame' && state.wmImg) {
        oCtx.drawImage(state.wmImg, 0, 0, ow, oh);
      } else {
        const wmW = ow * (wm.scale || 0.2);
        const wmH = state.wmImg ? wmW * (state.wmImg.height / Math.max(1, state.wmImg.width)) : wmW * 0.45;
        const m = (wm.marginPx || 32) * (ow / 1080);
        let wx = ow - wmW - m, wy = m;
        if (wm.position === 'top-left') { wx = m; wy = m; }
        else if (wm.position === 'bottom-left') { wx = m; wy = oh - wmH - m; }
        else if (wm.position === 'bottom-right') { wx = ow - wmW - m; wy = oh - wmH - m; }
        else if (wm.position === 'center-top') { wx = (ow - wmW) / 2; wy = m; }
        else if (wm.position === 'custom') { wx = ow * (wm.customX || 0.72); wy = oh * (wm.customY || 0.06); }

        if (state.wmImg) {
          oCtx.drawImage(state.wmImg, wx, wy, wmW, wmH);
        } else {
          oCtx.fillStyle = 'rgba(245,158,11,0.85)';
          oCtx.fillRect(wx, wy, wmW, wmH);
          oCtx.fillStyle = '#000';
          oCtx.font = 'bold 8px Segoe UI';
          oCtx.fillText('WM', wx + 4, wy + 11);
        }
      }
      oCtx.restore();
    }

    // Simulated Viral Subtitle position
    if ($('chkSubtitles').checked) {
      const pos = $('selSubPos').value;
      const sy = pos === 'center' ? oh * 0.5 : (pos === 'split-seam' ? oh * 0.64 : oh * 0.82);
      oCtx.font = 'bold 11px Impact, Segoe UI';
      oCtx.textAlign = 'center';
      oCtx.lineWidth = 3;
      oCtx.strokeStyle = '#000';
      oCtx.strokeText('CONTOH SUBTITLE', ow / 2, sy);
      oCtx.fillStyle = $('selSubPreset').value === 'viral-yellow' ? '#ffe600' : '#ffffff';
      oCtx.fillText('CONTOH SUBTITLE', ow / 2, sy);
      oCtx.textAlign = 'left';
    }
  }

  function drawBoxOverlay(ctx, b, w, h, color, label, active) {
    const x = b.x * w, y = b.y * h, bw = b.w * w, bh = b.h * h;
    ctx.save();
    ctx.fillStyle = color + (active ? '2e' : '18');
    ctx.fillRect(x, y, bw, bh);
    ctx.strokeStyle = color;
    ctx.lineWidth = active ? 2.5 : 1.5;
    ctx.strokeRect(x, y, bw, bh);
    ctx.fillStyle = color;
    ctx.fillRect(x, y, Math.min(bw, 118), 16);
    ctx.fillStyle = '#fff';
    ctx.font = 'bold 10px Segoe UI';
    ctx.fillText(label, x + 4, y + 11);
    ctx.restore();
  }

  function drawCoverSlice(ctx, img, sx, sy, sw, sh, dx, dy, dw, dh) {
    const srcAR = sw / Math.max(1, sh);
    const dstAR = dw / Math.max(1, dh);
    let rx = sx, ry = sy, rw = sw, rh = sh;
    if (srcAR > dstAR) {
      rw = sh * dstAR;
      rx = sx + (sw - rw) / 2;
    } else {
      rh = sw / dstAR;
      ry = sy + (sh - rh) / 2;
    }
    ctx.drawImage(img, rx, ry, rw, rh, dx, dy, dw, dh);
  }

  function setupCropDrag() {
    const canvas = $('cropSourceCanvas');
    let dragging = false;
    let startPt = null;

    const toNorm = (e) => {
      const r = canvas.getBoundingClientRect();
      return {
        x: Math.max(0, Math.min(1, (e.clientX - r.left) / r.width)),
        y: Math.max(0, Math.min(1, (e.clientY - r.top) / r.height)),
      };
    };

    canvas.addEventListener('mousedown', (e) => {
      const curMode = $('selCropMode').value;
      const keepModes = ['split_custom', 'gaming_cinema', 'facecam_top_ribbon', 'facecam_top_clean'];
      if (!keepModes.includes(curMode)) {
        const eff = getEffectiveBoxes();
        state.layout.gameplayBox = { ...eff.gp };
        state.layout.facecamBox = { ...eff.fc };
        $('selCropMode').value = 'split_custom';
      }
      dragging = true;
      startPt = toNorm(e);
    });

    window.addEventListener('mousemove', (e) => {
      if (!dragging || !startPt) return;
      const cur = toNorm(e);
      const box = {
        x: Math.min(startPt.x, cur.x),
        y: Math.min(startPt.y, cur.y),
        w: Math.max(0.10, Math.abs(cur.x - startPt.x)),
        h: Math.max(0.10, Math.abs(cur.y - startPt.y)),
      };
      if (box.x + box.w > 1) box.w = 1 - box.x;
      if (box.y + box.h > 1) box.h = 1 - box.y;
      if ($('selCropMode').value === 'gaming_cinema' || state.editingBox === 'facecam') {
        state.layout.facecamBox = box;
      } else {
        state.layout.gameplayBox = box;
      }
      drawCropCanvases();
    });

    window.addEventListener('mouseup', () => {
      dragging = false;
      startPt = null;
    });
  }

  // ==================== Jobs & Clips Gallery ====================
  function updateJobBadge() {
    const active = state.jobs.filter((j) => j.status === 'queued' || j.status === 'downloading' || j.status === 'rendering').length;
    const total = state.jobs.length;
    $('navJobCount').textContent = active > 0 ? `${active}⚡` : total;
  }

  function renderJobs() {
    const box = $('jobsList');
    if (state.jobs.length === 0) {
      box.innerHTML = '<div class="muted" style="padding:24px;">Belum ada clip yang dirender. Pilih momen hits di tab Studio Clipper lalu klik Auto-Clip!</div>';
      return;
    }

    box.innerHTML = '';
    state.jobs.forEach((j) => {
      const card = document.createElement('div');
      card.className = 'job-card';

      const comp = j.compliance || {};
      const compBadges = [];
      if (comp.watermarkApplied) compBadges.push('<span class="comp-ok">✅ Watermark</span>');
      if (comp.outroApplied) compBadges.push('<span class="comp-ok">✅ Outro 3s (Bersih)</span>');
      if (comp.hasValueAdd) compBadges.push('<span class="comp-ok">✅ Value-Add Edit</span>');
      if (comp.hashtagsReady) compBadges.push('<span class="comp-ok">✅ Hashtag Wajib</span>');
      (comp.warnings || []).forEach((w) => compBadges.push(`<span class="comp-warn">⚠️ ${escapeHtml(w)}</span>`));

      const videoHtml = j.status === 'done'
        ? `<video class="job-video" src="/api/jobs/${encodeURIComponent(j.id)}/video" controls preload="metadata"></video>`
        : '';

      const errHtml = j.error ? `<div class="text-xs" style="color:#fda4af;">Error: ${escapeHtml(j.error)}</div>` : '';
      const metaLine = j.status === 'done'
        ? `${j.campaignName} · ${j.finalDuration ? j.finalDuration.toFixed(1) + 's' : ''} · ${fmtBytes(j.fileSizeBytes)}`
        : `${j.campaignName} · ${j.stage || j.status} ${j.speed ? '(' + j.speed + ')' : ''}`;

      card.innerHTML = `
        <div class="job-head">
          <div>
            <div class="job-title">${escapeHtml(j.title)}</div>
            <div class="text-xs muted">${escapeHtml(metaLine)}</div>
          </div>
          <span class="job-status st-${j.status}">${j.status}</span>
        </div>
        ${j.status !== 'done' ? `<div class="progress-bar"><div class="progress-fill" style="width:${Math.round(j.progress || 0)}%"></div></div>` : ''}
        ${errHtml}
        ${videoHtml}
        <div class="compliance-row">${compBadges.join('')}</div>
        ${j.postCaption ? `<div class="post-kit-box">${escapeHtml(j.postCaption)}</div>` : ''}
        <div class="job-actions">
          ${j.status === 'done' ? `<a class="btn btn-xs btn-accent" href="/api/jobs/${encodeURIComponent(j.id)}/video?download=1">⬇️ Download MP4</a>` : ''}
          ${j.postCaption ? `<button class="btn btn-xs btn-secondary btn-copy-cap">📋 Copy Caption + Hashtags</button>` : ''}
          ${j.hashtagLine ? `<button class="btn btn-xs btn-ghost btn-copy-hash">#️⃣ Copy Hashtags</button>` : ''}
          ${(j.status === 'queued' || j.status === 'downloading' || j.status === 'rendering') ? `<button class="btn btn-xs btn-danger-ghost btn-cancel-job">⏹ Cancel</button>` : ''}
          ${(j.status === 'error' || j.status === 'canceled') ? `<button class="btn btn-xs btn-secondary btn-retry-job">🔁 Retry</button>` : ''}
          <button class="btn btn-xs btn-danger-ghost btn-del-job ml-auto">🗑️</button>
        </div>
      `;

      const copyCapBtn = card.querySelector('.btn-copy-cap');
      if (copyCapBtn) copyCapBtn.addEventListener('click', () => copyText(j.postCaption, 'Caption + Hashtags disalin!'));
      const copyHashBtn = card.querySelector('.btn-copy-hash');
      if (copyHashBtn) copyHashBtn.addEventListener('click', () => copyText(j.hashtagLine, 'Urutan Hashtag wajib disalin!'));
      const cancelBtn = card.querySelector('.btn-cancel-job');
      if (cancelBtn) cancelBtn.addEventListener('click', () => api(`/api/jobs/${j.id}/cancel`, { method: 'POST' }));
      const retryBtn = card.querySelector('.btn-retry-job');
      if (retryBtn) retryBtn.addEventListener('click', () => api(`/api/jobs/${j.id}/retry`, { method: 'POST' }).then(() => toast('Job diulang!', 'info')));
      card.querySelector('.btn-del-job').addEventListener('click', async () => {
        await api(`/api/jobs/${j.id}`, { method: 'DELETE' });
        state.jobs = state.jobs.filter((x) => x.id !== j.id);
        updateJobBadge();
        renderJobs();
      });

      box.appendChild(card);
    });
  }

  // ==================== Settings & Dependencies ====================
  function renderDepsAndSettings() {
    const d = state.deps;
    const badge = $('depBadge');
    if (d) {
      badge.textContent = d.ready ? 'Engine Ready' : 'Missing Tools';
      badge.className = 'dep-pill ' + (d.ready ? 'ok' : 'err');
      $('depsPanel').innerHTML = `
        <div class="dep-row"><span>FFmpeg</span><strong>${escapeHtml(d.ffmpegPath || 'NOT FOUND')}</strong></div>
        <div class="dep-row"><span>FFprobe</span><strong>${escapeHtml(d.ffprobePath || 'NOT FOUND')}</strong></div>
        <div class="dep-row"><span>yt-dlp (${escapeHtml(d.ytDlpVersion || '-')})</span><strong>${escapeHtml(d.ytDlpPath || 'NOT FOUND')}</strong></div>
        <div class="dep-row"><span>Available H.264 Encoders</span><strong>${(d.encoders || ['libx264']).join(', ')}</strong></div>
      `;
      const encSel = $('sEncoder');
      encSel.innerHTML = '';
      (d.encoders || ['libx264']).forEach((enc) => {
        const opt = document.createElement('option');
        opt.value = enc;
        opt.textContent = enc === 'libx264' ? 'libx264 (CPU Universal — Paling Stabil)' : `${enc} (Hardware GPU)`;
        encSel.appendChild(opt);
      });
    }

    const s = state.settings;
    if (s) {
      $('sWorkers').value = s.maxWorkers || 2;
      $('sEncoder').value = s.defaultEncoder || 'libx264';
      $('sPreset').value = s.defaultPreset || 'veryfast';
      $('sCrf').value = s.defaultCrf || 20;
      $('sQuality').value = s.downloadQuality || '1080p';
      $('sCookiesBrowser').value = s.cookiesBrowser || '';
      $('sCookiesFile').value = s.cookiesFile || '';
      $('sOutputDir').value = s.outputDir || '';
    }
  }

  // ==================== Utilities ====================
  function copyText(txt, okMsg) {
    navigator.clipboard.writeText(txt).then(() => toast(okMsg, 'ok')).catch(() => {
      const ta = document.createElement('textarea');
      ta.value = txt;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      ta.remove();
      toast(okMsg, 'ok');
    });
  }

  function escapeHtml(s) {
    return String(s || '').replace(/[&<>"']/g, (m) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[m]));
  }
  function escapeAttr(s) {
    return escapeHtml(s);
  }

  // ==================== Event Wiring ====================
  $('btnAnalyze').addEventListener('click', async () => {
    const url = $('ytUrlInput').value.trim();
    if (!url) {
      toast('Tempel link YouTube terlebih dahulu!', 'err');
      return;
    }
    try {
      const proj = await api('/api/projects/analyze', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          url,
          campaignId: state.activeCampaignId,
          downloadFull: $('optDownloadFull').checked,
          includeChat: $('optIncludeChat').checked,
          includeAudio: $('optIncludeAudio').checked,
        }),
      });
      state.activeProject = proj;
      state.projects.unshift(proj);
      renderProjectDropdown();
      $('projectHistorySelect').value = proj.id;
      renderActiveProject();
      toast('Memulai scan multi-signal YouTube...', 'info');
    } catch (e) {
      toast('Gagal scan: ' + e.message, 'err');
    }
  });

  $('ytUrlInput').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') $('btnAnalyze').click();
  });

  $('activeCampaignSelect').addEventListener('change', (e) => {
    state.activeCampaignId = e.target.value;
    renderCampaignStrip();
    applyCampaignDefaultsToStudio();
  });

  $('btnCopyStripTags').addEventListener('click', () => {
    const camp = getActiveCampaign();
    if (camp?.hashtags?.length) copyText(camp.hashtags.join(' '), 'Hashtags urutan wajib disalin!');
  });
  $('btnGoCampaign').addEventListener('click', () => {
    state.editingCampaignId = state.activeCampaignId;
    renderCampaignList();
    loadCampaignIntoEditor(state.editingCampaignId);
    switchTab('campaigns');
  });

  $('projectHistorySelect').addEventListener('change', async (e) => {
    const id = e.target.value;
    if (!id) return;
    try {
      state.activeProject = await api(`/api/projects/${encodeURIComponent(id)}`);
      state.selectedCandidateId = null;
      renderActiveProject();
    } catch (err) {
      toast(err.message, 'err');
    }
  });

  $('btnDeleteProject').addEventListener('click', async () => {
    if (!state.activeProject) return;
    if (!confirm(`Hapus project "${state.activeProject.title}"?`)) return;
    await api(`/api/projects/${encodeURIComponent(state.activeProject.id)}`, { method: 'DELETE' });
    state.projects = state.projects.filter((p) => p.id !== state.activeProject.id);
    state.activeProject = state.projects[0] || null;
    renderProjectDropdown();
    renderActiveProject();
    toast('Project dihapus.', 'info');
  });

  $('btnToggleTuning').addEventListener('click', () => $('tuningPanel').classList.toggle('hidden'));
  ['rngWHeat', 'rngWChat', 'rngWAudio', 'rngMinScore'].forEach((id) => {
    $(id).addEventListener('input', () => {
      $('lblWHeat').textContent = $('rngWHeat').value + '%';
      $('lblWChat').textContent = $('rngWChat').value + '%';
      $('lblWAudio').textContent = $('rngWAudio').value + '%';
      $('lblMinScore').textContent = $('rngMinScore').value;
    });
  });

  $('btnReanalyze').addEventListener('click', async () => {
    if (!state.activeProject) return;
    const cfg = {
      heatmapWeight: (parseInt($('rngWHeat').value, 10) || 45) / 100,
      chatWeight: (parseInt($('rngWChat').value, 10) || 35) / 100,
      audioWeight: (parseInt($('rngWAudio').value, 10) || 20) / 100,
      minScore: parseFloat($('rngMinScore').value) || 35,
      minDurationSec: parseFloat($('inpMinDur').value) || 20,
      maxDurationSec: parseFloat($('inpMaxDur').value) || 55,
      prePaddingSec: parseFloat($('inpPrePad').value) || 12,
      postPaddingSec: parseFloat($('inpPostPad').value) || 22,
      maxClips: parseInt($('inpMaxClips').value, 10) || 15,
      skipIntroSec: parseFloat($('inpSkipIntro').value) || 15,
    };
    try {
      state.activeProject = await api(`/api/projects/${encodeURIComponent(state.activeProject.id)}/reanalyze`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(cfg),
      });
      renderActiveProject();
      toast(`Dihitung ulang: ${state.activeProject.candidates?.length || 0} momen!`, 'ok');
    } catch (e) {
      toast(e.message, 'err');
    }
  });

  $('btnScanAudioFast').addEventListener('click', async () => {
    if (!state.activeProject) return;
    try {
      await api(`/api/projects/${encodeURIComponent(state.activeProject.id)}/scan-audio`, { method: 'POST' });
      toast('🔊 Memulai Fast Audio Scream Scan (mencari teriakan/ketawa paling pecah)...', 'info');
    } catch (e) {
      toast(e.message, 'err');
    }
  });

  $('btnDownloadFullProj').addEventListener('click', async () => {
    if (!state.activeProject) return;
    try {
      await api(`/api/projects/${encodeURIComponent(state.activeProject.id)}/download-full`, { method: 'POST' });
      toast('Memulai download video full MP4 + analisis audio...', 'info');
    } catch (e) {
      toast(e.message, 'err');
    }
  });

  $('btnGrabFrame').addEventListener('click', async () => {
    if (!state.activeProject) return;
    let seek = state.currentSeekSec;
    const lv = $('localVideo');
    if (!lv.classList.contains('hidden') && lv.currentTime > 0) seek = lv.currentTime;
    try {
      const res = await api(`/api/projects/${encodeURIComponent(state.activeProject.id)}/grab-frame`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ seekSec: seek }),
      });
      loadPreviewFrame(res.url);
      toast(`Preview frame diambil pada ${fmtTime(seek)}`, 'ok');
    } catch (e) {
      toast('Gagal ambil frame: ' + e.message, 'err');
    }
  });

  $('btnRenderManual').addEventListener('click', async () => {
    if (!state.activeProject) return;
    const s = parseFloat($('manualStart').value) || 0;
    const e = parseFloat($('manualEnd').value) || s + 30;
    const hook = $('manualHook').value.trim();
    try {
      const opts = buildRenderOptions(`Custom Clip ${fmtTime(s)}-${fmtTime(e)}`, s, e, hook);
      await api('/api/jobs/render', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(opts),
      });
      toast('Custom clip masuk antrean render!', 'ok');
      switchTab('queue');
    } catch (err) {
      toast(err.message, 'err');
    }
  });

  // Layout controls live redraw
  ['selCropMode', 'selAspect', 'selRes', 'chkSubtitles', 'chkUseWatermark', 'selSubPreset', 'selSubPos'].forEach((id) => {
    $(id).addEventListener('change', drawCropCanvases);
  });
  $('rngSplitRatio').addEventListener('input', () => {
    $('lblSplitRatio').textContent = $('rngSplitRatio').value + '%';
    state.layout.splitRatio = parseInt($('rngSplitRatio').value, 10) / 100;
    drawCropCanvases();
  });
  $('btnEditGameplay').addEventListener('click', () => {
    state.editingBox = 'gameplay';
    $('btnEditGameplay').classList.add('active');
    $('btnEditFacecam').classList.remove('active');
    drawCropCanvases();
  });
  $('btnEditFacecam').addEventListener('click', () => {
    state.editingBox = 'facecam';
    $('btnEditFacecam').classList.add('active');
    $('btnEditGameplay').classList.remove('active');
    drawCropCanvases();
  });

  $('btnSaveLayoutToCampaign').addEventListener('click', async () => {
    const camp = getActiveCampaign();
    if (!camp) return;
    camp.defaultLayout = {
      ...state.layout,
      aspectRatio: $('selAspect').value,
      resolution: $('selRes').value,
      cropMode: $('selCropMode').value,
      splitRatio: (parseInt($('rngSplitRatio').value, 10) || 62) / 100,
    };
    camp.defaultSubtitles = {
      enabled: $('chkSubtitles').checked,
      preset: $('selSubPreset').value,
      fontName: $('selSubFont').value,
      fontSize: 58,
      position: $('selSubPos').value,
      maxWordsPerRow: 4,
      uppercase: true,
    };
    camp.defaultAudioFx = {
      bgmAssetId: $('selBGM').value || '',
      bgmVolume: (parseInt($('rngBgmVol').value, 10) || 18) / 100,
      autoDuck: $('chkAutoDuck').checked,
      sfxAutoPreset: $('selSFXPreset').value || 'none',
      sfxCues: $('selSFXPreset').value === 'custom' ? state.customSFXCues : [],
    };
    camp.defaultVideoFx = {
      colorPreset: $('selColorPreset').value || 'none',
      motionPreset: $('selMotionPreset').value || 'none',
      fadeIn: $('chkFadeIn').checked,
      speed: parseFloat($('selSpeed').value) || 1.0,
      sharpen: $('chkSharpen').checked,
    };
    try {
      await api(`/api/campaigns/${encodeURIComponent(camp.id)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(camp),
      });
      toast(`Layout & Crop disimpan ke campaign "${camp.name}"!`, 'ok');
    } catch (e) {
      toast(e.message, 'err');
    }
  });

  // Selection buttons
  $('btnSelectTop5').addEventListener('click', () => {
    (state.activeProject?.candidates || []).forEach((c, i) => (c.selected = i < 5));
    renderCandidates();
    drawHypeChart();
  });
  $('btnSelectAll').addEventListener('click', () => {
    (state.activeProject?.candidates || []).forEach((c) => (c.selected = true));
    renderCandidates();
    drawHypeChart();
  });
  $('btnSelectNone').addEventListener('click', () => {
    (state.activeProject?.candidates || []).forEach((c) => (c.selected = false));
    renderCandidates();
    drawHypeChart();
  });
  $('btnAutoClipBatch').addEventListener('click', renderBatchSelected);

  // Campaign Editor Events
  $('btnNewCampaign').addEventListener('click', async () => {
    const name = prompt('Nama Campaign baru (contoh: MiawAug Horror Campaign):');
    if (!name || !name.trim()) return;
    try {
      const created = await api('/api/campaigns', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: name.trim(),
          platform: 'MotionKlip',
          discordTag: 'MKLP',
          hashtags: ['#motionklip', '#ezklip'],
          watermark: { enabled: false, position: 'top-right', scale: 0.2, opacity: 0.92, marginPx: 32 },
          outro: { enabled: false, keepRaw: true },
          defaultAudioNorm: true,
        }),
      });
      state.campaigns.push(created);
      state.editingCampaignId = created.id;
      renderCampaignSelectors();
      renderCampaignList();
      loadCampaignIntoEditor(created.id);
      toast('Campaign baru dibuat!', 'ok');
    } catch (e) {
      toast(e.message, 'err');
    }
  });

  $('btnSaveCamp').addEventListener('click', saveEditedCampaign);
  $('btnDeleteCamp').addEventListener('click', async () => {
    if (state.campaigns.length <= 1) {
      toast('Minimal harus tersisa 1 campaign!', 'err');
      return;
    }
    if (!confirm('Hapus campaign ini?')) return;
    await api(`/api/campaigns/${encodeURIComponent(state.editingCampaignId)}`, { method: 'DELETE' });
    state.campaigns = state.campaigns.filter((c) => c.id !== state.editingCampaignId);
    state.editingCampaignId = state.campaigns[0].id;
    state.activeCampaignId = state.campaigns[0].id;
    renderCampaignSelectors();
    renderCampaignList();
    loadCampaignIntoEditor(state.editingCampaignId);
    toast('Campaign dihapus.', 'info');
  });

  $('cWmPos').addEventListener('change', () => {
    $('wmCustomXY').classList.toggle('hidden', $('cWmPos').value !== 'custom');
  });
  $('cWmScale').addEventListener('input', () => ($('lblWmScale').textContent = $('cWmScale').value + '%'));
  $('cWmOpacity').addEventListener('input', () => ($('lblWmOpacity').textContent = $('cWmOpacity').value + '%'));
  $('fileWm').addEventListener('change', (e) => uploadCampaignAsset('watermark', e.target.files[0]));
  $('fileOutro').addEventListener('change', (e) => uploadCampaignAsset('outro', e.target.files[0]));
  $('fileIntro').addEventListener('change', (e) => uploadCampaignAsset('intro', e.target.files[0]));

  // Queue & Settings Events
  $('btnRefreshJobs').addEventListener('click', async () => {
    state.jobs = await api('/api/jobs');
    updateJobBadge();
    renderJobs();
  });
  const openOutputs = () => api('/api/open-output-folder', { method: 'POST' }).then((r) => toast('Membuka: ' + r.path, 'info'));
  $('btnOpenOutputs').addEventListener('click', openOutputs);
  $('btnOpenOutputs2').addEventListener('click', openOutputs);

  $('btnRefreshDeps').addEventListener('click', async () => {
    state.deps = await api('/api/deps/refresh', { method: 'POST' });
    renderDepsAndSettings();
    toast('Dependency dicek ulang.', 'ok');
  });

  $('btnSaveSettings').addEventListener('click', async () => {
    const cfg = {
      maxWorkers: parseInt($('sWorkers').value, 10) || 2,
      defaultEncoder: $('sEncoder').value || 'libx264',
      defaultPreset: $('sPreset').value || 'veryfast',
      defaultCrf: parseInt($('sCrf').value, 10) || 20,
      downloadQuality: $('sQuality').value || '1080p',
      cookiesBrowser: $('sCookiesBrowser').value || '',
      cookiesFile: $('sCookiesFile').value.trim(),
      outputDir: $('sOutputDir').value.trim(),
    };
    try {
      state.settings = await api('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(cfg),
      });
      toast('Settings tersimpan!', 'ok');
    } catch (e) {
      toast(e.message, 'err');
    }
  });

  $('btnClearLogs').addEventListener('click', () => ($('liveConsole').innerHTML = ''));

  // ==================== Audio & SFX Library ====================
  function renderAudioLibrary() {
    const bgms = state.audioAssets.filter((a) => a.kind === 'bgm');
    const sfxs = state.audioAssets.filter((a) => a.kind === 'sfx');

    // Update Studio BGM selector
    const selBgm = $('selBGM');
    const prevVal = selBgm.value;
    selBgm.innerHTML = '<option value="">— Tanpa Backsound (BGM) —</option>';
    bgms.forEach((b) => {
      const opt = document.createElement('option');
      opt.value = b.id;
      opt.textContent = `${b.builtIn ? '🎵' : '⭐'} ${b.name} (${b.category || 'BGM'})`;
      selBgm.appendChild(opt);
    });
    if (prevVal) selBgm.value = prevVal;

    // Render library cards
    renderAudioListBox('sfxLibList', sfxs);
    renderAudioListBox('bgmLibList', bgms);
    renderCustomSFXCues();
  }

  function renderAudioListBox(containerId, items) {
    const box = $(containerId);
    if (!box) return;
    if (items.length === 0) {
      box.innerHTML = '<div class="muted text-xs" style="padding:12px;">Belum ada audio. Klik "Restore Starter Pack" atau upload file MP3/WAV!</div>';
      return;
    }
    box.innerHTML = '';
    items.forEach((a) => {
      const el = document.createElement('div');
      el.className = 'audio-item';
      const isPlaying = state.playingAudioId === a.id;
      el.innerHTML = `
        <div>
          <strong>${a.builtIn ? '✨' : '⭐'} ${escapeHtml(a.name)}</strong>
          <div class="audio-item-meta">${escapeHtml(a.category || a.kind.toUpperCase())} · ${(a.duration || 0).toFixed(1)}s · ${a.builtIn ? 'Built-in DMCA-Free' : 'Custom Upload'}</div>
        </div>
        <div class="audio-item-actions">
          <button class="btn btn-xs btn-secondary btn-play-aud">${isPlaying ? '⏹ Stop' : '▶ Play'}</button>
          ${!a.builtIn ? '<button class="btn btn-xs btn-danger-ghost btn-del-aud">🗑️</button>' : ''}
        </div>
      `;
      el.querySelector('.btn-play-aud').addEventListener('click', () => togglePlayAudio(a));
      const delBtn = el.querySelector('.btn-del-aud');
      if (delBtn) {
        delBtn.addEventListener('click', async () => {
          await api(`/api/audio/${encodeURIComponent(a.id)}`, { method: 'DELETE' });
          state.audioAssets = state.audioAssets.filter((x) => x.id !== a.id);
          renderAudioLibrary();
          toast('Audio dihapus.', 'info');
        });
      }
      box.appendChild(el);
    });
  }

  function togglePlayAudio(asset) {
    const player = $('globalAudioPreview');
    if (state.playingAudioId === asset.id && !player.paused) {
      player.pause();
      state.playingAudioId = '';
      renderAudioLibrary();
      return;
    }
    state.playingAudioId = asset.id;
    player.src = `/api/audio/file/${encodeURIComponent(asset.fileName)}`;
    player.currentTime = 0;
    player.play().catch(() => {});
    player.onended = () => {
      state.playingAudioId = '';
      renderAudioLibrary();
    };
    renderAudioLibrary();
  }

  function toggleCustomSFXUI() {
    const isCustom = $('selSFXPreset').value === 'custom';
    $('btnAddCustomSFX').classList.toggle('hidden', !isCustom);
    $('customSFXList').classList.toggle('hidden', !isCustom);
    if (isCustom) renderCustomSFXCues();
  }

  function renderCustomSFXCues() {
    const box = $('customSFXList');
    if (!box) return;
    const sfxs = state.audioAssets.filter((a) => a.kind === 'sfx');
    box.innerHTML = '';
    state.customSFXCues.forEach((cue, idx) => {
      const row = document.createElement('div');
      row.className = 'sfx-cue-row';
      const optsHtml = sfxs.map((s) => `<option value="${escapeAttr(s.id)}" ${s.id === cue.assetId ? 'selected' : ''}>${escapeHtml(s.name)}</option>`).join('');
      row.innerHTML = `
        <select class="input input-sm cue-asset">${optsHtml}</select>
        <label class="check-inline text-xs"><input type="checkbox" class="cue-peak" ${cue.atPeak ? 'checked' : ''} /> Di Peak</label>
        <input type="number" step="0.2" min="0" class="input input-sm cue-sec" value="${cue.atSec}" ${cue.atPeak ? 'disabled' : ''} title="Detik dari awal klip" />
        <input type="number" step="0.1" min="0.1" max="1.5" class="input input-sm cue-vol" value="${cue.volume}" title="Volume (0.1 - 1.5)" />
        <button class="btn btn-2xs btn-danger-ghost cue-del">✕</button>
      `;
      row.querySelector('.cue-asset').addEventListener('change', (e) => (cue.assetId = e.target.value));
      row.querySelector('.cue-peak').addEventListener('change', (e) => {
        cue.atPeak = e.target.checked;
        renderCustomSFXCues();
      });
      row.querySelector('.cue-sec').addEventListener('change', (e) => (cue.atSec = parseFloat(e.target.value) || 0));
      row.querySelector('.cue-vol').addEventListener('change', (e) => (cue.volume = parseFloat(e.target.value) || 0.85));
      row.querySelector('.cue-del').addEventListener('click', () => {
        state.customSFXCues.splice(idx, 1);
        renderCustomSFXCues();
      });
      box.appendChild(row);
    });
  }

  async function uploadAudioFile(kind, fileInputId, nameInputId, catInputId) {
    const file = $(fileInputId).files[0];
    if (!file) {
      toast('Pilih file MP3/WAV dulu!', 'err');
      return;
    }
    const fd = new FormData();
    fd.append('file', file);
    fd.append('name', $(nameInputId).value.trim());
    fd.append('category', $(catInputId).value.trim());
    toast(`Mengunggah ${kind.toUpperCase()}...`, 'info');
    try {
      const added = await api(`/api/audio/upload/${kind}`, { method: 'POST', body: fd });
      state.audioAssets.unshift(added);
      $(fileInputId).value = '';
      $(nameInputId).value = '';
      renderAudioLibrary();
      toast(`${kind.toUpperCase()} "${added.name}" siap dipakai!`, 'ok');
    } catch (e) {
      toast('Upload audio gagal: ' + e.message, 'err');
    }
  }

  $('btnGoAudioLib').addEventListener('click', () => switchTab('audio'));
  $('rngBgmVol').addEventListener('input', () => ($('lblBgmVol').textContent = $('rngBgmVol').value + '%'));
  $('selSFXPreset').addEventListener('change', toggleCustomSFXUI);
  $('btnAddCustomSFX').addEventListener('click', () => {
    const firstSfx = state.audioAssets.find((a) => a.kind === 'sfx');
    state.customSFXCues.push({ assetId: firstSfx ? firstSfx.id : 'sfx-boom', atSec: 1.0, atPeak: false, volume: 0.9 });
    renderCustomSFXCues();
  });
  $('btnPreviewBGM').addEventListener('click', () => {
    const id = $('selBGM').value;
    if (!id) {
      toast('Pilih Backsound (BGM) dulu!', 'info');
      return;
    }
    const asset = state.audioAssets.find((a) => a.id === id);
    if (asset) togglePlayAudio(asset);
  });
  $('btnUploadSFX').addEventListener('click', () => uploadAudioFile('sfx', 'upSfxFile', 'upSfxName', 'upSfxCat'));
  $('btnUploadBGM').addEventListener('click', () => uploadAudioFile('bgm', 'upBgmFile', 'upBgmName', 'upBgmCat'));
  $('btnRegenAudio').addEventListener('click', async () => {
    state.audioAssets = await api('/api/audio/regenerate', { method: 'POST' });
    renderAudioLibrary();
    toast('Built-in SFX & BGM Starter Pack siap!', 'ok');
  });

  // ==================== Autopilot Scheduler & HITL Review Inbox ====================
  function renderAutopilotList() {
    const box = $('autopilotList');
    if (!box) return;
    // Also populate campaign selector inside autopilot editor
    const campSel = $('aCampaignId');
    if (campSel) {
      const prev = campSel.value;
      campSel.innerHTML = '';
      state.campaigns.forEach((c) => {
        const o = document.createElement('option');
        o.value = c.id;
        o.textContent = c.name;
        campSel.appendChild(o);
      });
      if (prev) campSel.value = prev;
    }

    box.innerHTML = '';
    state.autopilots.forEach((r) => {
      const el = document.createElement('div');
      el.className = 'camp-item' + (r.id === state.editingAutopilotId ? ' active' : '');
      el.innerHTML = `
        <div class="camp-item-name">${r.enabled ? '🟢' : '⚪'} ${escapeHtml(r.name)}</div>
        <div class="camp-item-meta">Tiap ${r.intervalMinutes}m · Top ${r.topClipsPerVideo} klip · ${(r.processedVideoIds || []).length} VOD diproses</div>
      `;
      el.addEventListener('click', () => {
        state.editingAutopilotId = r.id;
        renderAutopilotList();
        loadAutopilotIntoEditor(r.id);
      });
      box.appendChild(el);
    });
  }

  function loadAutopilotIntoEditor(id) {
    const r = state.autopilots.find((x) => x.id === id);
    if (!r) return;
    $('autoEditorTitle').textContent = `🤖 ${r.name}`;
    $('autoStatusLine').textContent = `${r.lastStatus || 'Siap'} ${r.nextRunAt && r.enabled ? '· Cek berikutnya: ' + new Date(r.nextRunAt).toLocaleTimeString('id-ID') : ''}`;
    $('aName').value = r.name || '';
    $('aEnabled').checked = !!r.enabled;
    $('aChannelUrl').value = r.channelUrl || '';
    $('aKeywords').value = (r.keywordFilter || []).join(', ');
    if (r.campaignId) $('aCampaignId').value = r.campaignId;
    $('aInterval').value = r.intervalMinutes || 60;
    $('aMaxVideos').value = r.maxVideosPerRun || 1;
    $('aTopClips').value = r.topClipsPerVideo || 5;
    $('aNotifyWebhook').value = r.notifyWebhookUrl || '';
    $('aApproveWebhook').value = r.approveWebhookUrl || '';
  }

  function renderReviewInbox() {
    const box = $('reviewInboxList');
    if (!box) return;
    const pending = state.jobs.filter((j) => j.reviewStatus === 'pending_review' || (!j.reviewStatus && j.status === 'done'));
    const approved = state.jobs.filter((j) => j.reviewStatus === 'approved');
    const rejected = state.jobs.filter((j) => j.reviewStatus === 'rejected');

    $('cntPending').textContent = pending.length;
    $('cntApproved').textContent = approved.length;
    $('cntRejected').textContent = rejected.length;
    $('navReviewCount').textContent = pending.length;

    const list = state.reviewFilter === 'approved' ? approved : (state.reviewFilter === 'rejected' ? rejected : pending);
    if (list.length === 0) {
      box.innerHTML = `<div class="muted" style="padding:20px;">Tidak ada klip pada kategori ini. Aktifkan Autopilot atau render klip dari Studio!</div>`;
      return;
    }

    box.innerHTML = '';
    list.forEach((j) => {
      const card = document.createElement('div');
      card.className = 'job-card';
      const comp = j.compliance || {};
      const badges = [];
      if (comp.watermarkApplied) badges.push('<span class="comp-ok">✅ Watermark</span>');
      if (comp.outroApplied) badges.push('<span class="comp-ok">✅ Outro 3s</span>');
      if (comp.hasValueAdd) badges.push('<span class="comp-ok">✅ Value-Add</span>');
      (comp.warnings || []).forEach((w) => badges.push(`<span class="comp-warn">⚠️ ${escapeHtml(w)}</span>`));

      card.innerHTML = `
        <div class="job-head">
          <div>
            <div class="job-title">${escapeHtml(j.title)}</div>
            <div class="text-xs muted">${escapeHtml(j.campaignName)} · ${j.finalDuration ? j.finalDuration.toFixed(1) + 's' : j.stage} · ${fmtBytes(j.fileSizeBytes)}</div>
          </div>
          <span class="job-status st-${j.status}">${j.reviewStatus || j.status}</span>
        </div>
        ${j.status === 'done' && j.outputPath ? `<video class="job-video" src="/api/jobs/${encodeURIComponent(j.id)}/video" controls preload="metadata"></video>` : `<div class="progress-bar"><div class="progress-fill" style="width:${Math.round(j.progress || 0)}%"></div></div>`}
        <div class="compliance-row">${badges.join('')}</div>
        <label class="text-xs">Edit Caption / Hashtag Sebelum Approve
          <textarea class="input textarea rev-caption" rows="3">${escapeHtml(j.postCaption || '')}</textarea>
        </label>
        <input type="text" class="input input-sm rev-note" value="${escapeAttr(j.reviewNote || '')}" placeholder="Catatan analisis kamu (opsional)..." />
        <div class="job-actions">
          <button class="btn btn-xs btn-accent btn-rev-approve">✅ Approve (Siap Upload)</button>
          <button class="btn btn-xs btn-danger-ghost btn-rev-reject">❌ Reject &amp; Hapus</button>
          ${j.status === 'done' && j.outputPath ? `<a class="btn btn-xs btn-secondary" href="/api/jobs/${encodeURIComponent(j.id)}/video?download=1">⬇️ MP4</a>` : ''}
          <button class="btn btn-xs btn-ghost btn-rev-copy">📋 Copy Caption</button>
        </div>
      `;

      card.querySelector('.btn-rev-approve').addEventListener('click', async () => {
        const cap = card.querySelector('.rev-caption').value;
        const note = card.querySelector('.rev-note').value;
        await api(`/api/jobs/${encodeURIComponent(j.id)}/review`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ reviewStatus: 'approved', postCaption: cap, reviewNote: note }),
        });
        toast('✅ Klip Approved & siap upload!', 'ok');
      });
      card.querySelector('.btn-rev-reject').addEventListener('click', async () => {
        if (!confirm('Reject dan hapus file MP4 klip ini?')) return;
        const note = card.querySelector('.rev-note').value;
        await api(`/api/jobs/${encodeURIComponent(j.id)}/review`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ reviewStatus: 'rejected', reviewNote: note, deleteFileOnReject: true }),
        });
        toast('❌ Klip ditolak & file dihapus.', 'info');
      });
      card.querySelector('.btn-rev-copy').addEventListener('click', () => {
        copyText(card.querySelector('.rev-caption').value, 'Caption + Hashtags disalin!');
      });

      box.appendChild(card);
    });
  }

  document.querySelectorAll('.review-filter').forEach((btn) => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.review-filter').forEach((b) => b.classList.remove('active'));
      btn.classList.add('active');
      state.reviewFilter = btn.dataset.rfilter;
      renderReviewInbox();
    });
  });

  $('btnNewAutopilot').addEventListener('click', async () => {
    const name = prompt('Nama Autopilot Rule baru (contoh: MiawAug Auto-Watcher):');
    if (!name || !name.trim()) return;
    try {
      const created = await api('/api/autopilot', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: name.trim(),
          enabled: false,
          channelUrl: 'https://www.youtube.com/@WindahBasudara/streams',
          keywordFilter: [],
          campaignId: state.activeCampaignId,
          intervalMinutes: 60,
          maxVideosPerRun: 1,
          topClipsPerVideo: 5,
        }),
      });
      state.autopilots.push(created);
      state.editingAutopilotId = created.id;
      renderAutopilotList();
      loadAutopilotIntoEditor(created.id);
      toast('Rule Autopilot dibuat!', 'ok');
    } catch (e) {
      toast(e.message, 'err');
    }
  });

  $('btnSaveAutopilot').addEventListener('click', async () => {
    const orig = state.autopilots.find((x) => x.id === state.editingAutopilotId);
    if (!orig) return;
    const kws = $('aKeywords').value.split(',').map((s) => s.trim()).filter(Boolean);
    const payload = {
      ...orig,
      name: $('aName').value.trim() || orig.name,
      enabled: $('aEnabled').checked,
      channelUrl: $('aChannelUrl').value.trim(),
      keywordFilter: kws,
      campaignId: $('aCampaignId').value,
      intervalMinutes: parseInt($('aInterval').value, 10) || 60,
      maxVideosPerRun: parseInt($('aMaxVideos').value, 10) || 1,
      topClipsPerVideo: parseInt($('aTopClips').value, 10) || 5,
      notifyWebhookUrl: $('aNotifyWebhook').value.trim(),
      approveWebhookUrl: $('aApproveWebhook').value.trim(),
    };
    try {
      const saved = await api(`/api/autopilot/${encodeURIComponent(orig.id)}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      onAutopilotUpdate(saved);
      loadAutopilotIntoEditor(saved.id);
      toast('Jadwal Autopilot tersimpan!', 'ok');
    } catch (e) {
      toast(e.message, 'err');
    }
  });

  $('btnRunAutopilotNow').addEventListener('click', async () => {
    if (!state.editingAutopilotId) return;
    try {
      await api(`/api/autopilot/${encodeURIComponent(state.editingAutopilotId)}/run`, { method: 'POST' });
      toast('🤖 Siklus Autopilot dijalankan sekarang!', 'info');
    } catch (e) {
      toast(e.message, 'err');
    }
  });

  $('btnDeleteAutopilot').addEventListener('click', async () => {
    if (state.autopilots.length <= 1) {
      toast('Minimal sisakan 1 Autopilot rule.', 'err');
      return;
    }
    if (!confirm('Hapus rule Autopilot ini?')) return;
    await api(`/api/autopilot/${encodeURIComponent(state.editingAutopilotId)}`, { method: 'DELETE' });
    state.autopilots = state.autopilots.filter((x) => x.id !== state.editingAutopilotId);
    state.editingAutopilotId = state.autopilots[0].id;
    renderAutopilotList();
    loadAutopilotIntoEditor(state.editingAutopilotId);
    toast('Rule Autopilot dihapus.', 'info');
  });

  // ==================== Instant Transcript Search & Top-N Compilation Builder ====================
  async function runTranscriptSearch(q) {
    if (!state.activeProject) return;
    const query = (q ?? $('inpTransSearch').value).trim();
    if (!query) {
      $('transSearchResults').classList.add('hidden');
      $('btnTransClear').classList.add('hidden');
      return;
    }
    $('inpTransSearch').value = query;
    $('btnTransClear').classList.remove('hidden');
    try {
      const data = await api(`/api/projects/${encodeURIComponent(state.activeProject.id)}/search?q=${encodeURIComponent(query)}`);
      const box = $('transSearchResults');
      box.classList.remove('hidden');
      const hits = data.hits || [];
      if (hits.length === 0) {
        box.innerHTML = `<div class="muted text-xs" style="padding:8px;">Tidak ditemukan kata "${escapeHtml(query)}" di transkrip VOD ini.</div>`;
        return;
      }
      box.innerHTML = '';
      hits.forEach((h) => {
        const row = document.createElement('div');
        row.className = 'trans-hit';
        row.innerHTML = `
          <span class="trans-hit-time">${escapeHtml(h.timeText)}</span>
          <span class="trans-hit-ctx">${escapeHtml(h.context)}</span>
          <div class="trans-hit-acts">
            <button class="btn btn-2xs btn-secondary btn-th-prev">▶ Preview</button>
            <button class="btn btn-2xs btn-accent btn-th-topn">➕ Top-N</button>
            <button class="btn btn-2xs btn-primary btn-th-clip">🎬 Klip</button>
          </div>
        `;
        const startS = Math.max(0, Math.floor(h.startSec - 4));
        const endS = Math.min(Math.floor(state.activeProject.durationSec || h.startSec + 25), startS + 22);
        row.querySelector('.btn-th-prev').addEventListener('click', () => {
          $('manualStart').value = startS;
          $('manualEnd').value = endS;
          setupPlayer(state.activeProject, startS, true);
        });
        row.querySelector('.btn-th-topn').addEventListener('click', () => {
          addSlotToTopN({
            projectId: state.activeProject.id,
            startSec: startS,
            endSec: Math.min(endS, startS + 18),
            label: query,
            endSfxId: 'sfx-metal-pipe',
            endSfxOffset: 1.0,
          });
        });
        row.querySelector('.btn-th-clip').addEventListener('click', async () => {
          const opts = buildRenderOptions(`${query.toUpperCase()} (${h.timeText})`, startS, endS, query.toUpperCase());
          await api('/api/jobs/render', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(opts) });
          toast(`🎬 Klip "${query}" (${h.timeText}) masuk antrean render!`, 'ok');
          switchTab('queue');
        });
        box.appendChild(row);
      });
    } catch (e) {
      toast('Gagal cari transkrip: ' + e.message, 'err');
    }
  }

  $('btnTransSearch').addEventListener('click', () => runTranscriptSearch());
  $('inpTransSearch').addEventListener('keydown', (e) => { if (e.key === 'Enter') runTranscriptSearch(); });
  $('btnTransClear').addEventListener('click', () => {
    $('inpTransSearch').value = '';
    $('transSearchResults').classList.add('hidden');
    $('btnTransClear').classList.add('hidden');
  });
  document.querySelectorAll('.topic-pill').forEach((p) => {
    p.addEventListener('click', () => runTranscriptSearch(p.dataset.q));
  });

  function addSlotToTopN(slot) {
    state.topNSlots.push(slot);
    renderTopNBuilder();
    toast(`➕ Ditambahkan ke Top-N Builder (${state.topNSlots.length} klip)`, 'ok');
  }

  function renderTopNBuilder() {
    const n = state.topNSlots.length;
    $('navTopNCount').textContent = n;
    $('btnRenderTopN').disabled = n === 0;
    const box = $('topNSlotsList');
    if (!box) return;

    const bumperDur = $('topNMode').value === 'top_countdown' ? (parseFloat($('topNBumperDur').value) || 0.85) : 0;
    let totalDur = 0;
    state.topNSlots.forEach((s) => (totalDur += Math.max(1, (s.endSec || 0) - (s.startSec || 0))));
    if (n > 1) totalDur += (n - 1) * bumperDur;
    const outroExtra = $('topNOutro').checked ? 4 : 0;

    $('topNSummaryText').textContent = `${n} klip dalam antrean Top-${n || 'N'}`;
    $('topNDurEstimate').textContent = n > 0
      ? `Estimasi total durasi: ~${Math.round(totalDur)} dtk + Outro ${outroExtra}s = ~${Math.round(totalDur + outroExtra)} detik ${totalDur >= 50 && totalDur <= 120 ? '(🔥 Durasi ideal FYP TikTok!)' : ''}`
      : 'Tambahkan 3–5 klip pendek (10–20 dtk) dari Studio atau Search Transkrip untuk membuat video Top-5 viral!';

    if (n === 0) {
      box.innerHTML = '<div class="muted text-xs" style="padding:20px;text-align:center;">Belum ada klip di daftar Top-N.<br/>Klik tombol <strong>➕ Top-N</strong> di kartu momen atau di hasil Search Transkrip, atau klik <strong>⚡ Isi Otomatis Top 5</strong> di atas!</div>';
      return;
    }

    const sfxs = state.audioAssets.filter((a) => a.kind === 'sfx');
    box.innerHTML = '';
    state.topNSlots.forEach((slot, idx) => {
      const rankNum = n - idx; // Plays countdown: N, N-1, ..., 1!
      const isClimax = rankNum === 1;
      const el = document.createElement('div');
      el.className = 'topn-slot' + (isClimax ? ' climax' : '');
      const sfxOpts = ['<option value="">Tanpa SFX Akhir</option>']
        .concat(sfxs.map((s) => `<option value="${escapeAttr(s.id)}" ${s.id === slot.endSfxId ? 'selected' : ''}>${escapeHtml(s.name)}</option>`))
        .join('');

      el.innerHTML = `
        <div class="topn-slot-head">
          <span class="rank-badge">#${rankNum}${isClimax ? ' 👑 KLIMAKS' : ''}</span>
          <span class="text-xs muted">${fmtTime(slot.startSec)} – ${fmtTime(slot.endSec)} (${Math.round(slot.endSec - slot.startSec)}s)</span>
          <div class="head-actions ml-auto">
            <button class="btn btn-2xs btn-secondary btn-slot-prev" title="Preview">▶</button>
            <button class="btn btn-2xs btn-ghost btn-slot-up" ${idx === 0 ? 'disabled' : ''}>⬆</button>
            <button class="btn btn-2xs btn-ghost btn-slot-dn" ${idx === n - 1 ? 'disabled' : ''}>⬇</button>
            <button class="btn btn-2xs btn-danger-ghost btn-slot-del">✕</button>
          </div>
        </div>
        <div class="topn-slot-grid">
          <label class="text-xs">Label Singkat (#${rankNum})
            <input type="text" class="input input-sm slot-lbl" value="${escapeAttr(slot.label || '')}" placeholder="cth: ngetrail / el alasan / paling jago" />
          </label>
          <label class="text-xs">Start (s)
            <input type="number" class="input input-sm slot-start" value="${Math.round(slot.startSec)}" min="0" />
          </label>
          <label class="text-xs">End (s)
            <input type="number" class="input input-sm slot-end" value="${Math.round(slot.endSec)}" min="1" />
          </label>
          <label class="text-xs">SFX Punch Akhir
            <select class="input input-sm slot-sfx">${sfxOpts}</select>
          </label>
        </div>
      `;

      el.querySelector('.slot-lbl').addEventListener('input', (e) => (slot.label = e.target.value));
      el.querySelector('.slot-start').addEventListener('change', (e) => {
        slot.startSec = Math.max(0, parseFloat(e.target.value) || 0);
        renderTopNBuilder();
      });
      el.querySelector('.slot-end').addEventListener('change', (e) => {
        slot.endSec = Math.max(slot.startSec + 2, parseFloat(e.target.value) || slot.startSec + 15);
        renderTopNBuilder();
      });
      el.querySelector('.slot-sfx').addEventListener('change', (e) => (slot.endSfxId = e.target.value));
      el.querySelector('.btn-slot-prev').addEventListener('click', () => {
        const proj = state.projects.find((p) => p.id === slot.projectId);
        if (proj) {
          state.activeProject = proj;
          switchTab('studio');
          setupPlayer(proj, slot.startSec, true);
        }
      });
      el.querySelector('.btn-slot-up').addEventListener('click', () => {
        if (idx > 0) {
          [state.topNSlots[idx - 1], state.topNSlots[idx]] = [state.topNSlots[idx], state.topNSlots[idx - 1]];
          renderTopNBuilder();
        }
      });
      el.querySelector('.btn-slot-dn').addEventListener('click', () => {
        if (idx < n - 1) {
          [state.topNSlots[idx + 1], state.topNSlots[idx]] = [state.topNSlots[idx], state.topNSlots[idx + 1]];
          renderTopNBuilder();
        }
      });
      el.querySelector('.btn-slot-del').addEventListener('click', () => {
        state.topNSlots.splice(idx, 1);
        renderTopNBuilder();
      });
      box.appendChild(el);
    });
  }

  $('btnAddManualToTopN').addEventListener('click', () => {
    if (!state.activeProject) return;
    const s = parseFloat($('manualStart').value) || 0;
    const e = parseFloat($('manualEnd').value) || s + 18;
    const lbl = $('manualHook').value.trim() || `momen ${fmtTime(s)}`;
    addSlotToTopN({ projectId: state.activeProject.id, startSec: s, endSec: e, label: lbl, endSfxId: 'sfx-metal-pipe', endSfxOffset: 1.0 });
  });

  $('btnSendSelectedToTopN').addEventListener('click', () => {
    if (!state.activeProject) return;
    const sel = (state.activeProject.candidates || []).filter((c) => c.selected);
    if (sel.length === 0) {
      toast('Centang minimal 2 kandidat momen dulu!', 'err');
      return;
    }
    // Reverse so lowest rank plays first (#5 -> #4 -> ... -> #1 climax!)
    const ordered = [...sel].reverse();
    ordered.forEach((c) => {
      const s = Math.max(0, Math.round(c.peakSec - 7));
      const e = Math.min(Math.round(state.activeProject.durationSec || c.endSec), s + 18);
      state.topNSlots.push({
        projectId: state.activeProject.id,
        startSec: s,
        endSec: e,
        label: (c.hookText || c.title || 'momen').slice(0, 24).toLowerCase(),
        endSfxId: 'sfx-metal-pipe',
        endSfxOffset: 1.0,
      });
    });
    renderTopNBuilder();
    switchTab('topn');
    toast(`🏆 ${sel.length} momen dimasukkan ke Top-N Builder! Silakan edit label & judul.`, 'ok');
  });

  $('btnFillTop5FromProject').addEventListener('click', () => {
    if (!state.activeProject || !state.activeProject.candidates?.length) {
      toast('Scan link YouTube di tab Studio dulu!', 'err');
      return;
    }
    state.topNSlots = [];
    const top5 = state.activeProject.candidates.slice(0, 5).reverse();
    top5.forEach((c) => {
      const s = Math.max(0, Math.round(c.peakSec - 7));
      const e = Math.min(Math.round(state.activeProject.durationSec || c.endSec), s + 18);
      state.topNSlots.push({
        projectId: state.activeProject.id,
        startSec: s,
        endSec: e,
        label: (c.hookText || c.title).slice(0, 22).toLowerCase(),
        endSfxId: 'sfx-metal-pipe',
        endSfxOffset: 1.0,
      });
    });
    renderTopNBuilder();
    toast('⚡ Top 5 otomatis terisi (#5 → #1 klimaks)!', 'ok');
  });

  $('btnClearTopN').addEventListener('click', () => {
    state.topNSlots = [];
    renderTopNBuilder();
  });

  ['topNMode', 'topNBumperDur', 'topNOutro'].forEach((id) => {
    $(id).addEventListener('change', renderTopNBuilder);
  });

  $('btnRenderTopN').addEventListener('click', async () => {
    if (state.topNSlots.length === 0) return;
    const camp = getActiveCampaign();
    const payload = {
      campaignId: camp ? camp.id : '',
      title: $('topNTitle').value.trim() || `Top ${state.topNSlots.length} Viral Moments`,
      mode: $('topNMode').value,
      segments: state.topNSlots,
      layout: {
        ...state.layout,
        aspectRatio: '9:16',
        resolution: $('selRes').value || '1080p',
        cropMode: $('topNLayout').value,
      },
      subtitles: { enabled: false },
      videoFx: {
        colorPreset: $('topNVivid').checked ? 'vivid_gaming' : 'none',
        motionPreset: 'none',
        speed: 1.0,
        sharpen: true,
      },
      bumperDuration: parseFloat($('topNBumperDur').value) || 0.85,
      bumperSfxId: $('topNBumperSFX').value || '',
      audioNorm: $('topNAudioNorm').checked,
      useWatermark: $('topNWatermark').checked,
      useOutro: $('topNOutro').checked,
    };
    try {
      await api('/api/jobs/compile', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
      toast(`🏆 Kompilasi "${payload.title}" masuk antrean render!`, 'ok');
      switchTab('queue');
    } catch (e) {
      toast('Gagal render kompilasi: ' + e.message, 'err');
    }
  });

  window.addEventListener('resize', () => {
    if (state.activeProject) drawHypeChart();
  });

  setupChartInteractions();
  setupCropDrag();
  init();
})();
