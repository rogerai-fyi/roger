# v6.8.0 - payouts, four times faster

The payout hold drops from a blanket 120 days to **30 days on the principal**, with
a **10% reserve slice held until day 90**. On a $100 earning: $90 becomes payable a
month after the request settled, and the last $10 follows at day 90.

Why: the 120-day hold was sized to the card networks' outer dispute window, but the
researched reality is that most disputes land inside 30-60 days - and the window
runs from the consumer's top-up (stretching to 540 days on some reason codes), so
no hold length ever covered the true tail anyway. The reserve slice covers the
realistic tail; past it, the clawback and transfer-reversal machinery remains the
last line of defense, unchanged.

The mechanics, pinned in `features/money/payouts.feature`:

- A payout pays the principal and leaves an unreleased reserve behind as its own
  lot - still the operator's money, still on its original tail, still clawable.
- The reserve is never payable early (even above the $25 minimum) and never
  stranded: it joins the payable balance the day its tail clears.
- Everything else holds: $25 minimum, monthly batches, Connect KYC, earnings
  accruing before onboarding, and disputes clawing unpaid money first.

Deployment knobs: ROGERAI_PAYOUT_HOLD_DAYS, ROGERAI_PAYOUT_RESERVE, and the new
ROGERAI_PAYOUT_RESERVE_DAYS.
