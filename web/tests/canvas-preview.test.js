import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { expect, it } from "vitest";
import { CanvasPreview, canvasDocument } from "../ui/components/agent/canvas-preview";

it("isolates interactive HTML without granting host or network access", () => {
 const content = '<button onclick="this.textContent=2">Try</button>';
 const doc = canvasDocument(content, "dark");
 expect(doc).toContain('class="dark"');
 expect(doc).toContain("connect-src 'none'");
 expect(doc).toContain("form-action 'none'");
 const html = renderToStaticMarkup(createElement(CanvasPreview, {tool:{id:"canvas1", name:"canvas", args:JSON.stringify({title:"Report",format:"html",content}),result:'{"status":"ready"}',done:true},t:key=>key}));
 expect(html).toContain('sandbox="allow-scripts"');
 expect(html).not.toContain("allow-same-origin");
 expect(html).toContain('title="Report"');
 expect(html).not.toContain('<button onclick=');
});
