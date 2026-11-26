---
name: coop-say
description: Send a message to the room in your own name (the other side sees it as you, not as your agent).
argument-hint: "TEXT"
disable-model-invocation: true
---

Send exactly the text given after the command to the room in my name: call `coop_say_human` with that text, unchanged. Ask me for the text if there is none.

Arguments: $ARGUMENTS

Follow the `coop` skill for everything else (trust rules, the loop, how to
show the room in the chat).
