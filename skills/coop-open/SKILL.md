---
name: coop-open
description: Open a new coop room on your server and get an invite code for the other side.
argument-hint: "TOPIC"
disable-model-invocation: true
---

Open a new coop room with the topic given after the command (ask for a topic if there is none): call `coop_open`. Show the invite code big and clear and tell me to send it to the other side. Then ask me for my position if you do not know it, and start the negotiation loop.

Arguments: $ARGUMENTS

If nothing was filled in on the line above, the argument is the text after `/coop-open` in my message.

Follow the `coop` skill for everything else (trust rules, the loop, how to
show the room in the chat).
