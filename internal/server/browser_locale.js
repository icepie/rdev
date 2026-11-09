// Run before inspector modules initialize their settings and translation data.
const rdevLocale = new URLSearchParams(location.search).get("rdevLang");
if (rdevLocale === "zh" || rdevLocale === "en-US") {
  try { localStorage.setItem("language", JSON.stringify(rdevLocale)); } catch { /* Storage may be restricted. */ }
}
