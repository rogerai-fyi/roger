# Factory-floor boards, mapped onto the Edge rack (research, 2026-09-26)

What factories do to keep a floor legible, and what of it the Edge adopts. Sources at the end.

## Findings that matter
1. An andon board per station is three things: a state light (run / help called / stopped), a
   number pair (target vs actual), accumulated downtime. One color = one meaning; read in 2 s.
2. Yellow on an andon means a PERSON was called; the board names who.
3. ISA-101 high-performance HMI: color only for the abnormal; gray normal. Detection was 48%
   faster before an alarm in the studies. It forbids 3D, animation, rainbow, bright normal states.
4. ISA-101 levels: L1 overview (is anything abnormal?), L2 area, L3 the unit where you act, L4
   config. One step per level.
5. ISA-18.2: an alarm is "a condition requiring operator action"; anything else is a status. 3-4
   priorities, ~5/15/80 split; target ~1 alarm per 10 min; a flood is 10 in 10 min; stale is
   active over 24 h; shelving (operator, timed) is distinct from suppression (design).
6. Alarm summary = active alarms sorted by priority then time, with ack, shelve, filter.
7. OEE boards live on one strip: a per-machine state timeline over the shift, a Pareto of the
   top downtime reasons, availability = MTBF / (MTBF + MTTR).
8. A modern machine tile: name, state color, TIME IN THAT STATE, one percent vs goal, an hourly
   bar, operator + job.
9. State timeline (Grafana): one row per entity, merged equal runs, no labels unless they fit.

## Adopted for the Edge (the rack's language)
- THE LOG: a 60-cell strip of the last hour per unit (1 cell = 1 min): ▁ on air, ▒ dark,
  ▲ trouble (red), · unknown. Beside the meter on the row; in the readout with its eyebrow.
- Time in state as words next to the eye: ON AIR 3h12m / DARK 41m / TROUBLE 7m - the most read
  number on any machine tile.
- ANDON: one tile per unit, one word, one color, one number (AIRTIME this shift), who is called.
- TROUBLE CALLS (alarms): sorted priority then age, k = ROGER (acknowledge), each with WHY IT
  MATTERS and DO; ack removes the color, not the row; the row leaves when the condition clears.
- AIRTIME per unit over the shift; time between faults / time to fix in the unit detail only.
- Levels: ANDON = L1, FLEET = L2, unit readout = L3, config = L4.

## Rejected
Rainbow status colors; 3D gauges, pies and donut dials; blinking text; gray-on-gray HMI
literalism (take the rule, not the palette); an OEE number for a compute node (performance and
quality have no honest meaning here; airtime only); an alarm without an action.

## Vocabulary
unit (a machine on the rack) · the rack · ON AIR · DARK · OFF SHIFT · TROUBLE · FADING ·
DEAD AIR (downtime) · AIRTIME (availability) · THE LOG (the state strip) · TROUBLE CALL (alarm) ·
ROGER (acknowledge) · STAND BY (shelve) · ALL CLEAR · PILE-UP (flood) · OP (operator) · JOB.

## Sources
- https://leantps.ca/jidoka-andon-board-takahama/ · https://www.allaboutlean.com/andon/
- https://www.instrumentationblog.in/scada-hmi-screen-design-isa-101-best-practices/
- https://www.realpars.com/blog/high-performance-hmi · https://malisko.com/isa-101/
- https://processcontrolguide.com/isa-18-2-alarm-management/
- https://www.yokogawa.com/us/library/resources/media-publications/implementing-alarm-management-per-the-ansi-isa-182-standard-control-engineering/
- https://evocon.com/feature/shift-view/ · https://evocon.com/feature/factory-overview/
- https://www.machinemetrics.com/blog/current-shift-dashboard
- https://grafana.com/docs/grafana/latest/visualizations/panels-visualizations/visualizations/state-timeline/
