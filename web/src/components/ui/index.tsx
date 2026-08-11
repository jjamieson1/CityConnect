// The primitives and design tokens live in shared/, because the staff console
// and the citizen portal are separate applications that must not drift apart
// visually or behaviourally. This re-exports them so existing imports of
// "@/components/ui" keep working.
export * from "@shared/ui";
