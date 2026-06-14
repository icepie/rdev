(function () {
  const ESC = '\x1b';
  const START = ESC + '_G';
  const END = ESC + '\\';
  const DEFAULT_MAX_SEQUENCE = 24 * 1024 * 1024;
  const DEFAULT_MAX_STORAGE = 64;

  function parseParams(raw) {
    const params = {};
    if (!raw) return params;
    raw.split(',').forEach(part => {
      const idx = part.indexOf('=');
      if (idx <= 0) return;
      params[part.slice(0, idx)] = part.slice(idx + 1);
    });
    return params;
  }

  function mimeFor(params) {
    if (params.f === '100') return 'image/png';
    if (params.f === '101') return 'image/jpeg';
    if (params.f === '102') return 'image/gif';
    return 'image/png';
  }

  function getCellSize(term) {
    const dims = term?._core?._renderService?.dimensions?.css?.cell;
    if (dims && dims.width && dims.height) return { width: dims.width, height: dims.height };
    const row = term.element?.querySelector('.xterm-rows > div');
    if (row) {
      const rect = row.getBoundingClientRect();
      const cols = Math.max(1, term.cols || 80);
      return { width: rect.width / cols, height: rect.height || 17 };
    }
    return { width: 8, height: 17 };
  }

  function ensureLayer(term) {
    if (!term.element) return null;
    term.element.style.position = term.element.style.position || 'relative';
    let layer = term.element.querySelector('.rdev-kitty-image-layer');
    if (layer) return layer;
    layer = document.createElement('div');
    layer.className = 'rdev-kitty-image-layer';
    layer.style.cssText = 'position:absolute;inset:0;pointer-events:none;overflow:hidden;z-index:4;';
    term.element.appendChild(layer);
    return layer;
  }

  function base64ToBytes(raw) {
    const bin = atob(raw.replace(/\s+/g, ''));
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return bytes;
  }

  function bytesToBase64(bytes) {
    let out = '';
    const chunk = 0x8000;
    for (let i = 0; i < bytes.length; i += chunk) {
      out += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
    }
    return btoa(out);
  }

  async function inflateZlib(bytes) {
    if (!('DecompressionStream' in window)) {
      throw new Error('compressed Kitty images are not supported by this browser');
    }
    const stream = new Blob([bytes]).stream().pipeThrough(new DecompressionStream('deflate'));
    return new Uint8Array(await new Response(stream).arrayBuffer());
  }

  async function payloadToDataURL(params, payload) {
    let bytes = base64ToBytes(payload);
    if (params.o === 'z') bytes = await inflateZlib(bytes);
    return 'data:' + mimeFor(params) + ';base64,' + bytesToBase64(bytes);
  }

  function create(term, options = {}) {
    const maxSequence = options.maxSequence || DEFAULT_MAX_SEQUENCE;
    const maxStorage = options.maxStorage || DEFAULT_MAX_STORAGE;
    const decoder = new TextDecoder();
    let pending = '';
    let chunks = new Map();
    let rendered = [];

    function writePlain(text) {
      if (text) term.write(text);
    }

    function trimRendered() {
      while (rendered.length > maxStorage) {
        const node = rendered.shift();
        if (node && node.parentNode) node.parentNode.removeChild(node);
      }
    }

    function render(params, payload) {
      if (!payload || params.t === 'f' || params.t === 't') {
        writePlain('\r\n\x1b[33m[RDev: kitty image path mode is not supported yet]\x1b[0m\r\n');
        return;
      }
      const layer = ensureLayer(term);
      if (!layer) return;
      const cell = getCellSize(term);
      const cursorX = term.buffer?.active?.cursorX || 0;
      const cursorY = term.buffer?.active?.cursorY || 0;
      const img = document.createElement('img');
      img.decoding = 'async';
      img.loading = 'eager';
      img.style.cssText = 'position:absolute;object-fit:contain;image-rendering:auto;';
      img.style.left = Math.max(0, cursorX * cell.width) + 'px';
      img.style.top = Math.max(0, cursorY * cell.height) + 'px';
      if (params.c) img.style.width = Math.max(1, parseInt(params.c, 10)) * cell.width + 'px';
      else if (params.s) img.style.width = Math.max(1, parseInt(params.s, 10)) + 'px';
      if (params.r) img.style.height = Math.max(1, parseInt(params.r, 10)) * cell.height + 'px';
      else if (params.v) img.style.height = Math.max(1, parseInt(params.v, 10)) + 'px';
      payloadToDataURL(params, payload)
        .then(src => { img.src = src; })
        .catch(err => {
          if (img.parentNode) img.parentNode.removeChild(img);
          writePlain('\r\n\x1b[31m[RDev: kitty image decode failed: ' + err.message + ']\x1b[0m\r\n');
        });
      layer.appendChild(img);
      rendered.push(img);
      trimRendered();
      if (params.r) writePlain('\n'.repeat(Math.max(1, parseInt(params.r, 10))));
    }

    function handleSequence(body) {
      const sep = body.indexOf(';');
      const params = parseParams(sep >= 0 ? body.slice(0, sep) : body);
      const payload = sep >= 0 ? body.slice(sep + 1) : '';
      const key = params.i || params.I || '_default';
      if (params.a && params.a !== 'T' && params.a !== 't') return;
      if (params.m === '1') {
        chunks.set(key, (chunks.get(key) || '') + payload);
        return;
      }
      const fullPayload = (chunks.get(key) || '') + payload;
      chunks.delete(key);
      render(params, fullPayload);
    }

    function processText(text) {
      pending += text;
      while (pending) {
        const start = pending.indexOf(START);
        if (start < 0) {
          const keep = pending.endsWith(ESC) || pending.endsWith(ESC + '_') ? pending.length - (pending.endsWith(ESC) ? 1 : 2) : pending.length;
          writePlain(pending.slice(0, keep));
          pending = pending.slice(keep);
          return;
        }
        writePlain(pending.slice(0, start));
        const sequence = pending.slice(start + START.length);
        const end = sequence.indexOf(END);
        if (end < 0) {
          pending = pending.slice(start);
          if (pending.length > maxSequence) {
            writePlain('\r\n\x1b[31m[RDev: kitty image sequence too large]\x1b[0m\r\n');
            pending = '';
          }
          return;
        }
        const body = sequence.slice(0, end);
        pending = sequence.slice(end + END.length);
        handleSequence(body);
      }
    }

    return {
      write(data) {
        if (typeof data === 'string') {
          processText(data);
        } else if (ArrayBuffer.isView(data)) {
          processText(decoder.decode(new Uint8Array(data.buffer, data.byteOffset, data.byteLength), { stream: true }));
        } else if (data instanceof ArrayBuffer || Object.prototype.toString.call(data) === '[object ArrayBuffer]') {
          processText(decoder.decode(new Uint8Array(data), { stream: true }));
        } else {
          processText(String(data || ''));
        }
      },
      flush() {
        writePlain(pending + decoder.decode());
        pending = '';
      }
    };
  }

  window.RDevTerminalImages = { create, parseParams, mimeFor, payloadToDataURL };
})();
