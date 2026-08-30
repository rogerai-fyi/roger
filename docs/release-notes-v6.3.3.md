# v6.3.3 - a confirm you can see is always yours to answer

A patch, and the tail of v6.3.2. Three fixes, no API change, no migration, and the module
path is unchanged at `rogerai.fm/roger/v6`.

## A gate that was already on screen could be taken away

v6.3.2 made a stopped turn give up rather than hang, which was right, but it applied that
rule one step too far. A turn cancelled while a y/N confirm was already showing abandoned
it. The prompt stayed on screen swallowing keys, and when you finally pressed `y` nothing
was listening: your approval went into a channel nobody would read, and the turn was told
the tool had been denied.

Cancellation is still decided before the gate is offered, which is the part that matters -
a stopped turn should never ask permission for work you have already called off. But past
that point the decision belongs to the person looking at it. A confirm you can see is
always yours to answer.

## A new turn no longer inherits a stopped turn's last words

The turn stream is one long-lived channel now, and a force-stopped turn can leave a step
or two sitting in it. The next turn read them and rendered them as its own - including a
final answer, which writes a session footer for a turn that was stopped, and tool calls,
which were then attributed to whatever you asked next. A turn starts on an empty channel.

## The deck stops saying "ready" when it is not

While a stopped turn's work is still unwinding in the background, a prompt you send waits
for it. The deck said `AGENT ready - ask it to do something`, which is the opposite of
what is happening: nothing was sent, and your prompt is queued behind something you cannot
see. It now says the previous turn is still unwinding.

The same line also offered `esc cancels`. In that window there is nothing on screen left
to cancel, and esc LEAVES the agent - so the one key the message suggested was the one
that would throw away the wait. That hint only appears while a turn is genuinely running.

## Upgrading

```sh
curl -fsSL https://rogerai.fm/install.sh | sh
```

Nothing to migrate.
