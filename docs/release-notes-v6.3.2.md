# v6.3.2 - the TUI no longer dies, or hangs, between one turn and the next

A patch. Five fixes, no API change, no migration, and the module path is unchanged at
`rogerai.fm/roger/v6`.

## The hang: a turn that stopped mid-answer and never came back

The agent streams a turn's steps to the screen through a single reader, re-armed after
every step. One kind of step forgot to re-arm it.

That step is the quiet `⋮` notice the harness posts on the turn's behalf: the session was
compacted, or could not be, or a recited prompt was trimmed off the reply. All three
happen in the MIDDLE of a turn and the turn carries on afterwards. But the reader stopped
there, so nothing after that line was ever read: the answer never appeared, the turn never
reported finished, the prompt stayed locked, and esc sat on "cancelling…". The turn had
not crashed and was not stuck on the network. Nobody was listening.

The trim notice fires for any station that reads its own instructions back, which is
routine, so this was not a rare corner.

It is the same mistake as the cost-tick freeze fixed earlier, one case further down the
same switch, and it is now impossible to make quietly: a test walks every kind of step the
harness can stream mid-turn and fails if any one of them leaves the reader unarmed.

## The crash: `panic: send on closed channel`

Queue a prompt while a turn is running - the STANDBY line - and the session could die the
moment that turn finished:

```
panic: send on closed channel
  internal/tui.model.startAgentTurn.func1.1.1   agent.go:1728
  internal/harness.(*Loop).Send.func2            loop.go:400
  internal/harness.(*Loop).Send                  loop.go:440
```

A turn ran in its own goroutine, streaming steps to the UI over a channel, and signalled
that it was finished by CLOSING that channel. On the way out it did two things in this
order:

```go
rt.running.Store(false)   // the guard that says "no turn is running"
close(rt.events)          // the channel every turn sends on
```

Between those two statements the guard was clear while the channel was still open and
about to close. Almost nothing could get into a window that narrow, because every path
that starts a turn waits for the turn-finished message - which arrives only *after* the
channel is closed and replaced.

Almost. A prompt parked on STANDBY arms a 120ms re-check, and that re-check starts the
next turn on the guard alone. Land inside those two instructions and the new turn grabbed
the outgoing channel, the old goroutine closed it, and the next thing the new turn tried
to say killed the process. Which is why this hit people mid-sentence, on a perfectly
ordinary second question, and never reproduced on demand.

There was a second way in. The channel was a plain field that the UI's reader REPLACED
when it saw the close, while three other goroutines were reading that same field with
nothing synchronising them. Winning the two-instruction race was not even required.

Both are now gone by construction rather than by timing. The events channel is created
once and is never closed and never replaced. End-of-turn travels on its own per-turn
signal, which is published *before* the guard drops - so there is no instant at which a
turn looks finished while it can still speak. A channel nobody closes cannot be sent on
after closing, and a field nobody replaces cannot be read stale.

Two smaller things fell out of it. A turn's last words now always arrive before its
"finished" does, instead of the two racing. And a cancelled turn that nobody is listening
to gives up rather than blocking forever - which, on a channel that is never closed,
would have quietly wedged every later turn.

## A stopped turn now actually stops

Two more places could leave a force-stopped turn's goroutine alive forever: the relay
reporting what a cancelled call cost, and a stopped turn reaching a tool that wanted
permission, which raised a y/N gate for work the operator had already abandoned. Both now
give up when their turn is cancelled, and an abandoned gate answers no rather than
waiting. That matters more than it sounds: a goroutine that never finishes is a turn that
never releases the session, and with the fix below it would have meant `/clear` and
`/model` never working again either.

## A second, quieter one: `/clear` during a force-stop

Pressing esc twice force-stops a turn: you get the prompt back immediately while the
turn's goroutine finishes unwinding in the background. Splitting those two is the whole
point of it.

But the slash-command gate only asked whether the UI was busy, and after a force-stop it
is not. So a `/clear` typed in that window reset the conversation from the UI while the
abandoned turn was still reading and writing it. No crash, and nothing you would notice at
the time - just two threads editing one conversation.

`/clear` and its neighbours now wait for the turn's goroutine to actually be gone, the
same rule the rest of the queue already followed. Commands that touch nothing shared -
`/perms`, `/webui`, `/console`, `/mouse` - still run instantly, so the force-stop window
does not become a dead zone.

## The website: two figures that were quietly wrong

On the Wave Spectrum page, the animated mark bunched up on a phone: the on-air dot crowded
`ROGERAI.FM` and the tier nameplate reached the edge of its box. The spacing was written
in the mark's own drawing units, tuned at the size it renders on a desktop. A phone draws
the same mark smaller, so the art and its breathing room shrank together and there was
nothing left to give. The gaps are now held at the size they have at full width, so a
phone gets the desktop spacing. Desktop is untouched.

The size rail had a real overlap, and not only on mobile: `Tera`, `Peta` and `Exa` sat on
one row with their labels on top of each other - by 6.4px and 12.4px, on every screen. The
figure's own note had concluded that the markers were far enough apart to skip the
stagger, which was true of the DOTS (46px apart) and not of the LABELS (58px wide). Peta
drops a row again, and the arithmetic is now measured by a test so a future re-spacing
cannot quietly undo it.

## Upgrading

```sh
curl -fsSL https://rogerai.fm/install.sh | sh
```

Nothing to migrate.
