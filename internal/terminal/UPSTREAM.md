This package originally copied the MyGo terminal plugin from github.com/egoist/mygo v0.2.14, under the MIT license in LICENSE.

Retty originally kept a local copy because that version did not expose the Ghostty search API or search highlights to applications. The search additions are in search.go, internal/vt/search.go, and the terminal renderer. view.go also copies completed mouse selections automatically. The renderer removes horizontal grid padding and extends last-column backgrounds to the viewport edge. Native library metadata remains pinned to the upstream build.

When updating MyGo, compare this package with the framework terminal plugin and carry forward the applicable fixes while preserving Retty's platform packaging.

Mouse-wheel and alternate-scroll arrow reports for one scroll event are encoded
once and repeated into one queued write. This preserves the exact event bytes,
row count, coordinates and input ordering while avoiding per-row FFI calls,
allocations and remote screen-input messages. Fractional movement still
accumulates before reports are generated; local scrollback is unchanged.
The input worker also drains an existing backlog into writes of up to 32 KiB
without waiting for more events. This preserves direction changes and keys
across touch events, completes short writes and clears consumed queue entries.
A single larger paste keeps its existing downstream chunking.

Options.SmoothScroll is enabled for the phone's live terminal. After precise
scroll input, exact rendered-row matches can identify a shifted body region;
only that region receives a bounded 32 ms presentation transition. Fixed rows,
program input and PTY geometry remain authoritative. Newer frames interrupt
an unfinished transition, and reversed input cancels it. Selection, images,
composition, search, reflow, changed geometry and ambiguous matches fall back
to immediate presentation. Tapping a moving body stops its transition without
forwarding a potentially misplaced click. Immutable row layouts are bounded
to 128 entries / 1 MiB, keyed by actual styles and graphemes rather than IDs
that native resets can reuse. Desktop terminals retain their default path.

Fork 1eb3968d0ca4 merges upstream through ee5f5374. Upstream terminal changes
add selection extension, mirror handling and Windows wheel scaling; they do
not require an API migration of this package. Retty retains its own selection,
projection and input batching code rather than replacing it wholesale.

Kitty graphics bindings, PNG decoder and layered bitmap painting were adapted
from the pinned MyGo terminal. Retty caps storage/PNG pixels at 8 MiB, disables
filesystem/shared-memory media, generates matching iOS symbols, and adds
portable graphics snapshots for retained/remote screens and local reflow.
Text-only history keeps its existing payload. Link hover previews use captured
host readers, cancellation, bounded decoding and delayed UI overlays.

Selection copying supports visible text (excluding SGR-concealed characters)
and raw terminal text through Options.CopyRawText and SetCopyRawText. All three
copy paths share the same formatter. The visible-text binding uses the pinned
Ghostty selection snapshot/formatter APIs and keeps wrap, scrollback and block
selection semantics; it never parses Markdown or strips visible punctuation.

Options.SelectOnDrag / SetSelectOnDrag allow native text drags while a
primary-screen program tracks the mouse, deferring plain clicks until release
and preserving mouse scrolling. Retty enables this for recognized Agent panes
so their generated Markdown markers remain selectable. Option bypasses it;
Shift still forces selection for other mouse-aware programs.

Theme.MinimumContrast optionally adjusts text foregrounds against their final
cell background, including RGB, extended-palette and faint text. Retty enables
this for its light theme. Backgrounds, concealed text and drawn block/line
characters are preserved. Adjustments use a bounded color-pair cache while
building changed rows; terminal state and selection copying are unaffected.

Options.AdaptiveColors / SetAdaptiveColors handle programs that cache
their startup palette. Neutral RGB/extended-palette backgrounds with the
opposite appearance are mapped relative to the light/dark theme backgrounds,
and text gets a 4.5 contrast floor in both themes. Standard ANSI backgrounds,
colored panels and block/line artwork are preserved. Retty enables this for
all panes; disabling it invalidates cached renderer rows.

Synchronized-output protection uses an inactivity watchdog rather than the
update's total age, and schedules its own wakeup even with a hidden cursor.
Active long redraws keep the previous frame visible until commit. Optional
OnRenderEvent reports slow updates and watchdog releases outside emulator
locks, with timing metadata only; Retty stores these in a bounded render log.

Software modifiers from MyGo's InputModifiers use the session's key encoder
for ASCII input and control keys, including Kitty event reporting and release
events. Terminal.SendKey gives native accessory buttons the same cursor/key
protocol behavior. Software Cmd+C/V/A invoke local selection/clipboard actions;
hardware app shortcuts retain their existing routing.

Selection drags auto-scroll within an 8-DIP band of the visible top/bottom
edge, so a maximized window does not require the pointer to leave its grid.
A long-press touch selection widens the band to two rows (Retty-local, not yet
in MyGo): a finger covers the text it selects and stops on the first or last
row instead of crossing the edge.
Ticks preserve the edge trigger and extend the selection across scrollback;
moving back inside or releasing the pointer stops scrolling.

By default, mouse-aware alternate-screen applications handle selection and their own
history scrolling automatically, based on terminal modes rather than program
identity. Their off-screen history is not in the terminal's scrollback.
Primary-screen drags keep SelectOnDrag behavior; Shift always forces local
terminal selection, and alternate screens without mouse tracking select locally.
`SelectOnDragInAltScreen` explicitly extends `SelectOnDrag` to editors; Retty
enables it for Vim/Neovim panes so dragging copies their visible text. Clicks,
wheel input and Option drags still reach the editor. Other applications retain
their existing selection ownership, including when the same pane changes programs.

MyGo's checked-element migration (`ui.Element` values, build-scoped
`Context`) is carried here as upstream did: the view keeps `ui.Services`
rather than a `*ui.Context`, and `OnPaste`/`OnSubmit` receive services,
because input handlers run outside a build pass. `view.painted` records the
box of the last paint for caret tests, as elements expire with their pass.

Retty also consults `OnPaste` for desktop Ctrl+V before encoding a raw key.
The host consumes it only for clipboard PNGs targeting a remote Codex pane,
uploading the file and pasting its remote path. A false return preserves the
ordinary Ctrl+V key; standard text paste and iOS paste behavior are unchanged.

The MyGo v0.3.6 fork upgrade (7cdc2f22b829) has no changes to the upstream
terminal plugin relative to the previously pinned fork commit 0663ec5c09b1.
The local terminal additions and Ghostty library pin remain applicable.

The MyGo v0.3.7 fork upgrade (33911222f556) also leaves the upstream
terminal plugin unchanged relative to 7cdc2f22b829. Retty keeps the same
local terminal additions and Ghostty library pin; the UI changes add list
sideways scrolling and text-line tracking, and fix unconstrained column
growth and table column dragging.

Fork commit de6ced6a3965 resolves Finder file-reference URLs during native
macOS file drops. It leaves the terminal plugin and UI APIs unchanged;
Retty retains the same local terminal additions and Ghostty library pin.

Fork commit 364c8ce0c826 merges upstream through 05b235d: text ranges retain
their styles during input-method composition, and apps without bindings no
longer generate a TypeScript client. The terminal plugin is unchanged from
de6ced6a3965; Retty retains its local additions and Ghostty library pin.

## Generic terminal enhancements returned to the MyGo fork

Fork commit `c1b0ee5` on `main` now contains the generic parts of Retty's terminal work:
Ghostty incremental search/highlights, visible/raw selection formatting,
primary-screen drag selection, edge auto-scroll and touch selection, software
key encoding and input-method context, fixed-grid connections and atomic screen
replacement, typed file links, optional color contrast/adaptation, locale
inheritance and the synchronized-output inactivity watchdog. Its existing
`OpenLink`, `OnPaste(text)` and `OnKey` APIs remain compatible.

MyGo keeps explicit copying by default; `CopyOnSelect` enables automatic copy.
It keeps its original horizontal grid padding. Retty's vendor still enables
automatic copying and uses zero horizontal padding.

Retty retains its vendor for the CLI's UI-free VT package and iOS's statically
linked Ghostty packaging. Local-only pieces include `ReflowView` compact mobile
presentation, pane split menu callbacks, iOS bindings/build assets and screen
transport integration. Session ownership, reconnection, transfers, notification
routing, connection-quality policies and tab/pane trees remain in Retty.

Future terminal fixes should land in MyGo first, then be carried into the local
vendor where applicable. Keep MyGo's native manifest/build generator and Retty's
iOS/CLI packaging independent when synchronizing.

Fork commit `c4a1f47` on `main` adds `Terminal.ClearSelection`, which cancels
the local highlight without sending input or changing clipboard contents.
Retty carries the same API into its vendor and also clears the compact mobile
presentation's selection. The mobile copy menu uses it when dismissed by an
outside press or Escape; copying still keeps the selection until dismissed.

Fork commit `f83962e` on `feat/ios-platform` incorporates upstream through
`296ea85` (v0.4.1), first merged into fork `main` as `45e1d2a`. The terminal
plugin and its native manifest are unchanged from `e482207`; Retty retains
its local terminal additions and Ghostty library pin. The merge preserves
startup first-frame readiness, iOS input handling and foreground wake leases.

Fork commit `4a0b646fc5a3` consolidates the existing iOS startup, foreground
wake and inline-combobox capabilities on `main`, and merges upstream through
`9a8bf7e` (including v0.4.2 and browser-native UI support). Relative to the
previous Retty pin `bc60b9d50231`, the framework terminal adds `ClearSelection`
and a fitted-font validation test, both already covered by Retty's vendor.
The Ghostty manifest and terminal renderer are unchanged; Retty keeps its
local CLI/iOS packaging and terminal additions.

Fork commit `cfa1af8d1350` adds opt-in `Options.OpenLinksOnTap`. Unmodified
primary taps open links on release; movement, cancellation, scrolling and
long presses do not open them. Retty carries the same behavior into its
vendor and enables it only for mobile sessions. Link hit testing uses the
visible emulator, including Retty's local compact/reflowed presentation,
so OSC 8 labels and wrapped URLs resolve at their displayed coordinates.

Fork commit `c55486a` also preserves soft wraps and cell-level OSC 8 targets
when serializing VT snapshots. The pinned Ghostty formatter includes those
targets in HTML but not VT, so the Go binding pairs both formats by exact
text positions, retaining duplicate labels, Unicode and history targets.
Retty mirrors the fix in both full and recent-history snapshots; native
library manifests and iOS/CLI packaging remain unchanged.

Fork commit `c8fd199` merges upstream through v0.4.8 (`c0e1068`), including
`ui/editor` for code and diff previews. The terminal adds an optional `OnScroll`
callback for application-owned remote history; Retty keeps its existing screen
transport and local terminal extensions. The Ghostty manifest is unchanged,
so the local CLI/iOS library packaging and snapshot/link fixes remain applicable.

Fork commit `932609c` merges upstream through v0.5.1, and `0f6c018` keeps an
inline combobox field from claiming its dialog's Escape. The framework terminal
adds clipboard reads with allow/deny/ask (OSC 52), pasted images for programs
that read the clipboard (OSC 5522, through `ghostty_terminal_paste`), `OnZoom`
and Windows Terminal key bindings; its copy on select matches what Retty's
vendor already does. These are new features rather than fixes to code Retty
shares, so the vendor does not carry them yet. The Ghostty manifest is
unchanged and Retty's local terminal additions remain applicable.

Fork commit `f45d9d0569e2` merges upstream through v0.5.5. Retty carries
the fix that automatically copies Select All through the same visible/raw
selection formatter as pointer selections, while keeping its reflowed display
and existing automatic-copy behavior. The upstream clipboard-format paste
changes depend on the newer clipboard protocols that Retty's vendor does not
yet expose; its download helper is internal to MyGo. The Ghostty manifest is
unchanged, so Retty keeps its independent CLI/iOS packaging and library pin.

Fork commit `9d63caa85ca9` adds the opt-in alternate-screen drag override above,
with a real Vim mouse-mode regression and protocol checks for clicks, wheel,
Option drags and switching back to application-owned selection. Retty carries
the same terminal option and enables it for Vim/Neovim, including vi/view aliases.

Fork commit `14d502e3a56a` enables higher iPhone display-link refresh rates
for ProMotion by setting the required generated Info.plist opt-in and requesting
a 60–120 Hz frame-rate range. The terminal plugin, Ghostty manifest and UI APIs
are unchanged from `9d63caa85ca9`; Retty retains its local terminal additions
and independent CLI/iOS packaging.

Fork commit `0a53a811f153` merges upstream list selection/drag selection,
comparable element keys, weighted text-range layout, effect-layer lifetime,
macOS tray positioning and sender-avatar notifications. The terminal plugin
and Ghostty manifest are unchanged from `14d502e3a56a`; Retty retains its
local terminal additions and independent CLI/iOS packaging.

Fork commit `b77d64225a35` recognizes hard-wrapped file paths in indented
table cells, preserving complete paths and line/column locations across rows.
Hit testing and hover underlines cover only the path cells on the pointed row,
excluding the table's left column and padding. Retty carries the same fix
while retaining its reflowed display and independent CLI/iOS packaging.

Fork commit `586185157d9c` merges Android support, software-modifier routing
for native text input, safe-area reporting and fixed-height list/grid layout.
Retty aligns its terminal grapheme segmenter with MyGo's typesetting fork;
the upstream newline mirror fix already matches the local implementation.
The terminal's Ghostty manifest is unchanged, so Retty retains its reflowed
display and independent CLI/iOS packaging.

Fork commit `affb7ef7eb39` incorporates upstream window/tray activation fixes
and restores platform builds after the Android merge, including exclusive
iOS CoreText/Metal/backend selection and the existing UIKit safe-area layout.
The terminal plugin and Ghostty manifest are unchanged from `586185157d9c`;
Retty retains its local terminal additions and independent CLI/iOS packaging.

Fork commit `955c5f2a7739` forwards `InputOptions` for native text-input
clients, restoring mobile terminal drag-to-dismiss behavior after Android's
`HandleTextInput` integration. Keyboard traits update and reset with the
focused element; both scroll directions keep working across keyboard resize.
The terminal plugin and Ghostty manifest remain unchanged.

Fork commit `913e9acb7a9a` keeps custom controls with `TextContext` on
iOS's surrounding-text snapshot bridge, even when they also register a
native text-input client for indexed backends such as Android. This preserves
the committed input and caret after English and Chinese edits instead of
resetting UIKit's proxy to empty text at offset zero. The terminal plugin,
Ghostty manifest and Retty's independent CLI/iOS packaging are unchanged.

Fork commit `963f451f` merges upstream through `3e9f9c6c`, including partial
GPU redraws, layout/text caches, asynchronous Metal drawable acquisition and
smooth scrolling for framework scroll containers. The terminal plugin's
implementation and Ghostty manifest are unchanged from `913e9acb7a9a`.
`ui.Tester.Scroll` now models precise scrolling; `Tester.Wheel` models mouse
wheel notches. Retty's wheel protocol/scrollback tests use `Wheel` to retain
their existing three-row notch semantics, while phone/reflow and tap gesture
tests retain precise scrolling. Retty keeps its local terminal additions and
independent CLI/iOS packaging; the iOS text-context and keyboard-dismiss fixes
remain in the fork.

Fork commit `5e44f43cd033` merges upstream through `c4d36967`, including
glyph-atlas sampling, native text input, virtual-list and window fixes. The
terminal plugin adds optional frame mirrors, Kitty graphics, configurable
padding and additional clipboard/drop APIs; Retty keeps its existing screen
transport and platform packaging. The Ghostty library pin is unchanged.
Retty carries the expanded contrast exemption for font-rendered box drawing,
legacy computing and Powerline artwork. Its search highlights already clip
wrapped matches using screen coordinates, including matches entering from
above the viewport. The build-scoped UI API requires no migration.

Fork commit `376f50a86890` incorporates upstream v0.6.3, prevents duplicate
native text-field paste on Windows, releases idle text-shaping scratch
buffers, improves caret lookup and optimizes continuous Metal shadows.
The terminal plugin, Ghostty manifest and public UI APIs are unchanged
from `5e44f43cd033`; Retty retains its local terminal additions and
independent CLI/iOS packaging.

Fork commit `27615b686564` supplies Android's EGL `hasContext` implementation
and merges upstream through `26a1300f`, including GTK context recovery and
text-selection fixes. The terminal plugin adds overlay-scrollbar hover and
drag behavior; Retty retains its vendored scrolling and input implementation.
The public UI APIs and Ghostty library pin remain compatible.

Fork commit `2c8dcd97db9f` merges upstream through `80499935` and requires
Go 1.27.3. Retty carries the fixes for filenames wrapped as `write(notes.md)`
and Powerline rounded caps that fill a taller terminal cell, with upstream
hit-span and pixel regressions. MyGo's mirror mode-ordering fixes target its
optional `Mirror`, which Retty does not use; Retty retains its own screen
transport. Unicode-placeholder image rendering is a new upstream feature and
is not carried into this vendor. The Ghostty manifest and binding API are
unchanged, so Retty retains its independent CLI/iOS packaging and local
terminal additions, including bounded rendering-buffer reuse. The build-scoped
UI migration preview reports no changes.

Fork commit `cc1299d9a720` merges upstream through `9c69cf82`. It fixes
bold/italic editor shaping, Linux tray activation and carries touchpad
momentum through native UI input. Upstream's terminal uses that signal to
keep a scrolling gesture from becoming a zoom when Command is pressed
during momentum. Retty's terminal has no Command-wheel zoom path, so that
plugin change does not apply to this vendor. The Ghostty manifest/bindings
and public UI APIs remain compatible; local terminal behavior and independent
CLI/iOS packaging are retained.

Fork commit `0ba164b88d00` merges upstream through `15cb4a34` (MyGo 0.6.4)
and adds the mobile backends' `Page.ProcessID` stubs. This update brings GPU
damage/repaint improvements, opt-in subtree caching and hidden elements,
editor range backgrounds and DIP scrolling, menu icons, and updater fixes.
The terminal plugin and Ghostty manifest/bindings are unchanged from
`cc1299d9a720`, so no terminal vendor changes are needed. Retty retains its
local terminal additions and independent CLI/iOS packaging.
The build-scoped UI migration preview requires no changes.

Fork 80d5e6a3ea2f carries upstream's drawn legacy-computing sextants
(U+1FB00 to U+1FB3B, upstream #920) into box.go, so QR codes printed with
them render without gaps. They keep their colors under the minimum contrast
as the other drawn shapes do.
