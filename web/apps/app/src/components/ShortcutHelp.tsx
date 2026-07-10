import { useEscape } from "./AssetThumb";

// Keyboard shortcut reference (?): the vocabulary accumulated across
// surfaces, grouped. Static by design — a registry can come later if
// shortcuts start moving.
const GROUPS: [string, [string, string][]][] = [
  [
    "Global",
    [
      ["⌘K", "Command palette — jump anywhere"],
      ["?", "This help"],
      ["Esc", "Close topmost modal / deselect"],
    ],
  ],
  [
    "Timeline",
    [
      ["Space", "Play / pause (at end: restart)"],
      ["B", "Blade at playhead"],
      ["⌫ / Delete", "Remove selected clip (⇧: ripple)"],
      ["⇧ + right-edge trim", "Ripple trim"],
      ["⌥ while dragging", "Disable snapping"],
      ["⌘Z / ⇧⌘Z", "Undo / redo"],
    ],
  ],
  [
    "Clip player",
    [
      ["J / K / L", "Back-jog / pause / play (L again: 2×, 4×)"],
      ["← / →", "Step one frame"],
      ["Space", "Play / pause"],
    ],
  ],
  [
    "Take picker",
    [
      ["1–9", "Select take N"],
      ["← → ↑ ↓", "Move highlight"],
      ["↵", "Commit highlight"],
    ],
  ],
  [
    "Canvas",
    [
      ["⌘Z / ⇧⌘Z", "Undo / redo"],
      ["Esc", "Clear selection"],
    ],
  ],
];

export function ShortcutHelp(props: { onClose: () => void }) {
  useEscape(props.onClose);
  return (
    <div className="overlay" onClick={props.onClose}>
      <div className="modal" role="dialog" aria-modal="true" aria-label="Keyboard shortcuts" onClick={(e) => e.stopPropagation()}>
        <div className="panel-header">
          <h3>Keyboard shortcuts</h3>
          <button className="btn secondary" onClick={props.onClose}>Close</button>
        </div>
        <div className="shortcut-groups">
          {GROUPS.map(([title, keys]) => (
            <div key={title} className="shortcut-group">
              <h4>{title}</h4>
              {keys.map(([k, desc]) => (
                <div key={k} className="shortcut-row">
                  <kbd>{k}</kbd>
                  <span className="meta">{desc}</span>
                </div>
              ))}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
