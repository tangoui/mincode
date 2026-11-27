import type { AiCapability, AiCapabilityRisk } from "@/domain/ai/capability-registry";
import type { NoteLinkType } from "@/domain/notes/types";
import { useVueI18n } from "@/i18n/messages";
import * as notes from "@/services/notes-service";
import { tool } from "./shared";

type Input = Record<string, unknown>;
type NotesService = Pick<typeof notes,
  | "getNoteTree" | "getNote" | "searchNotes" | "createNote" | "updateNote" | "moveNote" | "deleteNote"
  | "createNoteFolder" | "renameNoteFolder" | "deleteNoteFolder" | "listNoteTags" | "setNoteTags"
  | "addNoteLink" | "removeNoteLink" | "listNotesForObject"
>;

const text = { type: "string" };
const { t } = useVueI18n();
const tagsSchema = { type: "array", items: text };
const objectTypes: readonly NoteLinkType[] = ["ssh", "project", "database", "ai", "http"];
const objectProperties = {
  objectType: { type: "string", enum: objectTypes },
  objectId: text,
  scopeId: text,
};

function optionalText(input: Input, key: string): string | undefined {
  const value = input[key];
  if (value === undefined || typeof value === "string") return value;
  throw new Error(t("notes.tools.invalidArgument", { key }));
}

function requiredText(input: Input, key: string, allowEmpty = false): string {
  const value = optionalText(input, key);
  if (value === undefined || (!allowEmpty && !value.trim())) {
    throw new Error(t("notes.tools.requiredArgument", { key }));
  }
  return value;
}

function readTags(value: unknown): string[] {
  if (!Array.isArray(value) || !value.every((tag) => typeof tag === "string")) {
    throw new Error(t("notes.tools.invalidArgument", { key: "tags" }));
  }
  return value;
}

function readObject(input: Input) {
  const objectType = objectTypes.find((type) => type === input.objectType);
  if (!objectType) throw new Error(t("notes.tools.invalidArgument", { key: "objectType" }));
  const objectId = requiredText(input, "objectId");
  const scopeId = optionalText(input, "scopeId");
  return { objectType, objectId, ...(scopeId !== undefined ? { scopeId } : {}) };
}

function capability(
  name: string,
  description: string,
  risk: AiCapabilityRisk,
  properties: Input,
  required: string[],
  run: (input: Input) => unknown,
): AiCapability {
  return {
    module: "notes",
    target: "none",
    risk,
    mode: risk === "read" ? "read" : "write",
    tool: tool(`notes.${name}`, description, properties, required),
    execute: async ({ input }) => (await run(input)) ?? { ok: true },
  };
}

export function createNotesCapabilities(service: NotesService = notes): AiCapability[] {
  return [
    capability("list_tree", "List note folders and note metadata.", "read", {}, [], () => service.getNoteTree()),
    capability("get", "Read a note's Markdown body and metadata by ID.", "read", { id: text }, ["id"],
      (input) => service.getNote(requiredText(input, "id"))),
    capability("search", "Search note titles and bodies.", "read", {
      query: text,
      limit: { type: "integer", minimum: 1, maximum: 50 },
    }, ["query"], (input) => {
      const limit = input.limit;
      if (limit !== undefined && (typeof limit !== "number" || !Number.isInteger(limit) || limit < 1 || limit > 50)) {
        throw new Error(t("notes.tools.invalidArgument", { key: "limit" }));
      }
      return service.searchNotes(requiredText(input, "query").trim(), limit);
    }),
    capability("create", "Create a Markdown note, optionally in a folder and with tags.", "write", {
      title: text, content: text, folderId: text, tags: tagsSchema,
    }, ["title", "content"], (input) => {
      const folderId = optionalText(input, "folderId");
      return service.createNote({
        title: requiredText(input, "title", true),
        body: requiredText(input, "content", true),
        ...(folderId !== undefined ? { folderId } : {}),
        ...(input.tags !== undefined ? { tags: readTags(input.tags) } : {}),
      });
    }),
    capability("update", "Update a note's title or Markdown content. append=true adds content verbatim.", "write", {
      id: text, title: text, content: text, append: { type: "boolean" },
    }, ["id"], (input) => {
      const id = requiredText(input, "id");
      const title = optionalText(input, "title");
      const body = optionalText(input, "content");
      if (input.append !== undefined && typeof input.append !== "boolean") {
        throw new Error(t("notes.tools.invalidArgument", { key: "append" }));
      }
      if (body === undefined && (title === undefined || input.append === true)) {
        throw new Error(t("notes.tools.requiredArgument", { key: input.append ? "content" : "title / content" }));
      }
      return service.updateNote({
        id,
        ...(title !== undefined ? { title } : {}),
        ...(body !== undefined ? { body } : {}),
        ...(input.append === true ? { append: true } : {}),
      });
    }),
    capability("move", "Move a note to a folder (empty folderId for root). Omit position to place it last.", "write", {
      id: text, folderId: text, position: { type: "number" },
    }, ["id", "folderId"], async (input) => {
      const id = requiredText(input, "id");
      const folderId = requiredText(input, "folderId", true);
      let position = input.position;
      if (position === undefined) {
        const tree = await service.getNoteTree();
        position = tree.notes.reduce((last, note) => note.folderId === folderId && note.id !== id
          ? Math.max(last, note.position) : last, 0) + 1;
      }
      if (typeof position !== "number" || !Number.isFinite(position)) {
        throw new Error(t("notes.tools.invalidArgument", { key: "position" }));
      }
      return service.moveNote(id, folderId, position);
    }),
    capability("delete", "Delete a note.", "dangerous", { id: text }, ["id"],
      (input) => service.deleteNote(requiredText(input, "id"))),
    capability("create_folder", "Create a note folder. Omit parentId for the root.", "write", {
      name: text, parentId: text,
    }, ["name"], (input) => service.createNoteFolder(requiredText(input, "name"), optionalText(input, "parentId"))),
    capability("rename_folder", "Rename a note folder.", "write", { id: text, name: text }, ["id", "name"],
      (input) => service.renameNoteFolder(requiredText(input, "id"), requiredText(input, "name"))),
    capability("delete_folder", "Delete a note folder and its contents.", "dangerous", { id: text }, ["id"],
      (input) => service.deleteNoteFolder(requiredText(input, "id"))),
    capability("list_tags", "List note tags and their usage counts.", "read", {}, [], () => service.listNoteTags()),
    capability("set_tags", "Replace a note's tags. Pass an empty array to clear them.", "write", {
      id: text, tags: tagsSchema,
    }, ["id", "tags"], (input) => service.setNoteTags(requiredText(input, "id"), readTags(input.tags))),
    capability("link_object", "Associate a note with a MinCode object.", "write", {
      id: text, ...objectProperties, parentId: text, nameSnapshot: text, scopeNameSnapshot: text,
    }, ["id", "objectType", "objectId"], (input) => {
      const parentId = optionalText(input, "parentId");
      const nameSnapshot = optionalText(input, "nameSnapshot");
      const scopeNameSnapshot = optionalText(input, "scopeNameSnapshot");
      return service.addNoteLink(requiredText(input, "id"), {
        ...readObject(input),
        ...(parentId !== undefined ? { parentId } : {}),
        ...(nameSnapshot !== undefined ? { nameSnapshot } : {}),
        ...(scopeNameSnapshot !== undefined ? { scopeNameSnapshot } : {}),
      });
    }),
    capability("unlink_object", "Remove a note's association with a MinCode object.", "write", {
      id: text, ...objectProperties,
    }, ["id", "objectType", "objectId"], (input) => {
      const { objectType, objectId, scopeId } = readObject(input);
      return service.removeNoteLink(requiredText(input, "id"), objectType, objectId, scopeId);
    }),
    capability("list_for_object", "List notes linked to a MinCode object. Omit scopeId to search all scopes.", "read",
      objectProperties, ["objectType", "objectId"], (input) => {
        const { objectType, objectId, scopeId } = readObject(input);
        return service.listNotesForObject(objectType, objectId, scopeId);
      }),
  ];
}
