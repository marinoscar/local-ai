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

## Offline safety
- [ ] No new runtime internet dependency (or it's in the allowed list: cloud link, sync, self-update)
- [ ] Nothing here can delete the last verified copy of content

## Redaction
- [ ] `scripts/check-redaction.sh` passes; no IPs, tokens, keys, zone IDs, emails
