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

  // codeOf returns the call's code from a Meet path, /abc-defg-hij or
  // /meet/abc-defg-hij, or '' for any other page.
  const codeOf = (path) => {
    const m = /^\/(?:meet\/)?([a-z]{3}-[a-z]{4}-[a-z]{3})\/?$/.exec(path);
    return m ? m[1] : '';
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
    let chatErrorUnsent = false; // chatError is still to reach the server's log
    let lastError = '';
    let retryMs = retryBase;
    let flushTimer = null;
    let heartbeat = null;
    let endWatch = null;
    let inFlight = null; // the POST of the queue under way, if any
    let batchSize = MAX_BATCH; // halved after each 413, restored after a 200
    let starting = null; // the start() under way, if any
    let stopRequested = false; // stop() was called whilst start() was under way
    let stopResult = null; // what that stop() returns

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
        if (res.status !== 200) return { error: errorOf(res), status: res.status };
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

    // Meet's "Captions and translation" button opens a dialog holding the
    // "Turn on captions" button; this clicks it, then closes the dialog again.
    const turnOnCaptions = async () => {
      if (captionsAreOn()) return true;
      let button = document.querySelector(sel.selCaptionsOn);
      const menu = button ? null : document.querySelector(sel.selCaptionsMenu);
      if (menu) {
        menu.click();
        await waitFor(() => (button = document.querySelector(sel.selCaptionsOn)) !== null || captionsAreOn() || stopRequested, CAPTIONS_WAIT_MS);
      }
      if (button && !captionsAreOn()) button.click();
      const on = await waitFor(() => captionsAreOn() || stopRequested, CAPTIONS_WAIT_MS);
      if (menu && document.querySelector(sel.selCaptionsDialog)) menu.click();
      return on;
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
      const sent = await waitFor(() => input.value === '' || stopRequested, CHAT_WAIT_MS);
      // Stopped meanwhile: leave no notice behind for someone to send later.
      if (stopRequested && input.value === text) input.value = '';
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

    // Leaving the call for Meet's home page changes the address without
    // loading a new page, so the extension is still running. Otherwise the
    // whole page is read only once Meet's in-call controls (the captions
    // controls and Leave) have gone, as in meet.Status: during the call a
    // caption saying "you left the meeting" would otherwise end the recording.
    const callOver = () => {
      if (codeOf(location.pathname) !== deps.code) return true;
      if (!document.body) return false;
      if ([sel.selCaptionsOn, sel.selCaptionsOff, sel.selCaptionsMenu, sel.selLeave].some((s) => document.querySelector(s))) return false;
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

    // dropFront gives up the first n queued events, and renumbers the rest so
    // that the server sees no gap: it applies nothing past a missing seq.
    // None of them has been applied, since every one is above acked.
    const dropFront = (n) => {
      queue = queue.slice(n);
      for (const ev of queue) ev.seq -= n;
      lastSeq -= n;
    };

    // send makes one POST of the queue.
    const send = async () => {
      const batch = queue.slice(0, batchSize);
      const body = { events: batch, participants: participants() };
      if (chatErrorUnsent) body.chat_error = chatError;
      let res;
      try {
        res = await deps.request('POST', `/v1/sessions/${session.id}/events`, body);
      } catch (e) {
        failed(`Could not reach slonik-bot serve: ${e.message || e}`);
        return;
      }
      if (res.status === 200) {
        acked = res.body.acked;
        if (body.chat_error) chatErrorUnsent = false;
        queue = queue.filter((ev) => ev.seq > acked);
        lastError = '';
        retryMs = retryBase;
        batchSize = MAX_BATCH;
        if (queue.length) scheduleFlush(FLUSH_DELAY_MS);
      } else if (res.status === 413) {
        // Too big for the server's cap: send less, and give up on an event
        // that is too big by itself.
        if (batch.length > 1) {
          batchSize = Math.max(1, Math.floor(batch.length / 2));
          lastError = errorOf(res);
        } else {
          dropFront(1);
          lastError = 'A caption was too large for slonik-bot serve and was left out';
        }
        scheduleFlush(0);
      } else if (res.status === 400) {
        // Sending the same events again would fail the same way.
        dropFront(batch.length);
        lastError = `slonik-bot serve refused ${batch.length} caption events: ${errorOf(res)}`;
        if (queue.length) scheduleFlush(0);
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
        lastError = `Could not read a caption from the page: ${e.message || e}`;
        return;
      }
      if (ev.speaker === 'You' && settings.displayName) ev.speaker = settings.displayName;
      ev.seq = ++lastSeq;
      ev.at = deps.now();
      queue.push(ev);
      scheduleFlush(FLUSH_DELAY_MS);
    };

    // endSession sends what is queued and any unsent chat error (one try),
    // stops the session and puts the page back as it was.
    const endSession = async () => {
      transcribing = false;
      clearTimeout(flushTimer);
      clearInterval(heartbeat);
      clearInterval(endWatch);
      flushTimer = heartbeat = endWatch = null;
      if (inFlight) await inFlight;
      if (queue.length || chatErrorUnsent) await flush();
      let result = await stopSession();
      if (result.status === 404) {
        // The server restarted since the last POST that reached it.
        result = {
          error: queue.length
            ? `slonik-bot serve no longer has this session, probably because it restarted; ${queue.length} caption events were not saved`
            : 'slonik-bot serve no longer has this session, probably because it restarted',
        };
      } else {
        delete result.status;
      }
      if (result.error) lastError = result.error;
      session = null;
      queue = [];
      chatErrorUnsent = false;
      return result;
    };

    // A stop() whilst start() is under way has start end the session it
    // opened, and returns what that gave.
    const stop = async () => {
      if (starting) {
        stopRequested = true;
        transcribing = false;
        stopResult = { error: 'Not transcribing' };
        await starting;
        return stopResult;
      }
      if (!transcribing) return { error: 'Not transcribing' };
      return endSession();
    };

    // run is start's work. After each wait it checks whether stop() was
    // called meanwhile, and if so ends the session rather than going on.
    const run = async () => {
      stopRequested = false;
      const stopped = async () => {
        stopResult = await endSession();
        return { ok: false, error: 'Stopped' };
      };
      if (!(await openSession())) return { ok: false, error: lastError };
      if (stopRequested) return stopped();
      lastSeq = acked;
      const on = await turnOnCaptions();
      if (stopRequested) return stopped();
      if (!on) {
        await stopSession();
        session = null;
        lastError = 'Could not turn on captions; press c in Meet and try again';
        if (stopRequested) stopResult = { error: lastError };
        return { ok: false, error: lastError };
      }
      transcribing = true;
      chatError = notice ? await postChat(notice) : '';
      chatErrorUnsent = chatError !== '';
      if (stopRequested) return stopped();
      heartbeat = setInterval(flush, heartbeatMs);
      endWatch = setInterval(() => {
        if (transcribing && callOver()) stop();
      }, END_CHECK_MS);
      const result = { ok: true, dir: session.dir };
      if (chatError) result.chatError = chatError;
      return result;
    };

    // A second start() whilst one is under way gets the same promise.
    const start = () => {
      if (starting) return starting;
      if (transcribing) return Promise.resolve({ ok: true, dir: session.dir });
      starting = run().finally(() => {
        starting = null;
      });
      return starting;
    };

    const status = () => ({ transcribing, sessionId: session ? session.id : '', queued: queue.length, acked, chatError, lastError });

    return { start, stop, status, onCaption };
  }

  return { create, codeOf };
})();
