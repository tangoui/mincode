// Schema associations for the built-in JSON and TOML language servers. Both
// only complete from JSON Schemas, so without these well-known config files
// (package.json, tsconfig.json, Cargo.toml, ...) got no suggestions. The
// servers download the schemas from SchemaStore and the projects' own sites
// over HTTPS when such a file is opened; yaml-language-server already resolves
// SchemaStore schemas on its own.

const SCHEMASTORE = "https://www.schemastore.org";

/** json/schemaAssociations: file pattern (any folder unless it has a slash) → schema URLs. */
export const JSON_SCHEMA_ASSOCIATIONS: Record<string, string[]> = {
  "package.json": [`${SCHEMASTORE}/package.json`],
  "tsconfig.json": [`${SCHEMASTORE}/tsconfig.json`],
  "tsconfig.*.json": [`${SCHEMASTORE}/tsconfig.json`],
  "jsconfig.json": [`${SCHEMASTORE}/jsconfig.json`],
  "jsconfig.*.json": [`${SCHEMASTORE}/jsconfig.json`],
  "composer.json": ["https://getcomposer.org/schema.json"],
  ".eslintrc": [`${SCHEMASTORE}/eslintrc.json`],
  ".eslintrc.json": [`${SCHEMASTORE}/eslintrc.json`],
  ".prettierrc": [`${SCHEMASTORE}/prettierrc.json`],
  ".prettierrc.json": [`${SCHEMASTORE}/prettierrc.json`],
  ".babelrc": [`${SCHEMASTORE}/babelrc.json`],
  ".babelrc.json": [`${SCHEMASTORE}/babelrc.json`],
  "babel.config.json": [`${SCHEMASTORE}/babelrc.json`],
  ".stylelintrc": [`${SCHEMASTORE}/stylelintrc.json`],
  ".stylelintrc.json": [`${SCHEMASTORE}/stylelintrc.json`],
  "lerna.json": [`${SCHEMASTORE}/lerna.json`],
  "nodemon.json": [`${SCHEMASTORE}/nodemon.json`],
  "nest-cli.json": [`${SCHEMASTORE}/nest-cli.json`],
  "renovate.json": ["https://docs.renovatebot.com/renovate-schema.json"],
  "turbo.json": ["https://turborepo.org/schema.json"],
  "vercel.json": ["https://openapi.vercel.sh/vercel.json"],
  "deno.json": ["https://raw.githubusercontent.com/denoland/deno/main/cli/schemas/config-file.v1.json"],
  "biome.json": ["https://biomejs.dev/schemas/latest/schema.json"],
  "appsettings.json": [`${SCHEMASTORE}/appsettings.json`],
  "appsettings.*.json": [`${SCHEMASTORE}/appsettings.json`],
};

/** taplo schema.associations: regex over the document URI → schema URL. */
export const TOML_SCHEMA_ASSOCIATIONS: Record<string, string> = {
  "(^|/)Cargo\\.toml$": `${SCHEMASTORE}/cargo.json`,
  "(^|/)pyproject\\.toml$": `${SCHEMASTORE}/pyproject.json`,
  "(^|/)\\.?rustfmt\\.toml$": `${SCHEMASTORE}/rustfmt.json`,
  "(^|/)\\.?ruff\\.toml$": `${SCHEMASTORE}/ruff.json`,
  "(^|/)\\.?taplo\\.toml$": `${SCHEMASTORE}/taplo.json`,
  "(^|/)bunfig\\.toml$": `${SCHEMASTORE}/bunfig.json`,
};

export function lspSchemaAssociationNotification(serverId: string) {
  if (serverId !== "json-ls") return undefined;
  return { method: "json/schemaAssociations", params: JSON_SCHEMA_ASSOCIATIONS };
}

/**
 * Value for one workspace/configuration item; null lets the server use its
 * defaults. taplo's default SchemaStore catalog no longer parses ("did not
 * match any variant of untagged enum SchemaCatalog"), so it gets direct
 * associations and no catalog.
 */
export function lspConfigurationValue(serverId: string, section: unknown): unknown {
  if (serverId === "taplo" && section === "evenBetterToml") {
    return { schema: { enabled: true, catalogs: [], associations: TOML_SCHEMA_ASSOCIATIONS } };
  }
  return null;
}
