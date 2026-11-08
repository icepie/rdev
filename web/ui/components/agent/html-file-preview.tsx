import { useMemo } from "react";

// Remote files must not inherit the application's origin or access its APIs.
// Keep authored HTML/styles intact, unlike the theme-injected canvas surface.
export function htmlPreviewDocument(content: string): string {
  // Place the policy before any authored resources. Do not parse the input in
  // the host document (even an inert DOMParser may initiate resource loads).
  // HTML parsing merges an authored html/head/body into the implied document.
  return `<!doctype html><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; media-src data: blob:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'">${content}`;
}

export function HtmlFilePreview({ content, name, hidden }: {
  content: string; name: string; hidden: boolean;
}) {
  const document = useMemo(() => htmlPreviewDocument(content), [content]);
  return <iframe title={name} srcDoc={document} sandbox="allow-scripts" referrerPolicy="no-referrer"
    hidden={hidden} inert={hidden}
    className={hidden ? "hidden" : "absolute inset-0 h-full w-full border-0 bg-white"} />;
}
