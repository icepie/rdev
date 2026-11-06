import { JSDOM } from "jsdom";
import { act, createElement, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { expect, it } from "vitest";
import { DisclosureBody } from "../ui/components/disclosure-body";

it("mounts expensive details lazily and retains their state through streaming and collapse", async () => {
  const dom = new JSDOM('<div id="root"></div>');
  const keys = ["window", "document", "IS_REACT_ACT_ENVIRONMENT"];
  const previous = Object.fromEntries(
    keys.map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]),
  );
  Object.assign(globalThis, {
    window: dom.window,
    document: dom.window.document,
    IS_REACT_ACT_ENVIRONMENT: true,
  });
  let mounts = 0,
    unmounts = 0;
  function Details({ text }) {
    useEffect(() => {
      mounts++;
      return () => {
        unmounts++;
      };
    }, []);
    return createElement(
      "div",
      null,
      createElement("input", { defaultValue: "initial" }),
      text,
    );
  }
  const root = createRoot(document.getElementById("root"));
  const render = (open, text) =>
    act(() =>
      root.render(
        createElement(
          DisclosureBody,
          { open },
          createElement(Details, { text }),
        ),
      ),
    );
  try {
    await render(false, "not yet opened");
    expect(mounts).toBe(0);
    await render(true, "first token");
    const input = document.querySelector("input");
    input.value = "preserved";
    await render(true, "first token and more");
    expect(document.querySelector("input")).toBe(input);
    await render(false, "complete");
    expect(input.closest("[inert]")).not.toBeNull();
    expect(input.closest('[aria-hidden="true"]')).not.toBeNull();
    expect({ mounts, unmounts }).toEqual({ mounts: 1, unmounts: 0 });
    await render(true, "complete");
    expect(input.closest("[inert]")).toBeNull();
    expect(document.querySelector("input").value).toBe("preserved");
  } finally {
    await act(() => root.unmount());
    dom.window.close();
    for (const [key, descriptor] of Object.entries(previous)) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  }
});
