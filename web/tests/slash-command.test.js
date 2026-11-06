import { expect, it, vi } from "vitest";
import { dispatchSlashCommand } from "../ui/components/agent/slash-command";
import { parseJobsCommand } from "../ui/components/agent/jobs-command";

it("preserves a failed command draft and clears it only after a successful retry", () => {
  let draft = "/compact preserve errors\nand device names";
  const clear = () => { draft = ""; };
  const send = vi.fn().mockReturnValueOnce(false).mockReturnValueOnce(true);
  const commands = [{ name: "compact", run: send }];
  expect(dispatchSlashCommand(draft, commands, false, clear)).toBe(true);
  expect(draft).toBe("/compact preserve errors\nand device names");
  expect(send).toHaveBeenCalledWith("preserve errors\nand device names");
  expect(dispatchSlashCommand(draft, commands, false, clear)).toBe(true);
  expect(draft).toBe("");
});

it("keeps invalid jobs syntax editable without sending it as chat", () => {
  const send = vi.fn(() => true), clear = vi.fn();
  const commands = [{ name: "jobs", run: argument => {
    const request = parseJobsCommand(argument);
    return request ? send(request.payload) : false;
  } }];
  expect(dispatchSlashCommand("/jobs kill", commands, false, clear)).toBe(true);
  expect(send).not.toHaveBeenCalled();
  expect(clear).not.toHaveBeenCalled();
  expect(dispatchSlashCommand("/JOBS kill all", commands, false, clear)).toBe(true);
  expect(send).toHaveBeenCalledWith({ op: "cancel_job", id: "all" });
  expect(clear).toHaveBeenCalledTimes(1);
});

it("never sends inactive control commands to the model, while unknown slash text remains chat", () => {
  const run = vi.fn(() => true), clear = vi.fn();
  const commands = [{ name: "stop", runningOnly: true, run }];
  expect(dispatchSlashCommand("/stop", commands, false, clear)).toBe(true);
  expect(run).not.toHaveBeenCalled();
  expect(clear).not.toHaveBeenCalled();
  expect(dispatchSlashCommand("/unknown text", commands, false, clear)).toBe(false);
  expect(dispatchSlashCommand("regular message", commands, false, clear)).toBe(false);
  expect(dispatchSlashCommand("/stop", commands, true, clear)).toBe(true);
  expect(run).toHaveBeenCalledOnce();
  expect(clear).toHaveBeenCalledOnce();
});
