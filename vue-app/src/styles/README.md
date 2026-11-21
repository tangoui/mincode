# Stylesheet ownership

Stylesheets are grouped by the UI surface that owns them:

- `foundation/`: global design tokens and application foundations.
- `shared/`: reusable chrome shared by more than one workspace.
- `workspaces/<feature>/`: styles that belong to one workspace or native tool.

`src/styles.css` intentionally preserves the previous cascade order. Do **not**
alphabetize its imports. When a new rule is needed, put it in the owning feature
folder and add the import only where its required cascade position is explicit.
