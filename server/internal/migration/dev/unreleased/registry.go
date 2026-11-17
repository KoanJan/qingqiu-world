package unreleased

// registered is the single authority over development revisions: it fixes the
// order, the revision number, and what each step executes. A committed
// persistent change appends one entry and its scripts/<revision>.sql and/or
// scripts/<revision>.go file; the file names are a readability convention, not
// a lookup rule. Earlier steps stay unchanged; a rebase creates a new commit
// identity and triggers rebuilding as needed. Fold the final effects into the
// next release migration and clear the list.
var registered = []Step{
	{
		BaseRelease:  "0.1.21",
		FromRevision: 0,
		Revision:     1,
		Description:  "Add LLM compatibility probe state",
		SQLPath:      "scripts/1.sql",
	},
}
