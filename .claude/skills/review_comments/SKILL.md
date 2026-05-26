---
name: review_comments
description: Verdict PR review comments pragmatically - address valid ones, dismiss over-engineered or incorrect ones, and respond to each. Use this skill when handling PR review feedback, code review comments, or when asked to address reviewer suggestions.
---

# Review Comments

Pragmatically triage PR review comments: fix what matters, push back on what doesn't.

## Phase 1: Fetch Comments

1. Get the PR number from context or user input
2. Fetch all review comments:
   ```bash
   gh pr view <number> --json reviews,comments
   gh api repos/{owner}/{repo}/pulls/<number>/comments
   gh api repos/{owner}/{repo}/pulls/<number>/reviews
   ```
3. Read the actual code being reviewed to understand context

## Phase 2: Verdict Each Comment

For each comment, classify it into one of:

### ADDRESS - The comment is correct and actionable
- Genuine bugs or logic errors
- Missing edge cases that could actually happen
- Security issues
- Correctness issues backed by evidence

### DISMISS - The comment is over-engineered, stylistic, or wrong
- Adding abstractions for hypothetical future needs
- Suggesting error handling for impossible scenarios
- Style preferences with no functional impact
- Refactoring suggestions unrelated to the PR's purpose
- Incorrect assumptions about how the code works
- "What if..." scenarios that won't happen given the codebase constraints
- Suggesting tests for trivial code paths
- Adding comments/docs to self-evident code

### ACKNOWLEDGE - Minor valid point, not worth changing
- Technically correct but the cost of change outweighs the benefit
- Valid style point but not a blocker
- Good idea for a follow-up, not this PR

## Phase 3: Act on Each Comment

### For ADDRESS comments
1. Make the code change
2. Reply explaining what was fixed

### For DISMISS comments
Reply with a concise, respectful explanation of why you're not making the change. Be direct, not defensive. Examples:

- Over-engineering: "This is a single-use path - I'd rather keep it simple and extract if/when we need reuse."
- Impossible scenario: "This can't happen because [X guarantees Y]. Adding a guard here would suggest the invariant isn't trustworthy."
- Style preference: "Both approaches work - I'll keep the current style for consistency with the surrounding code."
- Wrong assumption: "Actually, [correct explanation]. The current code handles this because [reason]."

### For ACKNOWLEDGE comments
Reply acknowledging the point briefly: "Good point - I'll keep this in mind for follow-up" or "Fair, though I think the current approach is fine for now given [reason]."

## Phase 4: Respond

Use `gh` to reply to each comment:
```bash
gh api repos/{owner}/{repo}/pulls/<number>/comments/<comment_id>/replies \
  -f body="<response>"
```

## Decision Principles

1. **Ship over perfection** - The goal is a working, correct change, not a perfect one
2. **YAGNI** - Don't build for requirements that don't exist yet
3. **Minimal diff** - Every line changed is a line that could break; keep changes focused
4. **Trust the codebase** - Internal code doesn't need defensive programming against itself
5. **Reviewer != oracle** - Reviewers can be wrong, overly cautious, or applying rules from different contexts
6. **Evidence over authority** - "This could break" needs a scenario; "best practice says" needs a reason
7. **Be respectful, be direct** - Dismissing a comment isn't personal; explain your reasoning clearly
