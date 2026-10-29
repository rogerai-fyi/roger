// Base Station's pure render core (legacy: the window.RCView half of web/src/js/private.js):
// how a roster session reads, how each relayed RCFrame becomes a transcript line, and how the
// relay's SSE bytes become frames. Plain strings only: the component sets text, never HTML.

// stateLabel maps a roster session to its {dot, label}: the one red glint is the live dot.
export function stateLabel(s) {
  if (s.revoked) return { dot: "·", label: "ended", live: false };
  if (s.online) return { dot: "◉", label: "live", live: true };
  return { dot: "○", label: "offline", live: false };
}

// frameLine turns one RCFrame into a {cls, text} the stream renders, null for nothing to show.
export function frameLine(f) {
  switch (f.kind) {
    case "user": return { cls: "rc-user", text: "▸ (" + (f.origin || "someone") + ") " + (f.text || "") };
    case "assistant":
    case "final": return f.text && f.text.trim() ? { cls: "rc-asst", text: "◂ " + f.text } : null;
    case "tool_call": return { cls: "rc-tool", text: "◉ " + (f.tool || "") };
    case "tool_result": return { cls: "rc-tool", text: "✓ " + (f.tool || "") };
    case "confirm_req": return { cls: "rc-confirm", text: "? " + (f.tool || "") + " - approve? (runs on the host)", confirm: f.confirm_id || "" };
    case "confirm_done": return { cls: "rc-tool", text: "✓ " + (f.approve ? "approved" : "denied") + " from " + (f.origin || "") };
    // A guest-operator handoff (or the DJ-back return) renders dim, so the viewer never sees
    // the stream go dead mid-handoff. Operator-aware and content-blind: only the guest name
    // plus the model/spend metadata ride the frame; a status with neither renders nothing.
    // Enriched copy (founder ruling 3): "<op> has the mic on <model> · $<spend>", spend
    // formatted like the desk summary ($0.19); no model drops "on <model>", zero spend drops
    // "· $", and a frame with neither degrades to the pre-enrichment line (an old host).
    case "status": {
      let st = f.text || "";
      if (f.operator) {
        st = "◉ guest has the mic: " + f.operator;
        const spend = typeof f.spend === "number" ? f.spend : 0;
        if (f.model || spend > 0) {
          st = "◉ " + f.operator + " has the mic";
          if (f.model) st += " on " + f.model;
          if (spend > 0) st += " · $" + spend.toFixed(2);
        }
      }
      return st.trim() ? { cls: "rc-status", text: st } : null;
    }
    case "backfill": return f.text && f.text.trim() ? { cls: "rc-backfill", text: f.text } : null;
    case "error": return { cls: "rc-err", text: "✗ " + (f.text || "") };
    case "ended": return { cls: "rc-tool", text: "- session ended on the host -", ended: true };
    default: return null;
  }
}

// sseFrames takes the bytes decoded so far plus the carry from the last read and returns the
// complete "data:" frames in them (a partial or garbled line is ignored) and the new carry.
export function sseFrames(carry, chunk) {
  const parts = (carry + chunk).split("\n");
  const rest = parts.pop();
  const frames = [];
  for (const ln of parts) {
    if (ln.indexOf("data:") !== 0) continue;
    try { frames.push(JSON.parse(ln.slice(5).trim())); } catch { /* a partial or garbled line */ }
  }
  return { frames, rest };
}
