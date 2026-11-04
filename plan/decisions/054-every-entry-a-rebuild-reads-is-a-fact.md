# 054. Every Entry a rebuild reads is a fact

Decided: 2026-11-04    Status: current

## The problem
A Run's start and end, the release of a Session, the queued messages and notices, and the parts of a streaming reply were log Entries, which a load leaves out. A rebuild still needed them, so [049](049-the-store-contract-holds-only-what-every-store-must-do.md) added two things every store had to keep: `Header.State`, Gotato's own bytes with the Run in flight, busy, notices, the last Run end and the counts, and `Header.LogFrom`, from where `Load` also returned log Entries while a Run was in flight. Every adapter had to store one and apply the other, and the same state lived in the Entries and in the Header.

## The choice
A kind is a fact when a rebuild that missed it would build another Session. `run_started`, `run_ended`, `session_released`, `message_queued`, `message_dropped`, `notice_queued` and `reply_part_kept` become facts. A Checkpoint carries the totals before it: `SessionCreated` and `CompactionEnded` gain `Runs`, `Usage` and `LastRunEnd`, and `CompactionEnded` also `Running` and `Notices`. `Header.State` and `Header.LogFrom` are removed, and `Load` returns the facts from the last Checkpoint and nothing else. This replaces the Header fork of 049.

## Not chosen
- Keep `State` and `LogFrom`: two rules every adapter must get right, for state the Entries already hold.
- Keep the kinds as log and give up the reply that was streaming when an Instance died: the Run would end with nothing of the text the client already saw.

## Consequences
A store keeps a Header of `Epoch` and `Info` and never applies a position of its own in `Load`, and the state is in one place. A Session that never compacts loads three more Entries for each Run, and a long streamed reply's text is loaded twice, once in its parts and once in `reply_ended`, until a compaction writes a Checkpoint after it. 049 had refused these kinds as facts for that cost; with compaction the measured load grew from 126 to 128 Entries over 300 Turns of `long_session`.
