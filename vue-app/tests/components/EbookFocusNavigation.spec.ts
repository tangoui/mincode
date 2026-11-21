import { describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent } from "vue";
import ReaderView from "../../../official-plugins/ebook-reader/ui-src/ReaderView.vue";
import type { Transport } from "@/plugin-ui/transport";

const { turnPage } = vi.hoisted(() => ({ turnPage: vi.fn() }));
// Rendering chapter HTML/pdf.js needs native book resources. Keep the parent
// reader and real buttons; replace only that renderer boundary.
vi.mock("../../../official-plugins/ebook-reader/ui-src/BookContent.vue", () => ({
  default: defineComponent({ setup(_, { expose }) { expose({ turnPage, flush() {}, clearSelection() {} }); return () => null; } }),
}));
vi.mock("../../../official-plugins/ebook-reader/ui-src/PdfContent.vue", () => ({
  default: defineComponent({ setup(_, { expose }) { expose({ turnPage, flush() {} }); return () => null; } }),
}));
vi.mock("../../../official-plugins/ebook-reader/ui-src/ReaderSidebar.vue", () => ({ default: defineComponent({ render: () => null }) }));
vi.mock("../../../official-plugins/ebook-reader/ui-src/TypesetPanel.vue", () => ({ default: defineComponent({ render: () => null }) }));

function transport(format: string) {
  return {
    request: async (method: string) => method === 'book/open' ? {
      format, title: 'Test book', toc: [{ chapter: 0, title: 'Chapter' }], chapterCount: 2,
      state: { position: { chapter: 0, block: 0, offset: 0 }, bookmarks: [],
        prefs: { layout: 'reading', style: 'book', theme: 'auto', fontSize: 18, lineHeight: 1.8, width: 800, flow: 'scroll' } },
    } : { annotations: [], eof: true },
    call: async () => true,
  } as unknown as Transport;
}
for (const format of ['epub', 'pdf']) {
  describe(`focus-mode page buttons (${format})`, () => {
    it('shows both controls only in focus mode and calls screen paging, not chapter navigation', async () => {
      turnPage.mockClear();
      const wrapper = mount(ReaderView, { props: { id: 'one', api: transport(format), t: (key: string) => key } });
      try {
        await flushPromises();
        expect(wrapper.find('.eb-focus-page--prev').exists()).toBe(false);
        const focus = wrapper.findAll('button').find(button => button.text() === 'focus');
        expect(focus).toBeDefined();
        await focus?.trigger('click');
        await wrapper.get('.eb-focus-page--prev').trigger('click');
        await wrapper.get('.eb-focus-page--next').trigger('click');
        expect(turnPage.mock.calls).toEqual([[-1], [1]]);
        expect(wrapper.get('.eb-focus-page--prev').attributes('aria-label')).toBe('prevPage');
        expect(wrapper.get('.eb-focus-page--next').attributes('aria-label')).toBe('nextPage');
        await wrapper.findAll('button').find(button => button.text() === 'exitFocus')?.trigger('click');
        expect(wrapper.find('.eb-focus-page--prev').exists()).toBe(false);
      } finally { wrapper.unmount(); await flushPromises(); }
    });
  });
}
