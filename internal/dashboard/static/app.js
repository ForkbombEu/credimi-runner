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
  function busyOverlay() { return $('#busy-overlay'); }
  function showBusy(message) {
    const overlay = busyOverlay();
    if (!overlay) return;
    const messageNode = $('[data-busy-message]', overlay);
    if (messageNode && message) messageNode.textContent = message;
    overlay.hidden = false;
    document.body.classList.add('busy-lock');
  }
  function hideBusy() {
    const overlay = busyOverlay();
    if (!overlay) return;
    overlay.hidden = true;
    document.body.classList.remove('busy-lock');
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
    showBusy(message);
  });
  document.body.addEventListener('htmx:afterRequest', (e) => {
    const trigger = busyTriggerForElement(e.detail.elt);
    const wasBusy = !!trigger;
    if (trigger && trigger.matches('[data-config-form]')) delete trigger.dataset.busyActive;
    if (wasBusy) hideBusy();
  });
  document.body.addEventListener('htmx:responseError', hideBusy);
  document.body.addEventListener('htmx:sendError', hideBusy);

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
            if (form.dataset.runnerConflictPending === '1' && form.dataset.runnerConflictTouched !== '1') return false;
            return true;
          case 'network': {
            const mode = value('CREDIMI_SERVICE_MODE');
            if (mode === 'manual') return !valueMissing('RUNNER_PUBLIC_URL');
            if (mode === 'cloudflare-managed') return !valueMissing('RUNNER_DOMAIN') && !valueMissing('CLOUDFLARE_TUNNEL_TOKEN');
            return true;
          }
          case 'device': {
            const runnerType = value('CREDIMI_RUNNER_TYPE');
            if (runnerType === 'android_phone' && value('CREDIMI_RUNNER_DEVICE_MODE') === 'wifi') {
              return !valueMissing('CREDIMI_RUNNER_WIFI_IP');
            }
            if (runnerType === 'redroid') {
              return !valueMissing('REDROID_DATA_DIR') && !valueMissing('REDROID_DATA_TAR');
            }
            if (runnerType === 'android_emulator' || runnerType === 'ios_simulator') {
              return !valueMissing('BASE_NAME');
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
            if (form.dataset.runnerConflictPending === '1' && form.dataset.runnerConflictTouched !== '1') {
              return 'Choose whether to update the existing runner or create a new one.';
            }
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
            if (runnerType === 'android_phone' && value('CREDIMI_RUNNER_DEVICE_MODE') === 'wifi' && valueMissing('CREDIMI_RUNNER_WIFI_IP')) {
              return 'Wi-Fi mode requires an Android Wi-Fi IP.';
            }
            if (runnerType === 'redroid' && valueMissing('REDROID_DATA_DIR')) return 'Redroid data directory is required.';
            if (runnerType === 'redroid' && valueMissing('REDROID_DATA_TAR')) return 'Redroid data archive is required.';
            if ((runnerType === 'android_emulator' || runnerType === 'ios_simulator') && valueMissing('BASE_NAME')) {
              return 'Base name is required.';
            }
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
      const value = (name) => (field(name) || {}).value || '';
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
        const conflict = $('[data-runner-conflict]', form);
        const actionInput = $('[data-runner-conflict-action]', form);
        const message = $('[data-runner-conflict-message]', form);
        if (!conflict || !actionInput) return;
        const action = actionInput.value || 'update';
        conflict.style.display = preview && preview.conflict ? '' : 'none';
        if (!preview || !preview.conflict) {
          delete form.dataset.runnerConflictPending;
          delete form.dataset.runnerConflictTouched;
          syncStepActions();
          return;
        }
        form.dataset.runnerConflictPending = '1';
        if (message) message.textContent = `Runner ${preview.base_runner_id} already exists.`;
        $$('[data-runner-conflict-choice]', conflict).forEach((btn) => {
          btn.classList.toggle('on', btn.dataset.runnerConflictChoice === action);
        });
        syncStepActions();
      };
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
          if (actionInput && form.dataset.runnerConflictTouched !== '1') {
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
        delete form.dataset.runnerConflictTouched;
        canonifyName();
        syncStepActions();
      });
      form.addEventListener('click', (e) => {
        const choice = e.target.closest('[data-runner-conflict-choice]');
        if (!choice) return;
        const actionInput = $('[data-runner-conflict-action]', form);
        if (actionInput) actionInput.value = choice.dataset.runnerConflictChoice;
        form.dataset.runnerConflictTouched = '1';
        $$('[data-runner-conflict-choice]', form).forEach((btn) => btn.classList.toggle('on', btn === choice));
        previewRunnerID();
      });
      const beforeNext = async () => {
        const panel = panels[current];
        if (!panel) return;
        if (panel.dataset.step === 'identity') {
          await resolveOrganization();
          await canonifyName();
          const preview = await previewRunnerID();
          if (preview && preview.conflict && form.dataset.runnerConflictTouched !== '1') {
            setError('Choose whether to update the existing runner or create a new one.');
            return false;
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
  const setSectionVisible = (el, visible) => {
    el.style.display = visible ? '' : 'none';
    el.querySelectorAll('input, select, textarea').forEach(control => {
      control.disabled = !visible;
    });
  };
  const fieldValue = (root, name) => {
    const radio = root.querySelector(`[name="${name}"]:checked`);
    if (radio) return radio.value || '';
    const input = root.querySelector(`[name="${name}"]`);
    return input ? (input.value || '') : '';
  };
  const setFieldValue = (root, name, value) => {
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
    return params;
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
      setSectionVisible(el, types.includes(type));
    });
    root.querySelectorAll('[data-dev-mode]').forEach(el => {
      const modes = (el.dataset.devMode || '').split(/\s+/);
      const parent = el.closest('[data-dev-type]');
      const parentHidden = parent && parent.style.display === 'none';
      setSectionVisible(el, !parentHidden && modes.includes(mode));
    });
    root.querySelectorAll('[data-dev-pick]').forEach(p => {
      p.classList.toggle('on', p.dataset.devPick === type);
    });
  };
  document.addEventListener('click', (e) => {
    const pick = e.target.closest('[data-dev-pick]');
    if (!pick) return;
    const root = pick.closest('form') || document;
    const radio = pick.querySelector('input[type="radio"]');
    if (radio) {
      radio.checked = true;
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
    if (e.target.name === 'CREDIMI_RUNNER_DEVICE_MODE') updateDeviceFields(e.target.closest('form') || document);
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
})();
