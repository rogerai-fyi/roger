# v6.5.1 - a clean frame on every terminal

A paint-integrity patch. On short or narrow terminals, opening the station log (`i`)
or the band card (`b`) from the tune-in list could leave ghost rows behind - stacked
ROGER logos, repeated STATION LOG lines. The cause was frames taller (or lines wider)
than the terminal scrolling the alternate screen.

- Every screen now fits its measured budget: the dial's row window accounts for its
  real chrome, the spend-limits table scrolls around the cursor, the band card sheds
  its section spacing on short windows, and the wide band grid reflows a step earlier.
- A renderer backstop guarantees no frame can ever exceed the terminal again, keeping
  the input line, status, and the way out on screen.
- The tune-in footer now teaches `b` (band card) at every width.

All pinned by a full-mode geometry audit that walks the founder-realistic screens at
short and narrow sizes.
