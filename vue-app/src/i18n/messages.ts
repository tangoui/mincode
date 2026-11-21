import { computed, ref, shallowReactive, watch } from "vue";
import { messagesZh, type MessageKey } from "@/i18n/locales/zh";

export type { MessageKey } from "@/i18n/locales/zh";

export type AppLocale = "zh" | "en";
type LocaleMessages = Record<MessageKey, string>;

const locale = ref<AppLocale>("zh");
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

function translate(key: MessageKey, params?: Record<string, string | number>) {
  const template = (loadedMessages[locale.value] ?? messagesZh)[key];
  if (!params) return template;
  return Object.entries(params).reduce(
    (result, [name, value]) => result.split(`{${name}}`).join(String(value)),
    template,
  );
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

const localeChain = computed<readonly string[]>(() => [locale.value]);
const intlLocale = computed(() => locale.value === "zh" ? "zh-CN" : "en-US");

if (typeof document !== "undefined") {
  watch(locale, () => {
    document.documentElement.lang = intlLocale.value;
  }, { immediate: true });
}

/** 是否为已知的文案键（键集合以 zh 为准，en 由类型保证同步）。 */
function hasMessage(key: string): key is MessageKey {
  return Object.prototype.hasOwnProperty.call(messagesZh, key);
}

export function useVueI18n() {
  return {
    locale: computed(() => locale.value),
    localeTag: computed(() => locale.value),
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
