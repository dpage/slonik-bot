// Runs in the Meet page: turns captions on, posts the chat notice, and sends
// the caption events observer.js reports to slonik-bot serve. It uses no
// chrome.* API; the content script passes in request, which carries the
// token, so that this file can be tested in a plain page. Needs selectors.js.
globalThis.SlonikTranscriber = (() => {
  const MAX_BATCH = 200; // events per POST
  const FLUSH_DELAY_MS = 250; // gathers a burst of caption updates into one POST
  const MAX_RETRY_MS = 30000;
  const CAPTIONS_WAIT_MS = 5000;
  const CHAT_WAIT_MS = 3000;
  const END_CHECK_MS = 2000;
  const POLL_MS = 100;

  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

  // waitFor polls cond until it is true or ms pass, and reports which.
  const waitFor = async (cond, ms) => {
    const end = Date.now() + ms;
    for (;;) {
      if (cond()) return true;
      if (Date.now() >= end) return false;
      await sleep(POLL_MS);
    }
  };

  const errorOf = (res) => (res.body && res.body.error) || `HTTP ${res.status}`;

  function create(deps) {
    const sel = globalThis.SlonikSelectors;
    const settings = deps.settings || {};
    const heartbeatMs = deps.heartbeatMs || 15000;
    const retryBase = deps.retryMs || 1000;

    let session = null; // {id, dir} from POST /v1/sessions
    let notice = '';
    let transcribing = false;
    let queue = []; // events not yet acknowledged, in seq order
    let lastSeq = 0;
    let acked = 0;
    let chatError = '';
    let lastError = '';
    let retryMs = retryBase;
    let flushTimer = null;
    let heartbeat = null;
    let endWatch = null;
    let inFlight = null; // the POST of the queue under way, if any

    // openSession creates the session, or rejoins this tab's open one.
    const openSession = async () => {
      let res;
      try {
        res = await deps.request('POST', '/v1/sessions', { code: deps.code, tab: deps.tab });
      } catch (e) {
        lastError = `Could not reach slonik-bot serve: ${e.message || e}`;
        return false;
      }
      if (res.status !== 200 && res.status !== 201) {
        lastError = errorOf(res);
        return false;
      }
      session = { id: res.body.id, dir: res.body.dir };
      notice = res.body.chat_notice || '';
      acked = res.body.acked || 0;
      return true;
    };

    const stopSession = async () => {
      try {
        const res = await deps.request('POST', `/v1/sessions/${session.id}/stop`, {});
        if (res.status !== 200) return { error: errorOf(res) };
        return { dir: res.body.dir };
      } catch (e) {
        return { error: `Could not reach slonik-bot serve: ${e.message || e}` };
      }
    };

    // ---- the page ----

    const captionsAreOn = () => {
      if (document.querySelector(sel.selCaptionsOff)) return true;
      const menu = document.querySelector(sel.selCaptionsMenu);
      return menu !== null && menu.getAttribute('aria-pressed') === 'true';
    };

    const turnOnCaptions = async () => {
      if (captionsAreOn()) return true;
      const button = document.querySelector(sel.selCaptionsOn) || document.querySelector(sel.selCaptionsMenu);
      if (button) button.click();
      return waitFor(captionsAreOn, CAPTIONS_WAIT_MS);
    };

    // Hiding the region rather than removing it keeps the caption nodes in
    // the DOM, where observer.js reads them.
    const hideCaptions = () => {
      if (document.getElementById('slonik-hide')) return;
      const style = document.createElement('style');
      style.id = 'slonik-hide';
      style.textContent = `${sel.selCaptionsRegion} { opacity: 0 !important; pointer-events: none !important; }`;
      (document.head || document.documentElement).appendChild(style);
    };

    const showCaptions = () => {
      const style = document.getElementById('slonik-hide');
      if (style) style.remove();
    };

    // postChat sends text to the meeting chat, as meet.SendChat does, and
    // returns an error message, or '' once the chat box has emptied.
    const postChat = async (text) => {
      let input = document.querySelector(sel.selChatInput);
      if (!input) {
        const button = document.querySelector(sel.selChatButton);
        if (!button) return 'Could not find the chat button';
        button.click();
        await waitFor(() => (input = document.querySelector(sel.selChatInput)) !== null, CHAT_WAIT_MS);
        if (!input) return 'The chat did not open';
      }
      input.focus();
      input.value = text;
      input.dispatchEvent(new Event('input', { bubbles: true }));
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', keyCode: 13, which: 13, bubbles: true }));
      const sent = await waitFor(() => input.value === '', CHAT_WAIT_MS);
      // An open chat panel narrows the captions area.
      const button = document.querySelector(sel.selChatButton);
      if (button && document.querySelector(sel.selChatInput)) button.click();
      return sent ? '' : 'The chat message was not sent';
    };

    const participants = () => {
      const names = new Set();
      for (const el of document.querySelectorAll(sel.selParticipantMenus)) {
        const label = el.getAttribute('aria-label') || '';
        if (label.startsWith(sel.participantMenuPrefix)) {
          const name = label.slice(sel.participantMenuPrefix.length).trim();
          if (name) names.add(name);
        }
      }
      if (settings.displayName) names.add(settings.displayName);
      return Array.from(names);
    };

    // The whole page is read only once Meet's captions control has gone, as
    // in meet.Status: during the call a caption saying "you left the
    // meeting" would otherwise end the recording.
    const callOver = () => {
      if (!document.body) return false;
      if ([sel.selCaptionsOn, sel.selCaptionsOff, sel.selCaptionsMenu].some((s) => document.querySelector(s))) return false;
      const text = document.body.innerText.replace(/\u2019/g, "'");
      return sel.textsEnded.concat(sel.textsRemoved).some((t) => text.includes(t));
    };

    // ---- sending ----

    const scheduleFlush = (delay) => {
      if (!transcribing || flushTimer) return;
      flushTimer = setTimeout(() => {
        flushTimer = null;
        flush();
      }, delay);
    };

    // failed records a failed POST and tries again after a delay that
    // doubles each time, up to MAX_RETRY_MS.
    const failed = (message) => {
      lastError = message;
      if (!transcribing) return;
      if (flushTimer) clearTimeout(flushTimer);
      flushTimer = null;
      scheduleFlush(retryMs);
      retryMs = Math.min(retryMs * 2, MAX_RETRY_MS);
    };

    // renew follows a server restart: the old session has gone, so the queue
    // goes to a new one, numbered from where that session starts.
    const renew = async () => {
      if (!(await openSession())) {
        failed(lastError);
        return;
      }
      queue.forEach((ev, i) => {
        ev.seq = acked + i + 1;
      });
      lastSeq = acked + queue.length;
      scheduleFlush(0);
    };

    // send makes one POST of the queue.
    const send = async () => {
      const batch = queue.slice(0, MAX_BATCH);
      let res;
      try {
        res = await deps.request('POST', `/v1/sessions/${session.id}/events`, { events: batch, participants: participants() });
      } catch (e) {
        failed(`Could not reach slonik-bot serve: ${e.message || e}`);
        return;
      }
      if (res.status === 200) {
        acked = res.body.acked;
        queue = queue.filter((ev) => ev.seq > acked);
        lastError = '';
        retryMs = retryBase;
        if (queue.length) scheduleFlush(FLUSH_DELAY_MS);
      } else if (res.status === 404 && transcribing) {
        await renew();
      } else {
        failed(errorOf(res));
      }
    };

    const flush = () => {
      if (!inFlight) inFlight = send().finally(() => { inFlight = null; });
      return inFlight;
    };

    // ---- the interface ----

    const onCaption = (json) => {
      if (!transcribing) return;
      let ev;
      try {
        ev = JSON.parse(json);
      } catch (e) {
        return;
      }
      if (ev.speaker === 'You' && settings.displayName) ev.speaker = settings.displayName;
      ev.seq = ++lastSeq;
      ev.at = deps.now();
      queue.push(ev);
      scheduleFlush(FLUSH_DELAY_MS);
    };

    const stop = async () => {
      if (!transcribing) return { error: 'Not transcribing' };
      transcribing = false;
      clearTimeout(flushTimer);
      clearInterval(heartbeat);
      clearInterval(endWatch);
      flushTimer = heartbeat = endWatch = null;
      if (inFlight) await inFlight;
      if (queue.length) await flush();
      const result = await stopSession();
      if (result.error) lastError = result.error;
      showCaptions();
      session = null;
      queue = [];
      return result;
    };

    const start = async () => {
      if (transcribing) return { ok: true, dir: session.dir };
      if (!(await openSession())) return { ok: false, error: lastError };
      lastSeq = acked;
      if (!(await turnOnCaptions())) {
        await stopSession();
        session = null;
        lastError = 'Could not turn on captions; press c in Meet and try again';
        return { ok: false, error: lastError };
      }
      if (settings.hideCaptions) hideCaptions();
      transcribing = true;
      chatError = notice ? await postChat(notice) : '';
      heartbeat = setInterval(flush, heartbeatMs);
      endWatch = setInterval(() => {
        if (transcribing && callOver()) stop();
      }, END_CHECK_MS);
      const result = { ok: true, dir: session.dir };
      if (chatError) result.chatError = chatError;
      return result;
    };

    const status = () => ({ transcribing, queued: queue.length, acked, chatError, lastError });

    return { start, stop, status, onCaption };
  }

  return { create };
})();
