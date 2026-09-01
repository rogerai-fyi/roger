# v6.7.0 - curated operators earn

Curated settlement grows an income half. A curated station's operator now receives
**their declared list back whole, plus half of the 10% routing fee**; the network
keeps the other half. On a $1.00-list model: the consumer still pays $1.10, the
operator is credited $1.05, the network retains $0.05.

Why this shape and not a simple split of the posted price: the operator's share is
stated as a **share of the fee pool**, so it is greater than or equal to their
provider bill by construction, at any markup the network could ever set. A
percent-of-posted (a 95/5 split, say) silently goes underwater the moment the
markup constant moves - the exact bug class the curated rule exists to prevent.

What stays true: consumers pay the same posted price and their history still shows
the honest split (the provider's list and the routing fee, now annotated
server-side); reimbursement and income are labeled apart on the operator's
dashboard; free upstreams stay free; seed credits still mint no earnings; and a
relayed curated request pays the tower from the network's half, so neither the
operator's share nor the platform's remainder can go negative.

The change is one constant beside the markup (`curatedFeeShare = 0.50` in
`curated_pricing.go`), pinned by the amended `curated_pricing.feature` - including
a structural scenario asserting the never-underwater guarantee itself.
