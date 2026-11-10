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

## Shared tree interactions

The standalone Files workspace and Agent Files panel now share the same tree:
folder-first natural sorting, lazy loading, keyboard navigation, loaded-name
filtering, and context menus for copying paths, download, rename and delete.
Folder menus offer new files/folders. Expanded folders and selection are retained
per device/workspace/root in session storage. New-file creation checks the current
directory for name collisions before starting the normal upload; concurrent writes
from other applications still follow the existing upload replacement semantics.

Desktop tree/preview dividers support pointer and keyboard resizing and retain
their split per workspace. Narrow standalone panes show either tree or preview;
the Agent panel uses a vertical split, switching to one surface on mobile.
Directory refresh invalidates child caches while retaining expanded paths. Lazy
listing requests are isolated from navigation and preview requests and canceled on
disconnection. Each lazy directory is bounded to 10,000 entries; larger directories
can be opened directly and paged using Load more.

Rename/delete require the updated Go or Rust client; Android already supports the
protocol, and now rejects root mutation and existing rename destinations and does
not follow final symlinks during deletion. Update these clients with the server/UI.
Old clients without mutation support return a request timeout. Recursive deletion
requires explicit confirmation and does not use a recycle bin.
