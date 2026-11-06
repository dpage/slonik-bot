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
  let active = false; // the port and badge are in use
  let port = null;
  let portID = '';
  let watch = null;

  // observer.js reports captions here.
  window.__slonikCaption = (json) => {
    if (current) current.onCaption(json);
  };

  const sessionID = (s) => s.sessionId || '';
  const tell = (msg) => chrome.runtime.sendMessage(msg).catch(() => {});

  // A port to a stopped service worker throws on use, so none of these calls
  // may be allowed to stop what follows them.
  const post = (msg) => {
    try {
      port.postMessage(msg);
    } catch (e) {
      // The port's onDisconnect reconnects.
    }
  };

  const closePort = () => {
    clearInterval(watch);
    watch = null;
    if (port) {
      post({ done: true });
      try {
        port.disconnect();
      } catch (e) {
        // Already gone.
      }
    }
    port = null;
    portID = '';
    if (active) {
      active = false;
      tell({ type: 'state', transcribing: false });
    }
  };

  // connect opens the port and tells the service worker the session id and
  // the badge state. Chrome stops the worker when it has been idle, which
  // closes the port; a new worker then needs telling again.
  const connect = () => {
    let p;
    try {
      p = chrome.runtime.connect({ name: 'transcribing' });
    } catch (e) {
      return; // follow tries again
    }
    port = p;
    p.onDisconnect.addListener(() => {
      if (port !== p) return;
      port = null;
      portID = '';
      if (active && current && current.status().transcribing) connect();
    });
    portID = sessionID(current.status());
    post({ sessionId: portID });
    tell({ type: 'state', transcribing: true });
  };

  // follow keeps the port and badge in step with the transcriber, which can
  // stop by itself when the call ends, and can change session after a server
  // restart.
  const follow = () => {
    const s = current.status();
    if (!s.transcribing) {
      if (active) closePort();
      return;
    }
    if (!active) return;
    if (!port) {
      connect();
    } else if (sessionID(s) && sessionID(s) !== portID) {
      portID = sessionID(s);
      post({ sessionId: portID });
    }
  };

  const openPort = () => {
    if (active) return;
    active = true;
    connect();
    watch = setInterval(follow, 1000);
  };

  const meetingCode = () => SlonikTranscriber.codeOf(location.pathname);

  const start = async () => {
    if (current && (starting || current.status().transcribing)) return current.start();
    const stored = await chrome.storage.local.get(['displayName']);
    current = SlonikTranscriber.create({
      request,
      settings: {
        displayName: stored.displayName || '',
      },
      tab,
      code: meetingCode(),
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
      start().then(sendResponse, (e) => sendResponse({ ok: false, error: String(e.message || e) }));
      return true;
    }
    if (msg.type === 'stop') {
      if (!current) {
        sendResponse({ error: 'Not transcribing' });
        return false;
      }
      current.stop().then(
        (result) => {
          closePort();
          sendResponse(result);
        },
        (e) => {
          closePort();
          sendResponse({ error: String(e.message || e) });
        },
      );
      return true;
    }
    if (msg.type === 'status') {
      if (current) follow();
      sendResponse({
        ...(current ? current.status() : { transcribing: false, queued: 0, chatError: '', lastError: '' }),
        starting,
        inCall: meetingCode() !== '',
      });
      return false;
    }
    return false;
  });
})();
