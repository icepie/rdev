// @vitest-environment jsdom
import { act, createElement, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
const t = key => key;
const mounts = [];
function Pane({kind,device,onOpenFile}) {
  useEffect(()=>{mounts.push(`${kind}:${device}`)},[]);
  return createElement("div", {"data-pane":kind,"data-device":device,"data-agent-preview":!!onOpenFile});
}
vi.mock("../ui/lib/i18n",()=>({useI18n:()=>({t})}));
vi.mock("../ui/lib/devices",()=>({useDevices:()=>({devices:[{id:"one"},{id:"two"}]})}));
vi.mock("../ui/lib/server-capabilities",()=>({useServerCapabilities:()=>({browserEnabled:true,loading:false})}));
vi.mock("../ui/components/icon",()=>({Icon:()=>null}));
vi.mock("../ui/components/select",()=>({
 Select:({value,onValueChange,disabled,children})=>createElement("select",{value,disabled,onChange:e=>onValueChange(e.target.value)},createElement("option",{value:""},"Choose"),children),
 SelectTrigger:({children})=>children,SelectValue:()=>null,SelectContent:({children})=>children,
 SelectItem:({value,children,disabled})=>createElement("option",{value,disabled},children),
}));
vi.mock("../ui/components/agent/panel-terminal",()=>({PanelTerminal:props=>createElement(Pane,{...props,kind:"terminal"})}));
vi.mock("../ui/components/agent/panel-files",()=>({PanelFiles:props=>createElement(Pane,{...props,kind:"files"})}));
vi.mock("../ui/components/agent/panel-desktop",()=>({PanelDesktop:props=>createElement(Pane,{...props,kind:"desktop"})}));
vi.mock("../ui/components/browser-panel",()=>({BrowserPanel:props=>createElement(Pane,{...props,kind:"browser"})}));
vi.mock("../ui/components/tooltip",()=>({Tooltip:({children})=>children,TooltipTrigger:({children})=>children,TooltipContent:()=>null}));
import { AgentRightPanel } from "../ui/components/agent/right-panel";

it("keeps independent device targets and multiple workspaces in a server conversation",async()=>{
 globalThis.IS_REACT_ACT_ENVIRONMENT=true;
 vi.stubGlobal("ResizeObserver",class {observe(){} unobserve(){} disconnect(){}});
 mounts.length=0;
 const key="@server:thread";
 localStorage.setItem("rdev.agent.panel.tabs",JSON.stringify({[key]:{tabs:[{kind:"files",device:"one"},{kind:"desktop",device:"two"},{kind:"terminal",num:1,device:"one"},{kind:"browser",device:"two"}],active:{kind:"files"},recent:[]}}));
 const props={visible:true,connected:true,info:{device:"@server",thread:"thread",cwd:"",branch:"",model:""},threadKey:key,changes:null,diffs:{},fileStates:{},onRefresh:vi.fn(),onRequestDiff:vi.fn(),onRequestFile:vi.fn(),onSearchFiles:async()=>[],onAddToChat:vi.fn(),onHide:vi.fn()};
 const host=document.createElement("div");document.body.append(host);let root=createRoot(host);
 try {
  await act(()=>root.render(createElement(AgentRightPanel,props)));
  expect(mounts).toEqual(["files:one","desktop:two","terminal:one","browser:two"]);
  expect(host.querySelector('[data-pane="files"]').dataset.agentPreview).toBe("false");
  for(const label of ["agent.openFiles","agent.openDesktop","agent.newTerminal","nav.browser"]){
   await act(()=>host.querySelector('[aria-label="agent.openNewTab"]').click());
   await act(()=>[...host.querySelectorAll("button")].find(b=>b.textContent===label).click());
   const select=[...host.querySelectorAll("select")].find(s=>s.value==="");
   expect(select).toBeTruthy();
   await act(()=>{select.value="two";select.dispatchEvent(new Event("change",{bubbles:true}));});
  }
  expect(host.querySelectorAll('[data-pane="files"]')).toHaveLength(2);
  expect(host.querySelectorAll('[data-pane="desktop"]')).toHaveLength(2);
  expect(host.querySelectorAll('[data-pane="terminal"]')).toHaveLength(2);
  expect(host.querySelectorAll('[data-pane="browser"]')).toHaveLength(2);
  const count=mounts.length;
  await act(()=>root.render(createElement(AgentRightPanel,{...props,visible:false})));
  await act(()=>root.render(createElement(AgentRightPanel,props)));
  expect(mounts).toHaveLength(count);
  const stored=JSON.parse(localStorage.getItem("rdev.agent.panel.tabs"))[key];
  expect(stored.tabs).toContainEqual({kind:"files",num:2,device:"two"});
  expect(stored.tabs).toContainEqual({kind:"desktop",num:2,device:"two"});
  await act(()=>root.unmount()); root=createRoot(host);
  await act(()=>root.render(createElement(AgentRightPanel,props)));
  expect(host.querySelectorAll('[data-pane="desktop"]')).toHaveLength(2);
  expect([...host.querySelectorAll('[data-pane]')].some(p=>p.dataset.device==="@server")).toBe(false);
  await act(()=>root.render(createElement(AgentRightPanel,{...props,automation:{kind:"desktop",device:"one",source:"gpu:0",action:"screenshot",paused:false}})));
  expect([...host.querySelectorAll('[data-pane="desktop"]')].map(p=>p.dataset.device)).toEqual(["one","two"]);
  expect(host.textContent).not.toContain("automation.takeover");
  expect(host.textContent).not.toContain("automation.agent");
  expect(host.querySelector('[inert]')).toBeNull();
 } finally {await act(()=>root.unmount());host.remove();localStorage.clear();vi.unstubAllGlobals();delete globalThis.IS_REACT_ACT_ENVIRONMENT;}
});
