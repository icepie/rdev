interface Window {
    ManagedMediaSource: any;
}

interface KeyboardLockManager {
    lock?: (keyCodes?: string[]) => Promise<void>;
    unlock?: () => void;
}

interface Navigator {
    keyboard?: KeyboardLockManager;
}

type PointerEventWithCoalescedEvents = PointerEvent & {
    getCoalescedEvents?: () => PointerEvent[];
};

type HTMLVideoElementWithRemotePlayback = HTMLVideoElement & {
    disableRemotePlayback?: boolean;
};

enum LogLevel {
    ERROR = 0,
    WARN,
    INFO,
    DEBUG,
    TRACE,
}

let log_pre: HTMLPreElement;
let log_level: LogLevel = LogLevel.ERROR;
let no_log_messages: boolean = true;

let fps_out: HTMLOutputElement;
let capture_backend_out: HTMLOutputElement;
let encoder_backend_out: HTMLOutputElement;
let input_backend_out: HTMLOutputElement;
let pointer_backend_out: HTMLOutputElement;
let keyboard_backend_out: HTMLOutputElement;
let keyboard_event_count = 0;
let text_input_event_count = 0;
let pointer_event_count = 0;
let frame_count = 0;
let last_fps_calc: number = performance.now();

let check_video: HTMLInputElement;

function normalized_rotation(value: string | number): number {
    const raw = typeof value === "number" ? value : Number(value);
    const normalized = ((raw % 360) + 360) % 360;
    return [0, 90, 180, 270].includes(normalized) ? normalized : 0;
}

function get_display_rotation(): number {
    if (!settings)
        return 0;
    return normalized_rotation(settings.display_rotation_select?.value || "0");
}

function get_input_rotation(): number {
    if (!settings)
        return 0;
    return normalized_rotation(settings.input_rotation_select?.value || "0");
}

function transform_video_point(x: number, y: number, rotation: number): [number, number] {
    switch (normalized_rotation(rotation)) {
        case 90:
            return [y, 1 - x];
        case 180:
            return [1 - x, 1 - y];
        case 270:
            return [1 - y, x];
        default:
            return [x, y];
    }
}

function run(level: string) {
    window.onload = () => {
        log_pre = document.getElementById("log") as HTMLPreElement;
        log_pre.textContent = "";
        log_level = LogLevel[level];
        fps_out = document.getElementById("fps") as HTMLOutputElement;
        capture_backend_out = document.getElementById("capture_backend") as HTMLOutputElement;
        encoder_backend_out = document.getElementById("encoder_backend") as HTMLOutputElement;
        input_backend_out = document.getElementById("input_backend_status") as HTMLOutputElement;
        pointer_backend_out = document.getElementById("pointer_backend") as HTMLOutputElement;
        keyboard_backend_out = document.getElementById("keyboard_backend") as HTMLOutputElement;
        check_video = document.getElementById("enable_video") as HTMLInputElement;
        window.addEventListener("error", (e: ErrorEvent | Event | UIEvent) => {
            if ((e as ErrorEvent).error) {
                let err = e as ErrorEvent;
                log(LogLevel.ERROR, err.filename + ":L" + err.lineno + ":" + err.colno + ": " + err.message + " Error object: " + JSON.stringify(err.error));
            } else if ((e as Event | UIEvent).target) {
                let ev = e as Event;
                let src = (e.target as any).src;
                if (ev.target instanceof HTMLVideoElement)
                    log(LogLevel.ERROR, "Failed to decode video, try reducing resolution or disabling hardware acceleration and reload the page. Error src: " + src);
                else
                    log(LogLevel.ERROR, "Failed to obtain resource, target: " + ev.target + " type: " + ev.type + " src: " + src + " Error object: " + JSON.stringify(ev));
            } else {
                log(LogLevel.WARN, "Got unknown event: " + JSON.stringify(e));
            }
            return false;
        }, true)
        init();
    };
}

function log(level: LogLevel, msg: string) {
    if (level > log_level)
        return;

    if (level == LogLevel.TRACE)
        console.trace(msg);
    else if (level == LogLevel.DEBUG)
        console.debug(msg);
    else if (level == LogLevel.INFO)
        console.info(msg);
    else if (level == LogLevel.WARN)
        console.warn(msg);
    else if (level == LogLevel.ERROR)
        console.error(msg);

    if (no_log_messages) {
        no_log_messages = false;
        document.getElementById("log_section").classList.remove("hide");
    }
    log_pre.textContent += LogLevel[level] + ": " + msg + "\n";
}

function frame_rate_scale(x: number) {
    return Math.pow(x / 100, 1.5);
}

function frame_rate_scale_inv(x: number) {
    return 100 * Math.pow(x, 2 / 3);
}


function calc_max_video_resolution(scale: number) {
    return [
        Math.round(scale * window.innerWidth * window.devicePixelRatio),
        Math.round(scale * window.innerHeight * window.devicePixelRatio)
    ];
}

function fresh_canvas() {
    let canvas_old = document.getElementById("canvas");
    let canvas = document.createElement("canvas");
    canvas.id = canvas_old.id;
    canvas_old.classList.forEach((cls) => canvas.classList.add(cls));
    canvas_old.replaceWith(canvas);
    return canvas;
}

function getPointerEvents(event: PointerEvent): PointerEvent[] {
    const coalescedEvent = event as PointerEventWithCoalescedEvents;
    if (typeof coalescedEvent.getCoalescedEvents === "function") {
        return coalescedEvent.getCoalescedEvents();
    }
    return [event];
}

function capturePointer(event: PointerEvent) {
    const target = event.currentTarget;
    if (target instanceof Element) {
        try {
            target.setPointerCapture(event.pointerId);
        } catch (_err) {
        }
    }
}

function releasePointer(event: PointerEvent) {
    const target = event.currentTarget;
    if (target instanceof Element && target.hasPointerCapture(event.pointerId)) {
        try {
            target.releasePointerCapture(event.pointerId);
        } catch (_err) {
        }
    }
}

function socketOpen(webSocket: WebSocket) {
    return webSocket && webSocket.readyState === WebSocket.OPEN;
}

function socketStateLabel(webSocket: WebSocket) {
    switch (webSocket?.readyState) {
        case WebSocket.CONNECTING:
            return "连接中";
        case WebSocket.OPEN:
            return "已连接";
        case WebSocket.CLOSING:
            return "关闭中";
        case WebSocket.CLOSED:
            return "已断开";
        default:
            return "未知";
    }
}

function isEditableTarget(target: EventTarget | null) {
    if (!(target instanceof HTMLElement))
        return false;
    if (target.isContentEditable)
        return true;
    const tagName = target.tagName;
    return tagName === "INPUT" || tagName === "TEXTAREA" || tagName === "SELECT";
}

function focusRemoteInputSurface(force = false) {
    if (!force && isEditableTarget(document.activeElement))
        return;
    for (const id of ["video", "canvas"]) {
        const elem = document.getElementById(id) as HTMLElement;
        if (elem && elem.tabIndex < 0)
            elem.tabIndex = 0;
    }
    if (document.body) {
        if (document.body.tabIndex < 0)
            document.body.tabIndex = 0;
        try {
            document.body.focus({ preventScroll: true });
        } catch (_err) {
            document.body.focus();
        }
    }
}

function elemForPointerLock() {
    const canvas = document.getElementById("canvas");
    const video = document.getElementById("video");
    for (const elem of [canvas, video]) {
        if (elem && !elem.classList.contains("vanish"))
            return elem;
    }
    return canvas || video || document.body;
}

function isPointerLocked() {
    return document.pointerLockElement === elemForPointerLock();
}

class VirtualCursor {
    elem: HTMLElement;
    visible = false;
    hotspotX = 10;
    hotspotY = 4;

    constructor() {
        this.elem = document.createElement("div");
        this.elem.id = "virtual_cursor_overlay";
        document.body.appendChild(this.elem);
    }

    enabled(): boolean {
        return !!settings?.checks.get("virtual_cursor")?.checked;
    }

    show(x: number, y: number) {
        if (!this.enabled())
            return this.hide();
        this.visible = true;
        this.elem.style.opacity = "1";
        this.elem.style.transform = `translate(${Math.round(x - this.hotspotX)}px, ${Math.round(y - this.hotspotY)}px)`;
    }

    hide() {
        if (!this.elem || !this.visible)
            return;
        this.visible = false;
        this.elem.style.opacity = "0";
        this.elem.style.transform = "translate(-100px, -100px)";
    }
}

let virtualCursor: VirtualCursor;

function requestPointerLock() {
    const elem = elemForPointerLock() as HTMLElement;
    if (!elem || document.pointerLockElement === elem)
        return;
    try {
        elem.requestPointerLock();
    } catch (_err) { }
}

function requestKeyboardLock(): boolean {
    if (!navigator.keyboard?.lock)
        return false;
    navigator.keyboard.lock().then(() => {
        if (inputCapture)
            inputCapture.keyboardLockRequested = true;
        if (keyboard_backend_out)
            keyboard_backend_out.value = "keyboard lock active";
    }).catch((err) => {
        if (inputCapture)
            inputCapture.keyboardLockRequested = false;
        log(LogLevel.DEBUG, "Keyboard lock failed: " + err);
    });
    return true;
}

function releaseKeyboardLock() {
    try {
        navigator.keyboard?.unlock?.();
    } catch (_err) { }
}

class InputCaptureState {
    keyboard = false;
    pointer = false;
    keyboardButton: HTMLButtonElement;
    pointerButton: HTMLButtonElement;
    bothButton: HTMLButtonElement;
    onKeyboardRelease: () => void = () => { };
    keyboardLockRequested = false;

    constructor() {
        this.keyboardButton = document.getElementById("keyboard_capture_toggle") as HTMLButtonElement;
        this.pointerButton = document.getElementById("pointer_capture_toggle") as HTMLButtonElement;
        this.bothButton = document.getElementById("input_capture_toggle") as HTMLButtonElement;
        if (this.keyboardButton)
            this.keyboardButton.onclick = (e) => { this.handleButton(e, !this.keyboard, this.pointer); };
        if (this.pointerButton)
            this.pointerButton.onclick = (e) => { this.handleButton(e, this.keyboard, !this.pointer); };
        if (this.bothButton)
            this.bothButton.onclick = (e) => { this.handleButton(e, !(this.keyboard && this.pointer), !(this.keyboard && this.pointer)); };
        document.addEventListener("pointerlockchange", () => {
            if (this.pointer && document.pointerLockElement !== elemForPointerLock())
                this.set(this.keyboard, false);
        });
        document.addEventListener("pointerlockerror", () => {
            if (this.pointer)
                this.set(this.keyboard, false);
        });
        this.updateButtons();
    }

    handleButton(event: MouseEvent, keyboard: boolean, pointer: boolean) {
        event.preventDefault();
        event.stopPropagation();
        this.set(keyboard, pointer);
    }

    set(keyboard: boolean, pointer: boolean) {
        if (!keyboard && this.keyboard)
            this.onKeyboardRelease();
        if (!keyboard) {
            releaseKeyboardLock();
            this.keyboardLockRequested = false;
        }
        this.keyboard = keyboard;
        this.pointer = pointer;
        this.updateButtons();
        focusRemoteInputSurface(keyboard || pointer);
        if (keyboard && !this.keyboardLockRequested)
            this.keyboardLockRequested = requestKeyboardLock();
        if (pointer)
            requestPointerLock();
        else if (document.pointerLockElement)
            document.exitPointerLock();
        if (keyboard_backend_out)
            keyboard_backend_out.value = this.label();
    }

    releaseAll() {
        this.set(false, false);
    }

    label() {
        if (this.keyboard && this.pointer)
            return "capture keyboard+mouse";
        if (this.keyboard)
            return "capture keyboard";
        if (this.pointer)
            return "capture mouse";
        return "capture off";
    }

    updateButtons() {
        this.updateButton(this.keyboardButton, this.keyboard, this.keyboard ? "键盘已捕获" : "捕获键盘");
        this.updateButton(this.pointerButton, this.pointer, this.pointer ? "鼠标已捕获" : "捕获鼠标");
        this.updateButton(this.bothButton, this.keyboard && this.pointer, this.keyboard && this.pointer ? "全部已捕获" : "同时捕获");
    }

    updateButton(button: HTMLButtonElement, active: boolean, text: string) {
        if (!button)
            return;
        button.textContent = text;
        button.classList.toggle("active", active);
        button.title = "Ctrl+Alt+Shift+K";
    }
}

let inputCapture: InputCaptureState;

class Rect {
    x: number;
    y: number;
    w: number;
    h: number;
}

class CustomInputAreas {
    mouse: Rect;
    touch: Rect;
    pen: Rect;
}

class Settings {
    webSocket: WebSocket;
    checks: Map<string, HTMLInputElement>;
    capturable_select: HTMLSelectElement;
    frame_rate_input: HTMLInputElement;
    frame_rate_output: HTMLOutputElement;
    scale_video_input: HTMLInputElement;
    scale_video_output: HTMLOutputElement;
    encoder_select: HTMLSelectElement;
    pointer_backend_select: HTMLSelectElement;
    keyboard_backend_select: HTMLSelectElement;
    display_rotation_select: HTMLSelectElement;
    input_rotation_select: HTMLSelectElement;
    range_min_pressure: HTMLInputElement;
    check_aggressive_seek: HTMLInputElement;
    client_name_input: HTMLInputElement;
    visible: boolean;
    custom_input_areas: CustomInputAreas;
    settings: HTMLElement;

    constructor(webSocket: WebSocket) {
        this.webSocket = webSocket;
        this.checks = new Map<string, HTMLInputElement>();
        this.capturable_select = document.getElementById("window") as HTMLSelectElement;
        this.frame_rate_input = document.getElementById("frame_rate") as HTMLInputElement;
        this.frame_rate_input.min = frame_rate_scale_inv(0).toString();
        this.frame_rate_input.max = frame_rate_scale_inv(120).toString();
        this.frame_rate_output = this.frame_rate_input.nextElementSibling as HTMLOutputElement;
        this.scale_video_input = document.getElementById("scale_video") as HTMLInputElement;
        this.scale_video_output = this.scale_video_input.nextElementSibling as HTMLOutputElement;
        this.encoder_select = document.getElementById("encoder") as HTMLSelectElement;
        this.pointer_backend_select = document.getElementById("pointer_backend_select") as HTMLSelectElement;
        this.keyboard_backend_select = document.getElementById("keyboard_backend_select") as HTMLSelectElement;
        this.display_rotation_select = document.getElementById("display_rotation") as HTMLSelectElement;
        this.input_rotation_select = document.getElementById("input_rotation") as HTMLSelectElement;
        this.range_min_pressure = document.getElementById("min_pressure") as HTMLInputElement;
        this.client_name_input = document.getElementById("client_name") as HTMLInputElement;
        this.frame_rate_input.oninput = () => {
            this.frame_rate_output.value = Math.round(frame_rate_scale(this.frame_rate_input.valueAsNumber)).toString();
        }
        this.scale_video_input.oninput = () => {
            let [w, h] = calc_max_video_resolution(this.scale_video_input.valueAsNumber)
            this.scale_video_output.value = w + "x" + h
        }
        this.visible = true;

        // Settings UI
        this.settings = document.getElementById("settings");
        this.settings.onclick = (e) => e.stopPropagation();
        let handle = document.getElementById("handle");

        // Settings elements
        this.settings.querySelectorAll("input[type=checkbox]").forEach(
            (elem, _key, _parent) => this.checks.set(elem.id, elem as HTMLInputElement)
        );

        this.load_settings();

        // event handling

        // client only
        handle.onclick = () => { this.toggle() };
        this.checks.get("lefty").onchange = (e) => {
            if ((e.target as HTMLInputElement).checked)
                this.settings.classList.add("lefty");
            else
                this.settings.classList.remove("lefty");
            this.save_settings();
        }

        document.getElementById("vanish").onclick = () => {
            this.settings.classList.add("vanish");
        }

        this.checks.get("stretch").onchange = () => {
            stretch_video();
            this.save_settings();
        };
        this.display_rotation_select.onchange = () => {
            stretch_video();
            this.save_settings();
        };
        this.input_rotation_select.onchange = () => {
            this.save_settings();
        };

        this.checks.get("enable_debug_overlay").onchange = (e) => {
            let enabled = (e.target as HTMLInputElement).checked;
            if (enabled) {
                debug_overlay.classList.remove("hide");
            } else {
                debug_overlay.classList.add("hide");
            }
            this.save_settings();
        };

        this.check_aggressive_seek = this.checks.get("aggressive_seeking");
        this.check_aggressive_seek.onchange = () => {
            this.save_settings();
        };

        this.checks.get("enable_video").onchange = (e) => {
            let enabled = (e.target as HTMLInputElement).checked;
            document.getElementById("video").classList.toggle("vanish", !enabled);
            document.getElementById("canvas").classList.toggle("vanish", enabled);
            this.save_settings();
            if (enabled) {
                this.webSocket.send('"ResumeVideo"');
            } else {
                this.webSocket.send('"PauseVideo"');
            }
        }

        let upd_pointer = () => {
            this.save_settings();
            new PointerHandler(this.webSocket);
        }
        this.checks.get("enable_mouse").onchange = upd_pointer;
        this.checks.get("enable_stylus").onchange = upd_pointer;
        this.checks.get("enable_touch").onchange = upd_pointer;

        this.checks.get("energysaving").onchange = (e) => {
            this.save_settings();
            this.toggle_energysaving((e.target as HTMLInputElement).checked);
        };

        if (this.checks.get("enable_custom_input_areas")) {
            this.checks.get("enable_custom_input_areas").onchange = () => {
                this.save_settings();
            };
        }
        this.checks.get("virtual_cursor").onchange = () => {
            this.save_settings();
            if (!this.checks.get("virtual_cursor").checked)
                virtualCursor?.hide();
        };

        this.frame_rate_input.onchange = () => this.save_settings();
        this.range_min_pressure.onchange = () => this.save_settings();

        // server
        let upd_server_config = () => { this.save_settings(); this.send_server_config() };
        if (this.checks.get("uinput_support"))
            this.checks.get("uinput_support").onchange = upd_server_config;
        this.checks.get("capture_cursor").onchange = upd_server_config;
        this.scale_video_input.onchange = upd_server_config;
        this.encoder_select.onchange = upd_server_config;
        this.pointer_backend_select.onchange = upd_server_config;
        this.keyboard_backend_select.onchange = upd_server_config;
        this.client_name_input.onchange = upd_server_config;
        this.frame_rate_input.onchange = upd_server_config;

        document.getElementById("refresh").onclick = () => this.webSocket.send('"GetCapturableList"');
        let custom_input_areas = document.getElementById("custom_input_areas");
        if (custom_input_areas) {
            custom_input_areas.onclick = () => {
                this.webSocket.send('"ChooseCustomInputAreas"');
            };
        }
        this.capturable_select.onchange = () => {
            this.send_server_config();
            focusRemoteInputSurface(inputCapture?.keyboard || inputCapture?.pointer);
        };
    }

    send_server_config() {
        if (this.capturable_select.value === "")
            return;
        let config = new Object(null);
        config["capturable_id"] = Number(this.capturable_select.value);
        const pointer_backend = this.pointer_backend_select.value || "auto";
        const keyboard_backend = this.keyboard_backend_select.value || "auto";
        config["uinput_support"] = pointer_backend !== "xtest" && keyboard_backend !== "xtest";
        config["pointer_backend"] = pointer_backend;
        config["keyboard_backend"] = keyboard_backend;
        config["capture_cursor"] = this.checks.get("capture_cursor").checked;
        let [w, h] = calc_max_video_resolution(this.scale_video_input.valueAsNumber);
        config["max_width"] = w;
        config["max_height"] = h;
        config["frame_rate"] = frame_rate_scale(this.frame_rate_input.valueAsNumber);
        config["encoder"] = this.encoder_select.value || "auto";
        if (this.client_name_input.value)
            config["client_name"] = this.client_name_input.value;
        this.webSocket.send(JSON.stringify({ "Config": config }));
    }

    save_settings() {
        let settings = Object(null);
        for (const [key, elem] of this.checks.entries())
            settings[key] = elem.checked;
        settings["frame_rate"] = frame_rate_scale(this.frame_rate_input.valueAsNumber).toString();
        settings["scale_video"] = this.scale_video_input.value;
        settings["encoder"] = this.encoder_select.value;
        settings["pointer_backend"] = this.pointer_backend_select.value;
        settings["keyboard_backend"] = this.keyboard_backend_select.value;
        settings["display_rotation"] = this.display_rotation_select.value;
        settings["input_rotation"] = this.input_rotation_select.value;
        settings["min_pressure"] = this.range_min_pressure.value;
        settings["custom_input_areas"] = this.custom_input_areas;
        settings["client_name"] = this.client_name_input.value;
        localStorage.setItem("settings", JSON.stringify(settings));
    }

    load_settings() {
        let settings_string = localStorage.getItem("settings");
        if (settings_string === null) {
            this.frame_rate_input.value = frame_rate_scale_inv(30).toString();
            this.frame_rate_output.value = (30).toString();
            let [w, h] = calc_max_video_resolution(this.scale_video_input.valueAsNumber)
            this.scale_video_output.value = w + "x" + h;
            return;
        }
        try {
            let settings = JSON.parse(settings_string);
            for (const [key, elem] of this.checks.entries()) {
                if (typeof settings[key] === "boolean")
                    elem.checked = settings[key];
            }
            let upd_limit = settings["frame_rate"];
            if (upd_limit)
                this.frame_rate_input.value = frame_rate_scale_inv(upd_limit).toString();
            else
                this.frame_rate_input.value = frame_rate_scale_inv(30).toString();
            this.frame_rate_output.value = Math.round(frame_rate_scale(this.frame_rate_input.valueAsNumber)).toString();

            let scale_video = settings["scale_video"];
            if (scale_video)
                this.scale_video_input.value = scale_video;
            let [w, h] = calc_max_video_resolution(this.scale_video_input.valueAsNumber);
            this.scale_video_output.value = w + "x" + h;

            if (settings["encoder"])
                this.encoder_select.value = settings["encoder"];
            const legacy_input_backend = settings["input_backend"];
            if (settings["pointer_backend"])
                this.pointer_backend_select.value = settings["pointer_backend"];
            else if (legacy_input_backend)
                this.pointer_backend_select.value = legacy_input_backend;
            if (settings["keyboard_backend"])
                this.keyboard_backend_select.value = settings["keyboard_backend"];
            else if (legacy_input_backend)
                this.keyboard_backend_select.value = legacy_input_backend;
            this.display_rotation_select.value = normalized_rotation(settings["display_rotation"] || "0").toString();
            this.input_rotation_select.value = normalized_rotation(settings["input_rotation"] || "0").toString();

            let min_pressure = settings["min_pressure"];
            if (min_pressure)
                this.range_min_pressure.value = min_pressure;

            this.custom_input_areas = settings["custom_input_areas"];

            if (this.checks.get("lefty").checked) {
                this.settings.classList.add("lefty");
            }

            if (!this.checks.get("enable_video").checked || this.checks.get("energysaving").checked) {
                this.checks.get("enable_video").checked = false;
                if (this.checks.get("energysaving").checked)
                    this.checks.get("enable_video").disabled = true;
                document.getElementById("video").classList.add("vanish");
                document.getElementById("canvas").classList.remove("vanish");
            }

            if (this.checks.get("energysaving").checked) {
                this.toggle_energysaving(true);
            }

            if (this.checks.get("enable_debug_overlay").checked) {
                debug_overlay.classList.remove("hide");
            }


            let custom_input_areas = document.getElementById("custom_input_areas");
            if (custom_input_areas && custom_input_areas.classList.contains("hide")) {
                this.checks.get("enable_custom_input_areas").checked = false;
            }

            let client_name = settings["client_name"];
            if (client_name)
                this.client_name_input.value = client_name;

        } catch {
            log(LogLevel.DEBUG, "Failed to load settings.")
            return;
        }
    }

    stretched_video() {
        return this.checks.get("stretch").checked
    }

    pointer_types() {
        let ptrs = [];
        if (this.checks.get("enable_mouse").checked)
            ptrs.push("mouse");
        if (this.checks.get("enable_stylus").checked)
            ptrs.push("pen");
        if (this.checks.get("enable_touch").checked)
            ptrs.push("touch");
        return ptrs;
    }

    toggle() {
        this.settings.classList.toggle("hide");
        this.visible = !this.visible;
    }

    onCapturableList(window_names: string[]) {
        let current_selection = undefined;
        if (this.capturable_select.selectedOptions[0])
            current_selection = this.capturable_select.selectedOptions[0].textContent;
        let new_index: number;
        this.capturable_select.innerText = "";
        window_names.forEach((name, i) => {
            let option = document.createElement("option");
            option.value = String(i);
            option.innerText = name;
            this.capturable_select.appendChild(option);
            if (name === current_selection)
                new_index = i;
        });
        if (new_index !== undefined && /\(DXGI\)$/.test(String(current_selection))) {
            let auto_index = window_names.findIndex((name) => name === String(current_selection).replace(/\(DXGI\)$/, "(AUTO)"));
            if (auto_index >= 0)
                new_index = auto_index;
        }
        if (new_index !== undefined)
            this.capturable_select.value = String(new_index);
        else if (window_names.length > 0)
            this.capturable_select.value = "0";
        else
            this.capturable_select.value = "";
        if (this.capturable_select.value !== "")
            this.send_server_config();
    }

    toggle_energysaving(energysaving: boolean) {
        let canvas = fresh_canvas();
        if (energysaving) {
            let ctx = canvas.getContext("2d");
            ctx.fillStyle = "#000";
            ctx.fillRect(0, 0, canvas.width, canvas.height);
        }

        if (energysaving) {
            this.checks.get("enable_video").checked = false;
            this.checks.get("enable_video").disabled = true;
            this.checks.get("enable_video").dispatchEvent(new Event("change"));
        } else
            this.checks.get("enable_video").disabled = false;
        if (settings)
            new PointerHandler(this.webSocket);
    }

    video_enabled(): boolean {
        return this.checks.get("enable_video").checked;
    }

    update_encoder_options(options: { value: string, label: string }[]) {
        const current = this.encoder_select.value || "auto";
        this.encoder_select.innerText = "";
        for (const option of options) {
            if (!option || !option.value)
                continue;
            const elem = document.createElement("option");
            elem.value = option.value;
            elem.innerText = option.label || option.value;
            this.encoder_select.appendChild(elem);
        }
        if ([...this.encoder_select.options].some((option) => option.value === current))
            this.encoder_select.value = current;
        else
            this.encoder_select.value = "auto";
        this.save_settings();
    }

    update_input_options(
        options: { value: string, label: string }[],
        pointerOptions?: { value: string, label: string }[],
        keyboardOptions?: { value: string, label: string }[],
    ) {
        const pointer_options = pointerOptions || options;
        // Backwards compatibility for old agents that only send one shared list.
        const keyboard_options = keyboardOptions || options.filter((option) => option && option.value !== "wlroots-pointer");
        this.refresh_backend_select(this.pointer_backend_select, pointer_options);
        this.refresh_backend_select(this.keyboard_backend_select, keyboard_options);
        this.save_settings();
    }

    refresh_backend_select(select: HTMLSelectElement, options: { value: string, label: string }[]) {
        const current = select.value || "auto";
        select.innerText = "";
        for (const option of options) {
            if (!option || !option.value)
                continue;
            const elem = document.createElement("option");
            elem.value = option.value;
            elem.innerText = option.label || option.value;
            select.appendChild(elem);
        }
        if ([...select.options].some((option) => option.value === current))
            select.value = current;
        else
            select.value = "auto";
    }
}

let settings: Settings;
let debug_overlay: HTMLElement;
let last_pointer_data: Object;

function pointerButtonMask(button: number): number {
    let btn = button;
    // for some reason the secondary and auxiliary buttons are ordered differently for
    // the button and buttons properties
    if (btn == 2)
        btn = 1;
    else if (btn == 1)
        btn = 2;
    return btn < 0 ? 0 : 1 << btn;
}

class PEvent {
    event_type: string;
    pointer_id: number;
    timestamp: number;
    is_primary: boolean;
    pointer_type: string;
    button: number;
    buttons: number;
    x: number;
    y: number;
    movement_x: number;
    movement_y: number;
    pressure: number;
    tilt_x: number;
    tilt_y: number;
    twist: number;
    width: number;
    height: number;

    constructor(
        eventType: string,
        event: PointerEvent,
        targetRect: DOMRect,
        lockedPoint?: [number, number],
        buttonOverride?: number,
        buttonsOverride?: number,
    ) {
        let diag_len = Math.sqrt(targetRect.width * targetRect.width + targetRect.height * targetRect.height)
        this.event_type = eventType.toString();
        this.pointer_id = event.pointerId;
        this.timestamp = Math.round(event.timeStamp * 1000);
        this.is_primary = event.isPrimary;
        this.pointer_type = event.pointerType;
        this.button = buttonOverride ?? pointerButtonMask(event.button);
        this.buttons = buttonsOverride ?? event.buttons;
        let x_offset = 0;
        let y_offset = 0;
        let x_scale = 1;
        let y_scale = 1;
        if (settings.checks.get("enable_custom_input_areas")?.checked && settings.custom_input_areas) {
            let custom_input_area: Rect = null;
            if (event.pointerType == "mouse") {
                custom_input_area = settings.custom_input_areas.mouse;
            } else if (event.pointerType == "touch") {
                custom_input_area = settings.custom_input_areas.touch;
            } else if (event.pointerType == "pen") {
                custom_input_area = settings.custom_input_areas.pen;
            }
            if (custom_input_area) {
                x_scale = custom_input_area.w;
                y_scale = custom_input_area.h;
                x_offset = custom_input_area.x;
                y_offset = custom_input_area.y;
            }
        }
        if (lockedPoint) {
            this.x = lockedPoint[0] * x_scale + x_offset;
            this.y = lockedPoint[1] * y_scale + y_offset;
        } else {
            this.x = (event.clientX - targetRect.left) / targetRect.width * x_scale + x_offset;
            this.y = (event.clientY - targetRect.top) / targetRect.height * y_scale + y_offset;
        }
        [this.x, this.y] = transform_video_point(this.x, this.y, get_input_rotation());
        this.movement_x = event.movementX ? event.movementX : 0;
        this.movement_y = event.movementY ? event.movementY : 0;
        this.pressure = Math.max(event.pressure, settings.range_min_pressure.valueAsNumber);
        this.tilt_x = event.tiltX;
        this.tilt_y = event.tiltY;
        this.width = event.width / diag_len;
        this.height = event.height / diag_len;
        this.twist = event.twist;
    }
}

class WEvent {
    dx: number;
    dy: number;
    timestamp: number;

    constructor(event: WheelEvent) {
        /* The WheelEvent can have different scrolling modes that affect how much scrolling
         * should be done. Unfortunately there is not always a way to accurately convert the scroll
         * distance into pixels. Thus the following is a guesstimate and scales the WheelEvent's
         * deltaX/Y values accordingly.
         */
        let scale = 1;
        switch (event.deltaMode) {
            case 0x01: // DOM_DELTA_LINE
                scale = 10;
                break;
            case 0x02: // DOM_DELTA_PAGE
                scale = 1000;
                break;
            default: // DOM_DELTA_PIXEL
        }
        this.dx = Math.round(scale * event.deltaX);
        this.dy = -Math.round(scale * event.deltaY);
        this.timestamp = Math.round(event.timeStamp * 1000);
    }
}

// in milliseconds
const fade_time = 5000;

const vs_source = `
  attribute vec3 aVertex;
  uniform float uTime;
  varying lowp vec4 vColor;

  void main() {
    float dt = uTime - aVertex[2];
    gl_Position = vec4(aVertex[0], aVertex[1], 1.0, 1.0);
    vColor = vec4(0.0, 170.0/255.0, 1.0, 1.0) * max(1.0 - dt/${fade_time}.0, 0.0);
  }
`;

const fs_source = `
  varying lowp vec4 vColor;

  void main() {
    gl_FragColor = vColor;
  }
`;

class Painter {
    canvas: HTMLCanvasElement;
    gl: WebGLRenderingContext;

    /* Store lines currently being drawn.
     *
     * Keys are pointerIds, values are an array of the last position (x, y), thickness and event
     * time and another array with vertices to be used by webgl. Each vertex is made of 3 floats, x
     * and y coordinates and the event time. Vertices always come in pairs of two. Two such vertices
     * describe the edges of the line to be drawn with regard to it's thickness. TRIANGLE_STRIP is
     * then used to connect them and draw an actual line with some thickness depending on the
     * pressure applied.
     */
    lines_active: Map<number, [[number, number, number, number], number[]]>

    // Array of vertices that are not actively drawn anymore and do not need updates, except
    // removing them after they faded away.
    lines_old: number[][];
    vertex_attr: GLint;
    vertex_buffer: WebGLBuffer;
    time_attr: WebGLUniformLocation;
    initialized: boolean;

    constructor(canvas: HTMLCanvasElement) {
        this.canvas = canvas;
        canvas.width = window.innerWidth * window.devicePixelRatio;
        canvas.height = window.innerHeight * window.devicePixelRatio;
        this.gl = canvas.getContext("webgl");
        if (this.gl) {
            this.lines_active = new Map();
            this.lines_old = [];
            this.setupWebGL();
        }
    }

    loadShader(type, source): WebGLShader {
        let gl = this.gl;
        const shader = gl.createShader(type);
        gl.shaderSource(shader, source);
        gl.compileShader(shader);
        if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
            log(LogLevel.WARN, "Failed to compile shaders: " + gl.getShaderInfoLog(shader));
            gl.deleteShader(shader);
            return null;
        }
        return shader;
    }

    setupWebGL() {
        let gl = this.gl;
        gl.enable(gl.BLEND);
        gl.clearColor(0, 0, 0, 0);
        gl.clear(gl.COLOR_BUFFER_BIT);

        const vertex_shader = this.loadShader(gl.VERTEX_SHADER, vs_source);
        const fragment_shader = this.loadShader(gl.FRAGMENT_SHADER, fs_source);
        if (!vertex_shader || !fragment_shader)
            return;
        const shader_program = gl.createProgram();
        gl.attachShader(shader_program, vertex_shader);
        gl.attachShader(shader_program, fragment_shader);
        gl.linkProgram(shader_program);

        if (!gl.getProgramParameter(shader_program, gl.LINK_STATUS)) {
            log(LogLevel.WARN, "Unable to initialize the shader program: " + gl.getProgramInfoLog(shader_program));
            return;
        }
        this.vertex_attr = gl.getAttribLocation(shader_program, "aVertex");
        this.time_attr = gl.getUniformLocation(shader_program, "uTime");
        this.vertex_buffer = gl.createBuffer();
        gl.bindBuffer(gl.ARRAY_BUFFER, this.vertex_buffer);
        gl.vertexAttribPointer(this.vertex_attr, 3, gl.FLOAT, false, 0, 0);
        gl.enableVertexAttribArray(this.vertex_attr);
        gl.useProgram(shader_program);
        this.initialized = true;
        requestAnimationFrame(() => this.render());
    }

    render() {
        // only do work if necessary
        if (!check_video.checked && (this.lines_active.size > 0 || this.lines_old.length > 0)) {
            if (this.lines_old.length > 0) {
                if (performance.now() - this.lines_old[0][this.lines_old[0].length - 1] > fade_time)
                    this.lines_old.shift();
            }
            let gl = this.gl;
            gl.viewport(0, 0, this.canvas.width, this.canvas.height);
            gl.clear(gl.COLOR_BUFFER_BIT);
            gl.uniform1f(this.time_attr, performance.now());
            gl.bindBuffer(gl.ARRAY_BUFFER, this.vertex_buffer);
            for (let vertices of this.lines_old) {
                gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(vertices), gl.DYNAMIC_DRAW);
                gl.drawArrays(gl.TRIANGLE_STRIP, 0, vertices.length / 3)
            }
            for (let [_, vertices] of this.lines_active.values()) {
                // sometimes there are no linesegments because there has been only a single
                // PointerEvent
                if (vertices.length == 0)
                    continue;
                gl.bufferData(gl.ARRAY_BUFFER, new Float32Array(vertices), gl.DYNAMIC_DRAW);
                gl.drawArrays(gl.TRIANGLE_STRIP, 0, vertices.length / 3)
            }
        }
        requestAnimationFrame(() => this.render());
    }

    appendEventToLine(event: PointerEvent) {
        let line = this.lines_active.get(event.pointerId);
        if (!line) {
            line = [null, []];
            this.lines_active.set(event.pointerId, line)
        }
        let max_pixels = Math.max(this.canvas.width, this.canvas.height);
        let x = event.clientX * window.devicePixelRatio / this.canvas.width * 2 - 1;
        let y = 1 - event.clientY * window.devicePixelRatio / this.canvas.height * 2;
        let delta = event.pressure + 0.4;
        let t = performance.now();
        // to draw a line segment, there has to be some previous position
        if (line[0]) {
            let [x0, y0, delta0, t0] = line[0];
            // get vector perpendicular to the linesegment to calculate quadrangel around the
            // segment with appropriate thickness
            let dx = (y - y0);
            let dy = -(x - x0);
            let dd = Math.sqrt(dx ** 2 + dy ** 2);
            if (dd == 0) {
                return;
            }
            dx = dx / dd * max_pixels / this.canvas.width * 0.004;
            dy = dy / dd * max_pixels / this.canvas.height * 0.004;

            if (line[1].length == 0)
                line[1].push(
                    x0 + delta0 * dx, y0 + delta0 * dy, t0, x0 - delta0 * dx, y0 - delta0 * dy, t0,
                );
            line[1].push(
                x + delta * dx, y + delta * dy, t, x - delta * dx, y - delta * dy, t
            )
        }
        line[0] = [x, y, delta, t];
    }

    onstart(event: PointerEvent) {
        this.appendEventToLine(event);
    }

    onmove(event: PointerEvent) {
        if (this.lines_active.has(event.pointerId)) {
            const events = getPointerEvents(event);
            for (const e of events) {
                this.appendEventToLine(e);
            }
        }
    }

    onstop(event: PointerEvent) {
        let lines = this.lines_active.get(event.pointerId);
        if (lines) {
            if (lines[1].length > 0)
                this.lines_old.push(lines[1]);
            this.lines_active.delete(event.pointerId);
        }
    }
}

class PointerHandler {
    webSocket: WebSocket;
    pointerTypes: string[];
    lockedMouseX: number = null;
    lockedMouseY: number = null;
    lockedClientX: number = null;
    lockedClientY: number = null;
    lockedButtons: number = 0;

    constructor(webSocket: WebSocket) {
        let video = document.getElementById("video");
        let canvas = document.getElementById("canvas");
        this.webSocket = webSocket;
        this.pointerTypes = settings.pointer_types();
        focusRemoteInputSurface(inputCapture?.pointer);

        video.onpointerdown = (e) => this.onEvent(e, "pointerdown");
        video.onpointerup = (e) => this.onEvent(e, "pointerup");
        video.onpointercancel = (e) => this.onEvent(e, "pointercancel");
        video.onpointermove = (e) => this.onEvent(e, "pointermove");
        video.onpointerout = (e) => this.onEvent(e, "pointerout");
        video.onpointerleave = (e) => this.onEvent(e, "pointerleave");
        video.onpointerenter = (e) => this.onEvent(e, "pointerenter");
        video.onpointerover = (e) => this.onEvent(e, "pointerover");

        let painter: Painter;
        if (!settings.checks.get("energysaving").checked)
            painter = new Painter(canvas as HTMLCanvasElement);

        if (painter && painter.initialized) {
            canvas.onpointerdown = (e) => { this.onEvent(e, "pointerdown"); painter.onstart(e); };
            canvas.onpointerup = (e) => { this.onEvent(e, "pointerup"); painter.onstop(e); };
            canvas.onpointercancel = (e) => { this.onEvent(e, "pointercancel"); painter.onstop(e); };
            canvas.onpointermove = (e) => { this.onEvent(e, "pointermove"); painter.onmove(e); };
            canvas.onpointerout = (e) => { this.onEvent(e, "pointerout"); painter.onstop(e); };
            canvas.onpointerleave = (e) => { this.onEvent(e, "pointerleave"); painter.onstop(e); };
            canvas.onpointerenter = (e) => { this.onEvent(e, "pointerenter"); painter.onmove(e); };
            canvas.onpointerover = (e) => { this.onEvent(e, "pointerover"); painter.onmove(e); };
        } else {
            canvas.onpointerdown = (e) => this.onEvent(e, "pointerdown");
            canvas.onpointerup = (e) => this.onEvent(e, "pointerup");
            canvas.onpointercancel = (e) => this.onEvent(e, "pointercancel");
            canvas.onpointermove = (e) => this.onEvent(e, "pointermove");
            canvas.onpointerout = (e) => this.onEvent(e, "pointerout");
            canvas.onpointerleave = (e) => this.onEvent(e, "pointerleave");
            canvas.onpointerenter = (e) => this.onEvent(e, "pointerenter");
            canvas.onpointerover = (e) => this.onEvent(e, "pointerover");
        }

        // This is a workaround for the following Safari/WebKit bug:
        // https://bugs.webkit.org/show_bug.cgi?id=217430
        // I have no idea why this works but it does.
        video.ontouchmove = (e) => e.preventDefault();
        canvas.ontouchmove = (e) => e.preventDefault();

        for (let elem of [video, canvas]) {
            elem.addEventListener("wheel", (e) => {
                e.preventDefault();
                e.stopPropagation();
                this.webSocket.send(JSON.stringify({ "WheelEvent": new WEvent(e) }));
            }, { passive: false });
        }
    }

    lockedMouseActive(event: PointerEvent): boolean {
        return !!inputCapture?.pointer && isPointerLocked() && event.pointerType === "mouse";
    }

    lockedMousePoint(event: PointerEvent, eventType: string, rect: DOMRect): [number, number] | undefined {
        if (!this.lockedMouseActive(event)) {
            this.lockedMouseX = null;
            this.lockedMouseY = null;
            this.lockedClientX = null;
            this.lockedClientY = null;
            this.lockedButtons = 0;
            if (event.pointerType === "mouse")
                virtualCursor?.hide();
            return undefined;
        }
        if (this.lockedMouseX === null || this.lockedMouseY === null) {
            this.lockedMouseX = (event.clientX - rect.left) / rect.width;
            this.lockedMouseY = (event.clientY - rect.top) / rect.height;
            this.lockedClientX = event.clientX;
            this.lockedClientY = event.clientY;
        } else if (eventType === "pointermove") {
            this.lockedMouseX += event.movementX / rect.width;
            this.lockedMouseY += event.movementY / rect.height;
            this.lockedClientX += event.movementX;
            this.lockedClientY += event.movementY;
        }
        this.lockedMouseX = Math.max(0, Math.min(1, this.lockedMouseX));
        this.lockedMouseY = Math.max(0, Math.min(1, this.lockedMouseY));
        this.lockedClientX = Math.max(rect.left, Math.min(rect.right, this.lockedClientX));
        this.lockedClientY = Math.max(rect.top, Math.min(rect.bottom, this.lockedClientY));
        virtualCursor?.show(this.lockedClientX, this.lockedClientY);
        return [this.lockedMouseX, this.lockedMouseY];
    }

    lockedButtonState(event: PointerEvent, eventType: string): [number, number] {
        const button = pointerButtonMask(event.button);
        if (eventType === "pointerdown") {
            this.lockedButtons |= button;
        } else if (eventType === "pointerup" || eventType === "pointercancel") {
            this.lockedButtons &= ~button;
        }
        return [button, this.lockedButtons];
    }

    onEvent(event: PointerEvent, event_type: string) {
        if (!socketOpen(this.webSocket)) {
            if (pointer_backend_out)
                pointer_backend_out.value = `websocket ${socketStateLabel(this.webSocket)}，指针事件未发送`;
            return;
        }
        if (settings.checks.get("enable_debug_overlay").checked) {
            let props = [
                "altKey",
                "altitudeAngle",
                "azimuthAngle",
                "button",
                "buttons",
                "clientX",
                "clientY",
                "ctrlKey",
                "height",
                "isPrimary",
                "metaKey",
                "movementX",
                "movementY",
                "offsetX",
                "offsetY",
                "pageX",
                "pageY",
                "pointerId",
                "pointerType",
                "pressure",
                "screenX",
                "screenY",
                "shiftKey",
                "tangentialPressure",
                "tiltX",
                "tiltY",
                "timeStamp",
                "twist",
                "type",
                "width",
                "x",
                "y",
            ];
            if (!last_pointer_data) {
                last_pointer_data = {};
                for (let prop of props) {
                    let span_id = `prop_${prop}_span`;
                    let span = document.getElementById(span_id);
                    span = document.createElement("span");
                    span.id = span_id;
                    debug_overlay.appendChild(span);
                    debug_overlay.appendChild(document.createElement("br"));
                }
            }
            for (let prop of props) {
                let span_id = `prop_${prop}_span`;
                let span = document.getElementById(span_id);
                let v = event[prop];
                span.textContent = `${prop}: ${v}`;
                if (last_pointer_data[prop] == v) {
                    span.classList.remove("updated");
                } else {
                    span.classList.add("updated");
                    last_pointer_data[prop] = v;
                }
            }
        }
        if (this.pointerTypes.includes(event.pointerType)) {
            focusRemoteInputSurface(inputCapture?.pointer);
            if (inputCapture?.pointer && event_type === "pointerdown")
                requestPointerLock();
            const currentTarget = event.currentTarget as HTMLElement;
            let rect = currentTarget.getBoundingClientRect();
            const locked = this.lockedMouseActive(event);
            if (locked && ["pointerover", "pointerenter", "pointerout", "pointerleave"].includes(event_type)) {
                event.preventDefault();
                event.stopPropagation();
                return;
            }
            const events = event_type === "pointermove" ? getPointerEvents(event) : [event];
            if (event_type === "pointerdown") {
                capturePointer(event);
            } else if ((event_type === "pointerup" || event_type === "pointercancel") && !locked) {
                releasePointer(event);
            }
            pointer_event_count += 1;
            if (pointer_backend_out) {
                pointer_backend_out.value = `web send #${pointer_event_count} ${event_type} type=${event.pointerType} button=${event.button} buttons=${event.buttons} x=${event.x.toFixed(0)} y=${event.y.toFixed(0)}`;
            }
            for (let event of events) {
                const lockedPoint = this.lockedMousePoint(event, event_type, rect);
                const [buttonOverride, buttonsOverride] = lockedPoint
                    ? this.lockedButtonState(event, event_type)
                    : [undefined, undefined];
                if (!lockedPoint && event.pointerType === "mouse") {
                    if (settings.checks.get("virtual_cursor").checked && inputCapture?.pointer)
                        virtualCursor?.show(event.clientX, event.clientY);
                    else
                        virtualCursor?.hide();
                }
                this.webSocket.send(
                    JSON.stringify(
                        {
                            "PointerEvent": new PEvent(
                                event_type,
                                event,
                                rect,
                                lockedPoint,
                                buttonOverride,
                                buttonsOverride,
                            )
                        }
                    )
                );
            }
            event.preventDefault();
            event.stopPropagation();
            if (settings.visible) {
                settings.toggle();
            }
        }
    }
}

class KEvent {
    event_type: string;
    code: string;
    key: string;
    location: number;
    alt: boolean;
    ctrl: boolean;
    shift: boolean;
    meta: boolean;

    constructor(event_type: string, event: KeyboardEvent) {
        this.event_type = event_type;
        this.code = event.code;
        this.key = event.key;
        this.location = event.location;
        this.alt = event.altKey;
        this.ctrl = event.ctrlKey;
        this.shift = event.shiftKey;
        this.meta = event.metaKey;
    }
}

class TextInputEventMessage {
    text: string;

    constructor(text: string) {
        this.text = text;
    }
}

class KeyboardHandler {
    webSocket: WebSocket;
    heldKeys: Map<string, KEvent>;
    composing: boolean;

    constructor(webSocket: WebSocket) {
        this.webSocket = webSocket;
        this.heldKeys = new Map();
        this.composing = false;
        if (inputCapture)
            inputCapture.onKeyboardRelease = () => this.releaseHeldKeys();

        function should_keep_local(event: KeyboardEvent) {
            return isEditableTarget(event.target);
        }

        const handleKeyDown = (e: KeyboardEvent) => {
            if (this.isReleaseChord(e)) {
                e.preventDefault();
                e.stopPropagation();
                inputCapture?.releaseAll();
                return;
            }
            if (!inputCapture?.keyboard && should_keep_local(e))
                return;
            if (!inputCapture?.keyboard && (e.isComposing || this.composing || e.key === "Process"))
                return;
            if (e.repeat)
                this.onEvent(e, "repeat");
            else
                this.onEvent(e, "down");
        };
        const handleKeyUp = (e: KeyboardEvent) => {
            if (!inputCapture?.keyboard && should_keep_local(e))
                return;
            if (!inputCapture?.keyboard && e.key === "Process")
                return;
            this.onEvent(e, "up");
        };
        const handleKeyPress = (e: KeyboardEvent) => {
            if (!inputCapture?.keyboard && should_keep_local(e))
                return;
            e.preventDefault();
            e.stopPropagation();
        };
        const handleBeforeInput = (e: InputEvent) => {
            if (!inputCapture?.keyboard && isEditableTarget(e.target))
                return;
            if (inputCapture?.keyboard) {
                e.preventDefault();
                e.stopPropagation();
                return;
            }
            if (e.inputType !== "insertText" && e.inputType !== "insertLineBreak")
                return;
            const text = e.inputType === "insertLineBreak" ? "\n" : e.data;
            if (text)
                this.flushTextInput(text);
            e.preventDefault();
            e.stopPropagation();
        };
        const handlePaste = (e: ClipboardEvent) => {
            if (!inputCapture?.keyboard && isEditableTarget(e.target))
                return;
            const text = e.clipboardData?.getData("text") || "";
            if (!text)
                return;
            this.flushTextInput(text);
            e.preventDefault();
            e.stopPropagation();
        };
        const handleCompositionStart = () => {
            this.composing = true;
        };
        const handleCompositionEnd = (e: CompositionEvent) => {
            this.composing = false;
            if (!inputCapture?.keyboard && isEditableTarget(e.target))
                return;
            if (inputCapture?.keyboard) {
                e.preventDefault();
                e.stopPropagation();
                return;
            }
            if (e.data)
                this.flushTextInput(e.data);
            e.preventDefault();
            e.stopPropagation();
        };

        window.addEventListener("focus", () => focusRemoteInputSurface(), true);
        window.addEventListener("blur", () => this.releaseHeldKeys(), true);
        document.addEventListener("visibilitychange", () => {
            if (document.hidden)
                this.releaseHeldKeys();
        }, true);
        document.addEventListener("pointerdown", () => focusRemoteInputSurface(), true);
        window.addEventListener("keydown", handleKeyDown, true);
        window.addEventListener("keyup", handleKeyUp, true);
        window.addEventListener("keypress", handleKeyPress, true);
        window.addEventListener("beforeinput", handleBeforeInput, true);
        window.addEventListener("paste", handlePaste, true);
        window.addEventListener("compositionstart", handleCompositionStart, true);
        window.addEventListener("compositionend", handleCompositionEnd, true);
        focusRemoteInputSurface();
    }

    isReleaseChord(event: KeyboardEvent) {
        return (inputCapture?.keyboard || inputCapture?.pointer)
            && event.code === "KeyK"
            && event.ctrlKey
            && event.altKey
            && event.shiftKey;
    }

    keyId(event: KeyboardEvent | KEvent) {
        return `${event.code}:${event.location}`;
    }

    releaseEventFrom(event: KEvent): KEvent {
        const up = Object.assign(Object.create(KEvent.prototype), event) as KEvent;
        up.event_type = "up";
        up.ctrl = false;
        up.alt = false;
        up.shift = false;
        up.meta = false;
        return up;
    }

    modifierReleaseEvents(): KEvent[] {
        const mods: [string, string, number][] = [
            ["ControlLeft", "Control", 1],
            ["ControlRight", "Control", 2],
            ["AltLeft", "Alt", 1],
            ["AltRight", "Alt", 2],
            ["ShiftLeft", "Shift", 1],
            ["ShiftRight", "Shift", 2],
            ["MetaLeft", "Meta", 1],
            ["MetaRight", "Meta", 2],
        ];
        return mods.map(([code, key, location]) => Object.assign(Object.create(KEvent.prototype), {
            event_type: "up",
            code,
            key,
            location,
            ctrl: false,
            alt: false,
            shift: false,
            meta: false,
        }) as KEvent);
    }

    sendKeyboardEvent(event: KEvent) {
        this.webSocket.send(JSON.stringify({ "KeyboardEvent": event }));
    }

    sendKeyboardReleaseRequest() {
        if (socketOpen(this.webSocket))
            this.webSocket.send('"ReleaseKeyboard"');
    }

    flushTextInput(text: string) {
        if (!text)
            return;
        if (!socketOpen(this.webSocket)) {
            if (keyboard_backend_out)
                keyboard_backend_out.value = `websocket ${socketStateLabel(this.webSocket)}，文本输入未发送`;
            return;
        }
        text_input_event_count += 1;
        if (keyboard_backend_out) {
            const preview = text.length > 16 ? text.substring(0, 16) + "..." : text;
            keyboard_backend_out.value = `web send text #${text_input_event_count} len=${text.length} text=${preview}`;
        }
        this.webSocket.send(JSON.stringify({ "TextInputEvent": new TextInputEventMessage(text) }));
    }

    releaseHeldKeys() {
        if (!socketOpen(this.webSocket)) {
            this.heldKeys.clear();
            return;
        }
        if (this.heldKeys.size === 0) {
            this.sendKeyboardReleaseRequest();
            return;
        }
        const released = new Set<string>();
        for (const held of this.heldKeys.values()) {
            const up = this.releaseEventFrom(held);
            released.add(this.keyId(up));
            this.sendKeyboardEvent(up);
        }
        for (const up of this.modifierReleaseEvents()) {
            if (!released.has(this.keyId(up)))
                this.sendKeyboardEvent(up);
        }
        this.sendKeyboardReleaseRequest();
        this.heldKeys.clear();
        if (keyboard_backend_out)
            keyboard_backend_out.value = "web released held keys";
    }

    onEvent(event: KeyboardEvent, event_type: string) {
        if (!socketOpen(this.webSocket)) {
            if (keyboard_backend_out)
                keyboard_backend_out.value = `websocket ${socketStateLabel(this.webSocket)}，键盘事件未发送`;
            return false;
        }
        focusRemoteInputSurface(inputCapture?.keyboard);
        keyboard_event_count += 1;
        const kEvent = new KEvent(event_type, event);
        const keyId = this.keyId(kEvent);
        if (event_type === "down")
            this.heldKeys.set(keyId, kEvent);
        else if (event_type === "up")
            this.heldKeys.delete(keyId);
        if (keyboard_backend_out) {
            keyboard_backend_out.value = `web send #${keyboard_event_count} ${event_type} code=${event.code} key=${event.key}`;
        }
        this.sendKeyboardEvent(kEvent);
        event.preventDefault();
        event.stopPropagation();
        return false;
    }
}

function frame_rate_stats() {
    let t = performance.now();
    let fps = Math.round(frame_count / (t - last_fps_calc) * 10000) / 10;
    fps_out.value = fps.toString();
    frame_count = 0;
    last_fps_calc = t;
    setTimeout(() => frame_rate_stats(), 1500);
}

function handle_messages(
    webSocket: WebSocket,
    video: HTMLVideoElement,
    onConfigOk: Function,
    onConfigError: Function,
    onCapturableList: Function,
) {
    let mediaSource: MediaSource = null;
    let sourceBuffer: SourceBuffer = null;
    let queue = [];
    const MAX_BUFFER_LENGTH = 20;  // In seconds
    function upd_buf() {
        if (sourceBuffer == null)
            return;
        if (!sourceBuffer.updating && queue.length > 0 && mediaSource.readyState == "open") {
            let buffer_length = 0;
            if (sourceBuffer.buffered.length) {
                // Assume only one time range...
                buffer_length = sourceBuffer.buffered.end(0) - sourceBuffer.buffered.start(0);
            }
            if (buffer_length > MAX_BUFFER_LENGTH) {
                sourceBuffer.remove(0, sourceBuffer.buffered.end(0) - MAX_BUFFER_LENGTH / 2);
                // This will trigger updateend when finished
            } else {
                try {
                    sourceBuffer.appendBuffer(queue.shift());
                } catch (err) {
                    log(LogLevel.DEBUG, "Error appending to sourceBuffer:" + err);
                    // Drop everything, and try to pick up the stream again
                    if (sourceBuffer.updating)
                        sourceBuffer.abort();
                    sourceBuffer.remove(0, Infinity);
                }
            }
        }
    }
    webSocket.onmessage = (event: MessageEvent) => {
        if (typeof event.data == "string") {
            let msg = JSON.parse(event.data);
            if (typeof msg == "string") {
                if (msg == "NewVideo") {
                    let MS = window.ManagedMediaSource ? window.ManagedMediaSource : window.MediaSource;
                    mediaSource = new MS();
                    sourceBuffer = null;
                    video.src = URL.createObjectURL(mediaSource);
                    mediaSource.addEventListener("sourceopen", (_) => {
                        let mimeType = 'video/mp4; codecs="avc1.4D403D"';
                        if (!MS.isTypeSupported(mimeType))
                            mimeType = "video/mp4";
                        sourceBuffer = mediaSource.addSourceBuffer(mimeType);
                        sourceBuffer.addEventListener("updateend", upd_buf);
                        // try to recover from errors by restarting the video
                        if (sourceBuffer.onerror)
                            sourceBuffer.onerror = () => settings.send_server_config();
                    })
                } else if (msg == "ConfigOk") {
                    onConfigOk();
                }
            } else if (typeof msg == "object") {
                if ("CapturableList" in msg)
                    onCapturableList(msg["CapturableList"]);
                else if ("Error" in msg)
                    alert(msg["Error"]);
                else if ("ConfigError" in msg) {
                    onConfigError(msg["ConfigError"]);
                } else if ("CustomInputAreas" in msg) {
                    settings.custom_input_areas = msg["CustomInputAreas"];
                    if (settings.checks.get("enable_custom_input_areas"))
                        settings.checks.get("enable_custom_input_areas").checked = true;
                    settings.save_settings();
                } else if ("RuntimeStatus" in msg) {
                    const status = msg["RuntimeStatus"];
                    if (status["captureBackend"] !== undefined)
                        capture_backend_out.value = status["captureBackend"] || "未知";
                    if (status["encoderBackend"] !== undefined)
                        encoder_backend_out.value = status["encoderBackend"] || "未知";
                    if (status["inputBackend"] !== undefined)
                        input_backend_out.value = status["inputBackend"] || "未知";
                    if (status["pointerBackend"] !== undefined)
                        pointer_backend_out.value = status["pointerBackend"] || "未知";
                    if (status["keyboardBackend"] !== undefined)
                        keyboard_backend_out.value = status["keyboardBackend"] || "未知";
                } else if ("EncoderCapabilities" in msg) {
                    settings.update_encoder_options(msg["EncoderCapabilities"]["options"] || []);
                } else if ("InputCapabilities" in msg) {
                    const capabilities = msg["InputCapabilities"];
                    settings.update_input_options(
                        capabilities["options"] || [],
                        capabilities["pointerOptions"],
                        capabilities["keyboardOptions"],
                    );
                }
            }

            return;
        }

        // not a string -> got a video frame
        queue.push(event.data);
        upd_buf();
        frame_count += 1;

        // only seek if there is data available, some browsers choke otherwise
        if (video.seekable.length > 0) {
            let seek_time = video.seekable.end(video.seekable.length - 1);
            if (video.readyState >= (settings.check_aggressive_seek.checked ? 3 : 4)
                // but make sure to catch up if the video is more than 3 seconds behind
                || seek_time - video.currentTime > 3) {
                if (isFinite(seek_time))
                    video.currentTime = seek_time;
                else
                    log(LogLevel.WARN, "Failed to seek to end of video.")
            }

        }
    }
}

function check_apis() {
    let apis = [
        {
            attrs: ["MediaSource", "ManagedMediaSource"],
            msg: "This browser doesn't support MSE/MMS required to playback video stream, try upgrading!"
        },
        {
            attrs: ["PointerEvent"],
            msg: "This browser doesn't support PointerEvents, input will not work, try upgrading!"
        },
    ];

    outer:
    for (let d of apis) {
        for (let attr of d.attrs) {
            if (attr in window) {
                continue outer;
            }
        }
        log(LogLevel.ERROR, d.msg);
    }
}

function init() {
    check_apis();

    let protocol = document.location.protocol == "https:" ? "wss://" : "ws://";
    let webSocket = new WebSocket(
        protocol + window.location.hostname + ":" +
        window.location.port + "/ws" + window.location.search
    );
    webSocket.binaryType = "arraybuffer";

    debug_overlay = document.getElementById("debug_overlay");
    settings = new Settings(webSocket);
    virtualCursor = new VirtualCursor();
    inputCapture = new InputCaptureState();
    inputCapture.set(true, false);
    document.onpointerlockchange = () => {
        if (!isPointerLocked())
            virtualCursor.hide();
    };

    let video = document.getElementById("video") as HTMLVideoElement;
    let canvas = document.getElementById("canvas") as HTMLCanvasElement;

    video.oncontextmenu = function(event) {
        event.preventDefault();
        event.stopPropagation();
        return false;
    };
    canvas.oncontextmenu = function(event) {
        event.preventDefault();
        event.stopPropagation();
        return false;
    };

    let toggle_fullscreen_btn = document.getElementById("fullscreen") as HTMLButtonElement;

    if (document.exitFullscreen) {
        toggle_fullscreen_btn.onclick = () => {
            if (!document.fullscreenElement)
                document.body.requestFullscreen({ navigationUI: "hide" });
            else
                document.exitFullscreen();
        }
    } else {
        // if document.exitFullscreen is not present we are probably running on iOS/iPadOS.
        // As input is broken in fullscreen mode on these, do not offer fullscreen in the first
        // place.
        toggle_fullscreen_btn.parentElement.removeChild(toggle_fullscreen_btn);
    }

    let handle_disconnect = (msg: string) => {
        document.body.onclick = video.onclick = (e) => {
            e.stopPropagation();
            if (window.confirm(msg + " Reload page?"))
                location.reload();
        }
    }
    webSocket.onerror = () => handle_disconnect("Lost connection.");
    webSocket.onclose = () => handle_disconnect("Connection closed.");
    window.onresize = () => {
        stretch_video();
        canvas.width = window.innerWidth * window.devicePixelRatio;
        canvas.height = window.innerHeight * window.devicePixelRatio;
        let [w, h] = calc_max_video_resolution(settings.scale_video_input.valueAsNumber);
        settings.scale_video_output.value = w + "x" + h;
        settings.send_server_config();
    }
    video.controls = false;
    (video as HTMLVideoElementWithRemotePlayback).disableRemotePlayback = true;
    video.onloadeddata = () => stretch_video();
    let is_connected = false;
    handle_messages(webSocket, video, () => {
        if (!is_connected) {
            new KeyboardHandler(webSocket);
            new PointerHandler(webSocket);
            is_connected = true;
        }
    },
        (err) => alert(err),
        (window_names) => settings.onCapturableList(window_names)
    );
    window.onunload = () => { webSocket.close(); }
    webSocket.onopen = function(event) {
        webSocket.send('"GetCapturableList"');
        if (!settings.video_enabled())
            webSocket.send('"PauseVideo"');

        settings.send_server_config();

        document.onvisibilitychange = () => {
            if (document.hidden) {
                webSocket.send('"PauseVideo"');
            } else if (settings.video_enabled()) {
                webSocket.send('"ResumeVideo"');
            }
        };
    }
    frame_rate_stats();
}

// object-fit: fill; <-- this is unfortunately not supported on iOS, so we use the following
// workaround
function stretch_video() {
    let video = document.getElementById("video") as HTMLVideoElement;
    const rotation = get_display_rotation();
    const rotated = rotation === 90 || rotation === 270;
    const visualWidth = rotated ? video.clientHeight : video.clientWidth;
    const visualHeight = rotated ? video.clientWidth : video.clientHeight;
    if (visualWidth <= 0 || visualHeight <= 0)
        return;
    let scaleX: number;
    let scaleY: number;
    if (settings.stretched_video()) {
        scaleX = document.body.clientWidth / visualWidth;
        scaleY = document.body.clientHeight / visualHeight;
    } else {
        let scale = Math.min(document.body.clientWidth / visualWidth, document.body.clientHeight / visualHeight);
        scaleX = scale;
        scaleY = scale;
    }
    video.style.transform = `rotate(${rotation}deg) scaleX(${scaleX}) scaleY(${scaleY})`;
}
