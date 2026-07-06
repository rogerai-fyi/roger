# Copyright 2026 RogerAI
# Distributed under the terms of the PolyForm Perimeter License 1.0.0

# A -bin ebuild: installs the prebuilt `roger` client from the GitHub release, no
# compilation. Bump by copying this file to roger-bin-<newver>.ebuild and regenerating
# the Manifest (`ebuild roger-bin-<newver>.ebuild manifest`) so the distfile hashes match.
EAPI=8

DESCRIPTION="RogerAI - a two-way radio for GPUs (prebuilt client binary)"
HOMEPAGE="https://rogerai.fyi https://github.com/rogerai-fyi/roger"

BASE="https://github.com/rogerai-fyi/roger/releases/download/v${PV}"
# The release ships bare, statically-linked binaries (CGO_ENABLED=0), so one amd64 build
# runs on glibc and musl alike. Rename each distfile per-arch so the cache can't collide.
SRC_URI="
	amd64? ( ${BASE}/roger-linux-amd64 -> ${P}.amd64 )
	arm64? ( ${BASE}/roger-linux-arm64 -> ${P}.arm64 )
"

LICENSE="PolyForm-Perimeter-1.0.0"
SLOT="0"
KEYWORDS="-* ~amd64 ~arm64"
# Prebuilt + already stripped (ldflags -s -w); source-available (not free) => no binhost redist.
RESTRICT="bindist strip test"

RDEPEND=""
BDEPEND=""

# The distfiles are bare binaries, not archives, so there is nothing to unpack.
S="${WORKDIR}"
QA_PREBUILT="usr/bin/roger"

src_unpack() {
	# Copy the arch-appropriate distfile in as the final binary name.
	if use amd64; then
		cp "${DISTDIR}/${P}.amd64" "${S}/roger" || die
	elif use arm64; then
		cp "${DISTDIR}/${P}.arm64" "${S}/roger" || die
	fi
}

src_install() {
	dobin roger
	# Back-compat alias: `rogerai` still works (matches web/install.sh and the old command name).
	dosym roger /usr/bin/rogerai
}
