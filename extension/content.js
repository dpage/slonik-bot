// Runs in the Meet tab, in the isolated world beside transcriber.js and
// observer.js. It builds the transcriber, answers the popup, and holds a port
// open to the service worker whilst transcribing so that the worker can stop
// the session if the tab goes away.
(() => {
  if (window !== window.top) return;

  // The content script cannot read its tab id, so a random one, kept for the
  // life of the tab, tells the server that a second start is the same tab.
  let tab = sessionStorage.getItem('slonikTab');
  if (!tab) {
    tab = Math.random().toString(36).slice(2);
    sessionStorage.setItem('slonikTab', tab);
  }

  const request = async (method, path, body) => {
    try {
      const res = await chrome.runtime.sendMessage({ type: 'request', method, path, body });
      if (res) return res;
      return { status: 0, body: { error: 'No answer from the extension' } };
    } catch (e) {
      return { status: 0, body: { error: String(e.message || e) } };
    }
  };

  let current = null; // the transcriber
  let starting = false;
  let port = null;
  let portID = '';
  let watch = null;

  // observer.js reports captions here.
  window.__slonikCaption = (json) => {
    if (current) current.onCaption(json);
  };

  const sessionID = (s) => s.sessionId || '';

  const closePort = () => {
    clearInterval(watch);
    watch = null;
    if (port) {
      port.postMessage({ done: true });
      port.disconnect();
    }
    port = null;
    portID = '';
    chrome.runtime.sendMessage({ type: 'state', transcribing: false }).catch(() => {});
  };

  // follow keeps the port and badge in step with the transcriber, which can
  // stop by itself when the call ends, and can change session after a server
  // restart.
  const follow = () => {
    const s = current.status();
    if (!s.transcribing) {
      closePort();
      return;
    }
    if (port && sessionID(s) && sessionID(s) !== portID) {
      portID = sessionID(s);
      port.postMessage({ sessionId: portID });
    }
  };

  const openPort = () => {
    if (port) return;
    port = chrome.runtime.connect({ name: 'transcribing' });
    portID = sessionID(current.status());
    port.postMessage({ sessionId: portID });
    chrome.runtime.sendMessage({ type: 'state', transcribing: true }).catch(() => {});
    watch = setInterval(follow, 1000);
  };

  const start = async () => {
    if (current && (starting || current.status().transcribing)) return current.start();
    const stored = await chrome.storage.local.get(['displayName', 'hideCaptions']);
    current = SlonikTranscriber.create({
      request,
      settings: {
        displayName: stored.displayName || '',
        hideCaptions: stored.hideCaptions !== false,
      },
      tab,
      code: location.pathname.replace(/^\/|\/$/g, ''),
      now: () => Date.now(),
    });
    starting = true;
    let result;
    try {
      result = await current.start();
    } finally {
      starting = false;
    }
    if (result.ok) openPort();
    return result;
  };

  chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
    if (msg.type === 'start') {
      start().then(sendResponse);
      return true;
    }
    if (msg.type === 'stop') {
      if (!current) {
        sendResponse({ error: 'Not transcribing' });
        return false;
      }
      current.stop().then((result) => {
        closePort();
        sendResponse(result);
      });
      return true;
    }
    if (msg.type === 'status') {
      if (current) follow();
      sendResponse({
        ...(current ? current.status() : { transcribing: false, queued: 0, chatError: '', lastError: '' }),
        starting,
        inCall: /^\/[a-z]{3}-[a-z]{4}-[a-z]{3}\/?$/.test(location.pathname),
      });
      return false;
    }
    return false;
  });
})();
