const toggle = document.getElementById('toggle');
const statusEl = document.getElementById('status');
const warningEl = document.getElementById('warning');
const errorEl = document.getElementById('error');

document.getElementById('options').addEventListener('click', (e) => {
  e.preventDefault();
  chrome.runtime.openOptionsPage();
});

let tabId = null;
let transcribing = false;
let busy = false;

// ask sends a message to the tab's content script. Without the tabs
// permission the popup cannot read the tab's URL, so a missing content script
// (any page but Meet, or a Meet tab opened before the extension was
// installed) is taken to mean that there is no call here.
const ask = async (type) => {
  try {
    return await chrome.tabs.sendMessage(tabId, { type });
  } catch (e) {
    return { unavailable: true };
  }
};

const render = (s) => {
  if (!s) s = { unavailable: true };
  transcribing = Boolean(s.transcribing);
  const away = Boolean(s.unavailable) || !s.inCall;
  toggle.disabled = busy || away;
  toggle.textContent = transcribing ? 'Stop transcribing' : 'Start transcribing';
  if (s.starting) toggle.textContent = 'Starting...';
  if (away) toggle.textContent = 'Open a Meet call';
  statusEl.textContent = away ? '' : transcribing ? `Transcribing (${s.queued || 0} queued)` : 'Not transcribing';
  warningEl.textContent = s.chatError ? `Chat notice not posted: post it yourself (${s.chatError})` : '';
  errorEl.textContent = s.error || s.lastError || '';
};

const refresh = async () => {
  if (tabId === null || busy) return;
  render(await ask('status'));
};

toggle.addEventListener('click', async () => {
  busy = true;
  toggle.disabled = true;
  const r = await ask(transcribing ? 'stop' : 'start');
  busy = false;
  render(await ask('status'));
  if (r.error) errorEl.textContent = r.error;
  if (r.chatError) warningEl.textContent = `Chat notice not posted: post it yourself (${r.chatError})`;
});

(async () => {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (!tab) return;
  tabId = tab.id;
  await refresh();
  setInterval(refresh, 1000);
})();
