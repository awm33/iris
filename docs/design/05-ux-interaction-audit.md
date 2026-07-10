# UX interaction audit — creative-tool patterns for Iris

*Deep-research run, 2026-07-09 (104 agents, adversarially verified; only claims surviving 3-vote verification are listed). Feeds the PR-33 affordances and the post-dogfood design sprint.*

## Summary

The verified evidence splits the field into two chrome philosophies: Adobe's fully flexible model (persistent Tools panel + Options bar, a rich dock/group/stack/collapse panel vocabulary, position-disambiguated drop zones) versus fixed-workspace models — DaVinci Resolve's single-click page navigation, and Figma's UI3, where floating panels were shipped in beta and then deliberately reverted to fixed docked panels because they measurably slowed users down. AI-native tools side decisively with simplification: Runway put generation in a conversational Chat Mode (June 2025) and shipped a deliberately minimal trim/stitch/reorder Studio timeline (June 2026), and even Adobe now layers a dismissible, selection-triggered Contextual Task Bar (with Generative Fill at the moment of selection) on top of its panels — strong evidence that Iris should keep its fixed rail + Context Dock model with contextual toolbars rather than adopt Adobe-style free docking for light professionals. For Iris's imminent features: the rail should follow the shadcn icon-collapse mode with Cmd/Ctrl+B (scoped away from text/blade contexts); the history panel should present per-gesture states with linear-by-default semantics (Photoshop's model) but exceed Photoshop by persisting across sessions and never destructively overwriting generative outputs (variant-grid branching, confirm-before-overwrite); op-grouping should break on any navigation/selection-change event rather than rely on timers, per Blender's documented design reasoning. Notably, no claims about ⌘K palette conventions (Linear/Notion/Raycast), shortcut-overlay conventions, or Final Cut Pro survived verification, so those recommendations cannot be grounded in this evidence base.

## Verified findings

### 1. Photoshop's baseline workspace architecture (still documented as of mid-2026) is maximal-flexibility persistent chrome: …

**Full claim:** Photoshop's baseline workspace architecture (still documented as of mid-2026) is maximal-flexibility persistent chrome: a Tools panel organized by related function, a separate Options bar showing settings for the selected tool, an Application bar with a workspace switcher, and free-floating/dockable panels with a full management vocabulary (dock/undock, move, add/remove, stack floating panels, expand/collapse to icons). This is the model Iris's later design-system pass would be buying into if it adopts Adobe-style docking — powerful but requiring users to learn six distinct panel-manipulation operations.

**Confidence:** high · **Verification vote:** 3-0 (claims 0, 2 merged)

**Evidence:** Adobe docs (updated Jun 2026): 'Tools panel: Features tools for creating and editing images... organized by related functions. Options bar: Displays settings for the currently selected tool.' Panel management is documented across six dedicated help topics: 'Dock or undock panels... Move panels... Add and remove panels... Stack floating panels... Expand or collapse panel icons.' Verifiers confirmed via Wayback capture and found no 2026 redesign removing this baseline.

**Sources:** https://helpx.adobe.com/photoshop/desktop/get-started/learn-the-basics/workspace-overview.html

### 2. Even Adobe has moved toward contextual, task-following UI layered ON TOP of its persistent panels: the Contextual Task B…

**Full claim:** Even Adobe has moved toward contextual, task-following UI layered ON TOP of its persistent panels: the Contextual Task Bar is a floating on-canvas menu whose contents dynamically change with the active tool or task (e.g., text controls while editing text), explicitly positioned as avoiding navigation through multiple panels and menus — and it is on by default but fully dismissible (dock, float, or hide via Window > Contextual Task Bar). Pattern for Iris: contextual toolbars supplement rather than replace stable chrome, and contextual layers must be user-controllable.

**Confidence:** high · **Verification vote:** 3-0 (claims 1, 3, 4 merged)

**Evidence:** 'The Contextual Task Bar is a dynamic menu based on your current tool selection or active task. It provides quick access to relevant options without requiring you to navigate multiple panels and menus.' 'Depending on your workflow needs, you can dock, float, or hide the Contextual Task Bar... turned on by default. To turn it off, select Window > Contextual Task Bar.' Verified verbatim against Adobe docs (last updated Oct 2025/Apr 2026); coexists with the Options bar rather than replacing it.

**Sources:** https://helpx.adobe.com/photoshop/desktop/get-started/learn-the-basics/workspace-overview.html · https://helpx.adobe.com/photoshop/desktop/get-started/learn-the-basics/boost-workflows-with-the-contextual-task-bar.html

### 3. Photoshop places its primary generative-AI entry point contextually at the moment of selection: once a selection exists,…

**Full claim:** Photoshop places its primary generative-AI entry point contextually at the moment of selection: once a selection exists, the Contextual Task Bar surfaces refine-selection, create-mask, fill, and Generative Fill inline. Precedent for Iris: gen-fill and similar generative actions should appear at the point of selection on canvas/timeline, not only inside the right-side Generate panel.

**Confidence:** high · **Verification vote:** 3-0 (claim 5)

**Evidence:** 'You can perform the following functions: Refine a selection. Create a mask. Fill a selected area with a solid color, gradient, or pattern. Use Generative Fill to fill the selected area with generated content.' Verified verbatim; 2026 third-party guides confirm the Generative Fill button appears in the CTB whenever a selection is active (Edit > Generative Fill also exists as a menu path).

**Sources:** https://helpx.adobe.com/photoshop/desktop/get-started/learn-the-basics/boost-workflows-with-the-contextual-task-bar.html

### 4. Photoshop's History panel records each individual gesture (a selection, a paint stroke, a rotation) as a separate histor…

**Full claim:** Photoshop's History panel records each individual gesture (a selection, a paint stroke, a rotation) as a separate history state, and clicking any state reverts the whole document to that point — granularity is per-gesture, not per-grouped-task. For Iris's visual history panel this is the incumbent convention: users of Adobe tools expect one visible row per gesture, which argues Iris's op-log needs display-level grouping only where a single user gesture emits multiple ops (so one gesture = one history row), not coarser task-level bundling.

**Confidence:** high · **Verification vote:** 3-0 (claim 6)

**Evidence:** 'Each change you make, like selecting, painting, or rotating, is recorded separately. When you select one of the states, the image reverts to how it looked when that change was first applied.' Corroborated by Adobe evangelist Julieanne Kost and third-party tutorials; no source describes grouping multiple gestures into one state. Default retention 50 states (configurable to 1000).

**Sources:** https://helpx.adobe.com/photoshop/desktop/get-started/set-up-toolbars-panels/history-panel-overview.html

### 5. Photoshop history is linear by default — stepping back and performing a new edit destroys all forward states (shown dimm…

**Full claim:** Photoshop history is linear by default — stepping back and performing a new edit destroys all forward states (shown dimmed until then) — and branching is an opt-in 'Allow Non-Linear History' option in which new states are appended to the end of the list while abandoned states remain. Design guidance for Iris: linear-with-destructive-truncation is the familiar default, but the non-linear option shows Adobe itself acknowledges branch preservation matters; an op-log-backed history can offer branch retention without Photoshop's confusing append-at-end presentation.

**Confidence:** high · **Verification vote:** 3-0 (claims 7, 20, 21 merged)

**Evidence:** Adobe docs: 'Allow Non-Linear History... Allows you to edit a selected state without deleting subsequent states... Dimmed states show edits that will be lost if you continue from a selected state.' Adobe 'Manage image states': selecting an earlier state and changing the image deletes all subsequent states unless non-linear is enabled; non-linear changes are 'appended at the end of the list.' Behavior stable since Photoshop 5.0 (1998); confirmed in current 2026 docs.

**Sources:** https://helpx.adobe.com/photoshop/desktop/get-started/set-up-toolbars-panels/history-panel-overview.html · https://jkost.com/blog/2021/02/working-with-undo-the-history-panel-history-and-art-history-brushes-in-photoshop.html

### 6. Photoshop history is session-scoped: all history states and snapshots are discarded when the document closes, and cross-…

**Full claim:** Photoshop history is session-scoped: all history states and snapshots are discarded when the document closes, and cross-session persistence remains an open user feature request as of mid-2026. Iris's persistent op-log can materially exceed the incumbent here — surviving history across sessions is a differentiator users have explicitly asked Adobe for and not received.

**Confidence:** high · **Verification vote:** 3-0 (claim 22)

**Evidence:** Kost (Adobe Principal Evangelist): 'All Snapshots and History states are discarded when a document is closed.' Confirmed by current Adobe HelpX: 'Closing the document clears all history states and snapshots.' Adobe Community feature request 'History Cleared When File Saved' still open through 2024-2025.

**Sources:** https://jkost.com/blog/2021/02/working-with-undo-the-history-panel-history-and-art-history-brushes-in-photoshop.html · https://helpx.adobe.com/photoshop/desktop/set-up-toolbars-panels/create-work-snapshots.html

### 7. Premiere Pro disambiguates dock-vs-group intent purely by drop position, with no mode or modifier: docking zones along p…

**Full claim:** Premiere Pro disambiguates dock-vs-group intent purely by drop position, with no mode or modifier: docking zones along panel/group/window edges insert the panel beside existing groups (resizing them), while grouping zones in the panel middle and tab area stack/tab it. If Iris ever builds docking, this spatial drop-zone grammar is the established Adobe interaction to copy (edge = split, center/tabs = group).

**Confidence:** high · **Verification vote:** 3-0 (claim 8)

**Evidence:** 'Docking zones: You'll view the docking zones along the edges of a panel, group, or window. Docking a panel places it near the existing group, resizing all groups... Grouping zones: You'll view grouping zones in the middle of a panel or group and along the tab area.' The only drag modifier (Ctrl/Cmd) produces a floating panel, not a dock/group switch. Corroborated by AGI Training and FilterGrade guides.

**Sources:** https://helpx.adobe.com/premiere/desktop/get-started/tour-the-workspace/dock-group-undock-panels.html

### 8. Figma shipped floating navigation/properties panels in the UI3 beta and then deliberately reverted to fixed (docked but …

**Full claim:** Figma shipped floating navigation/properties panels in the UI3 beta and then deliberately reverted to fixed (docked but resizable) panels, because beta feedback showed floating panels slowed users down, cramped the canvas on smaller screens, and made rulers less effective. This is the strongest direct evidence against free-floating/draggable panel chrome for a canvas tool aimed at Iris's audience — a company with enormous UX research capacity tested it and walked it back.

**Confidence:** high · **Verification vote:** 3-0 (claim 9)

**Evidence:** Figma blog: floating panels 'cramped the canvas, especially on smaller screens... made rulers less effective by moving them further away from designs'; 'The nail in the coffin was learning that they slowed people down'; 'we're reversing the change so that panels are fixed, but still resizable.' Corroborated by Figma Forum ('LAUNCHED: Fixed panels are back!') and UI3 GA coverage (Oct 2024).

**Sources:** https://www.figma.com/blog/our-approach-to-designing-ui3/

### 9. DaVinci Resolve uses a fixed, page-based navigation model — dedicated single-purpose workspaces (Media, Cut, Edit, Fusio…

**Full claim:** DaVinci Resolve uses a fixed, page-based navigation model — dedicated single-purpose workspaces (Media, Cut, Edit, Fusion, Color, Fairlight, Deliver) switched with a single click — rather than a freely dockable panel system; panels are toggleable/resizable but not drag-dockable, with only viewers floating. This is the closest structural analogue to Iris's left-rail sections (Story board/Scenes/Timelines/etc.): mode-per-task navigation with fixed layouts is a proven model for a multi-discipline editor and avoids the panel-management tax entirely.

**Confidence:** high · **Verification vote:** 3-0 (claim 10)

**Evidence:** 'DaVinci Resolve is divided into pages, each of which gives you a dedicated workspace and tools for a specific task... All it takes is a single click to switch between tasks!' Verifiers confirmed via a March 2026 Premiere-to-Resolve transition article, Creative Bloq, and Blackmagic's forum that there is no Premiere-style drag-and-dock system; current for Resolve 20 (2025-2026). Caveat: Resolve does offer layout presets, dual-monitor layouts, and floating viewers.

**Sources:** https://www.blackmagicdesign.com/products/davinciresolve

### 10. Resolve's Cut page implements a dual-timeline interaction — an upper whole-program overview plus a lower zoomed detail v…

**Full claim:** Resolve's Cut page implements a dual-timeline interaction — an upper whole-program overview plus a lower zoomed detail view at the playhead — alongside a source-tape review mode, positioned as a speed-oriented alternative to the traditional Edit page. Relevant to Iris's timeline editor: overview+detail dual timeline is a documented pattern for fast assembly work by non-specialist editors, and Resolve validates offering a fast surface and a precise surface over the same underlying timeline.

**Confidence:** high · **Verification vote:** 3-0 (claim 11)

**Evidence:** Blackmagic Cut page: 'The upper timeline shows you the entire program while the lower timeline shows you a zoomed in area of where you're working'; source tape shows 'all of the clips in your bin... in the viewer as a single long tape.' Corroborated by larryjordan.com and others; stable since Resolve 16 (2019) through 2026.

**Sources:** https://www.blackmagicdesign.com/products/davinciresolve

### 11. For Iris's collapsible rail, the off-the-shelf React convention is shadcn/ui's Sidebar: three documented collapse modes …

**Full claim:** For Iris's collapsible rail, the off-the-shelf React convention is shadcn/ui's Sidebar: three documented collapse modes — 'offcanvas' (slides fully off-screen), 'icon' (collapses to an icon-only strip), 'none' — with Cmd+B (Mac) / Ctrl+B (Windows) as the default toggle shortcut, implemented as a configurable constant (SIDEBAR_KEYBOARD_SHORTCUT = 'b'). Recommendation: use icon-collapse (keeps Story board/Scenes/Jobs one click away) and Cmd/Ctrl+B, but scope the binding so it never fires while text editing is focused (bold collision) or when B is bound as the blade tool in the timeline.

**Confidence:** high · **Verification vote:** 3-0 (claims 12, 13 merged)

**Evidence:** Verified live July 2026: 'offcanvas: A collapsible sidebar that slides in from the left or right. icon: A sidebar that collapses to icons. none: A non-collapsible sidebar.' and 'To trigger the sidebar, you use the cmd+b keyboard shortcut on Mac and ctrl+b on Windows... const SIDEBAR_KEYBOARD_SHORTCUT = "b".' Cmd/Ctrl+B matches VS Code's sidebar toggle, so it aligns with broader ecosystem muscle memory. Verifier caveat: B collides with bold and with NLE blade conventions — scope accordingly.

**Sources:** https://ui.shadcn.com/docs/components/radix/sidebar

### 12. Runway — the closest AI-native comparable to Iris — chose radically simplified surfaces over Adobe conventions: in June …

**Full claim:** Runway — the closest AI-native comparable to Iris — chose radically simplified surfaces over Adobe conventions: in June 2025 it shipped Chat Mode, consolidating image, video, and reference generation into a single conversational interface (added alongside, not replacing, its tool sessions), and in June 2026 it added Studio, a deliberately minimal integrated timeline limited to trim/stitch/reorder/export with no blade/ripple/multi-track tooling. Evidence for Iris's later pass: AI-native tools targeting light users converge on one generation surface plus a minimal assembly timeline, not docked palette systems — Iris's Context Dock + timeline already exceeds Runway's editing depth.

**Confidence:** high · **Verification vote:** 3-0 (claims 14, 15 merged)

**Evidence:** Changelog Jun 12, 2025 — Chat Mode: 'A new way to create with Gen-4 Images, Videos and References. Generate anything you want, all from within a single conversational interface.' Jun 18, 2026 — Studio: 'Trim, stitch, reorder and export a final video. All in one place.' Runway help center frames Studio 'for shorter projects or quick assembly tasks'; no source attributes blade/ripple-grade tooling to it. Chat Mode remained live and expanded (Aleph, third-party models) into 2026.

**Sources:** https://runwayml.com/changelog

### 13. Blender's core undo-grouping design (Campbell Barton, issue #71735) restricts automatic grouping to single-character ins…

**Full claim:** Blender's core undo-grouping design (Campbell Barton, issue #71735) restricts automatic grouping to single-character insertions/removals and mandates that undo never group edits separated by cursor motion — i.e., grouping should break at any navigation event, not on time or operation count. Direct guidance for Iris's op-log: group consecutive ops of the same kind on the same target (brush strokes, nudges), and hard-break the group on any navigation, selection change, or tool switch.

**Confidence:** high · **Verification vote:** 3-0 (claim 16)

**Evidence:** Verified via Gitea API against the issue body: 'Undo grouping will only be done for single character insertion/removal. - Undo wont group edits separated by cursor motion. - Word level grouping has the same delimiters as pressing Ctrl-Left/Right.' Authored by ideasman42 (Campbell Barton, 42,000+ commits). Historical (2019) design proposal, but its reasoning is the point, not currency.

**Sources:** https://projects.blender.org/blender/blender/issues/71735

### 14. Timer-based undo grouping (coalescing rapid successive inputs into one step) is documented as producing unpredictable gr…

**Full claim:** Timer-based undo grouping (coalescing rapid successive inputs into one step) is documented as producing unpredictable groupings when input speed hovers near the timer threshold — an anti-pattern for grouping policy when used as the sole signal. Iris's op-log grouper should use semantic boundaries (gesture/navigation/tool-change), with a timer at most as a secondary signal (as ProseMirror/CodeMirror do with newGroupDelay plus adjacency heuristics).

**Confidence:** medium · **Verification vote:** 2-1 (claim 17)

**Evidence:** Issue #71735: 'Using a timer may be unpredictable especially when the users typing speed hovers around the limit - undo groupings may seem unpredictable.' Split verifier vote (2-1): source hedges ('may seem') and this is one project's design judgment, not industry consensus — ProseMirror/CodeMirror history plugins do use ~500ms timers combined with adjacency heuristics as standard practice. The documented concern applies to timer-ONLY grouping.

**Sources:** https://projects.blender.org/blender/blender/issues/71735

### 15. The dominant convention among dedicated AI image-generation tools (Midjourney, Krea, Canva) is 'branched variations': ge…

**Full claim:** The dominant convention among dedicated AI image-generation tools (Midjourney, Krea, Canva) is 'branched variations': generate multiple options at once — commonly four, from a common seed — presented as a grid of thumbnails, any of which the user can branch from with further prompts/actions. This validates Iris's take-picker/variant-grid direction and implies takes should be first-class branchable nodes, not transient previews.

**Confidence:** high · **Verification vote:** 3-0 (claim 18)

**Evidence:** shapeof.ai (Emily Campbell's curated AI UX pattern library, citing Krea/Midjourney/Canva): 'Multiple options (commonly four) are created at once, deriving from a common seed, and shown in a grid of thumbnails.' Verifiers corroborated against primary docs: Midjourney's current 4-image grid with per-image V1-V4 variation branching (v7-era, 2025) and Canva Magic Media's 4 thumbnails per prompt. Scope caveat: chat-embedded generators (ChatGPT, Gemini) default to single images; 'common seed' is a simplification for tools assigning per-image seeds.

**Sources:** https://www.shapeof.ai/patterns/variations · https://docs.midjourney.com

### 16. Documented best practice for generative-output UIs: never destroy the prior result when regenerating — regeneration and …

**Full claim:** Documented best practice for generative-output UIs: never destroy the prior result when regenerating — regeneration and variant selection must be non-destructive unless the user confirms, with past results easy to recover. For Iris's op-log/history: generation ops should append takes rather than replace them, the history panel should preserve superseded takes as recoverable branches, and any overwrite path needs explicit confirmation.

**Confidence:** medium · **Verification vote:** 3-0 (claim 19)

**Evidence:** shapeof.ai: 'Never overwrite the original output without confirmation. Variations should extend the workspace, not risk data loss or undo effort'; sibling regenerate pattern: 'Make past results easy to recover,' show regeneration 'as an iterative version... rather than simply overwriting.' Independent audit source (trykrux) flags destructive regenerate as critical: 'users can lose edits unknowingly if a regenerate replaces content without preserving versions.' Normative guidance from secondary pattern libraries, though unanimous and consistent with shipped-tool practice (Midjourney/Runway/Firefly grids, ChatGPT response branching).

**Sources:** https://www.shapeof.ai/patterns/variations · https://www.shapeof.ai/patterns/regenerate · https://trykrux.com/blog/ai-ux-patterns

### 17. Synthesis recommendation for the later design-system pass: the verified evidence points away from Adobe-style free docki…

**Full claim:** Synthesis recommendation for the later design-system pass: the verified evidence points away from Adobe-style free docking and toward a fixed-workspace model with contextual layers for Iris. Three independent signals align — Figma tested floating panels and reverted to fixed docked panels on measured slowdown; Resolve serves professional editors across six disciplines with fixed single-click pages and no drag-docking; Runway's AI-native surfaces are even more minimal (chat + basic timeline). Meanwhile Adobe itself, while retaining full docking for its incumbent pro base, is layering dismissible contextual task bars over that chrome. The pattern fit for Iris: keep left-rail sections as fixed Resolve-style modes, keep the Generate panel as a fixed right dock (per Figma's fixed properties panel), add selection-triggered contextual toolbars (per Photoshop's CTB) — and adopt Premiere's edge/center drop-zone grammar only if power-user demand later forces real docking.

**Confidence:** high · **Verification vote:** synthesis (claims 3, 9, 10, 14, 15 combined)

**Evidence:** Cross-finding synthesis: Figma's documented reversal ('The nail in the coffin was learning that they slowed people down'), Resolve's single-click page model current in Resolve 20, Runway's Chat Mode (2025) and minimal Studio (2026), and Adobe's default-on-but-dismissible Contextual Task Bar all converge. No surviving claim documents an AI-native tool adopting Adobe-style docking. This is an inference across verified findings, each individually 3-0.

**Sources:** https://www.figma.com/blog/our-approach-to-designing-ui3/ · https://www.blackmagicdesign.com/products/davinciresolve · https://runwayml.com/changelog · https://helpx.adobe.com/photoshop/desktop/get-started/learn-the-basics/boost-workflows-with-the-contextual-task-bar.html

## Caveats

Coverage gaps are the biggest caveat: no claims about ⌘K command-palette conventions (Linear, Notion, Raycast — nested commands, recents, keyboard hints), '?'-key shortcut-overlay conventions, Final Cut Pro (both submitted claims were refuted 0-3, including the common but wrong 'magnetic timeline has no tracks/no collisions' characterization), Descript/CapCut/LTX Studio, Resolve's keyboard-first color grading, Adobe keyboard customization, or documented user complaints about Adobe panel management survived verification — so the ⌘K-palette and shortcut-overlay recommendations in Iris's imminent feature set cannot be grounded in this report and rest on general practitioner knowledge. Two refuted claims should not be cited: Figma's 'Minimize UI' framing and the FCP no-tracks claim. Source-quality notes: shapeof.ai and trykrux findings are secondary design-guidance (normative, not descriptive), the Blender timer-grouping anti-pattern claim carried a 2-1 split (timers ARE standard as a secondary signal in ProseMirror/CodeMirror), and several Adobe/Photoshop claims were verified via Wayback/search-indexed copies because helpx pages timed out on direct fetch. Time-sensitivity: Runway's Studio finding is ~3 weeks old and based on a changelog blurb plus a help-center snippet; Adobe, Figma, and Runway UIs iterate quickly, so descriptive claims are pinned to mid-2026 documentation states. Resolve's fixed-page claim carries the qualifier that layout presets, toggleable/resizable panels, and floating viewers exist — 'fixed' means no drag-docking, not zero flexibility.

## Refuted during verification (do NOT rely on these)

- {"claim": "Figma's UI3 introduced a 'Minimize UI' mode that collapses the side panels for distraction-free work while keeping tools quickly accessible, replacing the all-or-nothing 'Hide UI' feature \u2014 a direct precedent for collapsible-rail/panel behavior.", "vote": "0-3", "source": "https://www.figma.com/blog/our-approach-to-designing-ui3/"}
- {"claim": "Final Cut Pro X's magnetic timeline removes the concept of tracks entirely, so clips cannot collide or overwrite each other \u2014 surrounding clips automatically move out of the way when material is inserted or moved.", "vote": "0-3", "source": "https://blog.frame.io/2017/10/16/fcpx-magnetic-timeline/"}

## Open questions

- What are the concrete ⌘K palette conventions in Linear, Raycast, and Notion as of 2026 — how they unify search+actions+navigation, handle nested commands and recents, and show inline keyboard hints — and which scope (global actions vs current-context actions) should Iris's palette default to?
- How do shipped editors visually present GROUPED operations in a history panel (labeling, expand-to-sub-ops, thumbnail per state) — Photoshop shows one row per gesture with no grouping, but is there a verified precedent for expandable grouped history rows that Iris's multi-op gestures could follow?
- Where do Descript, CapCut desktop, and LTX Studio place their generation/AI panels (fixed right dock vs modal vs chat), which would directly validate or challenge Iris's Context Dock placement?
- What documented, citable user complaints exist about Adobe panel management (lost workspaces, accidental undocking) and Resolve's page-model learning curve, to strengthen the anti-patterns section beyond inference?

## All sources

- {"url": "https://helpx.adobe.com/photoshop/desktop/get-started/learn-the-basics/workspace-overview.html", "quality": "primary", "angle": "Incumbent pro-tool architecture (Adobe) \u2014 broad/primary", "claimCount": 5}
- {"url": "https://helpx.adobe.com/photoshop/desktop/get-started/learn-the-basics/boost-workflows-with-the-contextual-task-bar.html", "quality": "primary", "angle": "Incumbent pro-tool architecture (Adobe) \u2014 broad/primary", "claimCount": 5}
- {"url": "https://helpx.adobe.com/photoshop/desktop/get-started/set-up-toolbars-panels/history-panel-overview.html", "quality": "primary", "angle": "Incumbent pro-tool architecture (Adobe) \u2014 broad/primary", "claimCount": 5}
- {"url": "https://jkost.com/blog/2021/02/working-with-undo-the-history-panel-history-and-art-history-brushes-in-photoshop.html", "quality": "blog", "angle": "Incumbent pro-tool architecture (Adobe) \u2014 broad/primary", "claimCount": 5}
- {"url": "https://helpx.adobe.com/premiere/desktop/get-started/tour-the-workspace/dock-group-undock-panels.html", "quality": "primary", "angle": "Incumbent pro-tool architecture (Adobe) \u2014 broad/primary", "claimCount": 5}
- {"url": "https://community.adobe.com/t5/premiere-pro-discussions/how-may-i-dock-the-new-properties-panel-in-v-25/td-p/14942833", "quality": "forum", "angle": "Incumbent pro-tool architecture (Adobe) \u2014 broad/primary", "claimCount": 4}
- {"url": "https://www.figma.com/blog/our-approach-to-designing-ui3/", "quality": "primary", "angle": "Fixed-workspace alternatives (Resolve, Final Cut, Figma) \u2014 comparative model", "claimCount": 5}
- {"url": "https://forum.figma.com/suggest-a-feature-11/launched-fixed-panels-are-back-23789", "quality": "forum", "angle": "Fixed-workspace alternatives (Resolve, Final Cut, Figma) \u2014 comparative model", "claimCount": 5}
- {"url": "https://blog.frame.io/2017/10/16/fcpx-magnetic-timeline/", "quality": "blog", "angle": "Fixed-workspace alternatives (Resolve, Final Cut, Figma) \u2014 comparative model", "claimCount": 5}
- {"url": "https://www.blackmagicdesign.com/products/davinciresolve", "quality": "primary", "angle": "Fixed-workspace alternatives (Resolve, Final Cut, Figma) \u2014 comparative model", "claimCount": 5}
- {"url": "https://blog.superhuman.com/how-to-build-a-remarkable-command-palette/", "quality": "blog", "angle": "Command palette and keyboard-discovery conventions \u2014 practitioner/implementation", "claimCount": 5}
- {"url": "https://uxpatterns.dev/patterns/advanced/command-palette", "quality": "blog", "angle": "Command palette and keyboard-discovery conventions \u2014 practitioner/implementation", "claimCount": 5}
- {"url": "https://mobbin.com/glossary/command-palette", "quality": "blog", "angle": "Command palette and keyboard-discovery conventions \u2014 practitioner/implementation", "claimCount": 5}
- {"url": "https://medium.com/design-bootcamp/command-palette-ux-patterns-1-d6b6e68f30c1", "quality": "blog", "angle": "Command palette and keyboard-discovery conventions \u2014 practitioner/implementation", "claimCount": 5}
- {"url": "https://ui.shadcn.com/docs/components/radix/sidebar", "quality": "primary", "angle": "Command palette and keyboard-discovery conventions \u2014 practitioner/implementation", "claimCount": 5}
- {"url": "https://www.techinterview.org/post/3233475212/build-command-palette-cmd-k/", "quality": "blog", "angle": "Command palette and keyboard-discovery conventions \u2014 practitioner/implementation", "claimCount": 5}
- {"url": "https://www.shapeof.ai/patterns/variations", "quality": "secondary", "angle": "AI-native creative tools' interaction choices \u2014 state-of-the-art/recent", "claimCount": 5}
- {"url": "https://runwayml.com/changelog", "quality": "primary", "angle": "AI-native creative tools' interaction choices \u2014 state-of-the-art/recent", "claimCount": 5}
- {"url": "https://aiuxplayground.com/patterns/design/", "quality": "blog", "angle": "AI-native creative tools' interaction choices \u2014 state-of-the-art/recent", "claimCount": 4}
- {"url": "https://projects.blender.org/blender/blender/issues/71735", "quality": "primary", "angle": "Anti-patterns and grouped-undo evidence \u2014 contrarian/skeptical", "claimCount": 5}
- {"url": "https://community.adobe.com/bug-reports-711/panels-in-photoshop-won-t-stay-the-way-i-set-them-1559215", "quality": "forum", "angle": "Anti-patterns and grouped-undo evidence \u2014 contrarian/skeptical", "claimCount": 5}
- {"url": "https://www.dpreview.com/forums/threads/davinci-resolve-is-it-really-that-formidable.4829177/", "quality": "forum", "angle": "Anti-patterns and grouped-undo evidence \u2014 contrarian/skeptical", "claimCount": 5}