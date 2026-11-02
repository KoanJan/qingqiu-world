# Web Project Standards

These requirements apply to `web/` and related Electron interfaces. UI design should represent domain state accurately before adding visual effects.

## Structure and Components

- Prefer normal flow, flexbox, and grid. Avoid using absolute positioning for the main page structure. Content must remain readable and operable in a small window, with long text, and across languages.
- Keep one implementation for components, icons, state mappings, and copy that share the same business meaning. Check whether an existing component can serve a new entry point; do not give the same state different names on different pages.
- Present loading, empty, failed, in-progress, and completed states distinctly. Do not render a failed query as "no data."

## Visuals and Interaction

- Route new colors, highlights, backgrounds, and animations through existing theme and personalization settings. Do not add a fixed accent color to one page that bypasses global settings.
- Animation should help users understand changes in page, selection, or content location. A control's visual state must match what users can actually do. Keep a clear static state for environments where animation is unsuitable.
- Use consistent names for the same domain object across pages. Do not combine different objects into one filter dimension merely to simplify the UI.
- Icon buttons need accessible names. Hover text can supplement a name but cannot be its only source.

## Copy and Verification

- Write localized sentences in the natural word order of each language rather than translating by simple concatenation or token rearrangement. Style interpolated parameter values separately from fixed copy so inserted text remains in the intended element.
- Present only known states. Do not portray an unexecuted intention as an effect that has already occurred. Labels for time, identity, and source should let users tell who did what and whether a result actually happened.
- Check affected pages in small windows, with long names, empty lists, many records, and both Chinese and English. Run the project's existing type checks, build, or lint commands.
