# WCAG 2.2 Level AA

## Scope

Applies to every user-facing surface: the app, marketing pages, emails, PDFs and any embedded third-party widget. WCAG is not itself law, but Section 508, EN 301 549, AODA and the European Accessibility Act all point at a version of it, so meeting AA once satisfies the accessibility clause of several other obligations at the same time.

## What it demands

The four principles — perceivable, operable, understandable, robust — cash out as roughly fifty testable success criteria at A and AA. The ones that most often fail in a React app:

- **Keyboard operability.** Every interactive element reachable and usable by keyboard, with a visible focus indicator. Custom dropdowns, modals and drag interactions are where this breaks.
- **Focus management.** 2.4.11 Focus Not Obscured: a focused element must not be hidden behind a sticky header or toast.
- **Target size.** 2.5.8: interactive targets at least 24x24 CSS pixels, or adequately spaced.
- **Dragging alternatives.** 2.5.7: any drag interaction needs a single-pointer alternative. Board and backlog reordering is the usual offender.
- **Accessible authentication.** 3.3.8: no cognitive function test (remembering, transcribing) without an alternative — this constrains puzzle CAPTCHAs.
- **Redundant entry.** 3.3.7: do not make a user re-enter information already given in the same process.
- **Consistent help.** 3.2.6: if help exists, it sits in the same relative place on every page.
- **Contrast.** 4.5:1 for body text, 3:1 for large text and UI component boundaries — check both light and dark themes.
- **Names, roles, values.** Real semantics or correct ARIA; form controls with programmatic labels; status messages announced.

Note that 4.1.1 Parsing was removed in 2.2 — do not spend effort on it.

## How it lands in this app

Prefer semantic HTML over ARIA. Every icon-only button needs an accessible name. Every route change should move focus and update the document title. Test with keyboard only, then a screen reader, then automated tooling — axe or Lighthouse catches perhaps a third of real failures, so automation is a floor and not the check.

## Evidence to keep

A WCAG 2.2 AA conformance statement or VPAT, the date and method of the last audit, a list of known failures with owners, and any accessibility statement published to users.