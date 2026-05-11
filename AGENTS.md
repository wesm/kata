# Agent Rules

- Always Commit: Do not leave accepted repository changes uncommitted at the end of a task. Commit the completed work, or explicitly say why no commit was made.
- Do not commit rejected experiments. Revert them or ask before preserving them.

## Closing issues

Closing an issue asserts that the work is complete. This is a stronger
claim than a comment. If the work is not actually done, DO NOT close.
Instead:

    kata edit <ref> --label needs-review
    kata comment <ref> --body "what was attempted, what remains"

Close each issue as soon as its work is verified, not at the end of a
batch. The daemon throttles >3 sibling closes by one actor under one
parent in 5 minutes; close eagerly and you will not see it. Operators
can disable the throttle via `[close.throttle] enabled = false` in
`<KATA_HOME>/config.toml`.

When the work IS done, close with substantive prose and typed
`--evidence` so a reviewer can verify the claim later:

    kata close abc4 --done \
      --message "Fixed Safari callback double-submit; verified tests pass." \
      --commit <sha>

Other close forms:

    kata close abc4 --duplicate-of d4ex  --message "Same Safari race condition."
    kata close abc4 --superseded-by d4ex --message "Replaced by broader scope."
    kata close abc4 --wontfix --message "<>=60 chars of rationale>"
    kata close abc4 --audit-no-change \
                    --message "Reviewed schema and queries; no change needed." \
                    --evidence "no-change-audit:schema unchanged after review" \
                    --reviewed internal/db/schema.sql

The daemon refuses parent-close while open children remain. Reviewers
can replay activity with `kata audit closes` and undo specific lazy
closes with `kata reopen <ref>`.
