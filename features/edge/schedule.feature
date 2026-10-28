# ROGER EDGE - SCHEDULES: an action the owner wants taken again and again, without being there.
#
# STATUS: PROPOSED 2026-09-28. Not godog-bound: pinned by Go tests - internal/edge/schedule_test.go
# (the jobs, when they are due, the record) and cmd/rogerai/edgeschedule_test.go (`roger edge
# schedule`, the runner on the host's pass). Founder review pending.
#
# WHY. It is the last of the fleet-manager list: the ad-hoc maintenance an operator wants standing
# rather than remembered. "Restart the watcher on every production box each night." "Start the agent
# on shed in ten minutes, when its upstream is back." Everything needed to DO those exists - the
# maintenance actions and their two locks - and what is missing is a clock.
#
# A SCHEDULE IS ONLY A CLOCK IN FRONT OF WHAT THE OWNER COULD ALREADY DO, and never a way round it.
# This is the whole safety argument, and it is worth stating plainly. A run on ANOTHER machine goes
# through exactly the path a hand-typed `roger edge maintain` goes through - that machine's own
# maintenance switch, an authority-signed grant scoped to maintain AND that node, the mutual TLS, the
# caller matched to its certificate, the revocation check, the replay cache - so a grant that expires
# or a switch turned off stops it dead. A run on THIS machine is done LOCALLY, with no face, no grant
# and no switch, because it is the owner's own machine and it is the same thing `roger edge agent
# resume` does at the keyboard; the gate exists for a REMOTE caller, and a roger cannot reach its own
# face anyway. Either way a schedule adds no new path, no new capability and no authorization code.
#
# WHERE IT LIVES AND WHO RUNS IT. On the CALLER's machine, because the caller is the one holding the
# grant: the owner's own roger keeps the schedule and runs what is due on the pass it already makes.
# No member is told what to do at a distance; nothing is pushed to anybody.
#
# WHAT A JOB IS. An action (start, stop or restart a named agent), a scope (the fleet, a tag, or one
# node - the same scopes a rule and an expectation take), and a cadence: `--every <duration>`, or
# `--once`, which runs at the next opportunity and then retires. Reading a log tail is NOT schedulable:
# a tail nobody reads is only a copy of somebody's log lying around.
#
# HOW IT BEHAVES WHEN THINGS GO WRONG, which is most of this spec. A run that fails is RECORDED and
# waited out, never retried in a tight loop. A node with no face is not an error, it is a node that is
# dark, and that is what the record says. A missing grant is said once per run rather than every pass.
# One run at a time on one machine. A cadence shorter than the floor is refused, so a schedule can
# never become a hammer.
#
# GROUND TRUTH: internal/edge/schedule.go, cmd/rogerai/edgeschedule.go, cmd/rogerai/edgehost.go
# (the pass runs what is due), cmd/rogerai/edgemaintain.go (the one path a run takes).

Feature: schedules

  Background:
    Given an Edge with "desk" and "shed", both tagged "production"
    And maintenance is on at both, and this machine holds a maintain grant for each

  # ---- the jobs themselves ----------------------------------------------------

  @edge
  Scenario: an owner leaves an action standing
    When the owner schedules "restart watcher" on "tag:production" every 24 hours
    Then it is kept with an id, and the surfaces say what it is and when it next runs
    And scheduling the same action on the same scope again REPLACES it rather than adding a second
    And a one-off is kept the same way, and says it runs once

  @edge
  Scenario: a schedule that cannot mean what it says is refused where it is made
    When the owner schedules an action that is not start, stop or restart
    Then it is refused, naming the three that are
    When the owner schedules a log tail
    Then it is refused: a tail nobody reads is only a copy of somebody's log lying around
    When the owner schedules with no agent named
    Then it is refused
    When the owner asks for a cadence below the floor
    Then it is refused, so a schedule can never become a hammer
    And neither --every nor --once is refused, and both together are refused
    And a scope that is neither fleet, a tag, nor a node is refused

  @edge
  Scenario: a schedule can be dropped, and dropping what is not there is not an error
    When the owner removes one by id
    Then it is gone, and nothing is run for it again

  # ---- when it runs ----------------------------------------------------------

  @edge
  Scenario: what is due is what has not run inside its cadence
    Given a job every hour that ran fifty-nine minutes ago
    Then it is not due
    When another minute passes
    Then it is due
    And a job that has never run is due at once
    And a one-off that has run is never due again, and is retired from the list

  @edge
  Scenario: a run goes through the ordinary gate, and nothing else
    When a run comes due for "shed"
    Then it takes the same path a hand-typed maintain takes: the grant, the pin, the face, the switch
    And with the grant expired it does not run, and the record says the grant is missing
    And with maintenance off at the target it does not run, and the record says so in the target's words
    And it never reaches for a capability, a path or a certificate the owner did not already have

  @edge
  Scenario: the scope decides which units a run touches
    Given a job scoped to "tag:production"
    When it comes due
    Then every unit tagged production is acted on, and no other
    And a job scoped to one node touches only that node
    And a fleet-scoped job touches every member AND this machine, which it acts on locally

  @edge
  Scenario: a job on this machine runs here, with no face and no grant
    Given a job scoped to this machine
    When it comes due
    Then the action is taken locally, through the same functions the face's own handler runs
    And no grant is needed and no face is dialled, because this is the owner's own machine
    And the roger answering cannot stop ITSELF even here: the pid check still refuses
    And a single-machine Edge is therefore not a fleet where schedules can never do anything

  @edge
  Scenario: a job whose scope matches nothing says so
    Given a job scoped to a tag no unit carries
    When it comes due
    Then it is recorded as matching no unit, rather than coming back to it on every pass

  @edge
  Scenario: what happens when a run cannot be made
    Given a unit that is dark, with no face to dial
    When a run comes due for it
    Then it is recorded as dark rather than failed, because nothing was wrong with the job
    Given a unit whose face refuses
    Then the refusal is recorded in the words the face used
    And a failed run is WAITED OUT to the next cadence, never retried in a tight loop
    And a run records when it happened whether it worked or not, so the next one is not immediate

  @edge
  Scenario: one roger per machine runs the schedules
    Given a machine running two rogers, both making their ordinary pass
    Then only ONE of them runs what is due: the first live instance by name
    And the other passes over it, so they never race for a job nor take turns being its target
    When a job's target IS the roger that runs the schedules
    Then it is refused by the pid check, and the outcome NAMES which roger tried, so the owner can
      see why - a live run hit exactly this, restarting the roger that was running the schedule
    And the fix in the owner's hands is to name a different agent

  @edge
  Scenario: one run at a time, and a slow run does not stack
    Given a run in progress
    When the pass comes round again
    Then nothing new is started for that job

  # ---- what it says ----------------------------------------------------------

  @cli
  Scenario: `roger edge schedule`
    When the owner runs `roger edge schedule`
    Then each job is listed with its action, its scope, its cadence, when it last ran and what happened
    And a job that has never run says so, rather than showing an empty outcome
    And an Edge with no schedules says so, and says how to leave one
    When the owner runs `roger edge schedule add restart watcher --scope tag:production --every 24h`
    Then it says what will happen and when it first runs
    And `roger edge schedule rm <id>` drops it

  @edge
  Scenario: a run is on the timeline
    When a run happens
    Then the events timeline carries it against that unit, saying it was scheduled and what came of it
    And a run that changed nothing is not drawn as though it did
