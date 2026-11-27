---
name: coop-close
description: Close the coop room for both sides.
argument-hint: "REASON"
disable-model-invocation: true
---

I close the room: call `coop_close` with the reason given after the command, if any. Stop the negotiation loop.

Arguments: $ARGUMENTS

If nothing was filled in on the line above, the argument is the text after `/coop-close` in my message.

Follow the `coop` skill for everything else (trust rules, the loop, how to
show the room in the chat).
