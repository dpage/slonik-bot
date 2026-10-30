// Watches Meet's captions region and reports each caption block's speaker and
// text to Go through the __slonikCaption binding. Block ids are assigned here,
// per DOM node, because Meet's own elements carry no stable id.
(() => {
  if (window.__slonikObserver) return;
  window.__slonikObserver = true;

  const AVATAR = 'img[data-iml], img[src*="googleusercontent.com"]';
  const ids = new WeakMap();
  const last = new Map(); // id -> {node, speaker, text}
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

  // Runs inside the MutationObserver callback rather than on a timer, because
  // Chrome throttles timers in background tabs.
  const scan = () => {
    const seen = new Set();
    if (region && region.isConnected) {
      for (const b of blocksIn(region)) {
        let id = ids.get(b);
        if (!id) { id = nextId++; ids.set(b, id); }
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
      if (!seen.has(id) || !v.node.isConnected) {
        last.delete(id);
        send({ block: id, removed: true });
      }
    }
  };

  // Meet replaces the region when captions are toggled or the layout changes,
  // so look for it again every two seconds.
  const attach = () => {
    const r = findRegion();
    if (r === region) return;
    if (observer) observer.disconnect();
    region = r;
    if (r) {
      observer = new MutationObserver(scan);
      observer.observe(r, { childList: true, subtree: true, characterData: true });
    }
    scan();
  };

  attach();
  setInterval(attach, 2000);
})();
