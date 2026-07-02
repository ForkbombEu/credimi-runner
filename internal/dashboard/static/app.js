// app.js — progressive enhancement for the credimi-runner dashboard.
// htmx + SSE do the heavy lifting; this only wires UI affordances that are
// awkward as pure markup. No framework.

(() => {
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => [...r.querySelectorAll(s)];
  const TYPE_DEFAULTS = {
    phoneImage: 'ghcr.io/forkbombeu/credimi-runner-phone:latest',
    emulatorImage: 'ghcr.io/forkbombeu/credimi-runner-emulator:latest',
    baseName: 'credimi',
    goldenPath: '/avd-golden/credimi-golden',
    wifiPort: '5555',
    redroidDataDir: '/home/credimi/redroid-data',
    redroidDataTar: '/home/credimi/redroid-data.tar',
  };
  const TYPE_PREVIEW_KEYS = [
    'ANDROID_KEYS_DIR',
    'AVDCTL_SSH_KNOWN_HOSTS_PATH',
    'AVDCTL_SSH_PASSWORD',
    'AVDCTL_SSH_TARGET',
    'AVDCTL_SUDO',
    'AVDCTL_SUDO_PASSWORD',
    'BASE_NAME',
    'CREDIMI_CONTAINER_MODE',
    'CREDIMI_RUNNER_DEVICE_MODE',
    'CREDIMI_RUNNER_SERIAL',
    'CREDIMI_RUNNER_TYPE',
    'CREDIMI_RUNNER_WIFI_IP',
    'CREDIMI_RUNNER_WIFI_PORT',
    'GOLDEN_PATH',
    'HOST_AVD_GOLDEN_PATH',
    'HOST_AVD_HOME_PATH',
    'REDROID_DATA_DIR',
    'REDROID_DATA_TAR',
    'RUNNER_IMAGE',
  ];

  // ── Toast (driven by HX-Trigger {"toast":"…"}) ───────────────────────────
  function toast(msg) {
    const host = $('#toast-host');
    if (!host || !msg) return;
    host.innerHTML = `<div class="toast">${check()} ${escapeHtml(msg)}</div>`;
    setTimeout(() => (host.innerHTML = ''), 2800);
  }
  document.body.addEventListener('toast', (e) => toast(typeof e.detail === 'string' ? e.detail : e.detail && e.detail.value));
  document.body.addEventListener('closeModal', () => closeModals());

  // ── Global busy overlay for runtime-changing requests ───────────────────
  const setupBusyKey = 'credimi-runner:setup-startup-busy';
  let busyLogTimer = null;
  let busyStartupTimer = null;
  let busyLogSeen = new Set();
  let busyStartupNextID = 0;
  const startupBusyPhases = new Set(['starting', 'waiting_for_runner', 'registering']);
  function busyOverlay() { return $('#busy-overlay'); }
  function busyLogNode() {
    const overlay = busyOverlay();
    return overlay && $('[data-busy-log]', overlay);
  }
  function appendBusyLog(line) {
    const log = busyLogNode();
    const text = String(line || '').trim();
    if (!log || !text || busyLogSeen.has(text)) return;
    busyLogSeen.add(text);
    if (busyLogSeen.size > 160) busyLogSeen = new Set([...busyLogSeen].slice(-120));
    const stamp = new Date().toLocaleTimeString([], { hour12: false });
    log.textContent += `${stamp}  ${text}\n`;
    log.scrollTop = log.scrollHeight;
  }
  async function pollBusyLogs() {
    try {
      const res = await fetch('/runtime/logs', { headers: { Accept: 'application/json' } });
      if (!res.ok) return;
      const data = await res.json();
      (data.lines || []).slice(-24).forEach(appendBusyLog);
    } catch (_) {}
  }
  async function pollBusyStartupStatus() {
    try {
      const url = busyStartupNextID > 0 ? `/startup/status?since=${busyStartupNextID}` : '/startup/status';
      const res = await fetch(url, { headers: { Accept: 'application/json' } });
      if (!res.ok) return;
      const data = await res.json();
      const phase = String(data.phase || '');
      const message = String(data.message || '');
      (data.lines || []).forEach(appendBusyLog);
      if (Number.isFinite(Number(data.next_id))) busyStartupNextID = Number(data.next_id);
      if (message) {
        const overlay = busyOverlay();
        const messageNode = overlay && $('[data-busy-message]', overlay);
        if (messageNode) messageNode.textContent = message;
        appendBusyLog(message);
      }
      if (phase === 'idle' && sessionStorage.getItem(setupBusyKey)) {
        appendBusyLog('Waiting for setup job to start.');
        return;
      }
      if (!startupBusyPhases.has(phase)) {
        sessionStorage.removeItem(setupBusyKey);
        if (phase === 'ready') appendBusyLog('Setup complete. Opening dashboard.');
        if (phase === 'needs_attention') appendBusyLog('Setup needs attention. Check the dashboard message.');
        clearInterval(busyStartupTimer);
        busyStartupTimer = null;
        const delay = phase === 'needs_attention' ? 2500 : 1000;
        setTimeout(() => { window.location.assign('/'); }, delay);
      }
    } catch (_) {}
  }
  function showBusy(message, options = {}) {
    const overlay = busyOverlay();
    if (!overlay) return;
    const messageNode = $('[data-busy-message]', overlay);
    if (messageNode && message) messageNode.textContent = message;
    const log = busyLogNode();
    busyLogSeen = new Set();
    if (log) log.textContent = '';
    busyStartupNextID = 0;
    appendBusyLog(message || 'Starting runtime operation.');
    appendBusyLog('Writing configuration and preparing Docker services.');
    appendBusyLog('Large runner images can take several minutes the first time.');
    clearInterval(busyLogTimer);
    if (options.runtimeLogs !== false) {
      pollBusyLogs();
      busyLogTimer = setInterval(pollBusyLogs, 1500);
    }
    overlay.hidden = false;
    document.body.classList.add('busy-lock');
  }
  function hideBusy() {
    const overlay = busyOverlay();
    if (!overlay) return;
    clearInterval(busyLogTimer);
    clearInterval(busyStartupTimer);
    busyLogTimer = null;
    busyStartupTimer = null;
    overlay.hidden = true;
    document.body.classList.remove('busy-lock');
  }
  function showSetupBusy(message) {
    showBusy(message || 'Writing runner config and starting services. Keep this page open.', { runtimeLogs: false });
    clearInterval(busyStartupTimer);
    pollBusyStartupStatus();
    busyStartupTimer = setInterval(pollBusyStartupStatus, 1500);
  }
  function resumeSetupBusyIfNeeded() {
    const overlay = busyOverlay();
    const phase = overlay && overlay.dataset.startupPhase;
    const message = (overlay && overlay.dataset.startupMessage) || sessionStorage.getItem(setupBusyKey) || '';
    if (startupBusyPhases.has(phase) || sessionStorage.getItem(setupBusyKey)) {
      showSetupBusy(message);
    }
  }
  function busyTriggerForElement(el) {
    if (!el) return null;
    const trigger = el.closest('[data-runtime-action],[data-config-form],[data-setup-form]');
    if (!trigger) return null;
    if (trigger.matches('[data-config-form]') && !trigger.matches('[data-setup-form]') && trigger.dataset.busyActive !== '1') {
      return null;
    }
    return trigger;
  }
  document.body.addEventListener('htmx:beforeRequest', (e) => {
    const trigger = busyTriggerForElement(e.detail.elt);
    if (!trigger) return;
    const message = trigger.dataset.busyMessage || 'Applying runtime change. Keep this page open.';
    if (trigger.matches('[data-setup-form]')) {
      sessionStorage.setItem(setupBusyKey, message);
      showSetupBusy(message);
      return;
    }
    showBusy(message);
  });
  document.body.addEventListener('htmx:afterRequest', (e) => {
    const trigger = busyTriggerForElement(e.detail.elt);
    const wasBusy = !!trigger;
    if (trigger && trigger.matches('[data-config-form]')) delete trigger.dataset.busyActive;
    if (trigger && trigger.matches('[data-setup-form]')) {
      const redirected = e.detail.xhr && e.detail.xhr.getResponseHeader('HX-Redirect');
      if (redirected && e.detail.successful !== false) return;
      if (e.detail.successful !== false) return;
      sessionStorage.removeItem(setupBusyKey);
    }
    if (wasBusy) hideBusy();
  });
  document.body.addEventListener('htmx:responseError', () => {
    sessionStorage.removeItem(setupBusyKey);
    hideBusy();
  });
  document.body.addEventListener('htmx:sendError', () => {
    sessionStorage.removeItem(setupBusyKey);
    hideBusy();
  });
  resumeSetupBusyIfNeeded();

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
      let runnerPreview = null;
      const errBox = () => $('[data-setup-error]', panels[current]);
      const setError = (msg) => {
        const box = errBox();
        if (!box) return;
        box.hidden = !msg;
        box.textContent = msg || '';
      };
      const valueMissing = (name) => !String(value(name) || '').trim();
      const currentStepValid = () => {
        const panel = panels[current];
        if (!panel) return false;
        switch (panel.dataset.step) {
          case 'identity':
            if (authMode() === 'admin') {
              if (valueMissing('CREDIMI_INTERNAL_ADMIN_KEY') || valueMissing('CREDIMI_RUNNER_NAME') || valueMissing('CREDIMI_RUNNER_ORGANIZATION')) return false;
            } else {
              if (valueMissing('CREDIMI_USER_API_KEY') || valueMissing('CREDIMI_RUNNER_NAME')) return false;
            }
            return true;
          case 'network': {
            const mode = value('CREDIMI_SERVICE_MODE');
            if (mode === 'manual') return !valueMissing('RUNNER_PUBLIC_URL');
            if (mode === 'cloudflare-managed') return !valueMissing('RUNNER_DOMAIN') && !valueMissing('CLOUDFLARE_TUNNEL_TOKEN');
            return true;
          }
          case 'device': {
            const runnerType = value('CREDIMI_RUNNER_TYPE');
            const mode = value('CREDIMI_RUNNER_DEVICE_MODE');
            if (runnerType === 'android_phone' && mode !== 'wifi') {
              return !valueMissing('CREDIMI_RUNNER_SERIAL');
            }
            if (runnerType === 'android_phone' && value('CREDIMI_RUNNER_DEVICE_MODE') === 'wifi') {
              return !valueMissing('CREDIMI_RUNNER_WIFI_IP');
            }
            if (runnerType === 'android_emulator') {
              const panel = $('[data-android-emulator-assets-panel]', form);
              return !valueMissing('BASE_NAME') && (!panel || panel.dataset.ready === '1');
            }
            if (runnerType === 'ios_simulator') {
              const panel = $('[data-ios-simulator-panel]', form);
              return !valueMissing('BASE_NAME') && (!panel || panel.dataset.exists === '1');
            }
            if (runnerType === 'redroid') {
              return !valueMissing('REDROID_DATA_DIR') && !valueMissing('REDROID_DATA_TAR');
            }
            return !!runnerType;
          }
          default:
            return true;
        }
      };
      const currentStepError = () => {
        const panel = panels[current];
        if (!panel) return 'Complete the required fields before continuing.';
        switch (panel.dataset.step) {
          case 'identity':
            if (authMode() === 'admin') {
              if (valueMissing('CREDIMI_INTERNAL_ADMIN_KEY')) return 'Internal admin key is required.';
              if (valueMissing('CREDIMI_RUNNER_ORGANIZATION')) return 'Organization is required.';
            } else {
              if (valueMissing('CREDIMI_USER_API_KEY')) return 'User API key is required.';
            }
            if (valueMissing('CREDIMI_RUNNER_NAME')) return 'Runner name is required.';
            return '';
          case 'network': {
            const mode = value('CREDIMI_SERVICE_MODE');
            if (mode === 'manual' && valueMissing('RUNNER_PUBLIC_URL')) return 'Manual mode requires a public URL.';
            if (mode === 'cloudflare-managed' && valueMissing('RUNNER_DOMAIN')) return 'Managed mode requires a runner domain.';
            if (mode === 'cloudflare-managed' && valueMissing('CLOUDFLARE_TUNNEL_TOKEN')) return 'Managed mode requires a tunnel token.';
            return '';
          }
          case 'device': {
            const runnerType = value('CREDIMI_RUNNER_TYPE');
            if (!runnerType) return 'Runner type is required.';
            const mode = value('CREDIMI_RUNNER_DEVICE_MODE');
            if (runnerType === 'android_phone' && mode === 'wifi' && valueMissing('CREDIMI_RUNNER_WIFI_IP')) {
              return 'Wi-Fi mode requires an Android Wi-Fi IP.';
            }
            if (runnerType === 'android_phone' && mode !== 'wifi' && valueMissing('CREDIMI_RUNNER_SERIAL')) {
              return 'Select a connected Android device.';
            }
            if (runnerType === 'android_emulator' && valueMissing('BASE_NAME')) return 'Base name is required.';
            if (runnerType === 'android_emulator') {
              const panel = $('[data-android-emulator-assets-panel]', form);
              if (panel && panel.dataset.checking === '1') return 'Checking emulator assets.';
              return 'Emulator assets must be present before continuing.';
            }
            if (runnerType === 'ios_simulator' && valueMissing('BASE_NAME')) return 'Simulator name is required.';
            if (runnerType === 'ios_simulator') return 'Create or select the named simulator before continuing.';
            if (runnerType === 'redroid' && valueMissing('REDROID_DATA_DIR')) return 'Redroid data directory is required.';
            if (runnerType === 'redroid' && valueMissing('REDROID_DATA_TAR')) return 'Redroid data archive is required.';
            return '';
          }
          default:
            return '';
        }
      };
      const syncStepActions = () => {
        if (next && !next.hidden) next.disabled = !currentStepValid();
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
        form.dispatchEvent(new CustomEvent('dashboard:step-shown', { bubbles: true, detail: { step: panels[current] && panels[current].dataset.step } }));
        syncStepActions();
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
      const value = (name) => {
        const radio = $(`input[type="radio"][name="${name}"]:checked`, form);
        if (radio) return radio.value || '';
        return (field(name) || {}).value || '';
      };
      const authMode = () => {
        const admin = $('[data-auth-field="admin"]', form);
        return admin && !admin.hidden ? 'admin' : 'user';
      };
      const selectedAPIKey = () => authMode() === 'admin' ? value('CREDIMI_INTERNAL_ADMIN_KEY') : value('CREDIMI_USER_API_KEY');
      const orgPreview = () => $('[data-org-preview]', form);
      const setOrgPreview = (text) => {
        const el = orgPreview();
        if (!el) return;
        if (el.tagName === 'INPUT') el.value = text;
        else el.textContent = text;
      };
      const syncAdminOrganization = () => {
        const adminOrg = $('[data-admin-org-input]', form);
        if (!adminOrg) return;
        const orgValue = field('CREDIMI_RUNNER_ORGANIZATION');
        if (orgValue && authMode() === 'admin') orgValue.value = adminOrg.value.trim();
        setOrgPreview((orgValue && orgValue.value) || 'org');
      };
      const resolveOrganization = async () => {
        if (authMode() === 'admin') {
          syncAdminOrganization();
          return value('CREDIMI_RUNNER_ORGANIZATION');
        }
        const org = await jsonPost('/setup/organization', {
          instance_url: value('CREDIMI_URL'),
          api_key: selectedAPIKey(),
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
      const showIdentityFields = () => { const idf = identityFields(); if (idf) idf.style.display = 'contents'; };
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
            syncStepActions();
            return;
          }
          apiKeyTimer = setTimeout(async () => {
            try {
              await resolveOrganization();
              if (st) { st.style.display = 'flex'; st.innerHTML = check(); st.style.color = 'var(--ok)'; }
              if (err) err.style.display = 'none';
              showIdentityFields();
            } catch (e) {
              if (st) { st.style.display = 'flex'; st.innerHTML = xmark(); st.style.color = 'var(--down)'; }
              if (err) { err.style.display = 'block'; err.textContent = (e && e.message) || 'Invalid API key'; }
              if (idf) idf.style.display = 'none';
            } finally {
              syncStepActions();
            }
          }, 600);
        });
      }
      const adminKeyField = field('CREDIMI_INTERNAL_ADMIN_KEY');
      const adminOrgField = $('[data-admin-org-input]', form);
      if (adminKeyField) adminKeyField.addEventListener('input', () => { if (adminKeyField.value.trim()) showIdentityFields(); });
      if (adminOrgField) adminOrgField.addEventListener('input', () => { syncAdminOrganization(); previewRunnerID(); });
      if (authMode() === 'admin' && selectedAPIKey()) showIdentityFields();
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
            api_key: selectedAPIKey(),
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
      const setConflictState = (preview) => {
        const actionInput = $('[data-runner-conflict-action]', form);
        if (!actionInput) return;
        runnerPreview = preview;
        if (!preview || !preview.conflict) {
          return;
        }
        actionInput.value = actionInput.value || preview.default_action || 'update';
      };
      const applyConflictDecision = (preview, action) => {
        const actionInput = $('[data-runner-conflict-action]', form);
        const runnerID = field('CREDIMI_RUNNER_ID');
        const nextRunnerID = action === 'create' ? preview.preview_runner_id : preview.base_runner_id;
        if (actionInput) actionInput.value = action;
        if (runnerID) runnerID.value = nextRunnerID || '';
        const runnerPreviewEl = $('[data-runner-id-preview]', form);
        if (runnerPreviewEl) runnerPreviewEl.textContent = nextRunnerID || '';
        syncOTELServiceName(nextRunnerID || '');
      };
      const openRunnerConflictModal = (preview) => new Promise((resolve) => {
        const modal = $('#runner-conflict-modal');
        if (!modal) {
          resolve('cancel');
          return;
        }
        const summary = $('[data-runner-conflict-modal-summary]', modal);
        const existing = $('[data-runner-conflict-modal-existing]', modal);
        const suggested = $('[data-runner-conflict-modal-preview]', modal);
        if (summary) summary.textContent = 'The requested runner name already exists. Choose whether to update it or create a new runner ID.';
        if (existing) existing.innerHTML = `Existing runner: <span class="tag mono">${escapeHtml(preview.base_runner_id || '')}</span>`;
        if (suggested) suggested.innerHTML = `New available runner ID: <span class="tag mono">${escapeHtml(preview.preview_runner_id || preview.base_runner_id || '')}</span>`;
        modal.hidden = false;
        const primary = $('[data-runner-conflict-decision]', modal);
        if (primary) primary.focus();

        const finish = (decision) => {
          modal.hidden = true;
          modal.removeEventListener('click', onClick);
          document.removeEventListener('keydown', onKeydown);
          resolve(decision);
        };
        const onClick = (event) => {
          const decision = event.target.closest('[data-runner-conflict-decision]');
          if (decision) {
            finish(decision.dataset.runnerConflictDecision || 'cancel');
            return;
          }
          if (event.target.closest('[data-runner-conflict-cancel]')) finish('cancel');
        };
        const onKeydown = (event) => {
          if (event.key === 'Escape') finish('cancel');
        };
        modal.addEventListener('click', onClick);
        document.addEventListener('keydown', onKeydown);
      });
      const previewRunnerID = async () => {
        const instanceURL = value('CREDIMI_URL');
        const apiKey = selectedAPIKey();
        const organization = value('CREDIMI_RUNNER_ORGANIZATION');
        const name = value('CREDIMI_RUNNER_NAME');
        if (!instanceURL || !apiKey || !organization || !name) {
          const fallback = [organization || 'org', (name || 'runner-name').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')].join('/');
          const runnerID = field('CREDIMI_RUNNER_ID');
          if (runnerID) runnerID.value = fallback;
          const runnerPreview = $('[data-runner-id-preview]', form);
          if (runnerPreview) runnerPreview.textContent = fallback;
          syncOTELServiceName(fallback);
          setConflictState(null);
          return;
        }
        let rid = '';
        let previewData = null;
        try {
          const data = await jsonPost('/setup/runner-id', {
            instance_url: instanceURL,
            api_key: apiKey,
            organization: organization,
            name: name,
          });
          const actionInput = $('[data-runner-conflict-action]', form);
          if (actionInput && !actionInput.value) {
            actionInput.value = data.default_action || 'update';
          }
          rid = (actionInput && actionInput.value === 'create' ? data.preview_runner_id : data.base_runner_id) || data.runner_id || '';
          setConflictState(data);
          previewData = data;
        } catch (e) {
          rid = [organization, name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')].join('/');
          setConflictState(null);
        }
        const runnerID = field('CREDIMI_RUNNER_ID');
        if (runnerID) runnerID.value = rid;
        const runnerPreview = $('[data-runner-id-preview]', form);
        if (runnerPreview) runnerPreview.textContent = rid;
        syncOTELServiceName(rid);
        syncStepActions();
        return previewData;
      };
      // Live-canonify as the user types the runner name.
      const nameField = field('CREDIMI_RUNNER_NAME');
      if (nameField) nameField.addEventListener('input', () => {
        canonifyName();
        syncStepActions();
      });
      const beforeNext = async () => {
        const panel = panels[current];
        if (!panel) return;
        if (panel.dataset.step === 'identity') {
          await resolveOrganization();
          await canonifyName();
          const preview = await previewRunnerID();
          if (preview && preview.conflict) {
            const decision = await openRunnerConflictModal(preview);
            if (decision === 'cancel') return false;
            applyConflictDecision(preview, decision);
          }
        }
        return true;
      };
      buttons.forEach((b, i) => b.addEventListener('click', () => {
        if (i > current && !currentStepValid()) {
          setError(currentStepError());
          syncStepActions();
          return;
        }
        show(i);
      }));
      if (prev) prev.addEventListener('click', () => show(current - 1));
      if (next) next.addEventListener('click', async () => {
        next.disabled = true;
        try {
          if (!currentStepValid()) {
            setError(currentStepError());
            return;
          }
          const okToProceed = await beforeNext();
          if (okToProceed === false) return;
          show(current + 1);
        } catch (err) {
          setError(err && err.message ? err.message : 'Setup step failed');
        } finally {
          next.disabled = false;
        }
      });
      form.addEventListener('input', syncStepActions);
      form.addEventListener('change', syncStepActions);
      form.addEventListener('dashboard:device-ready-change', syncStepActions);
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
    const input = checked || document.querySelector('[name="CREDIMI_SERVICE_MODE"]');
    if (input) syncNetMode(input.value);
  };
  initNetMode();

  // ── Device step: radio cards + show/hide fields based on runner type and mode ──
  const setPanelVisible = (el, visible) => {
    el.style.display = visible ? '' : 'none';
  };
  const fieldValue = (root, name) => {
    const radio = root.querySelector(`[name="${name}"]:checked`);
    if (radio) return radio.value || '';
    const input = root.querySelector(`[name="${name}"]`);
    return input ? (input.value || '') : '';
  };
  const setFieldValue = (root, name, value) => {
    const radios = root.querySelectorAll(`input[type="radio"][name="${name}"]`);
    if (radios.length > 0) {
      let changed = false;
      radios.forEach((radio) => {
        const shouldCheck = (radio.value || '') === value;
        if (radio.checked !== shouldCheck) {
          radio.checked = shouldCheck;
          changed = true;
        }
      });
      if (!changed) return;
      const checked = root.querySelector(`input[type="radio"][name="${name}"]:checked`) || radios[0];
      checked.dispatchEvent(new Event('input', { bubbles: true }));
      checked.dispatchEvent(new Event('change', { bubbles: true }));
      return;
    }
    const input = root.querySelector(`[name="${name}"]`);
    if (!input || input.value === value) return;
    input.value = value;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    input.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const formParams = (root) => {
    const params = new URLSearchParams();
    root.querySelectorAll('[name]').forEach((input) => {
      if (input.disabled) return;
      if (input.type === 'radio') {
        if (input.checked) params.set(input.name, input.value || '');
        return;
      }
      if (input.type === 'checkbox') {
        if (input.checked) params.set(input.name, input.value || 'on');
        return;
      }
      params.set(input.name, input.value || '');
    });
    const runnerType = root.querySelector('[name="CREDIMI_RUNNER_TYPE"]:checked');
    if (runnerType) params.set('CREDIMI_RUNNER_TYPE', runnerType.value || '');
    return params;
  };
  const setCallout = (el, tone, message) => {
    if (!el) return;
    const icon = tone === 'danger' ? xmark() : tone === 'warn' ? warn() : check();
    el.className = `callout ${tone}`;
    el.innerHTML = `${icon}<div>${escapeHtml(message)}</div>`;
  };
  const androidEmulatorProgressState = new WeakMap();
  const formatBytes = (bytes) => {
    const value = Number(bytes) || 0;
    if (value >= 1024 * 1024 * 1024) return `${(value / (1024 * 1024 * 1024)).toFixed(1)} GB`;
    if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`;
    if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`;
    return `${Math.round(value)} B`;
  };
  const androidEmulatorProgressLabel = (phase) => {
    switch (phase) {
      case 'starting':
        return 'Preparing download';
      case 'base_avd_downloading':
        return 'Downloading base AVD';
      case 'base_avd_extracting':
        return 'Extracting base AVD';
      case 'golden_downloading':
        return 'Downloading golden image';
      case 'golden_extracting':
        return 'Extracting golden image';
      case 'complete':
        return 'Download complete';
      default:
        return 'Downloading emulator assets';
    }
  };
  const setAndroidEmulatorProgress = (panel, progress) => {
    if (!panel) return;
    const box = panel.querySelector('[data-android-emulator-progress]');
    const bar = panel.querySelector('[data-android-emulator-progress-bar]');
    const label = panel.querySelector('[data-android-emulator-progress-label]');
    if (!box || !bar || !label) return;
    box.hidden = false;
    const phase = progress && progress.phase ? progress.phase : 'starting';
    let pct = 0;
    if (progress && Number(progress.total) > 0) {
      pct = Math.max(0, Math.min(100, Math.round((Number(progress.bytes) / Number(progress.total)) * 100)));
    } else if (phase === 'base_avd_extracting') {
      pct = 45;
    } else if (phase === 'golden_extracting') {
      pct = 90;
    } else if (phase === 'complete') {
      pct = 100;
    }
    bar.style.width = `${pct}%`;
    let speedText = '';
    if (progress && Number(progress.bytes) > 0 && phase.includes('downloading')) {
      const now = (window.performance && window.performance.now) ? window.performance.now() : Date.now();
      let state = androidEmulatorProgressState.get(panel);
      if (!state || state.phase !== phase || Number(progress.bytes) < state.startBytes) {
        state = { phase, startBytes: Number(progress.bytes), startTime: now };
        androidEmulatorProgressState.set(panel, state);
      }
      const elapsedSeconds = Math.max((now - state.startTime) / 1000, 0.25);
      const bytesPerSecond = Math.max(0, (Number(progress.bytes) - state.startBytes) / elapsedSeconds);
      if (bytesPerSecond > 0) speedText = ` · ${formatBytes(bytesPerSecond)}/s`;
    }
    const pctText = pct > 0 && phase !== 'complete' ? ` ${pct}%` : '';
    label.textContent = `${androidEmulatorProgressLabel(phase)}${pctText}${speedText}`;
  };
  const resetAndroidEmulatorProgress = (panel) => {
    if (!panel) return;
    androidEmulatorProgressState.delete(panel);
    const box = panel.querySelector('[data-android-emulator-progress]');
    const bar = panel.querySelector('[data-android-emulator-progress-bar]');
    const label = panel.querySelector('[data-android-emulator-progress-label]');
    if (box) box.hidden = true;
    if (bar) bar.style.width = '0%';
    if (label) label.textContent = '0%';
  };
  const readAndroidEmulatorProgress = async (res, panel) => {
    if (!res.body || !window.TextDecoder) {
      const text = (await res.text()).trim();
      if (text) {
        text.split(/\n+/).forEach((line) => {
          if (!line.trim()) return;
          const progress = JSON.parse(line);
          if (progress.phase === 'error') throw new Error(progress.error || 'Failed to download emulator assets.');
          setAndroidEmulatorProgress(panel, progress);
        });
      }
      return;
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    for (;;) {
      const { done, value } = await reader.read();
      buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
      const lines = buffer.split('\n');
      buffer = lines.pop() || '';
      for (const line of lines) {
        if (!line.trim()) continue;
        const progress = JSON.parse(line);
        if (progress.phase === 'error') throw new Error(progress.error || 'Failed to download emulator assets.');
        setAndroidEmulatorProgress(panel, progress);
      }
      if (done) break;
    }
    if (buffer.trim()) {
      const progress = JSON.parse(buffer);
      if (progress.phase === 'error') throw new Error(progress.error || 'Failed to download emulator assets.');
      setAndroidEmulatorProgress(panel, progress);
    }
  };
  const populateSelect = (select, options, placeholder) => {
    if (!select) return;
    const previous = select.value;
    select.innerHTML = '';
    if (placeholder) {
      const empty = document.createElement('option');
      empty.value = '';
      empty.textContent = placeholder;
      select.appendChild(empty);
    }
    options.forEach((option) => {
      const node = document.createElement('option');
      node.value = option.identifier || option.path || option.name || '';
      node.textContent = option.label || option.name || option.path || '';
      if (option.path) node.dataset.path = option.path;
      if (option.name) node.dataset.name = option.name;
      select.appendChild(node);
    });
    if (previous) select.value = previous;
    if (!select.value && select.options.length > 0) select.selectedIndex = placeholder && options.length > 0 ? 1 : 0;
  };
  const refreshIOSSimulatorPanel = async (root) => {
    const panel = root.querySelector('[data-ios-simulator-panel]');
    if (!panel) return;
    const selectedType = fieldValue(root, 'CREDIMI_RUNNER_TYPE');
    const visible = selectedType === 'ios_simulator';
    setPanelVisible(panel, visible);
    if (!visible) return;

    const name = fieldValue(root, 'BASE_NAME').trim();
    const message = panel.querySelector('[data-ios-simulator-message]');
    const selects = panel.querySelector('[data-ios-simulator-selects]');
    const create = panel.querySelector('[data-ios-simulator-create]');
    panel.dataset.exists = '0';
    if (selects) selects.hidden = true;
    if (create) create.hidden = true;

    if (!name) {
      setCallout(message, 'warn', 'Simulator name is required before provisioning can be checked.');
      return;
    }

    try {
      const res = await fetch(`/devices/ios-simulator/status?name=${encodeURIComponent(name)}`);
      if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
      const data = await res.json();
      if (!data.supported) {
        setCallout(message, 'warn', 'xcrun simctl is not available on this machine.');
        return;
      }
      if (data.exists) {
        panel.dataset.exists = '1';
        setCallout(message, 'info', `Simulator ${name} already exists and can be used.`);
        root.dispatchEvent(new CustomEvent('dashboard:device-ready-change', { bubbles: true }));
        return;
      }
      populateSelect(panel.querySelector('[data-ios-simulator-device-type]'), data.device_types || [], 'Select a device type');
      populateSelect(panel.querySelector('[data-ios-simulator-runtime]'), data.runtimes || [], 'Select a runtime');
      if (selects) selects.hidden = false;
      if (create) create.hidden = false;
      setCallout(message, 'warn', `No simulator named ${name} exists yet. Choose a device type and runtime to create it.`);
    } catch (error) {
      setCallout(message, 'danger', error && error.message ? error.message : 'Failed to load simulator status.');
    } finally {
      root.dispatchEvent(new CustomEvent('dashboard:device-ready-change', { bubbles: true }));
    }
  };
  const refreshAndroidEmulatorAssetsPanel = async (root) => {
    const panel = root.querySelector('[data-android-emulator-assets-panel]');
    if (!panel) return;
    const selectedType = fieldValue(root, 'CREDIMI_RUNNER_TYPE');
    const visible = selectedType === 'android_emulator';
    setPanelVisible(panel, visible);
    if (!visible) return;

    const message = panel.querySelector('[data-android-emulator-assets-message]');
    const avdControls = panel.querySelector('[data-android-emulator-avd-controls]');
    const avdField = panel.querySelector('[data-android-emulator-avd-field]');
    const goldenField = panel.querySelector('[data-android-emulator-golden-field]');
    const applyAVD = panel.querySelector('[data-android-emulator-apply-avd]');
    const applyGolden = panel.querySelector('[data-android-emulator-apply-golden]');
    const download = panel.querySelector('[data-android-emulator-download]');
    panel.dataset.ready = '0';
    panel.dataset.checking = '1';
    if (avdControls) avdControls.hidden = true;
    if (avdField) avdField.hidden = true;
    if (goldenField) goldenField.hidden = true;
    if (applyAVD) applyAVD.hidden = true;
    if (applyGolden) applyGolden.hidden = true;
    if (download) download.hidden = true;

    try {
      const query = new URLSearchParams({
        base_name: fieldValue(root, 'BASE_NAME'),
        avd_home: fieldValue(root, 'HOST_AVD_HOME_PATH'),
        golden_root: fieldValue(root, 'HOST_AVD_GOLDEN_PATH'),
        golden_path: fieldValue(root, 'GOLDEN_PATH'),
      });
      const res = await fetch(`/devices/android-emulator/assets/status?${query.toString()}`);
      if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
      const data = await res.json();
      const avdReady = data.avd_present === true;
      const goldenReady = data.golden_present === true;
      const avdOptions = data.avd_options || [];
      const goldenOptions = data.golden_options || [];
      panel.dataset.ready = avdReady && goldenReady ? '1' : '0';
      if (avdReady && goldenReady) {
        setCallout(message, 'info', `Emulator assets are present for ${data.base_name || fieldValue(root, 'BASE_NAME')}.`);
        root.dispatchEvent(new CustomEvent('dashboard:device-ready-change', { bubbles: true }));
        return;
      }
      populateSelect(panel.querySelector('[data-android-emulator-avd-select]'), avdOptions, 'Select an existing AVD');
      populateSelect(panel.querySelector('[data-android-emulator-golden-select]'), goldenOptions, 'Select a golden image folder');
      const showAVDChoice = !avdReady && avdOptions.length > 0;
      const showGoldenChoice = !goldenReady && goldenOptions.length > 0;
      if (avdControls) avdControls.hidden = !(showAVDChoice || showGoldenChoice);
      if (avdField) avdField.hidden = !showAVDChoice;
      if (goldenField) goldenField.hidden = !showGoldenChoice;
      if (applyAVD) applyAVD.hidden = !showAVDChoice;
      if (applyGolden) applyGolden.hidden = !showGoldenChoice;
      if (download) download.hidden = false;
      const missing = [];
      if (!avdReady) missing.push('base AVD');
      if (!goldenReady) missing.push(`golden image ${data.golden_leaf || ''}`.trim());
      setCallout(message, 'warn', `${missing.join(' and ')} missing. Choose an existing asset or download Credimi assets.`);
    } catch (error) {
      setCallout(message, 'danger', error && error.message ? error.message : 'Failed to load emulator asset status.');
    } finally {
      delete panel.dataset.checking;
      root.dispatchEvent(new CustomEvent('dashboard:device-ready-change', { bubbles: true }));
    }
  };
  const deriveHomeDefaults = (root) => {
    const androidKeysDir = fieldValue(root, 'ANDROID_KEYS_DIR');
    if (!androidKeysDir.endsWith('/.android')) return {};
    const homeDir = androidKeysDir.slice(0, -'/.android'.length);
    if (!homeDir) return {};
    return {
      androidKeysDir,
      hostAVDHomePath: androidKeysDir + '/avd',
      hostAVDGoldenPath: homeDir + '/avd-golden',
    };
  };
  const applyRunnerTypeDefaults = (root, type) => {
    const derived = deriveHomeDefaults(root);
    switch (type) {
      case 'android_emulator':
        setFieldValue(root, 'RUNNER_IMAGE', TYPE_DEFAULTS.emulatorImage);
        setFieldValue(root, 'CREDIMI_RUNNER_DEVICE_MODE', '');
        setFieldValue(root, 'CREDIMI_RUNNER_SERIAL', '');
        setFieldValue(root, 'CREDIMI_RUNNER_WIFI_IP', '');
        setFieldValue(root, 'CREDIMI_RUNNER_WIFI_PORT', '');
        setFieldValue(root, 'BASE_NAME', TYPE_DEFAULTS.baseName);
        if (derived.androidKeysDir) setFieldValue(root, 'ANDROID_KEYS_DIR', derived.androidKeysDir);
        if (derived.hostAVDHomePath) setFieldValue(root, 'HOST_AVD_HOME_PATH', derived.hostAVDHomePath);
        if (derived.hostAVDGoldenPath) setFieldValue(root, 'HOST_AVD_GOLDEN_PATH', derived.hostAVDGoldenPath);
        setFieldValue(root, 'GOLDEN_PATH', TYPE_DEFAULTS.goldenPath);
        setFieldValue(root, 'REDROID_DATA_DIR', '');
        setFieldValue(root, 'REDROID_DATA_TAR', '');
        setFieldValue(root, 'AVDCTL_SSH_TARGET', '');
        setFieldValue(root, 'AVDCTL_SSH_PASSWORD', '');
        setFieldValue(root, 'AVDCTL_SSH_KNOWN_HOSTS_PATH', '');
        setFieldValue(root, 'AVDCTL_SUDO', '');
        setFieldValue(root, 'AVDCTL_SUDO_PASSWORD', '');
        break;
      case 'ios_simulator':
        setFieldValue(root, 'RUNNER_IMAGE', TYPE_DEFAULTS.phoneImage);
        setFieldValue(root, 'CREDIMI_RUNNER_DEVICE_MODE', '');
        setFieldValue(root, 'CREDIMI_RUNNER_SERIAL', '');
        setFieldValue(root, 'CREDIMI_RUNNER_WIFI_IP', '');
        setFieldValue(root, 'CREDIMI_RUNNER_WIFI_PORT', '');
        setFieldValue(root, 'BASE_NAME', TYPE_DEFAULTS.baseName);
        setFieldValue(root, 'HOST_AVD_HOME_PATH', '');
        setFieldValue(root, 'HOST_AVD_GOLDEN_PATH', '');
        setFieldValue(root, 'GOLDEN_PATH', '');
        setFieldValue(root, 'REDROID_DATA_DIR', '');
        setFieldValue(root, 'REDROID_DATA_TAR', '');
        setFieldValue(root, 'AVDCTL_SSH_TARGET', '');
        setFieldValue(root, 'AVDCTL_SSH_PASSWORD', '');
        setFieldValue(root, 'AVDCTL_SSH_KNOWN_HOSTS_PATH', '');
        setFieldValue(root, 'AVDCTL_SUDO', '');
        setFieldValue(root, 'AVDCTL_SUDO_PASSWORD', '');
        break;
      case 'redroid':
        setFieldValue(root, 'RUNNER_IMAGE', TYPE_DEFAULTS.phoneImage);
        setFieldValue(root, 'CREDIMI_RUNNER_DEVICE_MODE', 'no_device');
        setFieldValue(root, 'CREDIMI_RUNNER_SERIAL', '');
        setFieldValue(root, 'CREDIMI_RUNNER_WIFI_IP', '');
        setFieldValue(root, 'CREDIMI_RUNNER_WIFI_PORT', '');
        setFieldValue(root, 'BASE_NAME', '');
        setFieldValue(root, 'HOST_AVD_HOME_PATH', '');
        setFieldValue(root, 'HOST_AVD_GOLDEN_PATH', '');
        setFieldValue(root, 'GOLDEN_PATH', '');
        setFieldValue(root, 'REDROID_DATA_DIR', TYPE_DEFAULTS.redroidDataDir);
        setFieldValue(root, 'REDROID_DATA_TAR', TYPE_DEFAULTS.redroidDataTar);
        break;
      default:
        setFieldValue(root, 'RUNNER_IMAGE', TYPE_DEFAULTS.phoneImage);
        setFieldValue(root, 'CREDIMI_RUNNER_DEVICE_MODE', 'usb');
        setFieldValue(root, 'CREDIMI_RUNNER_SERIAL', '');
        setFieldValue(root, 'CREDIMI_RUNNER_WIFI_IP', '');
        setFieldValue(root, 'CREDIMI_RUNNER_WIFI_PORT', TYPE_DEFAULTS.wifiPort);
        setFieldValue(root, 'BASE_NAME', '');
        setFieldValue(root, 'HOST_AVD_HOME_PATH', '');
        setFieldValue(root, 'HOST_AVD_GOLDEN_PATH', '');
        setFieldValue(root, 'GOLDEN_PATH', '');
        setFieldValue(root, 'REDROID_DATA_DIR', '');
        setFieldValue(root, 'REDROID_DATA_TAR', '');
        setFieldValue(root, 'AVDCTL_SSH_TARGET', '');
        setFieldValue(root, 'AVDCTL_SSH_PASSWORD', '');
        setFieldValue(root, 'AVDCTL_SSH_KNOWN_HOSTS_PATH', '');
        setFieldValue(root, 'AVDCTL_SUDO', '');
        setFieldValue(root, 'AVDCTL_SUDO_PASSWORD', '');
        break;
    }
  };
  const applyNormalizedPreview = async (root) => {
    const res = await fetch('/config/normalize', {
      method: 'POST',
      body: formParams(root),
    });
    if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
    const data = await res.json();
    const values = data && data.values ? data.values : {};
    TYPE_PREVIEW_KEYS.forEach((key) => {
      if (Object.prototype.hasOwnProperty.call(values, key)) setFieldValue(root, key, values[key] || '');
    });
  };
  const updateDeviceFields = (root = document) => {
    const type = fieldValue(root, 'CREDIMI_RUNNER_TYPE');
    const mode = fieldValue(root, 'CREDIMI_RUNNER_DEVICE_MODE');
    root.querySelectorAll('[data-dev-type]').forEach(el => {
      const types = (el.dataset.devType || '').split(/\s+/);
      setPanelVisible(el, types.includes(type));
    });
    root.querySelectorAll('[data-dev-mode]').forEach(el => {
      const modes = (el.dataset.devMode || '').split(/\s+/);
      const parent = el.closest('[data-dev-type]');
      const parentHidden = parent && parent.style.display === 'none';
      setPanelVisible(el, !parentHidden && modes.includes(mode));
    });
    root.querySelectorAll('[data-dev-pick]').forEach(p => {
      p.classList.toggle('on', p.dataset.devPick === type);
    });
    refreshIOSSimulatorPanel(root);
    refreshAndroidEmulatorAssetsPanel(root);
  };
  document.addEventListener('change', (e) => {
    const select = e.target.closest('[data-android-phone-device-select]');
    if (!select) return;
    const root = select.closest('form') || document;
    setFieldValue(root, 'CREDIMI_RUNNER_SERIAL', select.value || '');
    root.dispatchEvent(new CustomEvent('dashboard:device-ready-change', { bubbles: true }));
  });
  document.addEventListener('click', (e) => {
    const pick = e.target.closest('[data-dev-pick]');
    if (!pick) return;
    const root = pick.closest('form') || document;
    const radio = pick.querySelector('input[type="radio"]');
    if (radio) {
      radio.checked = true;
      updateDeviceFields(root);
      markDirty();
      const finish = () => {
        updateDeviceFields(root);
        markDirty();
      };
      applyNormalizedPreview(root).then(finish).catch(() => {
        applyRunnerTypeDefaults(root, radio.value);
        finish();
      });
    }
  });
  document.addEventListener('change', (e) => {
    if (e.target.name === 'CREDIMI_RUNNER_DEVICE_MODE' || e.target.name === 'BASE_NAME' || e.target.name === 'HOST_AVD_HOME_PATH' || e.target.name === 'HOST_AVD_GOLDEN_PATH') {
      updateDeviceFields(e.target.closest('form') || document);
    }
  });
  document.addEventListener('dashboard:step-shown', (e) => {
    if (e.detail && e.detail.step === 'device') updateDeviceFields(e.target.closest('form') || document);
  });
  document.addEventListener('click', async (e) => {
    const create = e.target.closest('[data-ios-simulator-create]');
    if (create) {
      const root = create.closest('form') || document;
      const panel = create.closest('[data-ios-simulator-panel]');
      const message = panel && panel.querySelector('[data-ios-simulator-message]');
      const deviceType = panel && panel.querySelector('[data-ios-simulator-device-type]');
      const runtime = panel && panel.querySelector('[data-ios-simulator-runtime]');
      create.disabled = true;
      try {
        const res = await fetch('/devices/ios-simulator/create', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            name: fieldValue(root, 'BASE_NAME'),
            device_type_identifier: deviceType ? deviceType.value : '',
            runtime_identifier: runtime ? runtime.value : '',
          }),
        });
        if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
        setCallout(message, 'info', 'Simulator created. Refreshing status.');
        await refreshIOSSimulatorPanel(root);
      } catch (error) {
        setCallout(message, 'danger', error && error.message ? error.message : 'Failed to create simulator.');
      } finally {
        create.disabled = false;
      }
      return;
    }

    const applyAVD = e.target.closest('[data-android-emulator-apply-avd]');
    if (applyAVD) {
      const root = applyAVD.closest('form') || document;
      const panel = applyAVD.closest('[data-android-emulator-assets-panel]');
      const select = panel && panel.querySelector('[data-android-emulator-avd-select]');
      const option = select && select.selectedOptions[0];
      if (option && option.value) {
        setFieldValue(root, 'BASE_NAME', option.dataset.name || option.textContent.trim());
        await refreshAndroidEmulatorAssetsPanel(root);
      }
      return;
    }

    const applyGolden = e.target.closest('[data-android-emulator-apply-golden]');
    if (applyGolden) {
      const root = applyGolden.closest('form') || document;
      const panel = applyGolden.closest('[data-android-emulator-assets-panel]');
      const select = panel && panel.querySelector('[data-android-emulator-golden-select]');
      const option = select && select.selectedOptions[0];
      if (option && option.value) {
        const leaf = option.dataset.name || option.textContent.trim();
        setFieldValue(root, 'GOLDEN_PATH', '/avd-golden/' + leaf);
        await refreshAndroidEmulatorAssetsPanel(root);
      }
      return;
    }

    const download = e.target.closest('[data-android-emulator-download]');
    if (download) {
      const root = download.closest('form') || document;
      const panel = download.closest('[data-android-emulator-assets-panel]');
      const message = panel && panel.querySelector('[data-android-emulator-assets-message]');
      const avdControls = panel && panel.querySelector('[data-android-emulator-avd-controls]');
      const applyAVD = panel && panel.querySelector('[data-android-emulator-apply-avd]');
      const applyGolden = panel && panel.querySelector('[data-android-emulator-apply-golden]');
      download.disabled = true;
      setFieldValue(root, 'BASE_NAME', TYPE_DEFAULTS.baseName);
      setFieldValue(root, 'GOLDEN_PATH', TYPE_DEFAULTS.goldenPath);
      if (avdControls) avdControls.hidden = true;
      if (applyAVD) applyAVD.hidden = true;
      if (applyGolden) applyGolden.hidden = true;
      resetAndroidEmulatorProgress(panel);
      setCallout(message, 'info', 'Downloading and extracting Credimi emulator assets. Keep this page open.');
      try {
        const res = await fetch('/devices/android-emulator/assets/download', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            base_name: fieldValue(root, 'BASE_NAME'),
            avd_home: fieldValue(root, 'HOST_AVD_HOME_PATH'),
            golden_root: fieldValue(root, 'HOST_AVD_GOLDEN_PATH'),
            golden_path: fieldValue(root, 'GOLDEN_PATH'),
          }),
        });
        if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
        await readAndroidEmulatorProgress(res, panel);
        setAndroidEmulatorProgress(panel, { phase: 'complete' });
        setCallout(message, 'info', 'Credimi emulator assets downloaded.');
        await refreshAndroidEmulatorAssetsPanel(root);
      } catch (error) {
        setCallout(message, 'danger', error && error.message ? error.message : 'Failed to download emulator assets.');
      } finally {
        download.disabled = false;
      }
    }
  });
  const initDeviceFields = () => {
    $$('form').forEach((form) => {
      if (form.querySelector('[name="CREDIMI_RUNNER_TYPE"]:checked')) updateDeviceFields(form);
    });
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
    if (hidden) {
      hidden.value = btn.dataset.val;
      hidden.dispatchEvent(new Event('input', { bubbles: true }));
      hidden.dispatchEvent(new Event('change', { bubbles: true }));
      if (key === 'CREDIMI_SERVICE_MODE') syncNetMode(hidden.value);
    }
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
    $$('[data-admin-org-field]').forEach((f) => (f.hidden = mode !== 'admin'));
    $$('[data-identity-fields]').forEach((f) => { if (mode === 'admin') f.style.display = 'contents'; });
    // clear the non-selected key so exactly one is persisted
    const clearKey = mode === 'user' ? seg.dataset.admin : seg.dataset.user;
    const inp = document.querySelector(`[name="${clearKey}"]`); if (inp) inp.value = '';
    markDirty();
  });

  // ── Publishing warning for user-scoped runners ──────────────────────────
  let pendingPublishControl = null;
  let pendingPublishSubmitForm = null;
  const formAuthMode = (form) => {
    const adminField = form && form.querySelector('[data-auth-field="admin"]');
    if (adminField && !adminField.hidden) return 'admin';
    const adminKey = form && form.querySelector('[name="CREDIMI_INTERNAL_ADMIN_KEY"]');
    return adminKey && adminKey.value.trim() ? 'admin' : 'user';
  };
  const publishIsOn = (control) => {
    const box = control && control.querySelector('input[type=checkbox]');
    return !!(box && box.checked);
  };
  const setPublishControl = (control, on) => {
    const tog = control.querySelector('.tog[data-toggle]');
    const box = control.querySelector('input[type=checkbox]');
    if (tog) {
      tog.classList.toggle('on', on);
      tog.setAttribute('aria-checked', on ? 'true' : 'false');
    }
    if (box) box.checked = on;
    control.dataset.publishConfirmed = on ? '1' : '0';
    markDirty();
    syncReview();
  };
  const openPublishWarning = (control) => {
    const modal = document.getElementById('publish-warning-modal');
    if (!modal) return false;
    pendingPublishControl = control;
    modal.hidden = false;
    return true;
  };
  document.addEventListener('click', (e) => {
    const cancel = e.target.closest('[data-publish-warning-cancel]');
    if (cancel) {
      e.preventDefault();
      const modal = document.getElementById('publish-warning-modal');
      if (modal) modal.hidden = true;
      pendingPublishControl = null;
      pendingPublishSubmitForm = null;
      return;
    }
    const confirm = e.target.closest('[data-publish-warning-confirm]');
    if (confirm) {
      e.preventDefault();
      if (pendingPublishControl) setPublishControl(pendingPublishControl, true);
      const submitForm = pendingPublishSubmitForm;
      const modal = document.getElementById('publish-warning-modal');
      if (modal) modal.hidden = true;
      pendingPublishControl = null;
      pendingPublishSubmitForm = null;
      if (submitForm) submitForm.requestSubmit();
    }
  });
  document.addEventListener('click', (e) => {
    const tog = e.target.closest('[data-publish-control] .tog[data-toggle]');
    if (!tog) return;
    const control = tog.closest('[data-publish-control]');
    const form = control.closest('form');
    const initial = String(control.dataset.initial || '').toLowerCase() === 'true';
    const willPublish = !publishIsOn(control);
    const needsWarning = willPublish && !initial && control.dataset.publishConfirmed !== '1' && formAuthMode(form) === 'user';
    if (!needsWarning) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    openPublishWarning(control);
  }, true);
  document.addEventListener('submit', (e) => {
    const form = e.target.closest('[data-config-form]');
    if (!form) return;
    const control = form.querySelector('[data-publish-control]');
    if (!control || !publishIsOn(control)) return;
    const initial = String(control.dataset.initial || '').toLowerCase() === 'true';
    if (initial || control.dataset.publishConfirmed === '1' || formAuthMode(form) !== 'user') return;
    e.preventDefault();
    e.stopImmediatePropagation();
    pendingPublishSubmitForm = form;
    openPublishWarning(control);
  }, true);

  // ── Toggles (.tog with data-toggle → sync hidden checkbox) ────────────────
  document.addEventListener('click', (e) => {
    const tog = e.target.closest('.tog[data-toggle]');
    if (!tog) return;
    tog.classList.toggle('on');
    const on = tog.classList.contains('on');
    tog.setAttribute('aria-checked', on);
    const box = tog.parentElement.querySelector('input[type=checkbox]');
    if (box) box.checked = on;
    const publishControl = tog.closest('[data-publish-control]');
    if (publishControl) {
      publishControl.dataset.publishConfirmed = on ? publishControl.dataset.publishConfirmed : '0';
      syncReview();
    }
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
  document.addEventListener('change', (e) => { if (e.target.closest('[data-config-form]')) markDirty(); });
  document.addEventListener('submit', async (e) => {
    const form = e.target.closest('[data-config-form]');
    if (!form || form.matches('[data-setup-form]')) return;
    if (form.dataset.confirmedSubmit === '1') {
      delete form.dataset.confirmedSubmit;
      return;
    }
    const savebar = $('[data-savebar]', form);
    if (savebar && savebar.hidden) return;
    e.preventDefault();
    const body = new URLSearchParams(new FormData(form));
    let message = 'Save these changes?';
    let confirmRequired = false;
    try {
      const res = await fetch('/config/diff', { method: 'POST', body });
      if (res.ok) {
        const data = await res.json();
        confirmRequired = data.confirm_required === true;
        message = data.message || message;
      }
    } catch (_) {}
    if (confirmRequired) {
      const ok = window.confirm(message);
      if (!ok) return;
      form.dataset.busyActive = '1';
    } else {
      delete form.dataset.busyActive;
    }
    form.dataset.confirmedSubmit = '1';
    form.requestSubmit();
  }, true);
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
      hideBusy();
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
  function warn() { return '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0Z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>'; }
})();
