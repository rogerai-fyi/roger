# v6.9.0 - the story, and the slow lane

Two threads: the website finally tells the network's whole story, and curated
verification stops costing operators real money.

## The slow probe lane

A curated station fronts a metered commercial API, so every verification canary
is billed to the operator's upstream and pays them nothing. The first live house
stations proved it: a dollar a day of pure verification on the adaptive lane.
Now a curated station is probed ONCE at first sight - that is what earns the
check mark - then only a minimal weekly recheck (ROGERAI_PROBE_CURATED_INTERVAL;
0 means the first probe is the only one, ever). Market browsing can never pull a
canary in early, the tools canary rides the same gate, and the overhead is
disclosed right in `roger share --curated`: under a cent a month per band. A
dead upstream key still surfaces on the first real request through the ordinary
failover and strike machinery. Pinned by curated_probes.feature.

## The dial

Curated rows wear the bare » mark; who is actually serving lives in the station
log (i) and the band card (b). An undetected context window now reads ~333k -
visibly a sentinel - instead of ~33k, a number plausible enough to be believed.

## The site

The homepage now carries the identity-unlinking claim, both earn paths (your
GPU at 90%; your contracts as a curated station), the tower network (5%
relaying, free standalone), and an honest you-could-go-direct tease. The why
page joins the nav and the footer instead of living off one pricing link. The
FAQ answers "can I resell my provider contract" and "does a model provider know
who I am". Models, tower, integrations, confidential and private pages all
carry their piece. Locked by features/web/network_story.feature.
