# 020. No Session is bound to an instance, and many instances behave like one

Decided: 2026-10-16    Status: current

## The problem
Instances come and go, a deploy should lose no work, and placing Sessions is the job of the load balancer and the autoscaler.

## The choice
A Session is taken over wherever its request lands when it is idle, and waited for, up to a bound, when it is busy. A single instance's behavior is the spec; the few places where many instances differ, such as losing the step in flight when its writer dies, are part of the contract and listed in [FEATURES.md → G-F27](../../FEATURES.md#g-f27--service-many-instances-). A refusal is a safety valve and not a balancer: an instance refuses a new Session only past a hard limit on the Sessions it has active, which is what its memory and its Claims grow with, and it marks the refusal "not run" so a retry is safe. The refusal comes before anything is loaded, claimed or created. A drain finishes before it disconnects: the instance marks its liveness row draining, stops taking new Sessions, lets every Run finish, save and release its claim, and only then closes its streams. Another instance waits for a Session a draining one is using, as for any live writer.

## Not chosen
- Keeping a Session on the instance that activated it: simple, but instances come and go.
- Promising more than one instance can, such as a message that always arrives.
- Gotato spreading load itself: a placement service of its own, which [024](024-gotato-builds-no-infrastructure.md) rules out.
- Closing connections or canceling Runs first in a drain, or letting another instance take over a Session the draining one is using: a deploy would lose work, and a takeover would fence a Run about to be saved.

## Consequences
Any instance can serve any request, and a deploy loses at most the step in flight. A message that misses the writer is answered by the writer's name ([037](037-when-the-hash-missed-the-answer-names-the-writer.md)).
