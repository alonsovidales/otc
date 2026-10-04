# Personal data breach: procedure and register (GDPR Art. 33-34)

What to do if personal data held by the bridge (see `records-of-processing.md`) is lost,
destroyed, changed, or seen or taken by someone who shouldn't have it. This covers a compromised
server, a leaked database or backup, an exposed secret, or an email sent to the wrong people.
Owners' own devices are out of scope: that data is the owner's responsibility.

## The clock

**72 hours** from the moment you are reasonably sure a breach happened, to report it to the
Autoriteit Persoonsgegevens (AP). This applies unless the breach is unlikely to put anyone's
rights at risk. If you can't know everything in 72 hours, report what you know and add to it
later.

## Steps

1. **Contain it** (first hour).
   - Cut the attacker's access: rotate the secrets involved, block the addresses, and take a node
     out of DNS if needed (the other node keeps serving).
   - Never `bash -x` scripts that read secrets.
2. **Preserve evidence.** Copy the relevant logs (journald, `auth_events`, servercheck reports
   in `~/Library/Logs/otc-servercheck/`, OVH logs) before anything rotates them out.
3. **Assess.** Work out:
   - what data;
   - how many people;
   - how sensitive (passwords are bcrypt hashes; tokens are hashed);
   - whether it was encrypted;
   - what could happen to the people affected (account takeover, phishing, someone posing as
     their device).
4. **Decide and record.** Write an entry in the register below, *even if you decide not to
   report*, with the reasoning.
5. **Report to the AP** if there is a risk: through the AP's online form "Datalek melden"
   (autoriteitpersoonsgegevens.nl), within 72 hours.
6. **Tell the people affected** directly, by email, if the risk to them is *high* (Art. 34).
   Say what happened, what data, what it means for them, what we did, and what they should do,
   such as changing their password or watching for phishing. Give info@off-the.cloud as the
   contact.
7. **Fix and follow up.** Fix the cause, check the fix, and update the register and the AP
   report.

## If a processor has the breach

OVH, Proton, Apple, Google and GitHub must tell us without undue delay. Their notice starts our
72 hours, and the same steps apply.

## Register

Every breach goes here, reported or not. One entry per incident.

| Date found | What happened | Data and people affected | Consequences | Measures taken | Reported to AP (date, reference) / why not | People informed (date) / why not |
|---|---|---|---|---|---|---|
| | | | | | | |
