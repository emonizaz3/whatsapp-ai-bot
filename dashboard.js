/**
 * WhatsApp AI Bot Dashboard Client Script
 * Newspaper-style Architecture: High-level features first, detail utilities last.
 */

// ============================================================================
// CONSTANTS & STATE
// ============================================================================
const STATUS_API = '/api/status';
const CONFIG_API = '/api/config';
const LOGS_API = '/api/logs';
const LOGS_CLEAR_API = '/api/logs/clear';
const LOGOUT_API = '/api/logout';
const CONNECT_API = '/api/connect';
const CANCEL_API = '/api/connect/cancel';
const POLL_INTERVAL_MS = 2500;

let currentLogs = [];
let currentFilter = 'all';
let isRequesting = false;
let lastQRCode = '';
let activeRepliesState = [];
let countdownIntervalId = null;

// ============================================================================
// INITIALIZATION
// ============================================================================
document.addEventListener('DOMContentLoaded', initializeDashboard);

/**
 * Orchestrates dashboard initialization by loading config, logs, and setting up polls.
 */
function initializeDashboard() {
  setupEventListeners();
  fetchBotConfig();
  fetchBotStatus();
  fetchActivityLogs();

  // Start polling routines
  setInterval(fetchBotStatus, POLL_INTERVAL_MS);
  setInterval(fetchActivityLogs, POLL_INTERVAL_MS);
}

// ============================================================================
// HIGH-LEVEL DATA FETCHING & ORCHESTRATION
// ============================================================================

/**
 * Fetches status state of the WhatsApp client from backend.
 */
async function fetchBotStatus() {
  try {
    const response = await fetch(STATUS_API);
    if (!response.ok) return;
    const data = await response.json();
    updateConnectionStatusUI(data);
  } catch (error) {
    console.error('Error fetching bot status:', error);
  }
}

/**
 * Fetches current bot configurations and populates form values.
 */
async function fetchBotConfig() {
  try {
    const response = await fetch(CONFIG_API);
    if (!response.ok) return;
    const data = await response.json();
    populateConfigForm(data);
  } catch (error) {
    console.error('Error loading config:', error);
  }
}

/**
 * Collects input configurations and updates the bot settings.
 */
async function saveBotConfig() {
  if (isRequesting) return;
  isRequesting = true;
  
  const payload = gatherConfigFromInputs();
  try {
    const response = await fetch(CONFIG_API, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    const result = await response.json();
    if (result.status === 'success') {
      showNotification('Configuration saved successfully!', 'success');
    }
  } catch (error) {
    showNotification('Failed to save configurations.', 'error');
  } finally {
    isRequesting = false;
  }
}

/**
 * Fetches and processes logs.
 */
async function fetchActivityLogs() {
  try {
    const response = await fetch(LOGS_API);
    if (!response.ok) return;
    currentLogs = await response.json();
    updateTargetFilterDropdown();
    renderLogsList();
  } catch (error) {
    console.error('Error loading activity logs:', error);
  }
}

// ============================================================================
// MID-LEVEL UI UPDATE FUNCTIONS
// ============================================================================

/**
 * Handles toggling connection containers depending on WhatsApp status.
 * @param {Object} data - API response holding status info.
 */
function updateConnectionStatusUI(data) {
  const status = data.connection_status ?? 'DISCONNECTED';
  const qrCode = data.qr_code ?? '';
  const phone = data.connected_phone ?? 'Unknown';

  updateStatusHeaderBadge(status);
  
  // Hide all auth state frames
  document.getElementById('auth-loading').classList.add('hidden');
  document.getElementById('auth-qr').classList.add('hidden');
  document.getElementById('auth-connected').classList.add('hidden');
  document.getElementById('auth-disconnected').classList.add('hidden');

  if (status === 'CONNECTED') {
    lastQRCode = '';
    showConnectedState(phone);
    updateActiveCountdowns(data.active_replies ?? []);
    return;
  }
  if (status === 'QR_CODE_READY' && qrCode) {
    showQRCodeState(qrCode);
    return;
  }
  
  lastQRCode = '';
  if (status === 'DISCONNECTED') {
    document.getElementById('auth-disconnected').classList.remove('hidden');
    return;
  }
  
  // Default to connecting/loading state
  document.getElementById('auth-loading').classList.remove('hidden');
}

/**
 * Updates active countdown list with local ticking logic.
 * @param {Array} replies - Array of active reply objects.
 */
function updateActiveCountdowns(replies) {
  const wrapper = document.getElementById('countdown-wrapper');
  const container = document.getElementById('countdown-list');
  
  activeRepliesState = replies;

  if (replies.length === 0) {
    wrapper.classList.add('hidden');
    container.innerHTML = '';
    return;
  }

  wrapper.classList.remove('hidden');
  container.innerHTML = replies
    .map(reply => {
      const remaining = Math.max(0, Math.ceil((new Date(reply.send_at).getTime() - Date.now()) / 1000));
      return `
        <div class="flex items-center justify-between bg-slate-950/70 border border-slate-800/80 rounded-lg px-2 py-1 font-mono text-[9px]">
          <span class="text-indigo-300 font-semibold select-all">${reply.target}</span>
          <span class="text-slate-400 flex items-center gap-1 select-none">
            Wait <span id="timer-${reply.target}" class="font-bold text-amber-400">${remaining}s</span>
          </span>
        </div>
      `;
    })
    .join('');

  startLocalCountdownTicker();
}

/**
 * Updates the filter target list based on targets present in current logs.
 */
function updateTargetFilterDropdown() {
  const dropdown = document.getElementById('target-filter');
  const selectedValue = dropdown.value;

  // Gather unique target numbers from logs
  const targets = [...new Set(currentLogs.map(log => log.target).filter(t => t && t !== ''))];

  // Re-build options
  dropdown.innerHTML = '<option value="all">All Targets</option>';
  targets.forEach(target => {
    const opt = document.createElement('option');
    opt.value = target;
    opt.textContent = target;
    dropdown.appendChild(opt);
  });

  // Preserve previous choice if still available
  if (targets.includes(selectedValue)) {
    dropdown.value = selectedValue;
  }
}

/**
 * Builds dynamic log lines according to filter configurations.
 */
function renderLogsList() {
  const container = document.getElementById('logs-container');
  if (!container) return;

  const targetFilterValue = document.getElementById('target-filter').value;
  
  const filtered = currentLogs.filter(log => {
    const matchesLevel = currentFilter === 'all' || log.level.toLowerCase() === currentFilter;
    const matchesTarget = targetFilterValue === 'all' || log.target === targetFilterValue;
    return matchesLevel && matchesTarget;
  });

  if (filtered.length === 0) {
    container.innerHTML = '<div class="text-slate-600 text-center py-4 font-sans text-xs">No logs found</div>';
    return;
  }

  container.innerHTML = '';
  filtered.map(createLogElement).forEach(el => container.appendChild(el));

  // Auto scroll to bottom
  setTimeout(() => {
    container.scrollTop = container.scrollHeight;
  }, 50);
}

/**
 * Populates configuration input controls from parsed config structure.
 * @param {Object} config - Config payload object.
 */
function populateConfigForm(config) {
  document.getElementById('target-number').value = (config.target_numbers ?? []).join(', ');
  document.getElementById('groq-key').value = config.groq_api_key ?? '';
  document.getElementById('ai-model').value = config.model ?? 'openai/gpt-oss-120b';
  document.getElementById('system-prompt').value = config.system_prompt ?? '';
  document.getElementById('cooldown').value = config.cooldown_minutes ?? 10;
  document.getElementById('daily-limit').value = config.daily_limit ?? 2;
  
  const active = config.bot_active ?? false;
  updateSwitchButtonState('bot-toggle-btn', 'bot-toggle-thumb', active);
  
  const enableDelay = config.enable_delay ?? false;
  updateSwitchButtonState('delay-toggle-btn', 'delay-toggle-thumb', enableDelay);
  toggleDelayInputContainer(enableDelay);

  document.getElementById('delay-min').value = config.delay_min_seconds ?? 60;
  document.getElementById('delay-max').value = config.delay_max_seconds ?? 180;
}

// ============================================================================
// LOW-LEVEL DETAILS, INTERACTIVE BINDINGS & UTILS
// ============================================================================

function setupEventListeners() {
  // Tabs
  setupTabButton('tab-general', 'panel-general');
  setupTabButton('tab-ai', 'panel-ai');
  setupTabButton('tab-timing', 'panel-timing');

  // Interactive buttons
  document.getElementById('save-config-btn').addEventListener('click', saveBotConfig);
  document.getElementById('bot-toggle-btn').addEventListener('click', handleBotActiveToggle);
  document.getElementById('delay-toggle-btn').addEventListener('click', handleDelayToggle);
  document.getElementById('clear-logs-btn').addEventListener('click', clearLogs);
  document.getElementById('logout-btn').addEventListener('click', logoutDevice);
  document.getElementById('connect-btn').addEventListener('click', connectDevice);
  document.getElementById('get-pair-code-btn').addEventListener('click', getPairingCode);
  document.getElementById('cancel-btn').addEventListener('click', cancelPairing);
  document.getElementById('cancel-loading-btn').addEventListener('click', cancelPairing);
  document.getElementById('toggle-key-visibility').addEventListener('click', togglePasswordReveal);

  // Filters
  setupFilterButton('filter-all', 'all');
  setupFilterButton('filter-success', 'success');
  setupFilterButton('filter-info', 'info');
  setupFilterButton('filter-warning', 'warning');
  setupFilterButton('filter-error', 'error');

  // Target Filter change
  document.getElementById('target-filter').addEventListener('change', renderLogsList);
}

/**
 * Local interval loop to count down active reply timers smoothly.
 */
function startLocalCountdownTicker() {
  if (countdownIntervalId) return;
  countdownIntervalId = setInterval(() => {
    if (activeRepliesState.length === 0) {
      clearInterval(countdownIntervalId);
      countdownIntervalId = null;
      document.getElementById('countdown-wrapper').classList.add('hidden');
      return;
    }
    
    // Decrement and filter active timers
    activeRepliesState = activeRepliesState.map(reply => {
      const remaining = Math.max(0, Math.ceil((new Date(reply.send_at).getTime() - Date.now()) / 1000));
      const el = document.getElementById(`timer-${reply.target}`);
      if (el) {
        el.textContent = `${remaining}s`;
      }
      return reply;
    }).filter(reply => {
      const remaining = Math.max(0, Math.ceil((new Date(reply.send_at).getTime() - Date.now()) / 1000));
      return remaining > 0;
    });
  }, 1000);
}

/**
 * Creates dynamic DOM element representing a log entry.
 * @param {Object} log - Log record object containing parameters.
 * @returns {HTMLElement} Log row element.
 */
function createLogElement(log) {
  const el = document.createElement('div');
  el.className = 'py-1.5 border-b border-slate-900 last:border-0 flex flex-wrap md:flex-nowrap items-start gap-2 text-[11px] leading-normal';

  const timeStr = moment(log.timestamp).format('HH:mm:ss');
  const levelBadge = getLogLevelBadge(log.level);
  const targetBadge = log.target 
    ? `<span class="bg-indigo-500/10 border border-indigo-500/25 text-indigo-300 px-1.5 py-0.5 rounded text-[10px] font-semibold shrink-0 select-none">To: ${log.target}</span>` 
    : '';
  
  el.innerHTML = `
    <span class="text-slate-500 font-bold shrink-0 select-none">${timeStr}</span>
    ${levelBadge}
    ${targetBadge}
    <span class="text-slate-400 shrink-0 font-medium select-none">[${log.type}]</span>
    <span class="text-slate-100 select-all font-sans text-xs break-all ml-1 flex-1">${log.message}</span>
  `;
  return el;
}

function showConnectedState(phone) {
  const frame = document.getElementById('auth-connected');
  frame.classList.remove('hidden');
  document.getElementById('connected-phone-display').textContent = phone;
}

function showQRCodeState(qrCode) {
  const frame = document.getElementById('auth-qr');
  frame.classList.remove('hidden');
  
  if (qrCode === lastQRCode) {
    return;
  }
  lastQRCode = qrCode;

  const qrImg = document.getElementById('qr-image');
  if (qrImg) {
    qrImg.src = `https://api.qrserver.com/v1/create-qr-code/?size=200x200&data=${encodeURIComponent(qrCode)}`;
  }
}

function updateStatusHeaderBadge(status) {
  const indicator = document.getElementById('status-indicator');
  const pulse = document.getElementById('status-pulse');
  const text = document.getElementById('status-text');

  text.textContent = status;

  if (status === 'CONNECTED') {
    indicator.className = 'relative inline-flex rounded-full h-2 w-2 bg-emerald-500';
    pulse.className = 'animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-500 opacity-75';
    return;
  }
  if (status === 'QR_CODE_READY') {
    indicator.className = 'relative inline-flex rounded-full h-2 w-2 bg-amber-500';
    pulse.className = 'animate-ping absolute inline-flex h-full w-full rounded-full bg-amber-500 opacity-75';
    return;
  }
  if (status === 'DISCONNECTED') {
    indicator.className = 'relative inline-flex rounded-full h-2 w-2 bg-rose-500';
    pulse.className = 'animate-ping absolute inline-flex h-full w-full rounded-full bg-rose-500 opacity-75';
    return;
  }
  
  // Default CONNECTING or other states
  indicator.className = 'relative inline-flex rounded-full h-2 w-2 bg-indigo-500';
  pulse.className = 'animate-ping absolute inline-flex h-full w-full rounded-full bg-indigo-500 opacity-75';
}

function gatherConfigFromInputs() {
  const targetInput = document.getElementById('target-number').value;
  const parsedTargets = targetInput
    .split(',')
    .map(num => num.trim())
    .filter(num => num !== '');

  return {
    bot_active: isSwitchActive('bot-toggle-btn'),
    target_numbers: parsedTargets,
    groq_api_key: document.getElementById('groq-key').value.trim(),
    model: document.getElementById('ai-model').value,
    system_prompt: document.getElementById('system-prompt').value.trim(),
    cooldown_minutes: parseInt(document.getElementById('cooldown').value || 10, 10),
    daily_limit: parseInt(document.getElementById('daily-limit').value || 0, 10),
    enable_delay: isSwitchActive('delay-toggle-btn'),
    delay_min_seconds: parseInt(document.getElementById('delay-min').value || 60, 10),
    delay_max_seconds: parseInt(document.getElementById('delay-max').value || 180, 10),
  };
}

async function handleBotActiveToggle() {
  const btn = document.getElementById('bot-toggle-btn');
  const active = btn.getAttribute('aria-checked') === 'true';
  const newActive = !active;
  
  updateSwitchButtonState('bot-toggle-btn', 'bot-toggle-thumb', newActive);
  
  // Save configuration change immediately
  await saveBotConfig();
}

function handleDelayToggle() {
  const btn = document.getElementById('delay-toggle-btn');
  const active = btn.getAttribute('aria-checked') === 'true';
  const newActive = !active;
  
  updateSwitchButtonState('delay-toggle-btn', 'delay-toggle-thumb', newActive);
  toggleDelayInputContainer(newActive);
}

function toggleDelayInputContainer(enable) {
  const container = document.getElementById('delay-inputs-container');
  if (enable) {
    container.classList.remove('hidden');
    return;
  }
  container.classList.add('hidden');
}

async function clearLogs() {
  if (!confirm('Are you sure you want to clear all logs?')) return;
  try {
    const response = await fetch(LOGS_CLEAR_API, { method: 'POST' });
    const result = await response.json();
    if (result.status === 'success') {
      currentLogs = [];
      updateTargetFilterDropdown();
      renderLogsList();
    }
  } catch (error) {
    showNotification('Failed to clear logs.', 'error');
  }
}

async function logoutDevice() {
  if (!confirm('Logout WhatsApp device? You will need to scan QR again.')) return;
  try {
    const response = await fetch(LOGOUT_API, { method: 'POST' });
    const result = await response.json();
    if (result.status === 'success') {
      showNotification('Successfully logged out.', 'success');
      fetchBotStatus();
    }
  } catch (error) {
    showNotification('Logout failed.', 'error');
  }
}

/**
 * Triggers background connection process for the WhatsApp device.
 */
async function connectDevice() {
  if (isRequesting) return;
  isRequesting = true;
  showNotification('Connecting WhatsApp...', 'info');
  try {
    const response = await fetch(CONNECT_API, { method: 'POST' });
    const result = await response.json();
    if (result.status === 'success' || result.status === 'already_connected') {
      fetchBotStatus();
    }
  } catch (error) {
    showNotification('Failed to start connection process.', 'error');
  } finally {
    isRequesting = false;
  }
}

/**
 * Requests a WhatsApp pairing code for the specified phone number.
 */
async function getPairingCode() {
  if (isRequesting) return;
  
  const phoneInput = document.getElementById('pair-phone-input');
  const phone = phoneInput.value.trim();
  if (!phone) {
    showNotification('Please enter a valid phone number.', 'error');
    return;
  }

  isRequesting = true;
  showNotification('Requesting pairing code...', 'info');
  
  const container = document.getElementById('pair-code-display-container');
  const codeVal = document.getElementById('pair-code-val');
  container.classList.add('hidden');

  try {
    const response = await fetch('/api/connect/code', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ phone })
    });
    
    if (!response.ok) {
      const errData = await response.json();
      showNotification(errData.error || 'Failed to generate code.', 'error');
      return;
    }

    const result = await response.json();
    if (result.status === 'success' && result.code) {
      codeVal.textContent = result.code;
      container.classList.remove('hidden');
      showNotification('Pairing code generated!', 'success');
      fetchBotStatus();
    } else if (result.status === 'already_connected') {
      showNotification('Device is already connected.', 'warning');
      fetchBotStatus();
    }
  } catch (error) {
    showNotification('Failed to connect to backend.', 'error');
  } finally {
    isRequesting = false;
  }
}

/**
 * Cancels any active WhatsApp connection or pairing process.
 */
async function cancelPairing() {
  if (isRequesting) return;
  isRequesting = true;
  showNotification('Cancelling connection...', 'info');
  try {
    const response = await fetch(CANCEL_API, { method: 'POST' });
    const result = await response.json();
    if (result.status === 'success') {
      showNotification('Connection cancelled.', 'success');
      fetchBotStatus();
    }
  } catch (error) {
    showNotification('Failed to cancel connection.', 'error');
  } finally {
    isRequesting = false;
  }
}

function setupTabButton(id, panelId) {
  document.getElementById(id).addEventListener('click', (e) => {
    // Select all tabs
    document.querySelectorAll('.tab-btn').forEach(btn => {
      btn.className = 'tab-btn px-3.5 py-2 text-xs font-medium text-slate-400 hover:text-slate-200 border-b-2 border-transparent -mb-[1px] transition-all';
    });
    // Set active tab
    e.target.className = 'tab-btn px-3.5 py-2 text-xs font-medium text-indigo-400 border-b-2 border-indigo-500 -mb-[1px] transition-all';
    
    // Select all panels
    document.querySelectorAll('.tab-panel').forEach(panel => panel.classList.add('hidden'));
    // Show active panel
    document.getElementById(panelId).classList.remove('hidden');
  });
}

function setupFilterButton(id, filter) {
  document.getElementById(id).addEventListener('click', (e) => {
    document.querySelectorAll('.filter-btn').forEach(btn => {
      btn.className = 'filter-btn px-2.5 py-1 rounded bg-slate-950/40 border border-slate-800 hover:bg-slate-800 text-slate-400';
    });
    e.target.className = 'filter-btn px-2.5 py-1 rounded bg-slate-800 text-white font-medium';
    currentFilter = filter;
    renderLogsList();
  });
}

/**
 * Standard utility to color-code status codes
 * @param {string} level 
 */
function getLogLevelBadge(level) {
  const lvl = level.toUpperCase();
  if (lvl === 'SUCCESS') {
    return '<span class="bg-emerald-500/10 border border-emerald-500/30 text-emerald-400 text-[9px] px-1.5 py-0.5 rounded font-bold uppercase tracking-wider shrink-0 select-none">SUCCESS</span>';
  }
  if (lvl === 'WARNING') {
    return '<span class="bg-amber-500/10 border border-amber-500/30 text-amber-400 text-[9px] px-1.5 py-0.5 rounded font-bold uppercase tracking-wider shrink-0 select-none font-sans">WARN</span>';
  }
  if (lvl === 'ERROR') {
    return '<span class="bg-rose-500/10 border border-rose-500/30 text-rose-400 text-[9px] px-1.5 py-0.5 rounded font-bold uppercase tracking-wider shrink-0 select-none">ERROR</span>';
  }
  return '<span class="bg-sky-500/10 border border-sky-500/30 text-sky-400 text-[9px] px-1.5 py-0.5 rounded font-bold uppercase tracking-wider shrink-0 select-none">INFO</span>';
}

function updateSwitchButtonState(btnId, thumbId, active) {
  const btn = document.getElementById(btnId);
  const thumb = document.getElementById(thumbId);
  
  if (active) {
    btn.setAttribute('aria-checked', 'true');
    btn.className = btn.className.replace('bg-slate-800', 'bg-indigo-600');
    thumb.className = thumb.className.replace('translate-x-0', 'translate-x-5');
    return;
  }
  btn.setAttribute('aria-checked', 'false');
  btn.className = btn.className.replace('bg-indigo-600', 'bg-slate-800');
  thumb.className = thumb.className.replace('translate-x-5', 'translate-x-0');
}

function isSwitchActive(btnId) {
  return document.getElementById(btnId).getAttribute('aria-checked') === 'true';
}

function togglePasswordReveal() {
  const input = document.getElementById('groq-key');
  const icon = document.querySelector('#toggle-key-visibility mat-icon');
  
  if (input.type === 'password') {
    input.type = 'text';
    icon.textContent = 'visibility_off';
    return;
  }
  input.type = 'password';
  icon.textContent = 'visibility';
}

function showNotification(message, type = 'success') {
  // Simple clean snackbar notification injected at runtime
  const snackbar = document.createElement('div');
  const isError = type === 'error';
  snackbar.className = `fixed bottom-6 right-6 px-4 py-3 rounded-xl border shadow-xl flex items-center gap-2.5 transition-all duration-300 transform translate-y-12 opacity-0 z-50 text-sm font-medium ${
    isError 
      ? 'bg-rose-950/90 text-rose-300 border-rose-800/80 shadow-rose-900/10' 
      : 'bg-emerald-950/90 text-emerald-300 border-emerald-800/80 shadow-emerald-900/10'
  }`;
  
  const icon = isError ? 'error_outline' : 'check_circle_outline';
  snackbar.innerHTML = `
    <mat-icon class="material-icons select-none">${icon}</mat-icon>
    <span>${message}</span>
  `;
  
  document.body.appendChild(snackbar);
  
  // Trigger animations
  setTimeout(() => {
    snackbar.classList.remove('translate-y-12', 'opacity-0');
  }, 10);
  
  // Tear down notification
  setTimeout(() => {
    snackbar.classList.add('translate-y-12', 'opacity-0');
    setTimeout(() => snackbar.remove(), 300);
  }, 3500);
}
