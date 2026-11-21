import { computed, ref, shallowReactive, shallowRef, watch } from "vue";
import { messagesZh, type MessageKey } from "@/i18n/locales/zh";
import { isProtectedMessageKey } from "@/i18n/protected-keys";

export type { MessageKey } from "@/i18n/locales/zh";

export type AppLocale = "zh" | "en";
type LocaleMessages = Record<MessageKey, string>;

/**
 * A validated language pack overlay (contributes.localizations). Built by
 * domain/i18n/locale-pack.ts `buildOverlay`; keys it lacks fall back to the
 * base `locale`. Protected keys are removed again when it is installed.
 */
export interface LocaleOverlay {
  tag: string;
  pluginId: string;
  messages: ReadonlyMap<string, string>;
}

/** The base (fallback) language; every `=== "zh"` comparison keeps using it. */
const locale = ref<AppLocale>("zh");
const overlay = shallowRef<LocaleOverlay | null>(null);
const loadedMessages = shallowReactive<Partial<Record<AppLocale, LocaleMessages>>>({
  zh: messagesZh,
});
let englishMessagesPromise: Promise<LocaleMessages> | null = null;
let localeRequest = 0;

async function loadLocaleMessages(value: AppLocale) {
  const loaded = loadedMessages[value];
  if (loaded) return loaded;
  englishMessagesPromise ??= import("@/i18n/locales/en").then(
    ({ messagesEn }) => messagesEn as LocaleMessages,
  );
  const messages = await englishMessagesPromise;
  loadedMessages.en = messages;
  return messages;
}

/** The base messages of a built-in language (English is the language pack reference catalogue). */
export function loadBaseMessages(value: AppLocale): Promise<Readonly<Record<string, string>>> {
  return loadLocaleMessages(value);
}

function translate(key: MessageKey, params?: Record<string, string | number>) {
  const template = overlay.value?.messages.get(key) ?? ((loadedMessages[locale.value] ?? messagesZh)[key] as string);
  if (!params) return template;
  return Object.entries(params).reduce(
    (result, [name, value]) => result.split(`{${name}}`).join(String(value)),
    template,
  );
}

/**
 * Installs (or with null removes) the language pack overlay. Protected
 * security and approval keys are filtered out here once, so `translate`
 * never has to consult the protected list.
 */
export function setLocaleOverlay(value: LocaleOverlay | null) {
  if (!value) {
    overlay.value = null;
    return;
  }
  const messages = new Map<string, string>();
  for (const [key, text] of value.messages) {
    if (!isProtectedMessageKey(key)) messages.set(key, text);
  }
  overlay.value = { tag: value.tag, pluginId: value.pluginId, messages };
}

/** The installed overlay, if any. */
export function currentLocaleOverlay(): LocaleOverlay | null {
  return overlay.value;
}

/** Loads the saved locale's messages before switching, so startup never flashes the other language. */
export async function applySavedLocale(value: unknown) {
  if (value !== "zh" && value !== "en") return;
  const request = ++localeRequest;
  await loadLocaleMessages(value);
  if (request === localeRequest) locale.value = value;
}

function setLocale(value: AppLocale) {
  const request = ++localeRequest;
  if (loadedMessages[value]) {
    locale.value = value;
    return;
  }
  void loadLocaleMessages(value).then(() => {
    if (request === localeRequest) locale.value = value;
  });
}

const localeTag = computed(() => overlay.value?.tag ?? locale.value);
const localeChain = computed<readonly string[]>(() =>
  overlay.value && overlay.value.tag !== locale.value ? [overlay.value.tag, locale.value] : [locale.value],
);
const intlLocale = computed(() => overlay.value?.tag ?? (locale.value === "zh" ? "zh-CN" : "en-US"));

if (typeof document !== "undefined") {
  watch(
    [locale, overlay],
    () => {
      document.documentElement.lang = overlay.value?.tag ?? (locale.value === "zh" ? "zh-CN" : "en");
    },
    { immediate: true },
  );
}

/** 是否为已知的文案键（键集合以 zh 为准，en 由类型保证同步）。 */
function hasMessage(key: string): key is MessageKey {
  return Object.prototype.hasOwnProperty.call(messagesZh, key);
}

export function useVueI18n() {
  return {
    /** Base language (zh | en); language pack text falls back to it. */
    locale: computed(() => locale.value),
    /** Active UI language tag: the language pack's tag, else the base. */
    localeTag,
    /** `[packTag, base]` with a language pack, else `[base]` (plugin title resolution). */
    localeChain,
    /** Tag for Intl formatters. */
    intlLocale,
    t: translate,
    /** 判断字符串是否为已知文案键，用于翻译以文案键作为消息的错误。 */
    te: hasMessage,
    setLocale,
    toggleLocale: () => setLocale(locale.value === "zh" ? "en" : "zh"),
  };
}
