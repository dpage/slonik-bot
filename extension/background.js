// The service worker. It makes every call to slonik-bot serve, because a
// content script's cross-origin fetch carries the page's origin (Chrome 85
// and later), and the server accepts chrome-extension:// origins only. It
// also keeps the badge, and stops a session whose tab goes away.
const DEFAULT_URL = 'http://127.0.0.1:7381';

async function request(method, path, body) {
  const { serverURL, token } = await chrome.storage.local.get(['serverURL', 'token']);
  const base = (serverURL || DEFAULT_URL).replace(/\/+$/, '');
  try {
    const res = await fetch(base + path, {
      method,
      headers: { Authorization: `Bearer ${token || ''}`, 'Content-Type': 'application/json' },
      body: method === 'GET' ? undefined : JSON.stringify(body || {}),
    });
    const text = await res.text();
    try {
      return { status: res.status, body: JSON.parse(text) };
    } catch (e) {
      return { status: res.status, body: { error: res.statusText || text } };
    }
  } catch (e) {
    return { status: 0, body: { error: String(e.message || e) } };
  }
}

function setBadge(tabId, transcribing) {
  chrome.action.setBadgeText({ tabId, text: transcribing ? 'REC' : '' }).catch(() => {});
  if (transcribing) chrome.action.setBadgeBackgroundColor({ tabId, color: '#d93025' }).catch(() => {});
}

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg.type === 'request') {
    request(msg.method, msg.path, msg.body).then(sendResponse);
    return true;
  }
  if (msg.type === 'state' && sender.tab) {
    setBadge(sender.tab.id, msg.transcribing);
    return false;
  }
  return false;
});

chrome.runtime.onConnect.addListener((port) => {
  if (port.name !== 'transcribing' || !port.sender || !port.sender.tab) return;
  // Chrome stops this worker after it has been idle, which closes the port;
  // the page then reconnects and sends the session id again.
  const entry = { tabId: port.sender.tab.id, sessionId: '' };
  // The page sends the session id when it opens the port, and again if the
  // session is replaced after a server restart.
  port.onMessage.addListener((msg) => {
    if (msg.sessionId) entry.sessionId = msg.sessionId;
    // A deliberate stop has already ended the session.
    if (msg.done) entry.sessionId = '';
  });
  port.onDisconnect.addListener(() => {
    if (!entry.sessionId) return;
    // The tab closed or navigated away whilst transcribing. The server's
    // idle timeout is the fallback should this fail.
    request('POST', `/v1/sessions/${entry.sessionId}/stop`, {});
    setBadge(entry.tabId, false);
  });
});
