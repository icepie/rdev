import { needsAttachmentRaster, rasterizeAttachment } from "./attachment-raster";
import { settledAskCardKeys } from "./transcript-rows";
import { observeTranscriptResize } from "./transcript-resize";
import { clearQueueReturns, requestQueueRemoval } from "./queue-actions";
import { ThreadNavigation } from "./thread-navigation";
import { ImagePreview } from "./image-preview";
import { JobNotification, parseJobNotification, parseJobNotificationData } from "./job-notification";
import { useComposerTextDraft } from "./use-composer-text-draft";
import { useComposerDraft } from "./use-composer-draft";
import { dispatchSlashCommand, matchSlashCommand, type ExecutableSlashCommand } from "./slash-command";
import { parseJobsCommand } from "./jobs-command";
/* ChatPaneView: one pane of the agent chat — transcript + queue + composer
 * + status strip, extracted from the Agent page so the split-screen layout
 * renders several of them side by side. Presentational over a ThreadSink:
 * the page feeds the connection's current thread, secondary panes feed the
 * thread they were dropped with. Composer state (draft text, pending
 * images) is retained per device/thread within each pane. */
import {
  Fragment,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { cn } from "../../lib/utils";
import type { LocalFileLink } from "../markdown-local-file-link";
import {
  COARSE_POINTER_PROMPT_ACTION_BUTTON_CLASS,
  COARSE_POINTER_PROMPT_ICON_ACTION_BUTTON_CLASS,
} from "../coarse-pointer-sizing";
import { Button } from "../button";
import { ScrollToBottomButton } from "../scroll-to-bottom-button";
import { Textarea } from "../textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "../tooltip";
import { Icon } from "../icon";
import { OsIcon } from "../os-icon";
import { ApprovalCard } from "./approval-card";
import { AskCard } from "./ask-card";
import { ContextWindowIndicator } from "./context-window-indicator";
import { CwdPicker } from "./cwd-picker";
import { MessageActionBar } from "./message-action-bar";
import { SERVER_AGENT_ID } from "./protocol";
import { JobsPanel } from "./jobs-panel";
import { TodoCard } from "./todo-card";
import { TurnView } from "./turn-view";
import type { Translator } from "./types";
import {
  MAX_IMAGE_BYTES,
  MAX_IMAGES,
  PERM_MODES,
  REASONING_LEVELS,
  nextKey,
  type ModelInfo,
  type PendingImage,
  type PermMode,
  type ReasoningLevel,
  type Status,
} from "./protocol";
import {
  decideApprovalIn,
  decideAskIn,
  type ThreadSink,
} from "./thread-sink";

const EMPTY_COMPOSER_IMAGES: PendingImage[] = [];

export interface ChatPaneViewProps {
  sink: ThreadSink;
  t: Translator;
  /* Thread id stamped on chat/stop/steer ops ("" = conn current). */
  threadId: string;
  sendRaw: (payload: Record<string, unknown>) => boolean;
  ready: boolean;
  emptyHint: string;
  copiedId: string | null;
  onCopy: (id: string, text: string) => void;
  /* Model selection is thread-local; reasoning and permission mode remain device-wide. */
  model: string;
  models: ModelInfo[];
  onModelChange: (id: string) => void;
  reasoning: ReasoningLevel;
  onReasoningChange: (level: ReasoningLevel) => void;
  permMode: PermMode;
  onPermModeChange: (mode: PermMode) => void;
  device: string;
  deviceOs?: string;
  connStatus: Status;
  contextWindow?: number;
  /* Current-thread-only extras (project picker needs the live cwd): omit
   * on secondary panes, which render a compact status strip. */
  cwd?: string;
  knownCwds?: string[];
  onSelectCwd?: (path: string) => void;
  onCreateProject?: () => void;
  branch?: string;
  /* Registers the pane's add-to-chat inserter so the right panel's
   * selection action can target the focused pane's composer (null on
   * unmount). */
  onRegisterAddToChat?: (fn: ((text: string) => void) | null) => void;
  /* bb thread→panel linkage: raise a file preview tab in the right panel
   * (tool-row path clicks, markdown file links). */
  onOpenFile?: (path: string, lineStart?: number, lineEnd?: number) => void;
  /* bb open-file-diff: file-change row titles focus the changes deck. */
  onOpenDiff?: (path: string) => void;
}

export function ChatPaneView({
  sink,
  t,
  threadId,
  sendRaw,
  ready,
  emptyHint,
  copiedId,
  onCopy,
  model,
  models,
  onModelChange,
  reasoning,
  onReasoningChange,
  permMode,
  onPermModeChange,
  device,
  deviceOs,
  connStatus,
  contextWindow,
  cwd,
  knownCwds,
  onSelectCwd,
  onCreateProject,
  branch,
  onRegisterAddToChat,
  onOpenFile,
  onOpenDiff,
}: ChatPaneViewProps) {
  const modelReasoningLevels = models.find((item) => item.id === model)?.reasoningLevels;
  const supportedReasoning = REASONING_LEVELS.filter((item) => !modelReasoningLevels || modelReasoningLevels.includes(item.level));
  const reasoningOptions = supportedReasoning.length ? supportedReasoning : REASONING_LEVELS;
  const selectedReasoning = reasoningOptions.find((item) => item.level === reasoning) ?? reasoningOptions[0];
  const { state, ops } = sink;
  const { items, todos, queue, queueIds, queueModes = [], queueError = "", running, stopping, turnUsage, currentTool, workingSince } = state;

  const [returningQueue, setReturningQueue] = useState(false);
  const returningQueueRef = useRef(false);
  const draftKey = JSON.stringify([device, threadId]);
  useEffect(() => () => clearQueueReturns(ops.queueReturns.current), [draftKey, ops.queueReturns]);
  const [input, setInput] = useComposerTextDraft(draftKey);
  const [pendingImages, setPendingImages] = useComposerDraft(draftKey, EMPTY_COMPOSER_IMAGES);
  const pendingImageGallery = useMemo(() => pendingImages.map(image => ({ name: image.name, src: image.preview })), [pendingImages]);
  const imageReaders = useRef(new Map<FileReader, PendingImage | null | undefined>());
  const imageConversion = useRef(new AbortController());
  const [readingImages, setReadingImages] = useState(0);
  const [composerError, setComposerError] = useState("");
  useEffect(() => setComposerError(""), [draftKey, input]);
  const cancelImageReads = useCallback(() => {
    const readers = [...imageReaders.current.keys()];
    imageReaders.current.clear();
    imageConversion.current.abort();
    imageConversion.current = new AbortController();
    setReadingImages(0);
    for (const reader of readers) reader.abort();
  }, []);
  useEffect(() => () => cancelImageReads(), [draftKey, cancelImageReads]);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const composerRef = useRef<HTMLTextAreaElement>(null);
  const [nowTick, setNowTick] = useState(0);
  const slashListId = useId();

  /* Elapsed-seconds ticker for the working indicator. Tick immediately on
   * (re)start: nowTick freezes while idle, so the first render after
   * switching into a running thread would otherwise subtract a stale tick
   * and under-report the elapsed time for up to a second. */
  useEffect(() => {
    if (!running) return;
    setNowTick(Date.now());
    const iv = window.setInterval(() => setNowTick(Date.now()), 1000);
    return () => window.clearInterval(iv);
  }, [running]);

  const elapsedSeconds =
    running && workingSince !== null
      ? Math.max(0, Math.floor(((nowTick || Date.now()) - workingSince) / 1000))
      : 0;

  const workingLabel = currentTool === "/compact"
    ? t("agent.compacting")
    : currentTool
    ? t("agent.workingWithTool", { seconds: elapsedSeconds, tool: currentTool })
    : t("agent.working", { seconds: elapsedSeconds });

  /* bb Add to chat: insert the message source into the composer (no
   * blockquote — the rdev composer has no quote rendering). */
  const addToChat = useCallback((text: string) => {
    setInput((prev) => (prev ? prev.replace(/\n+$/, "") + "\n" + text : text));
  }, [setInput]);

  /* Right-panel selection "Add to chat" reaches this pane through the page
   * registry (focused pane wins). */
  useEffect(() => {
    onRegisterAddToChat?.(addToChat);
    return () => onRegisterAddToChat?.(null);
  }, [onRegisterAddToChat, addToChat]);

  const onDecideApproval = useCallback(
    (id: string, allow: boolean, always: boolean) =>
      decideApprovalIn(ops, sendRaw, id, allow, always),
    [ops, sendRaw],
  );
  const onDecideAsk = useCallback(
    (id: string, answer: string) => decideAskIn(sendRaw, id, answer),
    [sendRaw],
  );

  /* Validate count/size locally (mirrors the server limits), then read each
   * file as a data URL: full URL for the chip thumbnail, base64 payload for
   * the chat message. */
  const addImageFiles = (filesIn: File[]) => {
    if (returningQueueRef.current) return;
    const files = filesIn.filter((f) => f.type.startsWith("image/"));
    if (!files.length) return;
    setComposerError("");
    let room = MAX_IMAGES - pendingImages.length - imageReaders.current.size;
    if (files.length > room) {
      setComposerError(t("agent.tooManyImages", { max: MAX_IMAGES }));
    }
    for (const f of files) {
      if (room <= 0) break;
      if (f.size > MAX_IMAGE_BYTES) {
        setComposerError(t("agent.imageTooLarge", { name: f.name }));
        continue;
      }
      room--;
      const reader = new FileReader();
      imageReaders.current.set(reader, undefined);
      setReadingImages(imageReaders.current.size);
      // Completion order is not selection order. Retain settled reads until
      // every earlier selection has settled, then append the ready prefix.
      const settle = (image: PendingImage | null) => {
        if (!imageReaders.current.has(reader) || imageReaders.current.get(reader) !== undefined) return false;
        imageReaders.current.set(reader, image);
        const ready: PendingImage[] = [];
        for (const [entry, value] of imageReaders.current) {
          if (value === undefined) break;
          imageReaders.current.delete(entry);
          if (value) ready.push(value);
        }
        setReadingImages(imageReaders.current.size);
        if (ready.length) setPendingImages(prev => [...prev, ...ready].slice(0, MAX_IMAGES));
        return true;
      };
      const readFailed = () => {
        if (settle(null)) setComposerError(t("agent.imageReadFailed", { name: f.name }));
      };
      reader.onerror = readFailed;
      reader.onload = async () => {
        if (!imageReaders.current.has(reader) || imageReaders.current.get(reader) !== undefined) return;
        let url = String(reader.result || "");
        let mime = f.type || "image/png";
        if (needsAttachmentRaster(mime)) {
          try {
            url = await rasterizeAttachment(url, imageConversion.current.signal);
            mime = "image/png";
          } catch { readFailed(); return; }
          if (!imageReaders.current.has(reader)) return;
        }
        const img: PendingImage = {
          name:
            f.name ||
            `pasted-${nextKey()}.${(f.type.split("/")[1] || "png").replace(/[^a-z0-9]/gi, "")}`,
          mime,
          data: url.slice(url.indexOf(",") + 1),
          preview: url,
        };
        settle(img);
      };
      try { reader.readAsDataURL(f); } catch { readFailed(); }
    }
  };

  const sendChat = () => {
    const text = input.trim();
    if (returningQueueRef.current) return;
    if ((!text && pendingImages.length === 0) || !ready || imageReaders.current.size > 0) return;
    const command = matchSlashCommand(text, slashCommands)?.command.name;
    if (command && command !== "steer" && pendingImages.length > 0) {
      setComposerError(t("agent.commandImagesUnsupported", { command: `/${command}` }));
      return;
    }
    if (pendingImages.length > 0 && /^\/steer(?:\s|$)/i.test(text)) {
      sendSteer(text);
      return;
    }
    if (text.startsWith("/") && pendingImages.length === 0 && runSlashLine(text)) {
      return;
    }
    const payload: Record<string, unknown> = { op: "chat", text };
    if (threadId) payload.thread = threadId;
    if (model) payload.model = model;
    if (pendingImages.length) {
      payload.images = pendingImages.map(({ name, mime, data }) => ({ name, mime, data }));
    }
    if (!sendRaw(payload)) return;
    cancelImageReads();
    setComposerError("");
    setInput("");
    setPendingImages(EMPTY_COMPOSER_IMAGES);
    if (!running) {
      /* Server broadcasts turn_start once execution begins; while running
       * the message lands in the queue and its bubble appears then. */
      ops.setRunning(true);
      ops.setWorkingSince(Date.now());
    }
  };

  const sendSteer = (draft = input) => {
    const text = draft.trim().replace(/^\/steer(?:\s+|$)/i, "").trim();
    if ((!text && pendingImages.length === 0) || !ready || returningQueueRef.current || imageReaders.current.size > 0) return;
    if (!running) {
      setComposerError(t("agent.commandRequiresRunning", { command: "/steer" }));
      return;
    }
    const command = matchSlashCommand(text, slashCommands)?.command.name;
    if (command && pendingImages.length > 0) {
      setComposerError(t("agent.commandImagesUnsupported", { command: `/${command}` }));
      return;
    }
    const payload: Record<string, unknown> = { op: "steer", text };
    if (threadId) payload.thread = threadId;
    if (pendingImages.length) payload.images = pendingImages.map(({ name, mime, data }) => ({ name, mime, data }));
    if (!sendRaw(payload)) return;
    cancelImageReads();
    setComposerError("");
    setInput("");
    setPendingImages(EMPTY_COMPOSER_IMAGES);
  };

  const sendStop = useCallback(() => {
    if (!ready || !running || stopping) return false;
    const payload: Record<string, unknown> = { op: "stop" };
    if (threadId) payload.thread = threadId;
    /* Lock until acknowledgement; reconnect/snapshot clears stale locks. */
    if (!sendRaw(payload)) return false;
    ops.setStopping(true);
    return true;
  }, [ready, running, stopping, threadId, sendRaw, ops]);

  /* Slash commands: typing "/" in the composer opens a filtered menu;
   * no-arg commands run on confirm, commands needing an argument expand
   * into the input. Unknown slash text falls through as a normal chat. */
  interface SlashCommand extends ExecutableSlashCommand {
    label: string;
    desc: string;
    icon: string;
    needsArg?: boolean;
  }
  const [modelPickerOpen, setModelPickerOpen] = useState(false);
  useEffect(() => { setModelPickerOpen(false); }, [threadId, ready, models.length]);
  const [jobsOpen, setJobsOpen] = useState(false);
  const [jobsFull, setJobsFull] = useState(false);
  useEffect(() => { setJobsOpen(false); setJobsFull(false); }, [threadId]);
  const slashCommands: SlashCommand[] = useMemo(
    () => [
      {
        name: "model",
        allowArgs: true,
        aliases: ["models"],
        label: t("agent.cmd.model"),
        desc: t("agent.cmd.modelDesc"),
        icon: "Settings",
        run: (arg) => {
          if (!ready) return false;
          if (!models.length) {
            setComposerError(t("agent.cmd.noModels"));
            return false;
          }
          if (!arg) {
            setComposerError("");
            setModelPickerOpen(true);
            return true;
          }
          if (!models.some(item => item.id === arg)) {
            setComposerError(t("agent.cmd.unknownModel", {model: arg}));
            return false;
          }
          onModelChange(arg);
          setComposerError("");
          return true;
        },
      },
      {
        name: "new",
        label: t("agent.cmd.new"),
        desc: t("agent.cmd.newDesc"),
        icon: "Plus",
        run: () => sendRaw({ op: "new_thread" }),
      },
      {
        name: "jobs",
        allowArgs: true,
        label: t("agent.jobs.title"),
        desc: t("agent.jobs.commandDesc"),
        icon: "ListTodo",
        run: (arg) => {
          const request = parseJobsCommand(arg);
          if (!request) {
            setComposerError("/jobs [full|kill <id>|kill all]");
            return false;
          }
          setJobsOpen(true);
          setJobsFull(request.full);
          return sendRaw({ ...request.payload, thread: threadId });
        },
      },
      {
        name: "compact",
        allowArgs: true,
        label: t("agent.cmd.compact"),
        desc: t("agent.cmd.compactDesc"),
        icon: "Minimize2",
        run: (arg) => {
          if (!ready) return false;
          if (running) {
            setComposerError(t("agent.compactWhileRunning"));
            return false;
          }
          const payload: Record<string, unknown> = { op: "chat", text: `/compact${arg ? ` ${arg}` : ""}` };
          if (threadId) payload.thread = threadId;
          if (model) payload.model = model;
          if (!sendRaw(payload)) return false;
          ops.setRunning(true);
          ops.setWorkingSince(Date.now());
          return true;
        },
      },
      {
        name: "bg",
        allowArgs: true,
        label: t("agent.cmd.bg"),
        desc: t("agent.cmd.bgDesc"),
        icon: "Terminal",
        needsArg: true,
        run: (arg) => {
          if (!arg) {
            setComposerError(t("agent.commandRequiresArgument", { command: "/bg" }));
            return false;
          }
          const payload: Record<string, unknown> = {
            op: "chat",
            text: `/bg ${arg}`,
          };
          if (threadId) payload.thread = threadId;
          if (model) payload.model = model;
          return sendRaw(payload);
        },
      },
      {
        name: "subagent",
        allowArgs: true,
        label: t("agent.cmd.subagent"),
        desc: t("agent.cmd.subagentDesc"),
        icon: "UserRoundPlus",
        needsArg: true,
        run: (arg) => {
          if (!arg) {
            setComposerError(t("agent.commandRequiresArgument", { command: "/subagent" }));
            return false;
          }
          const payload: Record<string, unknown> = {
            op: "chat",
            text: `/subagent ${arg}`,
          };
          if (threadId) payload.thread = threadId;
          if (model) payload.model = model;
          return sendRaw(payload);
        },
      },
      {
        name: "reset",
        label: t("agent.cmd.reset"),
        desc: t("agent.cmd.resetDesc"),
        icon: "RotateCcw",
        run: () => {
          const payload: Record<string, unknown> = { op: "reset" };
          if (threadId) payload.thread = threadId;
          return sendRaw(payload);
        },
      },
      {
        name: "stop",
        label: t("agent.cmd.stop"),
        desc: t("agent.cmd.stopDesc"),
        icon: "Square",
        runningOnly: true,
        run: () => sendStop(),
      },
      {
        name: "steer",
        allowArgs: true,
        label: t("agent.cmd.steer"),
        desc: t("agent.cmd.steerDesc"),
        icon: "Zap",
        needsArg: true,
        runningOnly: true,
        run: (arg) => {
          if (!arg) {
            setComposerError(t("agent.commandRequiresArgument", { command: "/steer" }));
            return false;
          }
          const payload: Record<string, unknown> = { op: "steer", text: arg };
          if (threadId) payload.thread = threadId;
          return sendRaw(payload);
        },
      },
    ],
    [t, sendRaw, threadId, model, models, onModelChange, sendStop, ops, ready, running],
  );

  const slashQuery = (() => {
    const m = input.match(/^\/([a-z]*)$/i);
    return m ? m[1].toLowerCase() : null;
  })();
  const slashFiltered = useMemo(
    () =>
      slashQuery === null
        ? []
        : slashCommands.filter(
            (c) =>
              (c.name.startsWith(slashQuery) || c.aliases?.some(alias => alias.startsWith(slashQuery))) && (!c.runningOnly || running),
          ),
    [slashCommands, slashQuery, running],
  );
  const [slashIndex, setSlashIndex] = useState(0);
  const [slashDismissed, setSlashDismissed] = useState(false);
  const slashOpen =
    ready && readingImages === 0 && pendingImages.length === 0 && slashQuery !== null && !slashDismissed && slashFiltered.length > 0;
  useEffect(() => {
    setSlashIndex(0);
    setSlashDismissed(false);
  }, [slashQuery]);

  const acceptSlash = (cmd: SlashCommand) => {
    if (!ready) return;
    if (cmd.needsArg) {
      setInput("/" + cmd.name + " ");
      setSlashDismissed(true);
      return;
    }
    if (cmd.run("")) {
      setInput("");
      setComposerError("");
    }
  };

  /* Executes a "/name arg" line; returns true when the input was consumed
   * as a command (unknown commands fall back to a normal chat send). */
  const runSlashLine = (text: string): boolean =>
    dispatchSlashCommand(text, slashCommands, running, () => {
      setInput("");
      setComposerError("");
    },
      (command, reason) => setComposerError(t(reason === "arguments" ? "agent.commandNoArguments" : "agent.commandRequiresRunning", { command: `/${command}` })));


  /* bb pastRowDimClassName: every turn but the transcript's last renders
   * its work rows dimmed (opacity-40). */
  const lastTurnKey = useMemo(() => {
    for (let i = items.length - 1; i >= 0; i--) {
      if (items[i].kind === "turn") return items[i].key;
    }
    return null;
  }, [items]);

  const activeTurnKey = running && ops.turnOpen.current ? lastTurnKey : null;
  const settledQuestions = useMemo(() => settledAskCardKeys(items, activeTurnKey), [items, activeTurnKey]);

  /* bb markdown local-file links → right-panel preview tab (line suffix
   * carried through for the pierre selectedLines highlight). */
  const handleOpenLocalFile = useCallback(
    (link: LocalFileLink) =>
      onOpenFile?.(link.path, link.lineStart, link.lineEnd),
    [onOpenFile],
  );

  /* BB's back-to-bottom cue: visible while scrolled up, shimmering only
   * while the conversation is actively running. */
  const transcriptRef = useRef<HTMLDivElement>(null);
  const transcriptContentRef = useRef<HTMLDivElement>(null);
  const registerTranscript = useCallback((node: HTMLDivElement | null) => {
    transcriptRef.current = node;
    ops.registerScrollEl(node);
  }, [ops.registerScrollEl]);
  const [atBottom, setAtBottom] = useState(true);
  useLayoutEffect(() => {
    const viewport = transcriptRef.current;
    const content = transcriptContentRef.current;
    if (viewport && content) return observeTranscriptResize(viewport, content, setAtBottom);
  }, [device, threadId]);

  return (
    <>
      {/* Transcript */}
      <div className="relative flex min-h-0 flex-1 flex-col">
      <ThreadNavigation items={items} scrollRef={transcriptRef} t={t} />
      <div
        ref={registerTranscript}
        className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto"
      >
        <div ref={transcriptContentRef} className="mx-auto flex w-full max-w-[760px] min-w-0 flex-col gap-1 px-4 pt-3 pb-3">
          {items.length === 0 && (
            <div className="px-4 py-12 text-center text-sm whitespace-pre-wrap text-muted-foreground">
              {emptyHint}
              {ready && device === SERVER_AGENT_ID && !input.trim() && pendingImages.length === 0 && (
                <div className="mt-4 flex justify-center">
                  <Button variant="outline" size="sm" onClick={() => {
                    setInput(t("agent.listDevicesPrompt"));
                    composerRef.current?.focus();
                  }}>
                    {t("agent.listDevicesPrompt")}
                  </Button>
                </div>
              )}
            </div>
          )}
          {items.map((item) => {
            if (item.kind === "user") {
              const job = parseJobNotificationData(item.job) || (!item.images?.length ? parseJobNotification(item.text) : null);
              if (job) return <JobNotification key={item.key} job={job} t={t} />;
              const imageGallery = item.images?.map((name, index) => ({ name, src: item.imagePreviews?.[index] ?? "" }));
              return (
                <div
                  key={item.key}
                  data-thread-message={item.key}
                  className="group/message ml-auto flex w-fit max-w-[70%] flex-col items-end"
                >
                  {item.steering && <span className="mb-1 flex items-center gap-1 text-xs text-muted-foreground"><Icon name="CornerDownRight" className="size-3" />Steer</span>}
                  {item.images && item.images.length > 0 && (
                    <div className="mb-1 flex max-w-full flex-wrap justify-end gap-1">
                      {item.images.map((name, i) => item.imagePreviews?.[i] ? (
                        <ImagePreview key={i} src={item.imagePreviews[i]} name={name} gallery={imageGallery} className="h-24 w-32" />
                      ) : (
                        <span
                          key={i}
                          className="inline-flex max-w-full items-center gap-1 rounded-full border border-border-seam px-2 py-0.5 text-xs text-muted-foreground"
                        >
                          <Icon name="FileAttachment" className="size-3 shrink-0" />
                          <span className="max-w-40 truncate">{name}</span>
                        </span>
                      ))}
                    </div>
                  )}
                  <div className="max-w-full rounded-xl border border-border-seam bg-surface-recessed px-4 py-2.5 text-sm leading-relaxed break-words whitespace-pre-wrap text-foreground">
                    {item.text}
                  </div>
                  {/* bb hover action bar below the bubble */}
                  <MessageActionBar
                    align="end"
                    text={item.text}
                    timestamp={item.time}
                    copied={copiedId === "u" + item.key}
                    onCopy={(text) => onCopy("u" + item.key, text)}
                    onAddToChat={addToChat}
                    t={t}
                  />
                </div>
              );
            }
            if (item.kind === "error") {
              return (
                <div
                  key={item.key}
                  className="max-w-full self-center rounded-xl border border-destructive bg-surface-destructive px-3.5 py-2.5 text-sm break-words whitespace-pre-wrap text-destructive-text"
                >
                  {item.text}
                </div>
              );
            }
            if (item.kind === "setup") {
              return (
                <div
                  key={item.key}
                  className="max-w-full self-center rounded-xl border border-attention bg-surface-attention px-3.5 py-2.5 text-sm break-words"
                >
                  <span className="mb-1 block font-semibold text-warning-text">
                    {t("agent.notConfigured")}
                  </span>
                  {item.text}
                </div>
              );
            }
            if (item.kind === "system") {
              return (
                <div
                  key={item.key}
                  className="self-center rounded-lg border border-dashed border-border px-3 py-1 text-xs text-muted-foreground"
                >
                  {item.text}
                </div>
              );
            }
            if (item.kind === "approval") {
              return (
                <ApprovalCard
                  key={item.key}
                  item={item}
                  t={t}
                  onDecide={onDecideApproval}
                />
              );
            }
            if (item.kind === "ask") {
              if (settledQuestions.has(item.key)) return null;
              return (
                <AskCard
                  key={item.key}
                  item={item}
                  t={t}
                  onAnswer={onDecideAsk}
                />
              );
            }
            if (item.kind !== "turn") return null;
            return (
              <div key={item.key} data-thread-message={item.key}>
              <TurnView
                turn={item}
                active={item.key === activeTurnKey}
                t={t}
                copied={copiedId === "t" + item.key}
                dimmed={item.key !== lastTurnKey}
                onCopy={(text) => onCopy("t" + item.key, text)}
                onAddToChat={addToChat}
                onOpenFile={onOpenFile}
                onOpenDiff={onOpenDiff}
                onOpenLocalFile={handleOpenLocalFile}
              />
              </div>
            );
          })}
          {running && (
            <div className="mt-4 flex min-h-7 items-center gap-2 px-2 text-sm text-muted-foreground">
              <Icon name="Spinner" className="size-3.5 animate-spin" />
              <span className="animate-shine">{workingLabel}</span>
            </div>
          )}
        </div>
      </div>

      </div>
      {/* Floating back-to-bottom button over the transcript. */}
      <ScrollToBottomButton
        visible={!atBottom}
        active={running}
        label={t("agent.scrollToBottom")}
        onClick={() => ops.scrollDown(true)}
      />

      {/* Queue: each row can be taken back into the composer (click) or
       * dropped entirely (✕); both go through the dequeue op so every
       * pane sees the rebroadcast list. */}
      {(queue.length > 0 || queueError) && (
        <div className="shrink-0 border-t border-border-seam px-3.5 py-1.5">
          <div className="mx-auto flex w-full max-w-[760px] flex-col gap-0.5">
            <div className="flex items-center gap-1.5 text-2xs tracking-wide text-muted-foreground uppercase">
              <Icon name="Rows2" className="size-3 shrink-0" />
              {t("agent.queue")}
              <span className="normal-case tracking-normal">
                ({queue.length})
              </span>
            </div>
            {queueError && <p role="alert" className="text-xs text-destructive-text">{queueError}</p>}
            {queue.map((q, i) => (
              <div
                key={queueIds[i] ?? i}
                className="group/queue flex items-center gap-1 rounded-md py-0.5 pr-1 pl-4 text-xs text-muted-foreground transition-colors hover:bg-sidebar-accent"
              >
                <span className="w-4 shrink-0 text-right text-2xs text-subtle-foreground/75 tabular-nums">
                  {queueModes[i] === "steer" ? <Icon name="CornerDownRight" className="size-3" aria-label={t("agent.queueSteering")} /> : i + 1}
                </span>
                <button
                  type="button"
                  title={t(pendingImages.length > 0 || readingImages > 0 ? "agent.queueEditImagesHint" : "agent.queueEditHint")}
                  disabled={!ready || !queueIds[i] || returningQueue || pendingImages.length > 0 || readingImages > 0}
                  className="min-w-0 flex-1 cursor-pointer truncate text-left transition-colors hover:text-foreground"
                  onClick={() => {
                    if (returningQueueRef.current || pendingImages.length || imageReaders.current.size) return;
                    returningQueueRef.current = true;
                    setReturningQueue(true);
                    const settled = () => { returningQueueRef.current = false; setReturningQueue(false); };
                    if (!requestQueueRemoval(ops.queueReturns.current, sendRaw, threadId, queueIds[i], (text, images) => {
                      if (text) addToChat(text);
                      setPendingImages(images);
                    }, settled)) settled();
                  }}
                >
                  {q || t("agent.queuedImages")}
                </button>
                {queueModes[i] === "follow_up" && running && <button type="button"
                  aria-label={t("agent.queuePromote")} title={t("agent.queuePromote")}
                  disabled={!ready || !queueIds[i] || returningQueue}
                  className="shrink-0 cursor-pointer rounded p-0.5 text-subtle-foreground/75 opacity-0 transition-opacity group-hover/queue:opacity-100 group-focus-within/queue:opacity-100 pointer-coarse:opacity-100 max-md:pointer-coarse:min-h-9 max-md:pointer-coarse:min-w-9 hover:text-foreground disabled:pointer-events-none"
                  onClick={() => { if (!sendRaw({ op: "promote_queue", thread: threadId, queueId: queueIds[i] })) setComposerError(t("agent.queuePromoteFailed")); }}>
                  <Icon name="CornerDownRight" className="size-3.5" />
                </button>}
                <button
                  type="button"
                  aria-label={t("agent.queueRemove")}
                  title={t("agent.queueRemove")}
                  disabled={!ready || !queueIds[i]}
                  className="shrink-0 cursor-pointer rounded p-0.5 text-subtle-foreground/75 opacity-0 transition-opacity group-hover/queue:opacity-100 group-focus-within/queue:opacity-100 pointer-coarse:opacity-100 max-md:pointer-coarse:min-h-9 max-md:pointer-coarse:min-w-9 hover:text-destructive-text disabled:pointer-events-none"
                  onClick={() => {
                    requestQueueRemoval(ops.queueReturns.current, sendRaw, threadId, queueIds[i]);
                  }}
                >
                  <Icon name="X" className="size-3.5" />
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Composer (bb footer: same 760px column and padding as the content) */}
      <div className="mx-auto w-full max-w-[760px] shrink-0 px-4 pb-2">
        <JobsPanel open={jobsOpen} onOpenChange={setJobsOpen} full={jobsFull} sink={sink} threadId={threadId} ready={ready} sendRaw={sendRaw} t={t} />
        {/* bb prompt-stack banner: the todo checklist card sits directly
         * above the composer. */}
        {todos.length > 0 && (
          <div className="mb-2">
            <TodoCard todos={todos} t={t} />
          </div>
        )}
        <div className="relative w-full rounded-xl border border-border bg-background shadow-lift">
          {slashOpen && (
            <div id={slashListId} role="listbox" aria-label={t("agent.slashCommands")} className="absolute inset-x-2 bottom-full z-20 mb-1 max-h-[min(20rem,45dvh)] overflow-y-auto rounded-lg border border-border bg-surface-raised-solid shadow-lift">
              <div className="px-2.5 pt-1.5 pb-1 text-2xs tracking-wide text-muted-foreground uppercase">
                {t("agent.slashCommands")}
              </div>
              {slashFiltered.map((cmd, i) => (
                <button
                  key={cmd.name}
                  type="button"
                  id={`${slashListId}-${cmd.name}`}
                  role="option"
                  aria-selected={i === Math.min(slashIndex, slashFiltered.length - 1)}
                  tabIndex={-1}
                  onMouseDown={(e) => e.preventDefault()}
                  className={cn(
                    "flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-sm transition-colors",
                    i === Math.min(slashIndex, slashFiltered.length - 1)
                      ? "bg-state-active text-foreground"
                      : "text-muted-foreground hover:bg-sidebar-accent",
                  )}
                  onMouseEnter={() => setSlashIndex(i)}
                  onClick={() => acceptSlash(cmd)}
                >
                  <Icon name={cmd.icon} className="size-3.5 shrink-0" />
                  <span className="shrink-0 font-medium text-foreground">
                    /{cmd.name}
                  </span>
                  <span className="min-w-0 flex-1 truncate text-xs">
                    {cmd.label} · {cmd.desc}
                  </span>
                </button>
              ))}
            </div>
          )}
          {returningQueue && <p role="status" className="px-4 pt-2 text-xs text-muted-foreground">{t("agent.queueReturning")}</p>}
          {composerError && <p role="alert" className="px-4 pt-2 text-xs text-destructive-text">{composerError}</p>}
          {readingImages > 0 && (
            <p role="status" className="px-4 pt-2 text-xs text-muted-foreground">
              {t("agent.readingImages", { count: readingImages })}
            </p>
          )}
          {pendingImages.length > 0 && (
            <div className="flex flex-wrap gap-2 px-3 pt-2.5">
              {pendingImages.map((img, i) => (
                <span
                  key={i}
                  className="flex items-center gap-1.5 rounded-md border border-border bg-surface-recessed py-1 pr-1.5 pl-1 text-xs text-muted-foreground"
                >
                  <ImagePreview src={img.preview} name={img.name} gallery={pendingImageGallery} className="h-16 w-24 object-cover" />
                  <span className="max-w-32 truncate">{img.name}</span>
                  <button
                    type="button"
                    aria-label={t("common.cancel")}
                    className="shrink-0 transition-colors hover:text-foreground"
                    onClick={() => {
                      setComposerError("");
                      setPendingImages((prev) => prev.filter((_, j) => j !== i));
                    }}
                  >
                    <Icon name="X" className="size-3.5" />
                  </button>
                </span>
              ))}
            </div>
          )}
          <Textarea
            ref={composerRef}
            rows={1}
            value={input}
            disabled={!ready}
            role="combobox"
            aria-label={t("agent.composerPlaceholder")}
            aria-autocomplete="list"
            aria-expanded={slashOpen}
            aria-controls={slashOpen ? slashListId : undefined}
            aria-activedescendant={slashOpen ? `${slashListId}-${slashFiltered[Math.min(slashIndex, slashFiltered.length - 1)].name}` : undefined}
            placeholder={t("agent.composerPlaceholder")}
            className="max-h-36 min-h-[68px] w-full resize-none border-0 bg-transparent px-4 pt-3 pb-1 pr-14 text-sm leading-relaxed shadow-none placeholder:font-light placeholder:text-subtle-foreground/70 focus-visible:ring-0"
            onChange={(e) => setInput(e.target.value)}
            onPaste={(e) => {
              const files = Array.from(e.clipboardData.items)
                .filter((it) => it.kind === "file" && it.type.startsWith("image/"))
                .map((it) => it.getAsFile())
                .filter((f): f is File => f !== null);
              if (files.length) {
                e.preventDefault();
                addImageFiles(files);
              }
            }}
            onKeyDown={(e) => {
              const composing = e.nativeEvent.isComposing || e.keyCode === 229;
              if (slashOpen && !composing) {
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  setSlashIndex((i) => (i + 1) % slashFiltered.length);
                  return;
                }
                if (e.key === "ArrowUp") {
                  e.preventDefault();
                  setSlashIndex(
                    (i) => (i - 1 + slashFiltered.length) % slashFiltered.length,
                  );
                  return;
                }
                if ((e.key === "Enter" || e.key === "Tab") && !e.shiftKey) {
                  e.preventDefault();
                  acceptSlash(slashFiltered[Math.min(slashIndex, slashFiltered.length - 1)]);
                  return;
                }
                if (e.key === "Escape") {
                  e.preventDefault();
                  setSlashDismissed(true);
                  return;
                }
              }
              if (e.key === "Enter" && !e.shiftKey && !composing) {
                e.preventDefault();
                sendChat();
              }
            }}
          />
          <div className="relative flex shrink-0 select-none flex-row flex-wrap items-center gap-3 pb-2 pl-3.5 pr-[13px] pt-1.5">
            {/* bb ExecutionControls: model picker + reasoning + attachments left */}
            <div className="flex min-w-9 flex-1 items-center gap-1">
              {/* bb: picker shows with ≥1 model (single model still opens
               * to confirm the selection); only zero models hides it. */}
              {models.length > 0 && (
                <Select
                  open={modelPickerOpen}
                  onOpenChange={setModelPickerOpen}
                  value={model}
                  disabled={!ready}
                  onValueChange={(id) => {
                    if (id && id !== model) onModelChange(id);
                  }}
                >
                  <SelectTrigger className="h-8 w-fit max-w-full min-w-0 items-center justify-start gap-1 border-none bg-transparent px-1 text-xs leading-tight text-muted-foreground shadow-none max-md:pointer-coarse:h-10">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent aria-label={t("agent.cmd.model")}>
                    {models.map((m) => (
                      <SelectItem key={m.id} value={m.id}>
                        {(m.name || m.id) + (m.provider ? ` (${m.provider})` : "")}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <button
                    type="button"
                    aria-label={t("agent.reasoning")}
                    disabled={!ready}
                    className="flex h-8 shrink-0 cursor-pointer items-center gap-1 rounded-md px-1 text-xs font-normal text-muted-foreground transition-colors hover:text-foreground disabled:pointer-events-none disabled:opacity-50 max-md:pointer-coarse:h-10"
                  >
                    <Icon name="AiBrain01" className="size-3.5" />
                    {t(selectedReasoning.label)}
                    <Icon name="ChevronDown" className="size-3.5 text-subtle-foreground/75" />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="start">
                  {reasoningOptions.map((r) => (
                    <DropdownMenuItem key={r.level} onSelect={() => onReasoningChange(r.level)}>
                      <span className="flex-1">{t(r.label)}</span>
                      {selectedReasoning.level === r.level && (
                        <Icon name="Check" className="size-4 shrink-0" />
                      )}
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    aria-label={t("agent.attachImages")}
                    disabled={!ready || returningQueue}
                    className={cn(COARSE_POINTER_PROMPT_ICON_ACTION_BUTTON_CLASS, "shrink-0 text-subtle-foreground/75 hover:text-muted-foreground")}
                    onClick={() => fileInputRef.current?.click()}
                  >
                    <Icon name="Paperclip" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>{t("agent.attachImages")}</TooltipContent>
              </Tooltip>
              <input
                ref={fileInputRef}
                type="file"
                accept="image/*"
                multiple
                className="hidden"
                onChange={(e) => {
                  addImageFiles(Array.from(e.target.files ?? []));
                  e.target.value = "";
                }}
              />
            </div>
            <div className="flex min-w-0 max-w-full shrink-0 flex-wrap items-center justify-end gap-1">
              {running && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={t("agent.steer")}
                      disabled={!ready || returningQueue || readingImages > 0 || (!input.trim() && pendingImages.length === 0)}
                      className={COARSE_POINTER_PROMPT_ICON_ACTION_BUTTON_CLASS}
                      onClick={() => sendSteer()}
                    >
                      <Icon name="Zap" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{t("agent.steer")}</TooltipContent>
                </Tooltip>
              )}
              {(running || stopping) && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      variant="secondary"
                      size="icon"
                      aria-label={t("agent.stop")}
                      disabled={!ready || !running || stopping}
                      aria-busy={stopping}
                      className={cn(COARSE_POINTER_PROMPT_ICON_ACTION_BUTTON_CLASS, "[&_[data-icon-root]]:size-3.5")}
                      onClick={sendStop}
                    >
                      <Icon name="Square" className="fill-current [&_*]:stroke-0" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{t("agent.stop")}</TooltipContent>
                </Tooltip>
              )}
              {running ? (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      size="sm"
                      aria-label={t("agent.queued")}
                      disabled={!ready || returningQueue || readingImages > 0 || (!input.trim() && pendingImages.length === 0)}
                      className={cn(COARSE_POINTER_PROMPT_ACTION_BUTTON_CLASS, "gap-1.5 px-2.5")}
                      onClick={sendChat}
                    >
                      <Icon name="CornerDownLeft" />
                      {t("agent.queued")}
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{t("agent.queuedHint")}</TooltipContent>
                </Tooltip>
              ) : (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      aria-label={t("agent.send")}
                      disabled={!ready || returningQueue || readingImages > 0 || (!input.trim() && pendingImages.length === 0)}
                      className={cn(COARSE_POINTER_PROMPT_ACTION_BUTTON_CLASS, "ml-1")}
                      onClick={sendChat}
                    >
                      <Icon name="CornerDownLeft" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{t("agent.send")}</TooltipContent>
                </Tooltip>
              )}
            </div>
          </div>
        </div>
        {/* bb footer strip: device + connection | permission mode + context usage */}
        <div className="mt-1 max-md:mt-0 flex min-h-6 max-h-6 max-md:pointer-coarse:min-h-11 max-md:pointer-coarse:max-h-11 select-none items-center justify-between gap-2 overflow-hidden pl-[15px] pr-3.5">
          <span className="flex min-w-0 flex-1 items-center gap-1 overflow-hidden text-xs text-muted-foreground">
            <OsIcon os={deviceOs} className="size-4 shrink-0" />
            <span className="min-w-0 truncate">
              {/* Short placeholder: the empty body above already shows the
               * full guidance; repeating it here duplicates the line. */}
              {device === SERVER_AGENT_ID ? t("agent.server") : device || t("agent.selectDeviceShort")}
            </span>
            <span
              role="img"
              aria-label={t(`agent.status.${connStatus}`)}
              title={t(`agent.status.${connStatus}`)}
              className={cn(
                "size-1.5 shrink-0 rounded-full",
                connStatus === "connected"
                  ? "bg-success"
                  : connStatus === "disconnected"
                    ? "bg-destructive"
                    : "bg-attention",
              )}
            />
            {device && connStatus !== "connected" && (
              <span role="status" className="shrink-0 text-2xs">
                {t(`agent.status.${connStatus}`)}
              </span>
            )}
            {device && device !== SERVER_AGENT_ID && cwd !== undefined && onSelectCwd && onCreateProject && (
              /* bb ProjectSelector: current thread cwd + known cwds. */
              <CwdPicker
                cwd={cwd}
                options={knownCwds ?? []}
                disabled={!ready}
                onSelect={onSelectCwd}
                onCreateProject={onCreateProject}
                t={t}
              />
            )}
            {device && branch && (
              <span
                className="flex h-6 min-w-0 cursor-pointer items-center gap-1 rounded-md px-1.5 py-0.5 text-xs text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground"
                title={branch}
              >
                <Icon name="GitBranch" className="size-3.5 shrink-0" />
                <span className="min-w-0 truncate">{branch}</span>
              </span>
            )}
          </span>
          <span className="flex shrink-0 items-center gap-2">
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  type="button"
                  aria-label={t("agent.permissions")}
                  disabled={!ready}
                  className={cn(
                    "flex h-6 shrink-0 cursor-pointer items-center gap-1 rounded-md px-1 text-xs transition-colors disabled:pointer-events-none",
                    /* bb: full access renders in --warning-text (orange). */
                    permMode === "full"
                      ? "text-warning-text hover:text-warning-text data-[state=open]:text-warning-text"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  <Icon
                    name={PERM_MODES.find((p) => p.mode === permMode)?.icon ?? "SecurityCheck"}
                    className="size-3.5"
                  />
                  {t(PERM_MODES.find((p) => p.mode === permMode)?.label ?? "agent.permWrite")}
                  <Icon name="ChevronDown" className="size-3.5 text-subtle-foreground/75" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                {PERM_MODES.map((p, i) => (
                  <Fragment key={p.mode}>
                    {i > 0 && <DropdownMenuSeparator />}
                    <DropdownMenuItem onSelect={() => onPermModeChange(p.mode)}>
                      {/* bb: the full-access option carries warning tone. */}
                      <span
                        className={cn(
                          "flex min-w-0 flex-1 items-start gap-2",
                          p.mode === "full" && "text-warning-text",
                        )}
                      >
                        <Icon name={p.icon} className="size-4 shrink-0" />
                        <span className="min-w-0 flex-1">
                          <span className="block">{t(p.label)}</span>
                          <span className="block text-2xs text-muted-foreground">
                            {t(p.hint)}
                          </span>
                        </span>
                      </span>
                      {permMode === p.mode && (
                        <Icon name="Check" className="size-4 shrink-0" />
                      )}
                    </DropdownMenuItem>
                  </Fragment>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
            {/* bb context-window ring: needs both a usage snapshot and a
             * model with a known context window. */}
            {turnUsage !== null && contextWindow !== undefined && (
              <ContextWindowIndicator
                used={turnUsage.inputTokens}
                contextWindow={contextWindow}
                busy={!ready || running}
                onCompact={() => slashCommands.find(command => command.name === "compact")?.run("")}
                t={t}
              />
            )}
          </span>
        </div>
      </div>
    </>
  );
}

