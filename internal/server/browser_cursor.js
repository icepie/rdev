/*
 * Adapted from Xetera/ghost-cursor src/mouse-helper.ts
 * https://github.com/Xetera/ghost-cursor
 * RDev: CDP-driven viewport coordinates, shadow-isolated arrow/click feedback,
 * no page mouse listeners, server-driven movement, explicit lifecycle disposal.
 *
 * MIT License
 * Copyright (c) 2021 Xetera
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in all
 * copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
 * SOFTWARE.
 */
(() => {
  if (window !== window.top || window.__rdevAgentCursor) return;
  const host = document.createElement('rdev-agent-cursor');
  host.setAttribute('aria-hidden', 'true');
  // Inline !important protects positioning from ordinary site styles. The
  // shadow tree isolates the drawing without intercepting page input.
  host.style.cssText = 'all:initial!important;position:fixed!important;left:0!important;top:0!important;width:0!important;height:0!important;pointer-events:none!important;z-index:2147483647!important;display:none!important;';
  const root = host.attachShadow({mode: 'closed'});
  root.innerHTML = `<style>
    :host { pointer-events:none; }
    svg { position:absolute;left:0;top:0;width:24px;height:28px;overflow:visible; }
    .ring { position:absolute;left:-15px;top:-15px;width:30px;height:30px;border:2px solid #2563eb;border-radius:50%;box-sizing:border-box;opacity:0; }
  </style><div class="ring"></div><svg viewBox="0 0 24 28"><path d="M1 1 L1 22 L7 17 L12 27 L17 24 L12 15 L22 15 Z" fill="#2563eb" stroke="white" stroke-width="2" stroke-linejoin="round"/></svg>`;
  const ring = root.querySelector('.ring');
  let pulse;
  const attach = () => {
    const parent = document.fullscreenElement || document.documentElement;
    if (parent && host.parentNode !== parent) parent.appendChild(host);
  };
  document.addEventListener('fullscreenchange', attach);
  window.__rdevAgentCursor = {
    place(x, y, click) {
      attach();
      host.style.setProperty('display', 'block', 'important');
      host.style.setProperty('left', `${x}px`, 'important');
      host.style.setProperty('top', `${y}px`, 'important');
      if (click) {
        pulse?.cancel();
        pulse = ring.animate([{opacity:1,transform:'scale(.5)'},{opacity:0,transform:'scale(1.5)'}], {duration:450});
      }
    },
    dispose() {
      pulse?.cancel();
      document.removeEventListener('fullscreenchange', attach);
      host.remove();
      delete window.__rdevAgentCursor;
    }
  };
})();
