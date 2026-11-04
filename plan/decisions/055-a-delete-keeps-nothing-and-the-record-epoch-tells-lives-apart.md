# 055. A delete keeps nothing, and the Record epoch tells two lives of an ID apart

Decided: 2026-11-04    Status: current

## The problem
A delete removes a Record, and a caller that chooses its own IDs (one Session per chat thread, say) may create the ID again. Outside the store, something can still hold the old Record: a client that resumes a stream from a number, a fork or a spilled result that points into it by position. Read against the new Record, each would get another conversation and not notice.

## The choice
A delete keeps nothing of the Record, and the ID may be created again. Each Record gets an identity when it is created, the Record epoch, and every reader and writer that holds an old reference checks it: a write of another epoch is `ErrNotFound`, and a resume from another epoch is `cannot_resume` (`specs/store.md`). The Record epoch is another number than the Epoch of a Claim, which a release and a takeover change and a delete does not touch.

## Not chosen
- A tombstone, so an ID is never created again: a reader of an old reference then only meets "not found", and the Record epoch goes. It stores a row for each deleted Record, and a caller can no longer reset a Session by deleting it and creating its ID again.
- IDs that only Gotato makes: no ID could come back, but a caller could not name a Session after its own key, which a known service does.

## Consequences
A delete leaves nothing behind in the store. Every place that reads or writes by an old reference keeps its epoch check, which is the cost of that choice and not bloat; a change that removes one must show what stops the old reference instead.
