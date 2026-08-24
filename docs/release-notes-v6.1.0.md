# v6.1.0 - relay traffic stops being ceremonial

A minor, not a patch: there are new capabilities here, not only fixes. Nothing breaks -
no API change, no migration, no configuration change, and the module path is unchanged at
`rogerai.fm/roger/v6`.

## The edge bridge: consumer traffic finally rides Towers

This is the release's headline, and it closes a gap that made a whole feature decorative.

The direct and edge fabrics were two endpoints that never considered each other. Every
real consumer called the direct one, and the direct one refused Towers outright - so an
approved Tower carried canary probes and nothing else, and an operator's 10% relay share
was ceremonial. A Tower could be enrolled, approved, healthy, and still never earn,
because no consumer request could reach it.

`/v1/chat/completions` now serves edge-only models through the Tower's sealed hub, and a
model both fabrics host splits traffic on a request-seeded coin. The consumer's contract
is unchanged: same endpoint, same shape, same headers.

The Tower still reads nothing. The payload is sealed to the station and the answer sealed
back to a key only the broker holds - the bridge drives the same authorize / seal / submit
/ open / verify / acknowledge loop the canary already proved, through one shared code path
the canary now calls too.

**Two critical defects were caught by audit before this shipped**, and both are worth
naming because they are the kind that bill real money:

- **The bridge resolved the billing account from a request header.** It re-read
  `X-Roger-Pubkey`, and relay's grant path never verifies a signature - so a grant-bearing
  caller with a forged header could have billed any victim's wallet. Fixed at the root:
  relay passes the identity it has ALREADY verified into the bridge, which never re-derives
  it, and refuses to serve when the wallet does not match the account the verified key owns.
- **A streaming request was billed for framing bytes and answered with nothing.** The
  bridge sealed a `stream:true` body verbatim; a real station replies in SSE frames; the
  JSON parse on the way out read nothing. The consumer got an empty answer while the drive
  reported success, so no fallback fired and settlement billed the framing. The bridge now
  forces `stream:false` before sealing and re-frames the single JSON body into a stream on
  the way out. The test upstream was a stub that answered JSON to a streaming request,
  which is what hid it - it now honours `stream:true` with real SSE, so a stub cannot mask
  this again.

Three classes are now refused rather than mis-served: a **grant** never rides the edge (a
grant authorizes an owner's own hardware, not a stranger's Tower), a **browser session**
has no device signature to bind an acknowledgement so it stays direct, and **confidential**
traffic never crosses a third-party Tower.

## A home-lab Tower is not a broken one

The LAN tier is first-class. A Tower running on a home network was previously judged
against the same reachability expectations as a public relay and reported as degraded for
conditions that are simply what a home network is. It now has its own tier, and the
approval path around it is visible end to end:

- The **waiting room is visible**, the approver is told there is someone in it, and Core
  never dials inward to find out - the Tower reports outward, which is the same direction
  every other connection in this system travels.
- The **approval queue the dashboard reads** is the queue itself, not a second view of it
  reconstructed elsewhere.
- A job that **rides a Tower says so** on its receipt. Relay participation was previously
  invisible at the point where somebody reads what they were charged.
- The **durable head is the chain authority**, not each instance's memory. A Tower's
  identity survives a restart because it is written down, not because one process
  remembers it.
- **`0.0.0.0` is a bind wildcard, not an address.** Binding every interface and
  advertising a reachable address are two different questions, and only one of them was
  being answered. A Tower told to listen on `0.0.0.0` no longer publishes it as the place
  to reach it, which nothing on the network can act on.
- **A serving Tower can be operated.** `route`, `drain`, `resume`, `revoke` and station
  revoke are all locally read-only - route computes a receipt and persists nothing, the
  rest only post a signed request to Core - yet each took the exclusive data-directory
  lock and refused with "already owns" against a Tower that was serving. `drain`'s entire
  purpose is to quiet a serving Tower, and `route` is how a standalone client is served
  *while* serve runs, so both were unusable exactly when they were needed. Readers no
  longer take the lock; writers still do.
- **The serving line names both halves.** "listening on [::]:8444" beside "advertising
  192.168.1.69:8444" read as a contradiction; they are two halves of one listener. It now
  says so: the wildcard is what makes the LAN address reachable, and the LAN address is
  what nodes and consumers dial.

## Guest operators: pi arrives, dsh stops pretending

`pi` joins the desk. It was already installed on operators' machines and could never
appear, because the registry - the one source of who can take the mic - had never listed
it. Detection only looks for what is registered, so no amount of installing surfaced it.

It is wired the way `opencode` is: `PI_CODING_AGENT_DIR` redirects pi's whole agent
directory at a generated, single-provider catalog. Your own `~/.pi/agent` is neither read
nor written, and the generated provider is the only one pi can see, so no user layer can
silently re-route the guest onto a model or an account you did not choose. The trade is
stated rather than hidden: that run does not see your pi themes, extensions or sessions. A
guest at the desk is a fresh session on the band.

**`dsh` has never worked, and now says so instead of failing.** It shared `opencode`'s
strategy constant, and the code treated a shared constant as a shared config *format* - so
dsh was launched with opencode's config file, opencode's environment variable and
opencode's `-m` flag, none of which dsh reads. It answered `error: --profile <name> is
required` every time. It is now gated with a note naming the real mechanism (a profile
under `$DSH_HOME`, a provider in `settings.yaml`, an `apiKeyEnv` holding the key) so an
operator can wire it by hand while a proper recipe is written.

The class of bug is closed, not just the instance: a guest registered with a config
strategy but no recipe of its own now **refuses to launch** rather than inheriting the
recipe of whoever is listed first.

## Detection stops believing a web page is a model server

`GET /v1/models` returning `200` was treated as proof of an OpenAI-compatible server. A
web application on a scanned port answers `200` with an HTML page, so it was reported as a
reachable server with zero models - and on at least one machine it took the saved-upstream
slot and left the console's SHARE tab permanently empty, with the interface's own advice
("try re-detect") re-taking the same path forever.

The body must now actually be a model listing. The distinction that keeps this safe is
pinned by test: `{"data": []}` is a real server between loads and still counts; HTML, and
JSON without a `data` key, do not. A `401` still reports "needs a key" - that is answered
before the body is read.

## Smaller, but worth naming

- **`[?] HELP` had fallen behind.** `b` (the band card) and `Q` (the quant filter) both
  shipped and worked while the help screen never mentioned them. In a keyboard-driven app
  an undiscoverable key is, for most people, a key that does not exist. The lock added
  with the fix reads the dial's own key handlers out of the source, because the thing that
  drifted was a hand-kept list.
- **The console's chat error is legible.** A fetch that never reaches the server rejects
  with the browser's wording - "NetworkError when attempting to fetch resource" in Firefox,
  "Failed to fetch" in Chrome - which reads as though the model or the band failed, so the
  natural next move is to retry something that cannot succeed. The overwhelmingly common
  cause is a tab that outlived its `roger webui` process, and the message now says so
  while keeping the browser's own words for real network faults.
- **`roger webui --port 8391`** works. Only `--port=8391` did, which made this the one
  subcommand that answered the universal habit with "unknown flag".

## The site

- The install command now says **what you are downloading**: `roger` is one binary, a
  local harness for local models. It finds the models already on your machine and tunes
  you into everyone else's.
- The **terminal app and the browser console have their own sections** on the App page,
  with real screenshots of both, and a jump nav to reach them.
- **No em dashes site-wide**, with one rule that covers the whole tree instead of the
  three pages that were previously checked - which is how two pages had accumulated them
  under a green suite.

## Verification

Full Go suite and 648 website tests green. Coverage floors held on every core package:
tui 90.3%, cmd/rogerai 90.4%, harness 93.7%, client 91.7%, detect 91.6%, operator 91.5%,
webui 90.7%.
