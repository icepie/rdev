// @ts-nocheck
document.getElementById('lang-slot').innerHTML = RDevUI.themeButton() + RDevI18n.langSelector();
    RDevI18n.apply();
    const adminToken = new URLSearchParams(location.search).get('token') || localStorage.getItem('rdevAdminToken') || '';
    if (adminToken) localStorage.setItem('rdevAdminToken', adminToken);
    function authURL(path) { return adminToken ? path + (path.includes('?') ? '&' : '?') + 'token=' + encodeURIComponent(adminToken) : path; }
    function authFetch(path, init) { return fetch(authURL(path), init); }
    document.querySelectorAll('[data-auth-link]').forEach(a => a.href = authURL(a.getAttribute('href')));

    const params = new URLSearchParams(location.search);
    const settings = document.getElementById('settings');
    const deviceSelect = document.getElementById('device');
    const passwordInput = document.getElementById('password');
    const modeSelect = document.getElementById('mode');
    const sourceSelect = document.getElementById('source');
    const fpsInput = document.getElementById('fps');
    const qualityInput = document.getElementById('quality');
    const maxWidthInput = document.getElementById('maxWidth');
    const maxHeightInput = document.getElementById('maxHeight');
    const fitModeSelect = document.getElementById('fitMode');
    const inputBackendSelect = document.getElementById('inputBackend');
    const controlInput = document.getElementById('controlInput');
    const showCursorInput = document.getElementById('showCursor');
    const manualSettings = document.getElementById('manual-settings');
    const gpuSourceSelect = document.getElementById('gpuSource');
    const gpuEncoderSelect = document.getElementById('gpuEncoder');
    const gpuRefreshButton = document.getElementById('gpuRefresh');
    const gpuAdvancedToggle = document.getElementById('gpuAdvancedToggle');
    const gpuAdvanced = document.getElementById('gpuAdvanced');
    const gpuEnableVideo = document.getElementById('gpuEnableVideo');
    const gpuLowLatency = document.getElementById('gpuLowLatency');
    const gpuStretch = document.getElementById('gpuStretch');
    const gpuEnableMouse = document.getElementById('gpuEnableMouse');
    const gpuEnablePen = document.getElementById('gpuEnablePen');
    const gpuEnableTouch = document.getElementById('gpuEnableTouch');
    const gpuScaleInput = document.getElementById('gpuScale');
    const gpuScaleOut = document.getElementById('gpuScaleOut');
    const gpuFrameRateInput = document.getElementById('gpuFrameRate');
    const gpuFrameRateOut = document.getElementById('gpuFrameRateOut');
    const gpuPointerBackend = document.getElementById('gpuPointerBackend');
    const gpuKeyboardBackend = document.getElementById('gpuKeyboardBackend');
    const gpuMinPressure = document.getElementById('gpuMinPressure');
    const frameInfo = document.getElementById('frameInfo');
    const statusEl = document.getElementById('status');
    const stage = document.getElementById('stage');
    const canvas = document.getElementById('screen');
    const ctx = canvas.getContext('2d');
    const gpuViewer = document.getElementById('gpuViewer');
    const gpuVideo = document.getElementById('gpuVideo');
    const gpuCanvas = document.getElementById('gpuCanvas');
    const gpuCtx = gpuCanvas.getContext('2d');
    const gpuOverlay = document.getElementById('gpuOverlay');
    const gpuEmpty = document.getElementById('gpuEmpty');
    const empty = document.getElementById('empty');
    const storagePrefix = 'rdevDesktop.';
    const gpuStoragePrefix = 'rdevGpuDesktop.';
    let ws = null, drawing = false, pendingFrame = null, deviceCache = [], lastCloseMessage = '', connectionSeq = 0, connectionMode = '', manualDisconnect = false;
    let frameCount = 0, frameBytes = 0, statsStartedAt = 0, lastFrameAt = 0, currentSource = '', remoteInput = false, resizeTimer = null, reconnectTimer = null, lastMouseSent = 0;
    let gpuReconnectDelay = 1000;
    let gpuMediaSource = null, gpuSourceBuffer = null, gpuQueue = [], gpuLastPointer = new Map(), gpuHeldKeys = new Map();
    let gpuVideoDecoder = null, gpuVideoMode = 'mse', gpuNeedKeyFrame = false;
    let controlPreferenceSet = false;

    function frameRateScale(value) {
        return Math.pow(Number(value) / 100, 1.5);
    }
    function frameRateScaleInv(value) {
        return 100 * Math.pow(Number(value), 2 / 3);
    }
    function isAndroidGpuDevice() {
        const device = deviceCache.find(d => d.id === deviceSelect.value) || {};
        const desktop = device.desktop || {};
        return desktop.platform === 'android' || desktop.displayServer === 'mediaprojection';
    }
    function gpuFrameRate() {
        const maxFps = isAndroidGpuDevice() ? 15 : 120;
        return Math.max(0, Math.min(maxFps, gpuFrameRateInput.valueAsNumber || 0));
    }
    function gpuMaxVideoSize() {
        if (isAndroidGpuDevice()) {
            return { width: 1080, height: 1920 };
        }
        const scale = Math.max(0.1, Math.min(2, gpuScaleInput.valueAsNumber || 1));
        return {
            width: Math.max(320, Math.round(scale * Math.max(1, gpuViewer.clientWidth || window.innerWidth) * (window.devicePixelRatio || 1))),
            height: Math.max(240, Math.round(scale * Math.max(1, gpuViewer.clientHeight || window.innerHeight) * (window.devicePixelRatio || 1))),
        };
    }
    function updateGpuRangeLabels() {
        const size = gpuMaxVideoSize();
        gpuScaleOut.value = `${size.width}×${size.height}`;
        gpuFrameRateOut.value = String(Math.round(gpuFrameRate()));
    }
    function gpuPointerTypes() {
        const types = [];
        if (gpuEnableMouse.checked) types.push('mouse');
        if (gpuEnablePen.checked) types.push('pen');
        if (gpuEnableTouch.checked) types.push('touch');
        return types;
    }
    function getCoalescedPointerEvents(event) {
        return typeof event.getCoalescedEvents === 'function' ? event.getCoalescedEvents() : [event];
    }

    function loadSavedDesktopSettings() {
        const mode = localStorage.getItem(storagePrefix + 'mode');
        const fit = localStorage.getItem(storagePrefix + 'fit');
        if (mode === 'auto' || mode === 'manual') modeSelect.value = mode;
        if (fit === 'fit' || fit === 'actual') fitModeSelect.value = fit;
        sourceSelect.value = localStorage.getItem(storagePrefix + 'source') || sourceSelect.value;
        inputBackendSelect.value = localStorage.getItem(storagePrefix + 'inputBackend') || inputBackendSelect.value;
        fpsInput.value = localStorage.getItem(storagePrefix + 'fps') || fpsInput.value;
        qualityInput.value = localStorage.getItem(storagePrefix + 'quality') || qualityInput.value;
        maxWidthInput.value = localStorage.getItem(storagePrefix + 'width') || maxWidthInput.value;
        maxHeightInput.value = localStorage.getItem(storagePrefix + 'height') || maxHeightInput.value;
        const control = localStorage.getItem(storagePrefix + 'control');
        controlPreferenceSet = control !== null;
        if (control === 'true') controlInput.checked = true;
        if (control === 'false') controlInput.checked = false;
        showCursorInput.checked = localStorage.getItem(storagePrefix + 'showCursor') === 'true';
        gpuSourceSelect.value = localStorage.getItem(gpuStoragePrefix + 'source') || '';
        gpuEncoderSelect.value = localStorage.getItem(gpuStoragePrefix + 'encoder') || 'auto';
        gpuPointerBackend.value = localStorage.getItem(gpuStoragePrefix + 'pointerBackend') || 'auto';
        gpuKeyboardBackend.value = localStorage.getItem(gpuStoragePrefix + 'keyboardBackend') || 'auto';
        gpuEnableVideo.checked = localStorage.getItem(gpuStoragePrefix + 'enableVideo') !== 'false';
        gpuLowLatency.checked = localStorage.getItem(gpuStoragePrefix + 'lowLatency') !== 'false';
        gpuStretch.checked = localStorage.getItem(gpuStoragePrefix + 'stretch') === 'true';
        gpuEnableMouse.checked = localStorage.getItem(gpuStoragePrefix + 'enableMouse') !== 'false';
        gpuEnablePen.checked = localStorage.getItem(gpuStoragePrefix + 'enablePen') !== 'false';
        gpuEnableTouch.checked = localStorage.getItem(gpuStoragePrefix + 'enableTouch') !== 'false';
        gpuScaleInput.value = localStorage.getItem(gpuStoragePrefix + 'scale') || gpuScaleInput.value;
        gpuFrameRateInput.value = localStorage.getItem(gpuStoragePrefix + 'frameRateSlider') || '30';
        gpuMinPressure.value = localStorage.getItem(gpuStoragePrefix + 'minPressure') || gpuMinPressure.value;
        if (localStorage.getItem(gpuStoragePrefix + 'advanced') === 'true') gpuAdvanced.classList.add('open');
        updateGpuRangeLabels();
        stage.dataset.control = String(controlInput.checked);
        stage.dataset.cursor = String(showCursorInput.checked);
    }
    function saveDesktopSettings() {
        if (deviceSelect.value) localStorage.setItem(storagePrefix + 'device', deviceSelect.value);
        localStorage.setItem(storagePrefix + 'mode', modeSelect.value);
        localStorage.setItem(storagePrefix + 'source', sourceSelect.value);
        localStorage.setItem(storagePrefix + 'inputBackend', inputBackendSelect.value);
        localStorage.setItem(storagePrefix + 'fit', fitModeSelect.value);
        localStorage.setItem(storagePrefix + 'control', String(controlInput.checked));
        localStorage.setItem(storagePrefix + 'showCursor', String(showCursorInput.checked));
        localStorage.setItem(storagePrefix + 'fps', String(clampInt(fpsInput.value, 1, 12, 4)));
        localStorage.setItem(storagePrefix + 'quality', String(clampInt(qualityInput.value, 25, 90, 50)));
        localStorage.setItem(storagePrefix + 'width', String(clampInt(maxWidthInput.value, 320, 3840, 1600)));
        localStorage.setItem(storagePrefix + 'height', String(clampInt(maxHeightInput.value, 240, 2160, 1000)));
    }
    function saveGpuDesktopSettings() {
        localStorage.setItem(gpuStoragePrefix + 'source', gpuSourceSelect.value || '');
        localStorage.setItem(gpuStoragePrefix + 'encoder', gpuEncoderSelect.value || 'auto');
        localStorage.setItem(gpuStoragePrefix + 'pointerBackend', gpuPointerBackend.value || 'auto');
        localStorage.setItem(gpuStoragePrefix + 'keyboardBackend', gpuKeyboardBackend.value || 'auto');
        localStorage.setItem(gpuStoragePrefix + 'enableVideo', String(gpuEnableVideo.checked));
        localStorage.setItem(gpuStoragePrefix + 'lowLatency', String(gpuLowLatency.checked));
        localStorage.setItem(gpuStoragePrefix + 'stretch', String(gpuStretch.checked));
        localStorage.setItem(gpuStoragePrefix + 'enableMouse', String(gpuEnableMouse.checked));
        localStorage.setItem(gpuStoragePrefix + 'enablePen', String(gpuEnablePen.checked));
        localStorage.setItem(gpuStoragePrefix + 'enableTouch', String(gpuEnableTouch.checked));
        localStorage.setItem(gpuStoragePrefix + 'scale', gpuScaleInput.value);
        localStorage.setItem(gpuStoragePrefix + 'frameRateSlider', gpuFrameRateInput.value);
        localStorage.setItem(gpuStoragePrefix + 'minPressure', gpuMinPressure.value);
        localStorage.setItem(gpuStoragePrefix + 'advanced', String(gpuAdvanced.classList.contains('open')));
    }
    function updateSourceOptions() {
        const device = deviceCache.find(d => d.id === deviceSelect.value) || {};
        settings.dataset.gpuDevice = String(!!device.gpuDesktop);
        const sources = (device.desktop && device.desktop.sources && device.desktop.sources.length) ? device.desktop.sources : [
            {id:'auto', label:t('desktop.sourceAuto'), kind:'screen'},
            {id:'screen:all', label:t('desktop.sourceAll'), kind:'screen'},
        ];
        const saved = localStorage.getItem(storagePrefix + 'source') || '';
        let current = sourceSelect.value || saved || 'auto';
        if (current === 'virtual' || current === 'screen:virtual' || current === 'screen:root') current = 'screen:all';
        if (current === 'primary') current = 'screen:primary';
        sourceSelect.innerHTML = sources.map(s => `<option value="${esc(s.id)}">${esc(sourceLabel(s))}</option>`).join('');
        sourceSelect.value = sources.some(s => s.id === current) ? current : 'auto';
        updateInputBackendOptions(device.desktop || {});
        saveDesktopSettings();
    }
    function updateInputBackendOptions(desktop) {
        const backends = (desktop.inputOptions && desktop.inputOptions.length) ? desktop.inputOptions : ((desktop.inputBackends || []).map(b => ({id:b, label:inputBackendLabel(b), kinds:[]})));
        const saved = localStorage.getItem(storagePrefix + 'inputBackend') || 'auto';
        const options = [{id:'auto', label:t('desktop.inputAuto')}].concat(backends.map(b => ({id:b.id || b, label:inputBackendLabel(b)})));
        inputBackendSelect.innerHTML = options.map(o => `<option value="${esc(o.id)}">${esc(o.label)}</option>`).join('');
        inputBackendSelect.value = options.some(o => o.id === saved) ? saved : 'auto';
        inputBackendSelect.disabled = backends.length === 0;
    }
    function inputBackendLabel(backend) {
        const id = backend.id || backend;
        const label = backend.label || id;
        const kinds = backend.kinds && backend.kinds.length ? ` · ${backend.kinds.join('/')}` : '';
        const requires = backend.requires && backend.requires.length ? ` · ${backend.requires.join(', ')}` : '';
        if (id === 'x11-xtest') return `${label}${kinds || ' · mouse/keyboard'}${requires}`;
        if (id === 'uinput') return `${label}${kinds || ' · mouse/keyboard/touch/pen'}${requires}`;
        if (id === 'win32') return `${label}${kinds || ' · mouse/keyboard'}${requires}`;
        if (id === 'win32-touch') return `${label}${kinds || ' · mouse/keyboard/touch/pen'}${requires}`;
        return `${label}${kinds}${requires}`;
    }
    function sourceLabel(source) {
        const parts = [source.label || source.id];
        if (source.kind) parts.push(source.kind);
        if (source.backend) parts.push(source.backend);
        if (source.width && source.height) parts.push(`${source.width}×${source.height}${source.x || source.y ? `@${source.x},${source.y}` : ''}`);
        return parts.join(' · ');
    }
    function adaptiveSize() {
        const rect = stage.getBoundingClientRect();
        const ratio = Math.min(window.devicePixelRatio || 1, 2);
        const width = Math.max(320, Math.min(3840, Math.round((rect.width || window.innerWidth || 1600) * ratio)));
        const height = Math.max(240, Math.min(2160, Math.round((rect.height || window.innerHeight || 1000) * ratio)));
        return { width, height };
    }
    function desktopOptions() {
        const manual = modeSelect.value === 'manual';
        const adaptive = adaptiveSize();
        return {
            mode: modeSelect.value,
            source: sourceSelect.value,
            fps: manual ? clampInt(fpsInput.value, 1, 12, 4) : 8,
            quality: manual ? clampInt(qualityInput.value, 25, 90, 50) : 55,
            width: manual ? clampInt(maxWidthInput.value, 320, 3840, 1600) : adaptive.width,
            height: manual ? clampInt(maxHeightInput.value, 240, 2160, 1000) : adaptive.height,
            inputBackend: inputBackendSelect.value || 'auto',
            showCursor: showCursorInput.checked,
        };
    }
    function desktopQuery() {
        const opts = desktopOptions();
        const q = new URLSearchParams({ device: deviceSelect.value, mode: opts.mode, source: opts.source, fps: String(opts.fps), quality: String(opts.quality), width: String(opts.width), height: String(opts.height), inputBackend: opts.inputBackend, showCursor: String(opts.showCursor) });
        return q.toString();
    }
    function authPayload() {
        return Object.assign({ op:'auth', password: passwordInput.value }, desktopOptions());
    }
    async function syncVNCSettings() {
        if (!deviceSelect.value) { setStatus('Select a device first', 'warn'); return; }
        saveDesktopSettings();
        const opts = desktopOptions();
        try {
            const r = await authFetch('/api/vnc/settings', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(Object.assign({device:deviceSelect.value}, opts))});
            const data = await r.json().catch(() => ({}));
            if (!r.ok || !data.ok) throw new Error(data.message || data.error || `HTTP ${r.status}`);
            const suffix = data.closedSessions ? `; reconnect VNC (${data.closedSessions} session reset)` : '';
            setStatus(data.vncAddr ? `VNC settings synced: ${data.vncAddr}${suffix}` : `VNC settings synced${suffix}`, 'ok');
        } catch (e) {
            setStatus(e.message || String(e), 'err');
        }
    }
    function clampInt(value, min, max, fallback) {
        const n = parseInt(value, 10);
        if (!Number.isFinite(n)) return fallback;
        return Math.max(min, Math.min(max, n));
    }
    function updateMode() {
        manualSettings.dataset.disabled = modeSelect.value === 'manual' ? 'false' : 'true';
    }
    function setStatus(text, cls = '') { statusEl.textContent = text; statusEl.className = 'status ' + cls; }
    function resetGPUReconnectBackoff() { gpuReconnectDelay = 1000; }
    function nextGPUReconnectDelay() {
        const base = gpuReconnectDelay;
        gpuReconnectDelay = Math.min(30000, gpuReconnectDelay * 2);
        const spread = Math.max(1, Math.floor(base / 5));
        return base - spread + Math.floor(Math.random() * (spread * 2 + 1));
    }
    function resetFrameStats() {
        frameCount = 0; frameBytes = 0; statsStartedAt = 0; lastFrameAt = 0; currentSource = ''; frameInfo.textContent = '';
    }
    function formatBytes(bytes) {
        if (bytes >= 1024 * 1024) return (bytes / 1024 / 1024).toFixed(1) + ' MB';
        if (bytes >= 1024) return (bytes / 1024).toFixed(1) + ' KB';
        return bytes + ' B';
    }
    function updateFrameInfo() {
        if (!canvas.width || !canvas.height) return;
        const source = currentSource ? ` · ${currentSource}` : '';
        const elapsed = statsStartedAt ? Math.max(0.001, (performance.now() - statsStartedAt) / 1000) : 0;
        const fps = elapsed ? (frameCount / elapsed).toFixed(1) : '0.0';
        const kbps = elapsed ? ((frameBytes * 8 / elapsed) / 1024).toFixed(0) : '0';
        const age = lastFrameAt ? Math.max(0, ((performance.now() - lastFrameAt) / 1000)).toFixed(1) : '0.0';
        frameInfo.textContent = `${canvas.width}×${canvas.height}${source} · ${fps} fps · ${kbps} kbps · ${formatBytes(frameBytes)} · ${age}s`;
    }
    async function loadDevices() {
        const r = await authFetch('/api/devices');
        const devices = await r.json();
        deviceCache = devices;
        const selected = params.get('device') || deviceSelect.value || localStorage.getItem(storagePrefix + 'device');
        deviceSelect.innerHTML = '<option value="">' + t('terminal.devicePlaceholder') + '</option>' + devices.map(d => {
            const desktop = d.desktop || {};
            const desktopState = (d.gpuDesktop || desktop.supported) ? t('index.desktopReady') : (desktop.reason || t('index.desktopUnavailable'));
            const label = `${d.id} · ${desktopState}`;
            return `<option value="${esc(d.id)}" ${d.id === selected ? 'selected' : ''}>${esc(label)}</option>`;
        }).join('');
        if (selected) deviceSelect.value = selected;
        updateSourceOptions();
    }
    function esc(s) { const d = document.createElement('div'); d.textContent = s || ''; return d.innerHTML; }
    function scheduleReconnectIfActive() {
        if (!ws) return;
        if (connectionMode === 'gpu') { gpuSendConfig(); return; }
        clearTimeout(reconnectTimer);
        reconnectTimer = setTimeout(() => connect(), 150);
    }
    function closeGPUVideo() {
        gpuQueue = [];
        gpuSourceBuffer = null;
        if (gpuMediaSource && gpuMediaSource.readyState === 'open') {
            try { gpuMediaSource.endOfStream(); } catch (_) {}
        }
        if (gpuVideoDecoder) {
            try { gpuVideoDecoder.close(); } catch (_) {}
        }
        gpuVideoDecoder = null;
        gpuVideoMode = 'mse';
        gpuNeedKeyFrame = false;
        gpuMediaSource = null;
        if (gpuVideo.src) URL.revokeObjectURL(gpuVideo.src);
        gpuVideo.removeAttribute('src');
        gpuVideo.load();
        gpuCanvas.style.display = 'none';
    }
    function disconnect() {
        manualDisconnect = true;
        connectionSeq++;
        clearTimeout(reconnectTimer);
        if (ws) { ws.close(); ws = null; }
        gpuReleaseKeyboard();
        closeGPUVideo();
        connectionMode = '';
        remoteInput = false;
        controlInput.disabled = false;
        stage.dataset.gpu = 'false';
        gpuViewer.style.display = 'none';
        canvas.style.display = 'none';
        gpuCanvas.style.display = 'none';
        empty.style.display = 'flex';
        lastCloseMessage = '';
        resetGPUReconnectBackoff();
        resetFrameStats();
        setStatus(t('desktop.closed'), 'warn');
    }
    function gpuURL(device) {
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        const params = new URLSearchParams();
        if (passwordInput.value) params.set('password', passwordInput.value);
        const path = `/gpu-desktop/${encodeURIComponent(device)}/ws`;
        const url = `${proto}//${location.host}${authURL(path + (params.toString() ? '?' + params.toString() : ''))}`;
        return url;
    }
    function gpuSend(message) {
        if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(message));
    }
    function gpuSendConfig() {
        if (connectionMode !== 'gpu' || !gpuSourceSelect.value) return;
        saveGpuDesktopSettings();
        updateGpuRangeLabels();
        applyGpuVideoFit();
        const size = gpuMaxVideoSize();
        gpuSend({Config:{
            capturable_id:Number(gpuSourceSelect.value),
            capture_cursor:showCursorInput.checked,
            max_width:size.width,
            max_height:size.height,
            client_name:'rdev-browser',
            frame_rate:gpuEnableVideo.checked ? gpuFrameRate() : 0,
            encoder:gpuEncoderSelect.value || 'auto',
            pointer_backend:gpuPointerBackend.value || 'auto',
            keyboard_backend:gpuKeyboardBackend.value || 'auto'
        }});
        gpuSend(gpuEnableVideo.checked ? 'ResumeVideo' : 'PauseVideo');
    }
    function applyGpuVideoFit() {
        const fit = gpuStretch.checked ? 'fill' : 'contain';
        gpuVideo.style.objectFit = fit;
        gpuCanvas.style.objectFit = fit;
    }
    function gpuUpdateFrameInfo() {
        const elapsed = statsStartedAt ? Math.max(0.001, (performance.now() - statsStartedAt) / 1000) : 0;
        const fps = elapsed ? (frameCount / elapsed).toFixed(1) : '0.0';
        const kbps = elapsed ? ((frameBytes * 8 / elapsed) / 1024).toFixed(0) : '0';
        const size = gpuVideoMode === 'webcodecs' ? `${gpuCanvas.width || 0}×${gpuCanvas.height || 0}` : (gpuVideo.videoWidth && gpuVideo.videoHeight ? `${gpuVideo.videoWidth}×${gpuVideo.videoHeight}` : 'video');
        const source = currentSource ? ` · ${currentSource}` : '';
        frameInfo.textContent = `${size}${source} · ${fps} fps · ${kbps} kbps · ${formatBytes(frameBytes)}`;
    }
    function gpuUpdateBuffer() {
        if (!gpuSourceBuffer || gpuSourceBuffer.updating || !gpuMediaSource || gpuMediaSource.readyState !== 'open' || gpuQueue.length === 0) return;
        try { gpuSourceBuffer.appendBuffer(gpuQueue.shift()); } catch (err) { gpuQueue = []; setStatus(err.message || String(err), 'err'); }
    }
    function gpuStartMedia() {
        const MS = window.ManagedMediaSource || window.MediaSource;
        if (!MS) { setStatus('MediaSource unsupported', 'err'); return; }
        closeGPUVideo();
        gpuVideoMode = 'mse';
        gpuMediaSource = new MS();
        frameCount = 0; frameBytes = 0; statsStartedAt = performance.now(); lastFrameAt = 0;
        canvas.style.display = 'none';
        gpuCanvas.style.display = 'none';
        gpuVideo.style.display = 'block';
        gpuVideo.src = URL.createObjectURL(gpuMediaSource);
        gpuMediaSource.addEventListener('sourceopen', () => {
            let mime = 'video/mp4; codecs="avc1.4D403D"';
            if (!MS.isTypeSupported(mime)) mime = 'video/mp4';
            gpuSourceBuffer = gpuMediaSource.addSourceBuffer(mime);
            gpuSourceBuffer.addEventListener('updateend', gpuUpdateBuffer);
        }, {once:true});
    }
    function gpuStartWebCodecsVideo(config) {
        if (!('VideoDecoder' in window) || !('EncodedVideoChunk' in window)) {
            setStatus('WebCodecs unsupported', 'err');
            return;
        }
        closeGPUVideo();
        gpuVideoMode = 'webcodecs';
        frameCount = 0; frameBytes = 0; statsStartedAt = performance.now(); lastFrameAt = 0;
        gpuVideo.style.display = 'none';
        canvas.style.display = 'none';
        gpuCanvas.style.display = 'block';
        gpuCanvas.width = config.width || 320;
        gpuCanvas.height = config.height || 640;
        gpuVideoDecoder = new VideoDecoder({
            output: frame => {
                gpuCanvas.width = frame.displayWidth || frame.codedWidth || gpuCanvas.width;
                gpuCanvas.height = frame.displayHeight || frame.codedHeight || gpuCanvas.height;
                gpuCtx.clearRect(0, 0, gpuCanvas.width, gpuCanvas.height);
                gpuCtx.drawImage(frame, 0, 0, gpuCanvas.width, gpuCanvas.height);
                gpuEmpty.textContent = '';
                frame.close();
                gpuUpdateFrameInfo();
            },
            error: err => setStatus(err.message || String(err), 'err')
        });
        gpuVideoDecoder.configure({
            codec: config.codec || 'avc1.42E01F',
            codedWidth: config.width || 320,
            codedHeight: config.height || 640,
            optimizeForLatency: true
        });
        gpuNeedKeyFrame = true;
        setStatus(t('index.desktopReady'), 'ok');
    }
    function gpuHandleAndroidVideoPacket(buffer) {
        const view = new DataView(buffer);
        if (view.byteLength < 13 || view.getUint8(0) !== 0x52 || view.getUint8(1) !== 0x44 || view.getUint8(2) !== 0x41 || view.getUint8(3) !== 0x31) return false;
        if (!gpuVideoDecoder || gpuVideoDecoder.state !== 'configured') return true;
        const isKey = view.getUint8(4) !== 0;
        if (gpuNeedKeyFrame && !isKey) return true;
        const timestamp = Number(view.getBigUint64(5));
        const data = new Uint8Array(buffer, 13);
        if (!isKey && gpuVideoDecoder.decodeQueueSize > 2) {
            gpuNeedKeyFrame = true;
            return true;
        }
        frameCount++;
        frameBytes += data.byteLength;
        lastFrameAt = performance.now();
        try {
            gpuVideoDecoder.decode(new EncodedVideoChunk({type:isKey ? 'key' : 'delta', timestamp, data}));
            gpuNeedKeyFrame = false;
        } catch (err) {
            gpuNeedKeyFrame = true;
            setStatus(err.message || String(err), 'err');
        }
        return true;
    }
    function gpuSetSources(list) {
        const saved = localStorage.getItem(gpuStoragePrefix + 'source') || gpuSourceSelect.value || '';
        const previousName = gpuSourceSelect.selectedOptions[0] ? gpuSourceSelect.selectedOptions[0].textContent : '';
        gpuSourceSelect.textContent = '';
        if (!list || list.length === 0) {
            const option = document.createElement('option'); option.value = ''; option.textContent = '无可用画面来源'; gpuSourceSelect.appendChild(option);
            gpuEmpty.innerHTML = '<strong>没有可用画面来源</strong><span>请确认远程设备已登录到真实桌面，或点击刷新重试。</span>';
            setStatus('No desktop source', 'err');
            return;
        }
        list.forEach((name, index) => {
            const option = document.createElement('option'); option.value = String(index); option.textContent = name; gpuSourceSelect.appendChild(option);
        });
        const same = [...gpuSourceSelect.options].find(o => o.textContent === previousName);
        if (saved && [...gpuSourceSelect.options].some(o => o.value === saved)) gpuSourceSelect.value = saved;
        else if (same) gpuSourceSelect.value = same.value;
        else gpuSourceSelect.value = String(Math.max(0, list.findIndex(name => /\(GDI\)/.test(name))));
        currentSource = gpuSourceSelect.selectedOptions[0] ? gpuSourceSelect.selectedOptions[0].textContent : '';
        gpuSendConfig();
    }
    function setSelectOptions(select, options, current) {
        select.textContent = '';
        (options || []).forEach(opt => {
            if (!opt || !opt.value) return;
            const option = document.createElement('option');
            option.value = opt.value;
            option.textContent = opt.label || opt.value;
            select.appendChild(option);
        });
        if (![...select.options].some(o => o.value === current)) {
            const option = document.createElement('option'); option.value = 'auto'; option.textContent = '自动'; select.insertBefore(option, select.firstChild);
        }
        select.value = [...select.options].some(o => o.value === current) ? current : 'auto';
    }
    function gpuSetEncoders(options) {
        const current = localStorage.getItem(gpuStoragePrefix + 'encoder') || gpuEncoderSelect.value || 'auto';
        setSelectOptions(gpuEncoderSelect, options, current);
    }
    function gpuSetInputCapabilities(capabilities) {
        setSelectOptions(gpuPointerBackend, capabilities.pointerOptions || capabilities.options || [], localStorage.getItem(gpuStoragePrefix + 'pointerBackend') || gpuPointerBackend.value || 'auto');
        setSelectOptions(gpuKeyboardBackend, capabilities.keyboardOptions || capabilities.options || [], localStorage.getItem(gpuStoragePrefix + 'keyboardBackend') || gpuKeyboardBackend.value || 'auto');
    }
    function scheduleGPUReconnect(device, connID) {
        if (manualDisconnect || connID !== connectionSeq || connectionMode !== 'gpu') return;
        const delay = nextGPUReconnectDelay();
        clearTimeout(reconnectTimer);
        reconnectTimer = setTimeout(() => {
            if (!manualDisconnect && connID === connectionSeq && connectionMode === 'gpu') connectGPUDesktop(device, true);
        }, delay);
    }
    function connectGPUDesktop(device, reconnecting = false) {
        connectionMode = 'gpu';
        remoteInput = true;
        controlInput.disabled = false;
        if (!controlPreferenceSet) controlInput.checked = true;
        stage.dataset.control = String(controlInput.checked);
        stage.dataset.cursor = String(showCursorInput.checked);
        stage.dataset.gpu = 'true';
        canvas.style.display = 'none';
        gpuViewer.style.display = 'block';
        empty.style.display = 'none';
        gpuEmpty.innerHTML = '<strong>正在连接远程桌面</strong><span>连接建立后会自动读取画面来源。</span>';
        resetFrameStats();
        ws = RDevUI.socket(gpuURL(device));
        const connID = reconnecting ? connectionSeq : ++connectionSeq;
        lastCloseMessage = '';
        ws.binaryType = 'arraybuffer';
        setStatus(reconnecting ? t('common.connecting') : t('common.connecting'));
        ws.onopen = () => { if (connID !== connectionSeq) return; resetGPUReconnectBackoff(); setStatus(t('desktop.starting')); gpuSend('GetCapturableList'); gpuSendConfig(); };
        ws.onerror = () => { if (connID !== connectionSeq) return; setStatus(t('common.error'), 'err'); };
        ws.onclose = () => { if (connID !== connectionSeq) return; ws = null; setStatus(lastCloseMessage || t('desktop.closed'), lastCloseMessage ? 'err' : 'warn'); scheduleGPUReconnect(device, connID); };
        ws.onmessage = evt => {
            if (connID !== connectionSeq) return;
            if (evt.data instanceof ArrayBuffer) {
                if (gpuHandleAndroidVideoPacket(evt.data)) return;
                frameCount++;
                frameBytes += evt.data.byteLength || 0;
                lastFrameAt = performance.now();
                gpuQueue.push(evt.data);
                gpuUpdateBuffer();
                gpuUpdateFrameInfo();
                if (gpuVideo.seekable.length > 0) {
                    const end = gpuVideo.seekable.end(gpuVideo.seekable.length - 1);
                    const readyState = gpuLowLatency.checked ? 3 : 4;
                    if (Number.isFinite(end) && (gpuVideo.readyState >= readyState || end - gpuVideo.currentTime > 3)) gpuVideo.currentTime = end;
                }
                return;
            }
            const msg = JSON.parse(evt.data);
            if (msg === 'NewVideo') { gpuStartMedia(); gpuEmpty.textContent = ''; }
            else if (msg && msg.AndroidVideoConfig) { gpuStartWebCodecsVideo(msg.AndroidVideoConfig); gpuEmpty.textContent = ''; }
            else if (msg === 'ConfigOk') { gpuEmpty.textContent = ''; setStatus(t('index.desktopReady'), 'ok'); }
            else if (msg && msg.CapturableList) gpuSetSources(msg.CapturableList);
            else if (msg && msg.EncoderCapabilities) gpuSetEncoders(msg.EncoderCapabilities.options || []);
            else if (msg && msg.InputCapabilities) gpuSetInputCapabilities(msg.InputCapabilities);
            else if (msg && msg.RuntimeStatus) gpuUpdateFrameInfo();
            else if (msg && msg.ConfigError) { lastCloseMessage = msg.ConfigError; setStatus(msg.ConfigError, 'err'); }
            else if (msg && msg.Error) { lastCloseMessage = msg.Error; setStatus(msg.Error, 'err'); }
        };
    }
    async function connect() {
        disconnect();
        manualDisconnect = false;
        saveDesktopSettings();
        const device = deviceSelect.value;
        if (!device) return setStatus(t('common.noDevices'), 'err');
        const selectedDevice = deviceCache.find(d => d.id === device) || {};
        if (selectedDevice.gpuDesktop) return connectGPUDesktop(device);
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        remoteInput = false;
        resetFrameStats();
        ws = RDevUI.socket(`${proto}//${location.host}${authURL('/desktop?' + desktopQuery())}`);
        const connID = ++connectionSeq;
        lastCloseMessage = '';
        ws.binaryType = 'arraybuffer';
        setStatus(t('common.connecting'));
        ws.onmessage = (evt) => {
            if (connID !== connectionSeq) return;
            if (evt.data instanceof ArrayBuffer) { drawFrame(evt.data); return; }
            const msg = JSON.parse(evt.data);
            if (msg.op === 'auth') {
                setStatus(msg.message || t('desktop.authRequired'), 'warn');
                const saved = RDevUI.getDevicePassword(device);
                if (saved && !passwordInput.value) passwordInput.value = saved;
                if (passwordInput.value) ws.send(JSON.stringify(authPayload()));
            } else if (msg.op === 'auth_ok') {
                if (passwordInput.value) RDevUI.rememberDevicePassword(device, passwordInput.value);
                setStatus(t('desktop.starting'));
            } else if (msg.op === 'auth_fail') {
                RDevUI.forgetDevicePassword(device);
                setStatus(msg.message || t('common.passwordWrong'), 'err');
            } else if (msg.op === 'starting') {
                setStatus(t('desktop.starting'));
            } else if (msg.op === 'ready') {
                canvas.width = msg.width || 1; canvas.height = msg.height || 1;
                canvas.style.display = 'block'; empty.style.display = 'none';
                currentSource = msg.source || '';
                if (msg.inputBackend) inputBackendSelect.value = msg.inputBackend;
                remoteInput = !!(msg.desktop && msg.desktop.input);
                controlInput.disabled = !remoteInput;
                if (remoteInput && !controlPreferenceSet) controlInput.checked = true;
                if (!remoteInput) controlInput.checked = false;
                stage.dataset.control = String(controlInput.checked && remoteInput);
                stage.dataset.cursor = String(showCursorInput.checked);
                if (remoteInput && !controlPreferenceSet) saveDesktopSettings();
                updateFrameInfo();
                const source = currentSource ? ` · ${currentSource}` : '';
                setStatus(`${t('index.desktopReady')} · ${canvas.width}×${canvas.height}${source}`, 'ok');
            } else if (msg.op === 'error') {
                lastCloseMessage = msg.message || t('common.error');
                setStatus(lastCloseMessage, 'err');
            } else if (msg.op === 'closed') {
                lastCloseMessage = msg.message || t('desktop.closed');
                setStatus(lastCloseMessage, msg.message ? 'err' : 'warn');
            } else if (msg.op === 'input_error') {
                setStatus(msg.message || t('common.error'), 'err');
            }
        };
        ws.onclose = () => { if (connID !== connectionSeq) return; ws = null; setStatus(lastCloseMessage || t('desktop.closed'), lastCloseMessage ? 'err' : 'warn'); };
    }
    function drawFrame(data) {
        pendingFrame = data;
        if (drawing) return;
        drawing = true;
        requestAnimationFrame(async () => {
            const frame = pendingFrame; pendingFrame = null;
            try {
                const blob = new Blob([frame], {type:'image/jpeg'});
                if (window.createImageBitmap) {
                    try {
                        const bitmap = await createImageBitmap(blob);
                        paintImage(bitmap, bitmap.width, bitmap.height);
                        bitmap.close && bitmap.close();
                    } catch (e) {
                        const img = await decodeImage(blob);
                        paintImage(img, img.naturalWidth || img.width, img.naturalHeight || img.height);
                    }
                } else {
                    const img = await decodeImage(blob);
                    paintImage(img, img.naturalWidth || img.width, img.naturalHeight || img.height);
                }
                frameCount++;
                frameBytes += frame.byteLength || frame.size || 0;
                if (!statsStartedAt) statsStartedAt = performance.now();
                lastFrameAt = performance.now();
                updateFrameInfo();
            } catch (e) { console.error(e); }
            drawing = false;
            if (pendingFrame) drawFrame(pendingFrame);
        });
    }
    function paintImage(image, width, height) {
        if (canvas.width !== width || canvas.height !== height) { canvas.width = width; canvas.height = height; }
        canvas.style.display = 'block'; empty.style.display = 'none';
        ctx.drawImage(image, 0, 0);
    }
    function decodeImage(blob) {
        return new Promise((resolve, reject) => {
            const url = URL.createObjectURL(blob);
            const img = new Image();
            img.onload = () => { URL.revokeObjectURL(url); resolve(img); };
            img.onerror = () => { URL.revokeObjectURL(url); reject(new Error('image decode failed')); };
            img.src = url;
        });
    }
    function canvasPoint(event) {
        const rect = canvas.getBoundingClientRect();
        const x = Math.max(0, Math.min(canvas.width - 1, Math.round((event.clientX - rect.left) * canvas.width / rect.width)));
        const y = Math.max(0, Math.min(canvas.height - 1, Math.round((event.clientY - rect.top) * canvas.height / rect.height)));
        return {x, y};
    }
    function sendInput(input) {
        if (!ws || ws.readyState !== WebSocket.OPEN || !controlInput.checked || !remoteInput) return;
        if (ws.bufferedAmount > 512 * 1024) return;
        try { ws.send(JSON.stringify(Object.assign({op:'input'}, input))); }
        catch (e) { setStatus(e.message || String(e), 'err'); }
    }
    function sendCursor(input) {
        if (!ws || ws.readyState !== WebSocket.OPEN || !showCursorInput.checked) return;
        if (ws.bufferedAmount > 128 * 1024) return;
        try { ws.send(JSON.stringify(Object.assign({op:'input', inputType:'cursor_move'}, input))); }
        catch (e) { setStatus(e.message || String(e), 'err'); }
    }
    function sendMouse(type, event, extra = {}) {
        const now = performance.now();
        if (type === 'mouse_move' && (now - lastMouseSent < 16 || (ws && ws.bufferedAmount > 128 * 1024))) return;
        lastMouseSent = now;
        const point = canvasPoint(event);
        if (type === 'mouse_move' && (!controlInput.checked || !remoteInput)) sendCursor({x:point.x, y:point.y, pointerType:event.pointerType || 'mouse', pointerId:event.pointerId || 0, pressure:event.pressure || 0});
        sendInput(Object.assign({inputType:type, x:point.x, y:point.y, button:event.button || 0, pointerType:event.pointerType || 'mouse', pointerId:event.pointerId || 0, pressure:event.pressure || 0}, extra));
    }
    function sendKey(type, event) {
        if (!controlInput.checked || !remoteInput) return;
        event.preventDefault();
        sendInput({inputType:type, key:event.key, code:event.code, ctrlKey:event.ctrlKey, altKey:event.altKey, shiftKey:event.shiftKey, metaKey:event.metaKey});
    }
    function gpuPointerBits(button) {
        if (button === 0) return 1;
        if (button === 2) return 2;
        if (button === 1) return 4;
        if (button === 3) return 8;
        if (button === 4) return 16;
        if (button === 5) return 32;
        return 0;
    }
    function gpuVideoContentRect() {
        const rect = gpuOverlay.getBoundingClientRect();
        const videoWidth = gpuVideoMode === 'webcodecs' ? (gpuCanvas.width || 0) : (gpuVideo.videoWidth || 0);
        const videoHeight = gpuVideoMode === 'webcodecs' ? (gpuCanvas.height || 0) : (gpuVideo.videoHeight || 0);
        if (!videoWidth || !videoHeight || !rect.width || !rect.height) return rect;
        const containerRatio = rect.width / rect.height;
        const videoRatio = videoWidth / videoHeight;
        if (containerRatio > videoRatio) {
            const width = rect.height * videoRatio;
            return {left: rect.left + (rect.width - width) / 2, top: rect.top, width, height: rect.height};
        }
        const height = rect.width / videoRatio;
        return {left: rect.left, top: rect.top + (rect.height - height) / 2, width: rect.width, height};
    }
    function gpuPointerMessage(event, eventType = event.type) {
        const rect = gpuVideoContentRect();
        const x = Math.max(0, Math.min(1, (event.clientX - rect.left) / Math.max(1, rect.width)));
        const y = Math.max(0, Math.min(1, (event.clientY - rect.top) / Math.max(1, rect.height)));
        const previous = gpuLastPointer.get(event.pointerId) || {x:event.clientX,y:event.clientY};
        gpuLastPointer.set(event.pointerId, {x:event.clientX,y:event.clientY});
        const diag = Math.max(1, Math.sqrt(rect.width * rect.width + rect.height * rect.height));
        const pressure = Math.max(event.pressure || (event.buttons ? 0.5 : 0), gpuMinPressure.valueAsNumber || 0);
        return {PointerEvent:{event_type:eventType,pointer_id:event.pointerId,timestamp:Math.round(event.timeStamp * 1000),is_primary:event.isPrimary,pointer_type:event.pointerType||'mouse',button:gpuPointerBits(event.button),buttons:event.buttons,x:x,y:y,movement_x:Math.round(event.movementX || event.clientX - previous.x),movement_y:Math.round(event.movementY || event.clientY - previous.y),pressure:pressure,tilt_x:event.tiltX || 0,tilt_y:event.tiltY || 0,twist:event.twist || 0,width:(event.width || 1) / diag,height:(event.height || 1) / diag}};
    }
    function gpuKeyMessage(event, eventType) { return {KeyboardEvent:{event_type:eventType,code:event.code,key:event.key,location:event.location,alt:event.altKey,ctrl:eventCtrlKey(event),shift:event.shiftKey,meta:event.metaKey}}; }
    function eventCtrlKey(event) { return event.ctrlKey || false; }
    function gpuReleaseKeyboard() {
        if (connectionMode !== 'gpu' || !ws || ws.readyState !== WebSocket.OPEN) { gpuHeldKeys.clear(); return; }
        for (const held of gpuHeldKeys.values()) gpuSend({KeyboardEvent:Object.assign({}, held, {event_type:'up', ctrl:false, alt:false, shift:false, meta:false})});
        gpuHeldKeys.clear();
        gpuSend('ReleaseKeyboard');
    }
    function gpuTextInput(text) {
        if (!text || connectionMode !== 'gpu' || !controlInput.checked || !remoteInput) return;
        gpuSend({TextInputEvent:{text}});
    }
    function scheduleAdaptiveReconnect() {
        if (!ws) return;
        if (connectionMode === 'gpu') { clearTimeout(resizeTimer); resizeTimer = setTimeout(gpuSendConfig, 300); return; }
        if (modeSelect.value !== 'auto') return;
        clearTimeout(resizeTimer);
        resizeTimer = setTimeout(() => connect(), 500);
    }
    function saveScreenshot() {
        if (connectionMode === 'gpu' && gpuVideoMode === 'webcodecs' && gpuCanvas.width && gpuCanvas.height) {
            return saveCanvasBlob(gpuCanvas);
        }
        if (connectionMode === 'gpu' && gpuVideo.videoWidth && gpuVideo.videoHeight) {
            const shot = document.createElement('canvas');
            shot.width = gpuVideo.videoWidth; shot.height = gpuVideo.videoHeight;
            shot.getContext('2d').drawImage(gpuVideo, 0, 0, shot.width, shot.height);
            return saveCanvasBlob(shot);
        }
        if (!canvas.width || !canvas.height || canvas.style.display === 'none') return;
        saveCanvasBlob(canvas);
    }
    function saveCanvasBlob(targetCanvas) {
        targetCanvas.toBlob(blob => {
            if (!blob) return;
            const url = URL.createObjectURL(blob);
            const a = document.createElement('a');
            a.href = url;
            a.download = `rdev-${deviceSelect.value || 'desktop'}-${new Date().toISOString().replace(/[:.]/g, '-')}.png`;
            a.click();
            setTimeout(() => URL.revokeObjectURL(url), 1000);
        }, 'image/png');
    }
    function toggleFullscreen() {
        if (document.fullscreenElement) { document.exitFullscreen && document.exitFullscreen(); return; }
        if (stage.requestFullscreen) stage.requestFullscreen();
    }
    controlInput.addEventListener('change', () => { controlPreferenceSet = true; stage.dataset.control = String(controlInput.checked && remoteInput); if (controlInput.checked) (connectionMode === 'gpu' ? gpuOverlay : canvas).focus(); saveDesktopSettings(); });
    showCursorInput.addEventListener('change', () => { stage.dataset.cursor = String(showCursorInput.checked); saveDesktopSettings(); scheduleReconnectIfActive(); });
    canvas.addEventListener('contextmenu', e => e.preventDefault());
    canvas.addEventListener('auxclick', e => e.preventDefault());
    canvas.addEventListener('pointermove', e => sendMouse('mouse_move', e));
    canvas.addEventListener('pointerdown', e => { e.preventDefault(); canvas.focus(); canvas.setPointerCapture && canvas.setPointerCapture(e.pointerId); sendMouse('mouse_down', e); });
    canvas.addEventListener('pointerup', e => { e.preventDefault(); sendMouse('mouse_up', e); });
    canvas.addEventListener('wheel', e => { if (!controlInput.checked || !remoteInput) return; e.preventDefault(); const point = canvasPoint(e); sendInput({inputType:'wheel', x:point.x, y:point.y, deltaX:Math.round(e.deltaX), deltaY:Math.round(e.deltaY)}); }, {passive:false});
    canvas.addEventListener('keydown', e => sendKey('key_down', e));
    canvas.addEventListener('keyup', e => sendKey('key_up', e));
    gpuOverlay.addEventListener('contextmenu', e => e.preventDefault());
    gpuOverlay.addEventListener('auxclick', e => e.preventDefault());
    ['pointerdown','pointermove','pointerup','pointercancel','pointerover','pointerenter','pointerleave','pointerout'].forEach(type => gpuOverlay.addEventListener(type, e => {
        if (connectionMode !== 'gpu' || !controlInput.checked || !remoteInput) return;
        if (!gpuPointerTypes().includes(e.pointerType || 'mouse')) return;
        gpuOverlay.focus();
        if (type === 'pointerdown') gpuOverlay.setPointerCapture && gpuOverlay.setPointerCapture(e.pointerId);
        const events = type === 'pointermove' ? getCoalescedPointerEvents(e) : [e];
        for (const event of events) gpuSend(gpuPointerMessage(event, type));
        e.preventDefault();
        e.stopPropagation();
    }));
    gpuOverlay.addEventListener('wheel', e => {
        if (connectionMode !== 'gpu' || !controlInput.checked || !remoteInput || !gpuEnableMouse.checked) return;
        let scale = 1;
        if (e.deltaMode === 1) scale = 10;
        else if (e.deltaMode === 2) scale = 1000;
        gpuSend({WheelEvent:{dx:Math.round(scale * e.deltaX),dy:-Math.round(scale * e.deltaY),timestamp:Math.round(e.timeStamp * 1000)}});
        e.preventDefault();
        e.stopPropagation();
    }, {passive:false});
    gpuOverlay.addEventListener('keydown', e => {
        if (connectionMode !== 'gpu' || !controlInput.checked || !remoteInput) return;
        const eventType = e.repeat ? 'repeat' : 'down';
        const msg = gpuKeyMessage(e, eventType);
        if (eventType === 'down') gpuHeldKeys.set(`${msg.KeyboardEvent.code}:${msg.KeyboardEvent.location}`, msg.KeyboardEvent);
        gpuSend(msg);
        e.preventDefault();
        e.stopPropagation();
    });
    gpuOverlay.addEventListener('keyup', e => {
        if (connectionMode !== 'gpu' || !controlInput.checked || !remoteInput) return;
        const msg = gpuKeyMessage(e, 'up');
        gpuHeldKeys.delete(`${msg.KeyboardEvent.code}:${msg.KeyboardEvent.location}`);
        gpuSend(msg);
        e.preventDefault();
        e.stopPropagation();
    });
    window.addEventListener('resize', scheduleAdaptiveReconnect);
    document.getElementById('connect').addEventListener('click', connect);
    document.getElementById('disconnect').addEventListener('click', disconnect);
    document.getElementById('screenshot').addEventListener('click', saveScreenshot);
    document.getElementById('syncVNC').addEventListener('click', syncVNCSettings);
    document.getElementById('fullscreen').addEventListener('click', toggleFullscreen);
    gpuAdvancedToggle.addEventListener('click', () => { gpuAdvanced.classList.toggle('open'); saveGpuDesktopSettings(); });
    gpuSourceSelect.addEventListener('change', gpuSendConfig);
    gpuEncoderSelect.addEventListener('change', gpuSendConfig);
    gpuRefreshButton.addEventListener('click', () => gpuSend('GetCapturableList'));
    [gpuEnableVideo, gpuLowLatency, gpuStretch, gpuEnableMouse, gpuEnablePen, gpuEnableTouch, gpuScaleInput, gpuFrameRateInput, gpuPointerBackend, gpuKeyboardBackend, gpuMinPressure].forEach(elem => {
        elem.addEventListener('input', () => { updateGpuRangeLabels(); applyGpuVideoFit(); saveGpuDesktopSettings(); });
        elem.addEventListener('change', gpuSendConfig);
    });
    deviceSelect.addEventListener('change', () => { updateSourceOptions(); saveDesktopSettings(); scheduleReconnectIfActive(); });
    modeSelect.addEventListener('change', () => { updateMode(); saveDesktopSettings(); scheduleReconnectIfActive(); });
    sourceSelect.addEventListener('change', () => { saveDesktopSettings(); scheduleReconnectIfActive(); });
    inputBackendSelect.addEventListener('change', () => { saveDesktopSettings(); scheduleReconnectIfActive(); });
    fitModeSelect.addEventListener('change', () => { stage.dataset.fit = fitModeSelect.value; saveDesktopSettings(); });
    [fpsInput, qualityInput, maxWidthInput, maxHeightInput].forEach(input => input.addEventListener('change', () => { saveDesktopSettings(); scheduleReconnectIfActive(); }));
    passwordInput.addEventListener('keydown', e => { if (e.key === 'Enter') connect(); });
    window.addEventListener('blur', gpuReleaseKeyboard, true);
    document.addEventListener('visibilitychange', () => { if (document.hidden) { gpuReleaseKeyboard(); if (connectionMode === 'gpu') gpuSend('PauseVideo'); } else if (connectionMode === 'gpu' && gpuEnableVideo.checked) gpuSend('ResumeVideo'); }, true);
    window.addEventListener('beforeinput', e => {
        if (connectionMode !== 'gpu' || !controlInput.checked || !remoteInput) return;
        if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement || e.target instanceof HTMLSelectElement) return;
        if (e.inputType !== 'insertText' && e.inputType !== 'insertLineBreak') return;
        gpuTextInput(e.inputType === 'insertLineBreak' ? '\n' : e.data);
        e.preventDefault();
        e.stopPropagation();
    }, true);
    window.addEventListener('paste', e => {
        if (connectionMode !== 'gpu' || !controlInput.checked || !remoteInput) return;
        if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement || e.target instanceof HTMLSelectElement) return;
        const text = e.clipboardData?.getData('text') || '';
        gpuTextInput(text);
        e.preventDefault();
        e.stopPropagation();
    }, true);
    window.addEventListener('compositionend', e => {
        if (connectionMode !== 'gpu' || !controlInput.checked || !remoteInput) return;
        if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement || e.target instanceof HTMLSelectElement) return;
        gpuTextInput(e.data || '');
        e.preventDefault();
        e.stopPropagation();
    }, true);
    window.addEventListener('beforeunload', disconnect);
    loadSavedDesktopSettings();
    updateMode();
    loadDevices().then(() => { if (deviceSelect.value) connect(); }).catch(e => setStatus(e.message || String(e), 'err'));
