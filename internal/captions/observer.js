// Watches Meet's captions region and reports each caption block's speaker and
// text to Go through the __slonikCaption binding. Block ids are assigned here,
// per DOM node, because Meet's own elements carry no stable id.
(() => {
  // Chrome runs scripts for new documents in every frame; captions are only
  // in the top one.
  if (window !== window.top) return;
  if (window.__slonikObserver) return;
  window.__slonikObserver = true;

  const AVATAR = 'img[data-iml], img[src*="googleusercontent.com"]';
  const ids = new WeakMap();
  const last = new Map(); // id -> {node, speaker, text}
  const gone = new WeakSet(); // blocks whose removal has been reported
  let nextId = 1;
  let region = null;
  let observer = null;

  const send = (ev) => window.__slonikCaption(JSON.stringify(ev));
  const clean = (s) => (s || '').replace(/\s+/g, ' ').trim();

  const findRegion = () =>
    document.querySelector('[role="region"][aria-label="Captions"]') ||
    document.querySelector('div[role="region"][tabindex="0"]');

  // Meet's class names first; failing that, a block is a child of the region
  // that holds a speaker avatar.
  const blocksIn = (r) => {
    const byClass = r.querySelectorAll('.nMcdL');
    if (byClass.length) return Array.from(byClass);
    const found = new Set();
    for (const img of r.querySelectorAll(AVATAR)) {
      let n = img;
      while (n.parentElement && n.parentElement !== r) n = n.parentElement;
      if (n.parentElement === r) found.add(n);
    }
    return Array.from(found);
  };

  const speakerOf = (b) => {
    const el = b.querySelector('.NWpY1d') || b.querySelector('span');
    return clean(el && el.textContent);
  };

  const textOf = (b) => {
    const el = b.querySelector('.ygicle');
    if (el) return clean(el.textContent);
    const divs = Array.from(b.children).filter((c) => c.tagName === 'DIV' && !c.querySelector(AVATAR));
    return divs.length ? clean(divs[divs.length - 1].textContent) : '';
  };

  const idOf = (b) => {
    let id = ids.get(b);
    if (!id) { id = nextId++; ids.set(b, id); }
    return id;
  };

  // A block that has left the page keeps its final text, which may never have
  // been reported: Meet can change a block and remove it, or add and remove
  // it, within one task, and the observer only runs after the task. So report
  // that text, then the removal.
  const finish = (id, b) => {
    if (gone.has(b)) return;
    gone.add(b);
    const prev = last.get(id);
    const speaker = speakerOf(b);
    const text = textOf(b);
    if (text && (!prev || prev.speaker !== speaker || prev.text !== text)) {
      send({ block: id, speaker, text });
    } else if (!prev) {
      return;
    }
    last.delete(id);
    send({ block: id, removed: true });
  };

  // The caption blocks among a mutation's removed nodes, including blocks
  // that left before anything saw them.
  const removedBlocks = (rec) => {
    const found = new Set();
    for (const n of rec.removedNodes) {
      if (n.nodeType !== Node.ELEMENT_NODE || n.isConnected) continue;
      // A wrapper around Meet's classed blocks is not a block itself, even
      // though it holds their avatars.
      if (ids.has(n) || n.matches('.nMcdL')) {
        found.add(n);
      } else if (n.querySelector('.nMcdL')) {
        for (const b of blocksIn(n)) found.add(b);
      } else if (rec.target === region && n.querySelector(AVATAR)) {
        found.add(n);
      } else {
        for (const b of blocksIn(n)) found.add(b);
      }
    }
    return found;
  };

  // Runs inside the MutationObserver callback rather than on a timer, because
  // Chrome throttles timers in background tabs.
  const scan = (records) => {
    for (const rec of records || []) {
      for (const b of removedBlocks(rec)) finish(idOf(b), b);
    }
    // A region that has just left the page is still read for the final state
    // of its blocks; the loop below then reports them removed, and gone keeps
    // later scans from reporting them again.
    const seen = new Set();
    if (region) {
      for (const b of blocksIn(region)) {
        if (gone.has(b)) {
          if (!b.isConnected) continue;
          gone.delete(b); // put back on the page
        }
        const id = idOf(b);
        seen.add(id);
        const speaker = speakerOf(b);
        const text = textOf(b);
        const prev = last.get(id);
        if (text && (!prev || prev.speaker !== speaker || prev.text !== text)) {
          last.set(id, { node: b, speaker, text });
          send({ block: id, speaker, text });
        }
      }
    }
    for (const [id, v] of last) {
      if (!seen.has(id) || !v.node.isConnected) finish(id, v.node);
    }
  };

  // Meet replaces the region when captions are toggled or the layout changes,
  // and a new document has none at first, so look for it again whenever the
  // page changes without a connected region, and every two seconds as a
  // backstop.
  const attach = () => {
    const r = findRegion();
    if (r === region) return;
    if (observer) {
      // Changes the old observer has not delivered yet would be lost.
      scan(observer.takeRecords());
      observer.disconnect();
      observer = null;
    }
    region = r;
    if (r) {
      observer = new MutationObserver(scan);
      observer.observe(r, { childList: true, subtree: true, characterData: true });
    }
    scan();
  };

  attach();
  // The document itself, not documentElement, which may not exist yet when
  // Chrome runs this at the start of a new document.
  new MutationObserver(() => {
    if (region && region.isConnected) return;
    attach();
  }).observe(document, { childList: true, subtree: true });
  setInterval(attach, 2000);
})();
