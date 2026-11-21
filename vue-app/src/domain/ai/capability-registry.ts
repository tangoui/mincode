import type { AiLoopToolDefinition } from "@/services/ai-agent-loop";
import { runWithAbort, throwIfAborted } from "@/domain/async/abort";
import type { AiOperationMode, AiOperationSnapshot } from "./operation-manager";
import type { AiResource, AiResourceResolution } from "./resource-registry";
import type { ToolAuthorizationRequest, ToolSessionGrant } from "./tool-authorization";

export type AiCapabilityRisk = "read" | "network" | "write" | "dangerous";

export interface AiCapabilityExecutionContext {
  resourceId: string;
  resource: AiResource;
  input: Record<string, unknown>;
  signal: AbortSignal;
  conversationId?: string;
}

export interface AiCapabilityApprovalContext {
  input: Record<string, unknown>;
  resource?: AiResource;
}

export interface AiCapability {
  module: string;
  discovery?: boolean;
  tool: AiLoopToolDefinition;
  mode: AiOperationMode;
  target?: "resource" | "none";
  risk: AiCapabilityRisk;
  /**
   * Scope of an "allow for this session" grant; defaults per sessionGrantBindsParameters.
   * Use "tool" only when parameters inherently differ on every call of an approved task.
   */
  sessionGrant?: ToolSessionGrant;
  approvalPreview?(
    context: AiCapabilityApprovalContext,
  ): Promise<string> | string;
  /**
   * Risk of this particular call, for capabilities that dispatch to operations of
   * differing risk. Approval follows it; `risk` stays the ceiling for authorization.
   */
  resolveRisk?(
    context: AiCapabilityApprovalContext,
  ): Promise<AiCapabilityRisk> | AiCapabilityRisk;
  execute(context: AiCapabilityExecutionContext): Promise<unknown> | unknown;
}

export interface AiCapabilityResources {
  resolve(target: {
    resourceId?: string;
    name?: string;
    capability?: string;
  }): AiResourceResolution;
}

interface AiCapabilityOperationHandle {
  done: Promise<void>;
  snapshot(): Pick<
    AiOperationSnapshot,
    "id" | "resourceId" | "state" | "result" | "error"
  >;
  cancel(): void;
}

interface AiCapabilityOperations {
  start(request: {
    resourceId: string;
    capability: string;
    mode: AiOperationMode;
    run(signal: AbortSignal): Promise<unknown> | unknown;
  }): AiCapabilityOperationHandle;
}

export interface AiCapabilityAuthorizationContext {
  conversationId: string;
  consume(request: ToolAuthorizationRequest): Promise<boolean>;
}

export interface AiCapabilityExecutionDependencies {
  resources: AiCapabilityResources;
  operations: AiCapabilityOperations;
  authorization?: AiCapabilityAuthorizationContext;
  conversationId?: string;
  signal?: AbortSignal;
}

export interface AiCapabilityExecutionResult {
  operationId?: string;
  resourceId?: string;
  result?: unknown;
}

export interface AiCapabilityRegistry {
  register(capability: AiCapability): () => void;
  unregisterModule(module: string): void;
  get(name: string): AiCapability | undefined;
  listTools(): AiLoopToolDefinition[];
  listCapabilities(): AiCapability[];
  resolveResourceFor(
    name: string,
    input: Record<string, unknown>,
    resources: AiCapabilityResources,
  ): AiResource;
  execute(
    name: string,
    input: Record<string, unknown>,
    dependencies: AiCapabilityExecutionDependencies,
  ): Promise<AiCapabilityExecutionResult>;
  subscribe(listener: () => void): () => void;
}

function stringInput(input: Record<string, unknown>, key: string) {
  const value = input[key];
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

function resolveResource(
  capability: AiCapability,
  input: Record<string, unknown>,
  resources: AiCapabilityResources,
) {
  const resolution = resources.resolve({
    resourceId: stringInput(input, "resourceId"),
    name: stringInput(input, "name"),
    capability: capability.tool.name,
  });
  if (resolution.status === "resolved") return resolution.resource;
  if (resolution.status === "ambiguous") {
    throw new Error(
      `目标不唯一: ${resolution.candidates.map((candidate) => candidate.name).join(", ")}`,
    );
  }
  throw new Error(`未找到支持 ${capability.tool.name} 的目标资源`);
}

export function createCapabilityRegistry(): AiCapabilityRegistry {
  const capabilities = new Map<string, AiCapability>();
  const listeners = new Set<() => void>();

  function notify() {
    for (const listener of listeners) listener();
  }

  return {
    register(capability) {
      if (!capability.risk) {
        throw new Error(
          `AI capability ${capability.tool.name} must declare risk`,
        );
      }
      if (capability.mode === "write" && capability.risk === "read") {
        throw new Error(
          `AI capability ${capability.tool.name} cannot use mode=write with risk=read`,
        );
      }
      const existing = capabilities.get(capability.tool.name);
      if (existing && existing.module !== capability.module) {
        throw new Error(
          `AI capability ${capability.tool.name} is already registered by ${existing.module}`,
        );
      }
      capabilities.set(capability.tool.name, capability);
      notify();
      return () => {
        if (capabilities.get(capability.tool.name) !== capability) return;
        capabilities.delete(capability.tool.name);
        notify();
      };
    },
    unregisterModule(module) {
      let changed = false;
      for (const [name, capability] of capabilities) {
        if (capability.module !== module) continue;
        capabilities.delete(name);
        changed = true;
      }
      if (changed) notify();
    },
    get(name) {
      return capabilities.get(name);
    },
    listTools() {
      return [...capabilities.values()].map((capability) => ({
        ...capability.tool,
      }));
    },
    listCapabilities() {
      return [...capabilities.values()];
    },
    resolveResourceFor(name, input, resources) {
      const capability = capabilities.get(name);
      if (!capability) throw new Error(`Unknown AI capability: ${name}`);
      if (capability.target === "none") {
        return {
          id: `${capability.module}:global`,
          kind: "global",
          name: capability.module,
          state: "available",
          capabilities: [name],
        };
      }
      return resolveResource(capability, input, resources);
    },
    async execute(name, input, dependencies) {
      throwIfAborted(dependencies.signal);
      const capability = capabilities.get(name);
      if (!capability) throw new Error(`Unknown AI capability: ${name}`);
      const resource = this.resolveResourceFor(
        name,
        input,
        dependencies.resources,
      );
      if (capability.risk !== "read") {
        const authorization = dependencies.authorization;
        if (!authorization) {
          throw new Error(
            `AI capability ${name} requires explicit authorization context`,
          );
        }
        const approved = await authorization.consume({
          conversationId: authorization.conversationId,
          toolName: name,
          resourceId: resource.id,
          parameters: input,
          risk: capability.risk,
        });
        if (!approved)
          throw new Error(`AI capability ${name} is not authorized`);
      }
      if (capability.target === "none") {
        const signal = dependencies.signal ?? new AbortController().signal;
        return {
          result: await runWithAbort(signal, () =>
            capability.execute({
              resourceId: resource.id,
              resource,
              input,
              signal,
              conversationId: dependencies.conversationId,
            }),
          ),
        };
      }
      const operation = dependencies.operations.start({
        resourceId: resource.id,
        capability: name,
        mode: capability.mode,
        run: (signal) =>
          capability.execute({
            resourceId: resource.id,
            resource,
            input,
            signal,
            conversationId: dependencies.conversationId,
          }),
      });
      const cancelOperation = () => operation.cancel();
      dependencies.signal?.addEventListener("abort", cancelOperation, {
        once: true,
      });
      try {
        await runWithAbort(dependencies.signal, () => operation.done);
      } finally {
        dependencies.signal?.removeEventListener("abort", cancelOperation);
      }
      const snapshot = operation.snapshot();
      if (snapshot.state !== "succeeded") {
        throw new Error(
          snapshot.error || `AI capability ${name} did not complete`,
        );
      }
      return {
        operationId: snapshot.id,
        resourceId: snapshot.resourceId,
        result: snapshot.result,
      };
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
  };
}

export const aiCapabilityRegistry = createCapabilityRegistry();
