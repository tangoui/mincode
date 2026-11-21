/**
 * Service name rule of the `services` contribution point, kept apart from
 * domain/plugins/services.ts so the host prompt parser (host-ui.ts, part of
 * the entry bundle) can check names without loading the services model.
 */
import contributionContract from "./contribution-points.json" with { type: "json" };

const spec = contributionContract.points.services;

const SERVICE_NAME_PATTERN = /^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*){1,3}$/;

/** Why a service name is rejected, or undefined when it is valid. */
export function serviceNameError(name: string): string | undefined {
  const maxLength = spec.limits.serviceMaxLength;
  if (name.length > maxLength) return `service "${name}" must be at most ${maxLength} characters`;
  if (!SERVICE_NAME_PATTERN.test(name)) return `service "${name}" must match ${SERVICE_NAME_PATTERN.source}`;
  const prefix = name.slice(0, name.indexOf("."));
  if ((spec.lists.reservedPrefixes as readonly string[]).includes(prefix)) return `service "${name}" uses the reserved prefix "${prefix}."`;
  return undefined;
}
