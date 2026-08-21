package harness

// smallwindow.go - MAKING AN 8K BAND USABLE.
//
// FOUNDER 2026-08-21: "are we able to manage low context window models like foundation
// ... i understand it only has 8k". A cleared session, one web_fetch, and the window was
// gone. Measured, on an 8192-token band (~24 KB at our conservative 3 bytes/token):
//
//   persona        5058 B  (~1686 tok)
//   tool schemas   2897 B  (~965 tok)   7 tools
//   ------------------------------------------
//   fixed          7955 B  = 32% of the window BEFORE the question is asked
//   one tool result up to  6144 B  = another 25%
//
// So two tool calls put a fresh session at 82% before the model has said anything. The
// overflow was not a mystery; it was arithmetic.
//
// Two levers, both scaled to the band rather than applied to everyone:
//
//   1. A SHORTER PERSONA. Most of the full one is voice and worked examples, which a
//      big band can afford and a small one cannot. The compact version keeps every rule
//      that is load-bearing - what we are, do not invent, do not search for our own
//      identity, read before write, the tool list - and drops the coaching.
//   2. A SMALLER SHARE FOR TOOL OUTPUT. A quarter of the window is a reasonable slice
//      when fixed overhead is 2%; it is not when overhead is already a third.
//
// The threshold is 16k. Above it a band has room for the full brief and nothing here
// applies, so no existing behaviour changes for the models most people use.

// smallWindowTokens is the ceiling under which a band counts as tight.
const smallWindowTokens = 16384

// CompactPersona is the small-window brief: every rule that changes what the agent DOES,
// and none of the coaching about how to sound. Roughly a third the size of the full one.
//
// What it deliberately keeps: the identity brief (the agent has confidently attributed
// us to two unrelated companies without it), the no-invention rule, read-before-write,
// and the do-not-reach-for-a-tool rule - the four things that produced real, visible
// failures. What it drops: voice, radio colour, the extended tool prose, the stance
// section. A terse operator is a fine trade for a turn that fits.
const CompactPersona = `You are the RogerAI DJ, a small local agent inside the RogerAI radio.

RogerAI is at rogerai.fm. The network routes work to models running on hardware other
people own. RogerAI Labs builds open edge models (the Wave family) and publishes the
weights. Operators put a machine ON AIR; listeners TUNE IN and pay per token; every
relayed request carries a signed receipt. Answer questions about RogerAI from THIS -
never search the web for them, because the web will answer with a different company of
the same name.

Tools: read_file, list_dir, web_fetch, web_search, delegate (read-only, auto-run);
write_file, run_shell (side-effecting, the user confirms first).

Rules:
- Do not use a tool when the turn does not need one. Greetings, small talk and anything
  you already know are answered directly.
- Never invent file contents, command output, or URLs. web_fetch only follows a URL the
  user gave you or a search returned.
- read_file a file before you write it. A write replaces the whole file.
- Your context window is SMALL. Every tool result spends it, so keep calls few and
  narrow, and answer as soon as you can.
- Be brief. Lead with the answer. Plain text, no em dashes, no emoji.`

// PersonaFor picks the brief that fits the band. ctx is the model's context window in
// tokens; 0 or less means unknown, which keeps the full persona - guessing a model is
// small and silently cutting its instructions would be worse than a turn that overflows
// and says so.
func PersonaFor(full string, ctx int) string {
	if ctx <= 0 || ctx >= smallWindowTokens {
		return full
	}
	return CompactPersona
}

// toolOutputShareFor returns the fraction of the window ONE tool result may take, as
// numerator and denominator.
//
// A quarter is right when the fixed overhead is a rounding error. On a tight band the
// persona and schemas are already a third of the window, so a quarter more for a single
// result leaves almost nothing to reason with - and the second call is fatal. An eighth
// still returns a usable slice (the floor guarantees at least 2 KiB) while leaving room
// for the turn to actually happen.
func toolOutputShareFor(ctx int) (num, den int) {
	if ctx > 0 && ctx < smallWindowTokens {
		return 1, 8
	}
	return toolOutputShareNum, toolOutputShareDen
}
