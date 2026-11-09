# File workspace

The Files page uses an Explorer-style directory tree and independent preview tabs,
with the existing BB theme. Folders load on expansion; double-clicking a folder
makes it the current root. Up and the path field change the root. Refresh also
invalidates expanded directory caches. Large directories reveal rows in batches.

The Details view retains size, modified time, download and cross-pane transfer
actions. The tree also supports dragging files/folders between device panes.
Uploads, downloads and transfer tasks keep using the existing authenticated socket.

On narrow panes, selecting a file opens the preview surface. Explorer returns to
the directory tree without closing tabs. Each tab preserves its renderer state;
hidden audio/video stop loading and playback. Preview responses are keyed by
request and device, so concurrent reads cannot overwrite another file. Closing,
refreshing, switching devices or disconnecting invalidates outstanding requests.
Loaded text remains viewable after disconnection. Tabs are local to the mounted
workspace, not persisted across a full browser reload.

## Preview formats

- Code and text: syntax-highlighted source, line wrapping and copying.
- Markdown, CSV and HTML: rendered preview and source switching.
- Images: the existing image preview and lightbox.
- Video: browser-supported codecs through the existing media player.
- PDF: the browser PDF viewer, with a separate-tab fallback for mobile browsers.
- Audio: MP3, WAV, Ogg/Opus, M4A, AAC and FLAC, subject to browser codec support.

PDF/audio/video use the same expiring, device-authorized media grants and bounded
Range reads. A changed device password invalidates old grants. No client upgrade
is required for devices that support the existing file transfer protocol. Agent
file previews use these same renderers. Office documents and archives do not yet
have dedicated renderers; download them for a local application.
