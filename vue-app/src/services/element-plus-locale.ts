import { computed, ref, watch, type App } from "vue";
import { provideGlobalConfig } from "element-plus";
import type { Language } from "element-plus/es/locale";
import { useVueI18n, type AppLocale } from "@/i18n/messages";

/**
 * Element Plus's own texts (MessageBox OK / Cancel, pagination, empty
 * tables, date pickers) in the given interface language. Each language is
 * a separate chunk, loaded when first needed.
 */
export async function elementPlusLocale(locale: AppLocale): Promise<Language> {
  const module = locale === "zh"
    ? await import("element-plus/es/locale/lang/zh-cn")
    : await import("element-plus/es/locale/lang/en");
  return module.default;
}

/**
 * Gives every Element Plus component in app, including MessageBox and
 * Message created outside any component, the interface language, and
 * follows it when the user switches.
 */
export function installElementPlusLocale(app: App): App {
  const language = ref<Language>();
  provideGlobalConfig(computed(() => ({ locale: language.value })), app, true);
  const { locale } = useVueI18n();
  let request = 0;
  watch(
    locale,
    async (value) => {
      const current = ++request;
      try {
        const loaded = await elementPlusLocale(value);
        if (current === request) language.value = loaded;
      } catch {
        // Keeps the previous language (or Element Plus's English).
      }
    },
    { immediate: true },
  );
  return app;
}
