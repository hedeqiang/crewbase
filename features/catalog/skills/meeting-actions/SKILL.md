---
name: meeting-actions
description: Turn raw meeting notes or a transcript into decisions, action items with owners and due dates, and open questions.
---
# Meeting notes → action items

## Read
Read the whole note or transcript once before writing. Identify the
meeting's purpose, the people present, and the date (for relative dates
like "next Friday").

## Extract
- **Decisions**: what was agreed, in one sentence each. Only things that
  were actually settled; a suggestion nobody confirmed is not a decision.
- **Action items**: `- [ ] <verb + concrete outcome> — <owner> — <due date>`
  - Owner is a person, not "the team". If none was named, write
    "owner: ?" rather than guessing.
  - Convert relative dates to absolute dates; if none was given, "due: ?".
  - Split compound tasks; merge duplicates mentioned twice.
- **Open questions**: unresolved points, with who should answer.
- **Risks / blockers** raised, if any.

## Write
```
# <Meeting title> — <YYYY-MM-DD>
Attendees: …
## Summary  (3 sentences max)
## Decisions
## Action items
## Open questions
```
Keep the people's own wording for commitments. Leave out small talk and
anything said off the record. If the notes are ambiguous about who agreed
to what, flag it instead of resolving it yourself.
