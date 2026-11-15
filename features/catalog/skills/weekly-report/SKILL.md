---
name: weekly-report
description: Write a short weekly status report from notes, commits, closed tasks and calendar items for a given week.
---
# Weekly report

## Collect (for the week asked; default: Monday–Friday of this week)
- Notes and memory entries from those days.
- Work that shipped: `git log --since=<monday> --until=<saturday>
  --author=<me> --oneline`, merged PRs, closed tasks or tickets.
- Meetings and decisions from the calendar or meeting notes.
Ask which sources to use if none are obvious; do not invent activity.

## Shape
Group by outcome, not by day or by tool:
1. **Done**: 3–7 bullets, each a result someone else cares about
   ("Customers can now export invoices as PDF"), with a link if available.
2. **In progress**: what moves next week and its expected finish.
3. **Blocked / needs help**: what, from whom, by when.
4. **Next week**: top 3 priorities.
5. Optional **Numbers**: metrics that changed, with the comparison period.

## Style
- Whole report readable in under a minute; under ~200 words unless asked.
- Plain words, no internal jargon or ticket numbers without context.
- Be honest about slips; state the new date.
- Match the format of earlier reports if the person has some.
