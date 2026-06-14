(function () {
  const sockets = new Map();

  function post(message, transfer) {
    if (transfer) self.postMessage(message, transfer);
    else self.postMessage(message);
  }

  self.onmessage = event => {
    const msg = event.data || {};
    if (msg.op === 'open') {
      try {
        const ws = new WebSocket(msg.url);
        ws.binaryType = msg.binaryType || 'arraybuffer';
        sockets.set(msg.id, ws);
        ws.onopen = () => post({ id: msg.id, op: 'open' });
        ws.onclose = evt => {
          sockets.delete(msg.id);
          post({ id: msg.id, op: 'close', code: evt.code, reason: evt.reason, wasClean: evt.wasClean });
        };
        ws.onerror = () => post({ id: msg.id, op: 'error' });
        ws.onmessage = evt => {
          if (evt.data instanceof ArrayBuffer) post({ id: msg.id, op: 'message', data: evt.data }, [evt.data]);
          else post({ id: msg.id, op: 'message', data: evt.data });
        };
      } catch (error) {
        post({ id: msg.id, op: 'error', message: error && error.message || String(error) });
      }
      return;
    }

    const ws = sockets.get(msg.id);
    if (!ws) return;
    if (msg.op === 'send') {
      if (ws.readyState === WebSocket.OPEN) ws.send(msg.data);
      return;
    }
    if (msg.op === 'close') {
      ws.close(msg.code || 1000, msg.reason || '');
      sockets.delete(msg.id);
    }
  };
})();
