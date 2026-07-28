# HANDOFF increment 4: the RETURN TRIP - what the guest did comes back.
#
# THE CONSTRAINT THAT SHAPES THIS. The existing recall path (internal/tui/context_capsule.go
# :180 readRecallCapsule -> :87 mergeReturnCapsule) calls capsule.Import, which VERIFIES an
# ed25519 signature. Claude Code has no key and cannot produce a signed capsule, so that
# path can never carry it. Asking the guest to shell out to `roger context export` (which
# would sign with the user's key) works in principle but depends entirely on the guest
# following instructions - a fragile thing to build a round trip on.
#
# So the return trip is a PLAIN MARKDOWN NOTE: `<workdir>/.roger/return.md`. The guest is
# asked, in the brief, to write what it did there before it exits. On return, RogerAI reads
# it and appends it to the thread as ONE attributed turn.
#
# WHY NO SIGNATURE IS NEEDED HERE. The signature protects the STRANGER path: a capsule that
# crossed the wire from another owner, where "did this really come from them, unmodified" is
# the whole question. This file was written by a process THIS session launched, in a
# directory this session created, on this machine, by this user. A guest that wanted to forge
# context could already run `roger context export` itself - it has the user's shell. The
# signature would be protecting against nothing it cannot already do, at the cost of a round
# trip that never works. The signed path stays supported for guests that can produce one.
#
# GROUND TRUTH: internal/tui/context_capsule.go:32-36 (handoffDir=".roger",
#   recallCapsuleFile="return.rcap.json"), :180 readRecallCapsule, :87 mergeReturnCapsule;
#   internal/tui/operator.go:945 onOperatorDone - where the return is read.
#
# Enforced by: internal/tui/handoff_desk_return_bdd_test.go (with the brief-side scenario
# in internal/brief/brief_bdd_test.go)

@handoff
Feature: What the guest did comes back into the session

  Rule: a plain note is merged back as one attributed turn

    Scenario: the guest's note lands in the thread
      Given a guest handoff whose guest left a note in .roger/return.md
      When the guest exits and the session resumes
      Then the note is appended to the thread as one turn
      And that turn is attributed to the guest, not to the user and not to the band
      # Attribution matters: the next capsule carries this onward, and a reader must be
      # able to tell what RogerAI's model said from what a different agent said.

    Scenario: the note travels in the next handoff
      Given a returned note in the thread
      When a capsule is exported afterwards
      Then the capsule carries the note as a turn
      # This is what makes it a round TRIP rather than a one-way trip with a receipt.

    Scenario: the user is told what came back
      Given a guest handoff whose guest left a note
      When the session resumes
      Then the transcript says a note came back from the guest

    Scenario: the guest is actually asked to leave a note
      Given a brief rendered for a guest
      Then it tells the guest where to write what it did
      # A reader with no writer is a round trip that never happens: nothing else in this
      # file matters if the guest is never asked.

    Scenario: a merged note is consumed, not re-merged
      Given a guest handoff whose guest left a note in .roger/return.md
      When the guest exits and the session resumes
      And a later handoff returns with no new note
      Then the note is not appended a second time
      # The note is a one-time rendezvous. Left behind, it would re-merge on every later
      # handoff in that workdir - attributed to whichever guest came next.

  Rule: nothing coming back is the normal case, not an error

    Scenario: no note is not a failure
      Given a guest handoff whose guest left no note
      When the guest exits and the session resumes
      Then the thread is unchanged
      And no error is shown
      # Most guests will never write one. Silence must be free.

    Scenario: an empty note is ignored
      Given a guest handoff whose note file is empty
      Then the thread is unchanged

  Rule: the note is untrusted input and is treated like it

    Scenario: an oversized note is truncated
      Given a returned note far larger than the note budget
      Then the appended turn is truncated to the budget
      And the truncation is marked
      # A guest could write a gigabyte. The ring and every future capsule would carry it.

    Scenario: control bytes in the note are stripped
      Given a returned note carrying ANSI escapes and control bytes
      Then the appended turn carries none of them
      # It goes straight into the transcript the user is looking at.

    Scenario: a note that is not valid text is refused
      Given a returned note that is binary
      Then the thread is unchanged
      And the transcript says the note could not be read

    Scenario: the note cannot impersonate the user or the band
      Given a returned note whose text claims to be a user turn from the band
      Then the appended turn is still attributed to the guest
      # Attribution is set by the harness from WHO wrote the file, never from its content.

  Rule: the signed return path still works

    Scenario: a guest that can produce a signed capsule is still merged
      Given a guest handoff whose guest left a valid signed return.rcap.json
      When the session resumes
      Then its turns are merged into the thread as before
      # Regression: the existing path is not replaced, only joined by a simpler one.

    Scenario: an INVALID signed capsule is still refused
      Given a returned return.rcap.json whose signature does not verify
      Then it is refused and the thread is unchanged
      # The signed path keeps its guarantee: the plain-note path is a separate, local-only
      # door, not a hole in the signed one.
