<!-- The WEB mascot: high-fidelity vector Ping (legacy: _partials/tube-ping.html). The ASCII
     Tube Ping in internal/tui/tube_ping.go is the TERMINAL form of the same character; keep
     each in its own medium. Geometry is mascot.svg, inlined so it inherits currentColor; ids
     are classes because the homepage renders it twice; the animations live in base.css. -->
<figure class="tube-ping" role="img" aria-label="Ping, the RogerAI on-air mascot">
  <svg class="tube-ping__mark" viewBox="0 -8 64 80" fill="currentColor" aria-hidden="true" focusable="false">
    <g class="ping-mark">
      <g fill="none" stroke="#E0231C" stroke-linecap="round" stroke-width="2">
        <path class="ping-mark__wave ping-mark__wave--a" d="M16 8 A18 18 0 0 1 48 8" opacity="0"/>
        <path class="ping-mark__wave ping-mark__wave--b" d="M11 10 A24 24 0 0 1 53 10" opacity="0"/>
      </g>
      <g class="ping-mark__body">
        <path d="M10 8 h9 v4.5 H14.5 v9 H19 V26 H10 Z"/>
        <path d="M54 8 h-9 v4.5 H49.5 v9 H45 V26 h9 Z"/>
        <path d="M22 30 h20 v18 a4 4 0 0 1 -4 4 h-12 a4 4 0 0 1 -4 -4 Z"/>
        <rect x="25" y="64" width="5" height="5" rx="1"/>
        <rect x="34" y="64" width="5" height="5" rx="1"/>
      </g>
      <circle class="ping-mark__glow" cx="32" cy="17" r="9" fill="#E0231C" opacity="0.3"/>
      <circle class="ping-mark__eye" cx="32" cy="17" r="5" fill="#E0231C"/>
    </g>
  </svg>
  <figcaption><b>PING</b><span>RogerAI Labs · right-sized intelligence</span></figcaption>
</figure>
