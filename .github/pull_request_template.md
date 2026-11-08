## Summary

## Linked issue
Closes #

## Specs / docs
- [ ] Behaviour matches the linked spec sections (or the spec is updated in this PR, explained below)
- [ ] Operator-visible change → runbook updated
- [ ] New doctor codes added to the spec §8 **and** `docs/specs/doctor-codes.md`

## Testing
- [ ] `go test ./...` (agent)
- [ ] App tests (cloud / desktop) if touched
- [ ] Manual test plan from the issue executed (paste results, **redacted**)

## Human first ([ADR-0009](../docs/decisions/0009-optimized-for-humans.md))
- [ ] A person who has never used a terminal can do this by reading the screen and choosing (menu, prompt or button); typed commands are not the only path
- [ ] Common path needs no flags or choices; errors carry a `LAI-…` code and a one-sentence fix; long work shows progress
- [ ] Works keyboard-only at 80×24, in English and Spanish; `--json`/API output unchanged
- [ ] Followed literally as a first-time user (result above), or: not operator-visible

## Offline safety
- [ ] No new runtime internet dependency (or it's in the allowed list: cloud link, sync, self-update)
- [ ] Nothing here can delete the last verified copy of content

## Redaction
- [ ] `scripts/check-redaction.sh` passes; no IPs, tokens, keys, zone IDs, emails
