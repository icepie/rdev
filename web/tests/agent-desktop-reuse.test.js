// @vitest-environment jsdom
import { act, createElement, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
let mounted = 0, unmounted = 0;
const t = key => key;
vi.mock("../ui/lib/i18n", () => ({ useI18n: () => ({ t }) }));
vi.mock("../ui/lib/server-capabilities", () => ({ useServerCapabilities: () => ({ browserEnabled: true, loading: false }) }));
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/agent/panel-desktop", () => ({ PanelDesktop: () => { useEffect(() => { mounted++; return () => { unmounted++; }; }, []); return createElement("div", { "data-desktop": true }); } }));
vi.mock("../ui/components/browser-panel", () => ({ BrowserPanel: () => null }));
vi.mock("../ui/components/agent/panel-terminal", () => ({ PanelTerminal: () => null }));
vi.mock("../ui/components/tooltip", () => ({ Tooltip: ({children}) => children, TooltipTrigger: ({children}) => children, TooltipContent: () => null }));
import { AgentRightPanel } from "../ui/components/agent/right-panel";
it("keeps an already-open device desktop mounted across automation actions and source changes", async () => {
 globalThis.IS_REACT_ACT_ENVIRONMENT = true;
 vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} unobserve() {} });
 localStorage.setItem("rdev.agent.panel.tabs", JSON.stringify({ "device:thread": { tabs: [{ kind:"desktop" }], active:{kind:"desktop"},recent:[] } }));
 const host=document.createElement("div");document.body.append(host);const root=createRoot(host);
 const props={visible:true,connected:true,info:{device:"device",thread:"thread",cwd:"",branch:"",model:""},threadKey:"device:thread",changes:null,diffs:{},fileStates:{},onRefresh:vi.fn(),onRequestDiff:vi.fn(),onRequestFile:vi.fn(),onSearchFiles:async()=>[],onAddToChat:vi.fn(),onHide:vi.fn()};
 try {
 await act(()=>root.render(createElement(AgentRightPanel,props)));
 expect(mounted).toBe(1);
 for (const [action,source] of [["screenshot","gpu:0"],["click","gpu:0"],["screenshot","gpu:1"]]) {
 await act(()=>root.render(createElement(AgentRightPanel,{...props,automation:{kind:"desktop",device:"device",action,source,paused:false}})));
 expect(mounted).toBe(1);expect(unmounted).toBe(0);
 }
 } finally {await act(()=>root.unmount());host.remove();localStorage.clear();vi.unstubAllGlobals();delete globalThis.IS_REACT_ACT_ENVIRONMENT;}
});
