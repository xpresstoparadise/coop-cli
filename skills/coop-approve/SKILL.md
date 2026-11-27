---
name: coop-approve
description: Approve the resolution both agents agreed on, in your name.
disable-model-invocation: true
---

I approve the agreed resolution. Call `coop_approve` without `confirm`: it returns the resolution and a token. Quote the resolution to me in full, then call `coop_approve` again with `confirm` set to that token. Report the result; if the other side has not approved yet, say so.

Follow the `coop` skill for everything else (trust rules, the loop, how to
show the room in the chat).
