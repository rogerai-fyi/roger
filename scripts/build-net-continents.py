#!/usr/bin/env python3
"""Build the broker's network-to-continent table (ROGERAI_NET_CONTINENTS) from public data.

Inputs, all public and read once at build time (the broker never contacts anything):
  - the five regional internet registries' delegated-extended statistics, which say which
    country each IPv4/IPv6 block is assigned to;
  - GeoNames' countryInfo.txt, which says which continent each country is on.

Output: "CIDR CONTINENT" lines (AF AN AS EU NA OC SA), collapsed per continent, the format
cmd/rogerai-broker/netcontinent.go loads. Continent is coarse on purpose: placement uses it only
as a tiebreak class (features/tower/placement_locality.feature), never an address or a city.

Usage: scripts/build-net-continents.py OUT_FILE [--cache DIR]
"""
import argparse
import ipaddress
import os
import sys
import urllib.request

RIRS = {
    "arin": "https://ftp.arin.net/pub/stats/arin/delegated-arin-extended-latest",
    "ripencc": "https://ftp.ripe.net/pub/stats/ripencc/delegated-ripencc-extended-latest",
    "apnic": "https://ftp.apnic.net/stats/apnic/delegated-apnic-extended-latest",
    "lacnic": "https://ftp.lacnic.net/pub/stats/lacnic/delegated-lacnic-extended-latest",
    "afrinic": "https://ftp.afrinic.net/pub/stats/afrinic/delegated-afrinic-extended-latest",
}
COUNTRY_INFO = "https://download.geonames.org/export/dump/countryInfo.txt"
CONTINENTS = {"AF", "AN", "AS", "EU", "NA", "OC", "SA"}
# Registry pseudo-countries: EU (European Union) and AP (Asia/Pacific region).
EXTRA = {"EU": "EU", "AP": "AS"}


def fetch(url, cache):
    name = os.path.join(cache, url.rsplit("/", 1)[-1]) if cache else None
    if name and os.path.exists(name):
        with open(name, "rb") as f:
            return f.read().decode("utf-8", "replace")
    req = urllib.request.Request(url, headers={"User-Agent": "rogerai-net-continents/1"})
    with urllib.request.urlopen(req, timeout=120) as r:
        data = r.read()
    if name:
        with open(name, "wb") as f:
            f.write(data)
    return data.decode("utf-8", "replace")


def country_continents(text):
    out = dict(EXTRA)
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        cols = line.split("\t")
        if len(cols) > 8 and cols[8] in CONTINENTS:
            out[cols[0].upper()] = cols[8]
    return out


def ranges(text, cc2cont):
    for line in text.splitlines():
        if not line or line.startswith("#"):
            continue
        cols = line.split("|")
        if len(cols) < 7 or cols[2] not in ("ipv4", "ipv6") or cols[6] not in ("allocated", "assigned"):
            continue
        cont = cc2cont.get(cols[1].upper())
        if not cont:
            continue
        try:
            if cols[2] == "ipv4":
                first = ipaddress.IPv4Address(cols[3])
                last = first + int(cols[4]) - 1
                for net in ipaddress.summarize_address_range(first, last):
                    yield net, cont
            else:
                yield ipaddress.IPv6Network(f"{cols[3]}/{cols[4]}", strict=False), cont
        except ValueError:
            continue


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("out")
    ap.add_argument("--cache", default="")
    a = ap.parse_args()
    if a.cache:
        os.makedirs(a.cache, exist_ok=True)
    cc2cont = country_continents(fetch(COUNTRY_INFO, a.cache))
    by_cont = {c: [] for c in CONTINENTS}
    for rir, url in RIRS.items():
        n = 0
        for net, cont in ranges(fetch(url, a.cache), cc2cont):
            by_cont[cont].append(net)
            n += 1
        print(f"{rir}: {n} blocks", file=sys.stderr)
    collapsed = {}
    for cont, nets in sorted(by_cont.items()):
        v4 = [x for x in nets if x.version == 4]
        v6 = [x for x in nets if x.version == 6]
        collapsed[cont] = list(ipaddress.collapse_addresses(v4)) + list(ipaddress.collapse_addresses(v6))
    # A block the registries place on two continents says nothing reliable about either: it is
    # left out (an address in it then has no continent) rather than resolved by line order.
    seen = {}
    for cont, nets in collapsed.items():
        for net in nets:
            seen.setdefault(net, set()).add(cont)
    ambiguous = {net for net, conts in seen.items() if len(conts) > 1}
    lines = [f"{net} {cont}" for cont, nets in sorted(collapsed.items()) for net in nets if net not in ambiguous]
    if ambiguous:
        print(f"left out {len(ambiguous)} blocks listed on more than one continent", file=sys.stderr)
    with open(a.out, "w") as f:
        f.write("# network-to-continent table for ROGERAI_NET_CONTINENTS\n")
        f.write("# built by scripts/build-net-continents.py from RIR delegated-extended stats + GeoNames\n")
        f.write("\n".join(lines) + "\n")
    print(f"wrote {len(lines)} ranges to {a.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
