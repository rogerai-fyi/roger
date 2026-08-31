# v6.4.0 - the agent gets the hands it was missing

A minor: the agent's toolset grows from seven tools to eleven, and one of them is a way to
ask you a question. Nothing breaks. No API change, no migration, and the module path is
unchanged at `rogerai.fm/roger/v6`. The wire gains two frame kinds and one inbound kind,
all additive - an older viewer renders what it does not know as nothing.

## Edit the line, not the file

Until now the agent's only way to change a file was `write_file`, which replaces the WHOLE
file. To fix one line it had to reproduce every other line from memory, and anything it
failed to reproduce was silently gone.

`edit_file` replaces an exact string. Everything ambiguous is loud: no match is an error,
not a quiet no-op; more than one match is an error that says how many, unless
`replace_all` says all of them were meant; replacing text with identical text is refused.
It confirms before running, exactly as `write_file` does, and the approval prompt names
the file and shows the change. Under `/perms edits` it runs unasked - it is the edit that
mode is named for.

## Search without asking permission to look

There was no way to search the tree except `run_shell`, which confirms - so finding
things, the most ordinary read there is, raised a y/N every time. That teaches you to
approve shell commands by reflex, which spends the attention the gate exists to collect.

`grep` searches file contents (a regular expression, optionally scoped to a subtree or a
filename pattern) and returns path:line:text. `glob` finds files by name, most recently
modified first. Both are reads and run without a prompt, like reading a file always has.
Both stay inside the working directory, skip `.git`, `node_modules` and the agent's own
spill, skip binaries, and cap their output while saying so.

## A long file can be finished

`read_file` clipped at 16 KiB with no way to ask for the rest: a larger file simply could
not be read, and the agent worked from the part it happened to get. It now takes `offset`
and `limit`, and a truncated read names the offset to continue from. Even the edge where a
SINGLE line exceeds the cap says what happened instead of pointing past the unread rest.

## The agent can ask you a question

The only channel to you was the y/N gate, which can say exactly one thing: may I run
this. An agent at a real fork - an instruction that could mean two things, two designs it
cannot choose between on the evidence, a destructive step worth naming - had to guess, or
stop.

`ask_operator` puts the question on screen and waits. Offered options are numbered;
pressing a digit picks one, and you can always type something it did not think to offer.
esc declines, which is itself an answer and is delivered as one.

Three things make it a question and not a permission prompt. No approval mode answers it
for you - `/perms all` auto-approves side effects because you said "run without asking",
and answering a QUESTION on your behalf would be a different thing entirely. A stopped
turn can never raise one, and one already on screen is never taken away while you are
reading it. And subagents do not get it: a child runs where you cannot see it, so a child
needing a decision reports back and lets the parent ask.

A question also follows you: it is mirrored to an attached BASE STATION, shows in `roger
remote` and the TUI viewer, can be answered from whichever surface you are looking at,
and closes everywhere the moment one of them answers. A late answer to an earlier
question is dropped rather than resolving the one now open.

## Upgrading

```sh
curl -fsSL https://rogerai.fm/install.sh | sh
```

Nothing to migrate.
