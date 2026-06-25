// app.js — progressive enhancement for the credimi-runner dashboard.
// htmx + SSE do the heavy lifting; this only wires UI affordances that are
// awkward as pure markup. No framework.

(() => {
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => [...r.querySelectorAll(s)];

  // ── Toast (driven by HX-Trigger {"toast":"…"}) ───────────────────────────
  function toast(msg) {
    const host = $('#toast-host');
    if (!host || !msg) return;
    host.innerHTML = `<div class="toast">${check()} ${escapeHtml(msg)}</div>`;
    setTimeout(() => (host.innerHTML = ''), 2800);
  }
  document.body.addEventListener('toast', (e) => toast(typeof e.detail === 'string' ? e.detail : e.detail && e.detail.value));
  document.body.addEventListener('closeModal', () => closeModals());

  // ── Modal open / close ───────────────────────────────────────────────────
  function closeModals() { $$('.modal-bk').forEach((m) => (m.hidden = true)); }
  document.addEventListener('click', (e) => {
    const open = e.target.closest('[data-open-modal]');
    if (open) { const m = $('#modal-' + open.dataset.openModal); if (m) { m.hidden = false; resetWizard(m); } }
    if (e.target.closest('[data-close-modal]')) closeModals();
    if (e.target.classList && e.target.classList.contains('modal-bk')) e.target.hidden = true;
  });
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closeModals(); });

  // ── Review step: sync card values from form fields ────────────────────
  const syncReview = () => {
    const form = document.querySelector('[data-setup-form]');
    if (!form) return;
    const get = (name) => {
      // For radio buttons, find the checked one.
      const radio = form.querySelector(`[name="${name}"]:checked`);
      if (radio) return radio.value || '';
      const el = form.querySelector(`[name="${name}"]`);
      if (!el) return '';
      if (el.type === 'checkbox') return el.checked ? 'true' : 'false';
      if (el.tagName === 'SELECT') return el.options[el.selectedIndex]?.value || '';
      return el.value || '';
    };
    form.querySelectorAll('[data-review]').forEach(el => {
      const key = el.dataset.review;
      let val = get(key);
      if (val === 'true') val = '<span class="chip online"><span class="d"></span>Enabled</span>';
      else if (val === 'false') val = '<span class="chip idle"><span class="d"></span>Disabled</span>';
      else if (!val) val = '—';
      if (el.dataset.reviewSecret && val !== '—') val = '••••••••';
      el.innerHTML = val;
    });
    const netMode = get('CREDIMI_SERVICE_MODE');
    form.querySelectorAll('[data-review-net]').forEach(el => {
      el.style.display = el.dataset.reviewNet === netMode ? '' : 'none';
    });
    const devType = get('CREDIMI_RUNNER_TYPE');
    form.querySelectorAll('[data-review-dev]').forEach(el => {
      const types = (el.dataset.reviewDev || '').split(/\s+/);
      el.style.display = types.includes(devType) ? '' : 'none';
    });
    form.querySelectorAll('[data-review-not]').forEach(el => {
      const notTypes = (el.dataset.reviewNot || '').split(/\s+/);
      el.style.display = notTypes.includes(devType) ? 'none' : '';
    });
  };

  // ── First-run setup wizard ───────────────────────────────────────────────
  function initSetupWizard(root = document) {
    $$('[data-setup-form]', root).forEach((form) => {
      if (form.dataset.setupReady) return;
      form.dataset.setupReady = '1';
      let current = 0;
      const buttons = $$('.wizard-step', form);
      const panels = $$('.wizard-panel', form);
      const prev = $('[data-step-prev]', form);
      const next = $('[data-step-next]', form);
      const submit = $('[data-step-submit]', form);
      const errBox = () => $('[data-setup-error]', panels[current]);
      const setError = (msg) => {
        const box = errBox();
        if (!box) return;
        box.hidden = !msg;
        box.textContent = msg || '';
      };
      const show = (idx) => {
        setError('');
        current = Math.max(0, Math.min(idx, panels.length - 1));
        panels.forEach((p, i) => (p.hidden = i !== current));
        buttons.forEach((b, i) => {
          b.classList.toggle('on', i === current);
          b.classList.toggle('done', i < current);
        });
        if (prev) prev.disabled = current === 0;
        if (next) next.hidden = current === panels.length - 1;
        if (submit) submit.style.display = current === panels.length - 1 ? '' : 'none';
        if (current === panels.length - 1) syncReview();
      };
      const jsonPost = async (url, body) => {
        const res = await fetch(url, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
        return res.json();
      };
      const field = (name) => $(`[name="${name}"]`, form);
      const value = (name) => (field(name) || {}).value || '';
      const orgPreview = () => $('[data-org-preview]', form);
      const setOrgPreview = (text) => {
        const el = orgPreview();
        if (!el) return;
        if (el.tagName === 'INPUT') el.value = text;
        else el.textContent = text;
      };
      const resolveOrganization = async () => {
        const org = await jsonPost('/setup/organization', {
          instance_url: value('CREDIMI_URL'),
          api_key: value('CREDIMI_USER_API_KEY'),
        });
        const orgName = org.canonified_name || '';
        const orgValue = field('CREDIMI_RUNNER_ORGANIZATION');
        if (orgValue) orgValue.value = orgName;
        setOrgPreview(orgName);
        return orgName;
      };
      // Live API-key validation: calls /setup/organization and shows feedback.
      let apiKeyTimer;
      const apiKeyField = field('CREDIMI_USER_API_KEY');
      const statusEl = () => $('[data-api-key-status]', form);
      const errorEl = () => $('[data-api-key-error]', form);
      const identityFields = () => $('[data-identity-fields]', form);
      if (apiKeyField) {
        apiKeyField.addEventListener('input', () => {
          clearTimeout(apiKeyTimer);
          const key = apiKeyField.value.trim();
          const st = statusEl();
          const err = errorEl();
          const idf = identityFields();
          if (!key) {
            if (st) st.style.display = 'none';
            if (err) err.style.display = 'none';
            if (idf) idf.style.display = 'none';
            return;
          }
          apiKeyTimer = setTimeout(async () => {
            try {
              await resolveOrganization();
              if (st) { st.style.display = 'flex'; st.innerHTML = check(); st.style.color = 'var(--ok)'; }
              if (err) err.style.display = 'none';
              if (idf) idf.style.display = 'contents';
            } catch (e) {
              if (st) { st.style.display = 'flex'; st.innerHTML = xmark(); st.style.color = 'var(--down)'; }
              if (err) { err.style.display = 'block'; err.textContent = (e && e.message) || 'Invalid API key'; }
              if (idf) idf.style.display = 'none';
            }
          }, 600);
        });
      }
      const canonifyName = async () => {
        const name = value('CREDIMI_RUNNER_NAME');
        const canonified = $('[data-canonified]', form);
        if (!name) {
          if (canonified) canonified.textContent = '';
          return;
        }
        try {
          const data = await jsonPost('/setup/canonify?name=' + encodeURIComponent(name), {
            instance_url: value('CREDIMI_URL'),
            api_key: value('CREDIMI_USER_API_KEY'),
          });
          if (canonified) canonified.textContent = data.canonified || '';
          const org = value('CREDIMI_RUNNER_ORGANIZATION');
          const runnerID = field('CREDIMI_RUNNER_ID');
          if (runnerID && data.canonified) { runnerID.value = org + '/' + data.canonified; syncOTELServiceName(runnerID.value); }
          const runnerPreview = $('[data-runner-id-preview]', form);
          if (runnerPreview && data.canonified) runnerPreview.textContent = org + '/' + data.canonified;
        } catch (e) {
          if (canonified) canonified.textContent = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
        }
      };
      const syncOTELServiceName = (runnerID) => {
        const otelName = field('OTEL_SERVICE_NAME');
        if (otelName && runnerID) otelName.value = runnerID;
      };
      const previewRunnerID = async () => {
        const data = await jsonPost('/setup/runner-id', {
          instance_url: value('CREDIMI_URL'),
          api_key: value('CREDIMI_USER_API_KEY'),
          organization: value('CREDIMI_RUNNER_ORGANIZATION'),
          name: value('CREDIMI_RUNNER_NAME'),
        });
        const rid = data.runner_id || '';
        const runnerID = field('CREDIMI_RUNNER_ID');
        if (runnerID) runnerID.value = rid;
        const runnerPreview = $('[data-runner-id-preview]', form);
        if (runnerPreview) runnerPreview.textContent = rid;
        syncOTELServiceName(rid);
      };
      // Live-canonify as the user types the runner name.
      const nameField = field('CREDIMI_RUNNER_NAME');
      if (nameField) nameField.addEventListener('input', canonifyName);
      const beforeNext = async () => {
        const panel = panels[current];
        if (!panel) return;
        if (panel.dataset.step === 'identity') { await canonifyName(); await previewRunnerID(); }
      };
      buttons.forEach((b, i) => b.addEventListener('click', () => show(i)));
      if (prev) prev.addEventListener('click', () => show(current - 1));
      if (next) next.addEventListener('click', async () => {
        next.disabled = true;
        try {
          await beforeNext();
          show(current + 1);
        } catch (err) {
          setError(err && err.message ? err.message : 'Setup step failed');
        } finally {
          next.disabled = false;
        }
      });
      show(0);
    });
  }
  initSetupWizard();

  // ── Network step: radio cards + show/hide fields based on service mode ──
  const syncNetMode = (mode) => {
    document.querySelectorAll('[data-net-mode]').forEach(el => {
      el.style.display = el.dataset.netMode === mode ? '' : 'none';
    });
    document.querySelectorAll('[data-net-pick]').forEach(p => {
      p.classList.toggle('on', p.dataset.netPick === mode);
    });
  };
  document.addEventListener('click', (e) => {
    const pick = e.target.closest('[data-net-pick]');
    if (!pick) return;
    const radio = pick.querySelector('input[type="radio"]');
    if (radio) { radio.checked = true; syncNetMode(radio.value); }
  });
  document.addEventListener('change', (e) => {
    if (e.target.name === 'CREDIMI_SERVICE_MODE') syncNetMode(e.target.value);
  });
  const initNetMode = () => {
    const checked = document.querySelector('[name="CREDIMI_SERVICE_MODE"]:checked');
    if (checked) syncNetMode(checked.value);
  };
  initNetMode();

  // ── Device step: radio cards + show/hide fields based on runner type and mode ──
  const updateDeviceFields = () => {
    const type = (document.querySelector('[name="CREDIMI_RUNNER_TYPE"]:checked') || {}).value || '';
    const mode = (document.querySelector('[name="CREDIMI_RUNNER_DEVICE_MODE"]') || {}).value || '';
    document.querySelectorAll('[data-dev-type]').forEach(el => {
      const types = (el.dataset.devType || '').split(/\s+/);
      el.style.display = types.includes(type) ? '' : 'none';
    });
    document.querySelectorAll('[data-dev-mode]').forEach(el => {
      const modes = (el.dataset.devMode || '').split(/\s+/);
      const parent = el.closest('[data-dev-type]');
      const parentHidden = parent && parent.style.display === 'none';
      el.style.display = !parentHidden && modes.includes(mode) ? '' : 'none';
    });
    document.querySelectorAll('[data-dev-pick]').forEach(p => {
      p.classList.toggle('on', p.dataset.devPick === type);
    });
  };
  document.addEventListener('click', (e) => {
    const pick = e.target.closest('[data-dev-pick]');
    if (!pick) return;
    const radio = pick.querySelector('input[type="radio"]');
    if (radio) { radio.checked = true; updateDeviceFields(); }
  });
  document.addEventListener('change', (e) => {
    if (e.target.name === 'CREDIMI_RUNNER_DEVICE_MODE') updateDeviceFields();
  });
  const initDeviceFields = () => {
    const checked = document.querySelector('[name="CREDIMI_RUNNER_TYPE"]:checked');
    if (checked) updateDeviceFields();
  };
  initDeviceFields();

  // ── API keys link button (reads from CREDIMI_URL field) ─────────────────
  document.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-api-keys-link]');
    if (!btn) return;
    e.preventDefault();
    const base = (document.querySelector('[name="CREDIMI_URL"]') || {}).value || 'https://credimi.io';
    const url = base.replace(/\/+$/, '') + '/my/profile/api-keys';
    window.open(url, '_blank', 'noopener');
  });
  document.addEventListener('input', (e) => {
    if (e.target.name === 'CREDIMI_URL') {
      const btn = document.querySelector('[data-api-keys-link]');
      if (btn) {
        const base = e.target.value || 'https://credimi.io';
        btn.textContent = base.replace(/^https?:\/\//, '') + '/my/profile/api-keys';
      }
    }
  });

  // ── Add-device wizard ────────────────────────────────────────────────────
  let step = 0;
  function resetWizard(m) { step = 0; renderWizard(m); }
  function renderWizard(m) {
    const type = typeOf(m);
    const mode = modeOf(m);
    const last = lastStep(type, mode);
    if (step > last) step = last;
    $$('[data-step]', m).forEach((el) => (el.hidden = +el.dataset.step !== step));
    $$('[data-steps] .st', m).forEach((el, i) => { el.classList.toggle('on', i === step); el.classList.toggle('done', i < step); });
    const phoneFlow = type === 'android_phone';
    $$('[data-phone-step]', m).forEach((el) => (el.hidden = !phoneFlow || +el.dataset.step !== step));
    $$('[data-wifi-step]', m).forEach((el) => (el.hidden = !phoneFlow || mode !== 'wifi' || +el.dataset.step !== step));
    $('[data-step-back]', m).hidden = step === 0;
    $('[data-step-next]', m).hidden = step >= last;
    $('[data-step-submit]', m).hidden = step < last;
  }
  function lastStep(type, mode) {
    if (type !== 'android_phone') return 0;
    return mode === 'wifi' ? 2 : 1;
  }
  document.addEventListener('click', (e) => {
    const m = e.target.closest('.modal-bk'); if (!m) return;
    if (e.target.closest('[data-step-next]')) { step = Math.min(step + 1, lastStep(typeOf(m), modeOf(m))); renderWizard(m); }
    if (e.target.closest('[data-step-back]')) { step = Math.max(step - 1, 0); renderWizard(m); }
    const pick = e.target.closest('[data-pick-type]');
    if (pick) { $$('[data-pick-type]', m).forEach((p) => p.classList.remove('on')); pick.classList.add('on'); $('input[name=type]', m).value = pick.dataset.pickType; renderWizard(m); }
    const segBtn = e.target.closest('[data-seg] [data-val]');
    if (segBtn && segBtn.closest('[data-seg]').dataset.seg === 'mode') {
      const seg = segBtn.closest('[data-seg]');
      $$('[data-val]', seg).forEach((b) => b.classList.remove('on')); segBtn.classList.add('on');
      $('input[name=mode]', m).value = segBtn.dataset.val;
      renderWizard(m);
    }
  });
  function typeOf(m) { return ($('input[name=type]', m) || {}).value || 'android_phone'; }
  function modeOf(m) { return ($('input[name=mode]', m) || {}).value || 'wifi'; }

  // ── Pick-card segmented control (network: service mode) ───────────────────
  document.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-seg] [data-val]');
    if (!btn) return;
    const seg = btn.closest('[data-seg]');
    const key = seg.dataset.seg;
    if (key === 'mode') return; // handled above
    $$('[data-val]', seg).forEach((b) => b.classList.remove('on'));
    btn.classList.add('on');
    const hidden = seg.parentElement.querySelector(`input[name="${key}"]`) || document.querySelector(`input[name="${key}"]`);
    if (hidden) { hidden.value = btn.dataset.val; hidden.dispatchEvent(new Event('input', { bubbles: true })); }
  });

  // ── Auth mode segmented control (config) ─────────────────────────────────
  document.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-auth-seg] [data-val]');
    if (!btn) return;
    const seg = btn.closest('[data-auth-seg]');
    $$('[data-val]', seg).forEach((b) => b.classList.remove('on'));
    btn.classList.add('on');
    const mode = btn.dataset.val;
    $$('[data-auth-field]').forEach((f) => (f.hidden = f.dataset.authField !== mode));
    // clear the non-selected key so exactly one is persisted
    const clearKey = mode === 'user' ? seg.dataset.admin : seg.dataset.user;
    const inp = document.querySelector(`[name="${clearKey}"]`); if (inp) inp.value = '';
    markDirty();
  });

  // ── Toggles (.tog with data-toggle → sync hidden checkbox) ────────────────
  document.addEventListener('click', (e) => {
    const tog = e.target.closest('.tog[data-toggle]');
    if (!tog) return;
    tog.classList.toggle('on');
    const on = tog.classList.contains('on');
    tog.setAttribute('aria-checked', on);
    const box = tog.parentElement.querySelector('input[type=checkbox]');
    if (box) box.checked = on;
    markDirty();
  });

  // ── Copy buttons ─────────────────────────────────────────────────────────
  document.addEventListener('click', async (e) => {
    const reveal = e.target.closest('[data-reveal-secret]');
    if (reveal) {
      const wrap = reveal.closest('.inp-wrap');
      const input = wrap && $('.inp', wrap);
      if (input) input.type = input.type === 'password' ? 'text' : 'password';
      return;
    }
    const v = e.target.closest('[data-copy-value]');
    if (v) { copy(v.dataset.copyValue, v); return; }
    const c = e.target.closest('[data-copy]');
    if (c) {
      const wrap = c.closest('.inp-wrap');
      const input = wrap && $('.inp', wrap);
      copy(input ? input.value : '', c);
    }
  });
  function copy(text, btn) {
    navigator.clipboard && navigator.clipboard.writeText(text);
    if (btn) { const old = btn.innerHTML; btn.classList.add('ok'); btn.innerHTML = check() + ' Copied';
      setTimeout(() => { btn.classList.remove('ok'); btn.innerHTML = old; }, 1400); }
  }

  // ── Dirty save bar ───────────────────────────────────────────────────────
  function markDirty() { $$('[data-savebar]').forEach((b) => (b.hidden = false)); }
  document.addEventListener('input', (e) => { if (e.target.closest('[data-config-form]')) markDirty(); });
  document.addEventListener('click', (e) => {
    if (e.target.closest('[data-discard]')) {
      const form = e.target.closest('[data-config-form]');
      htmx.ajax('GET', location.pathname, { target: 'main', select: 'main', swap: 'outerHTML' });
    }
  });

  // ── Sidebar active-state + crumb sync after client nav ───────────────────
  function syncNav() {
    const path = location.pathname === '/' ? '/' : location.pathname.replace(/\/$/, '');
    let label = 'Overview';
    $$('.nav-item').forEach((a) => {
      const href = a.getAttribute('href');
      const on = href === path;
      a.classList.toggle('on', on);
      if (on) label = a.textContent.trim().replace(/\d+\/\d+$|\d+$/, '').trim();
    });
    const crumb = $('.crumbs b'); if (crumb) crumb.textContent = label;
  }
  document.body.addEventListener('htmx:afterSwap', (e) => {
    if (e.detail.target && e.detail.target.tagName === 'MAIN') {
      syncNav();
      initSetupWizard(e.detail.target);
      initNetMode();
      initDeviceFields();
    }
  });
  window.addEventListener('popstate', syncNav);

  // ── helpers ──
  function escapeHtml(s) { const d = document.createElement('div'); d.textContent = s == null ? '' : s; return d.innerHTML; }
  function decodeHtml(s) { const d = document.createElement('textarea'); d.innerHTML = s; return d.value; }
  function check() { return '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>'; }
  function xmark() { return '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>'; }
})();
