# ROGER EDGE - INVENTORY: what software each unit runs, and which units are behind.
#
# STATUS: APPROVED 2026-09-28 (founder: "approved"). Not godog-bound: pinned by Go tests - internal/edge/software_test.go
# (the words, the version order, who is behind), cmd/rogerai/edgesoftware_test.go (the read from
# a machine, `roger edge inventory`), internal/fleetview/fleet_test.go and internal/webui tests
# (the software line on the readout and the console card).
#
# WHY. Every fleet manager an operator has used shows, per device, the agent version, the OS, the
# kernel, the architecture and the hardware, and flags the devices that are BEHIND - that is how an
# operator knows what to update before anything breaks. Roger Edge showed resources (RAM, GPU, disk,
# temperature) but not one software fact: an owner with six boxes could not tell which ran the
# roger they had just upgraded. No new surface: the facts ride in the instance record the face
# already reports over the node's own certificate, and the fleet reads them where it reads the rest.
#
# WHAT. Each running roger records, beside its resources, the SOFTWARE it is: the roger version,
# the OS (its pretty name), the kernel release, the architecture, the hostname and the hardware
# model where the machine names one (a Jetson, a Pi, a DMI product). The fleet's NEWEST roger
# version is the reference; every instance on an older version is BEHIND it, and says so on every
# surface. `roger edge inventory` lists it as a table; the fleet screen's readout and the console's
# card carry the same line and the same BEHIND mark.
#
# GROUND TRUTH: internal/store/edgenode.go (EdgeSoftware), internal/edge/software.go,
# cmd/rogerai/edgesoftware.go, cmd/rogerai/edgefleetlive.go, internal/webui/assets/console.js.

Feature: software inventory

  Background:
    Given an Edge with the nodes "desk" and "shed"
    And "desk" runs the instance "desk" on roger 6.11.2, Gentoo Linux, kernel 6.18.31, amd64
    And "shed" runs the instance "shed" on roger 6.9.0, Debian GNU/Linux 12, kernel 6.1.0, arm64, on a "Raspberry Pi 5"

  @edge
  Scenario: a roger reads its own software facts from the machine, and invents nothing
    When a roger registers on a machine with /etc/os-release, a kernel release and a device-tree model
    Then its record names the roger version, the OS pretty name, the kernel, the arch, the hostname and the model
    When the machine exposes none of those files
    Then the record names the roger version and the arch only, and the rest is absent, never blank words

  @edge
  Scenario: the newest roger on the Edge is the reference, and older ones are behind it
    When the fleet is read
    Then the newest version is 6.11.2
    And "shed/shed" is behind 6.11.2
    And "desk/desk" is not behind
    And a version that cannot be parsed is neither newest nor behind

  @cli
  Scenario: `roger edge inventory` lists every unit's software with who is behind
    When the owner runs `roger edge inventory`
    Then a table lists desk/desk and shed/shed with roger, OS, kernel, arch, machine and uptime
    And shed/shed carries "behind 6.11.2"
    And `--json` prints the same records

  @edge
  Scenario: the fleet screen's readout and the console card carry the software line
    When the owner selects "shed" on the fleet screen
    Then the readout has a software row: roger 6.9.0 · Debian GNU/Linux 12 · 6.1.0 · arm64 · Raspberry Pi 5
    And a "behind 6.11.2" mark in the alarm colour
    And the console's node card shows the same line under the instance

  @edge
  Scenario: an older face that reports no software is drawn without one
    Given a member whose instances carry no software record
    When the fleet is read
    Then its rows draw without a software line and it is not counted behind
