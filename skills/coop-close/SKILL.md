---
name: coop-close
description: Close the coop room for both sides.
argument-hint: "REASON"
disable-model-invocation: true
---

I close the room: call `coop_close` with the reason given after the command, if any. Stop the negotiation loop.

Arguments: $ARGUMENTS

Follow the `coop` skill for everything else (trust rules, the loop, how to
show the room in the chat).
