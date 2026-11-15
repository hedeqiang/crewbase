---
name: release-notes
description: Turn a range of commits, merged PRs or a CHANGELOG section into release notes written for users.
---
# Release notes

## Collect
- Range: `git log --no-merges --format='%h %s' <previous tag>..<new tag>`,
  or the merged PR titles, or the CHANGELOG's unreleased section.
- Open anything whose effect is unclear from its title.

## Sort
Drop changes users cannot notice (refactors, CI, internal tests) unless
they change performance or reliability in a way people feel. Group the rest:
1. **Breaking changes / action needed**: always first, with what to do.
2. **New**: features, in order of how many people will care.
3. **Improved**: faster, clearer, more reliable.
4. **Fixed**: describe the symptom people saw, not the code.
5. **Security**: if any, with severity and whether upgrading is urgent.

## Write
- One line each, starting with a verb or the feature name: "Export a chat
  as Markdown from the ⋯ menu." Not "Added MarkdownExporter class."
- Use the product's words, not internal names or ticket numbers (keep PR
  links at the end of the line if the project does that).
- Credit outside contributors if the project does.
- Open with a two-sentence summary of the release's theme.

Match the tone and format of earlier release notes in the project when
they exist. Never invent a change that is not in the source material.
