import assert from 'node:assert/strict';
import test from 'node:test';
import { decodeEntities, importBatches, parseBookmarkHTML, parseImportFile } from '../../official-plugins/web-navigation/ui-src/transfer.mjs';

const chromeExport = `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<!-- This is an automatically generated file. -->
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
    <DT><H3 ADD_DATE="1" PERSONAL_TOOLBAR_FOLDER="true">书签栏</H3>
    <DL><p>
        <DT><A HREF="https://github.com/" ADD_DATE="1" ICON="data:image/png;base64,AAAA">GitHub</A>
        <DT><H3>Dev &amp; Docs</H3>
        <DL><p>
            <DT><A HREF="https://go.dev/?a=1&amp;b=2">Go <b>lang</b></A>
            <DD>The Go &lt;site&gt;
            <DT><A HREF="javascript:void(0)">Bookmarklet</A>
            <DT><A HREF='https://vuejs.org'>Vue</A>
        </DL><p>
        <DT><A HREF=https://back.example/>Back in bar</A>
    </DL><p>
    <DT><A HREF="https://root.example/">Root</A>
    <HR>
    <DT><A HREF="place:sort=8">Recent</A>
</DL><p>`;

test('bookmark files map each link to its innermost folder', () => {
  const parsed = parseBookmarkHTML(chromeExport);
  assert.deepEqual(parsed.categories, ['书签栏', 'Dev & Docs']);
  assert.deepEqual(parsed.sites, [
    { name: 'GitHub', url: 'https://github.com/', description: '', category: '书签栏' },
    { name: 'Go lang', url: 'https://go.dev/?a=1&b=2', description: 'The Go <site>', category: 'Dev & Docs' },
    { name: 'Vue', url: 'https://vuejs.org', description: '', category: 'Dev & Docs' },
    { name: 'Back in bar', url: 'https://back.example/', description: '', category: '书签栏' },
    { name: 'Root', url: 'https://root.example/', description: '', category: '' },
  ]);
  assert.equal(parsed.skipped, 2);
  assert.deepEqual(parseImportFile(`\ufeff${chromeExport}`), parsed);
});

test('the plugin bookmark export parses back into the same categories', () => {
  const exported = `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<DL><p>
    <DT><H3>A &lt;x&gt;</H3>
    <DL><p>
        <DT><A HREF="https://q.example/?a=1&amp;b=%222%22">Q &amp; A</A>
        <DD>desc
    </DL><p>
    <DT><A HREF="https://loose.example/">Loose</A>
</DL><p>
`;
  assert.deepEqual(parseImportFile(exported), {
    categories: ['A <x>'],
    sites: [
      { name: 'Q & A', url: 'https://q.example/?a=1&b=%222%22', description: 'desc', category: 'A <x>' },
      { name: 'Loose', url: 'https://loose.example/', description: '', category: '' },
    ],
    skipped: 0,
  });
});

test('JSON imports accept the export format, stored state, config exports and plain lists', () => {
  const exportFile = { format: 'mincode-web-navigation', version: 1, categories: ['Empty', 'Dev'], sites: [
    { name: 'Go', url: 'https://go.dev/', category: 'Dev' }, { name: 'Bad', url: 'ftp://x' }, 'junk'] };
  assert.deepEqual(parseImportFile(JSON.stringify(exportFile)), {
    categories: ['Empty', 'Dev'],
    sites: [{ name: 'Go', url: 'https://go.dev/', description: '', category: 'Dev' }],
    skipped: 2,
  });

  const navigation = {
    categories: [{ id: 'b', name: 'Second', order: 1 }, { id: 'a', name: 'First', order: 0 }],
    sites: [
      { id: '1', categoryId: 'b', name: 'B1', url: 'https://b1.example/', description: '', order: 0 },
      { id: '2', categoryId: '', name: 'Loose', url: 'https://loose.example/', description: '', order: 0 },
      { id: '3', categoryId: 'a', name: 'A2', url: 'https://a2.example/', description: 'x', order: 1 },
      { id: '4', categoryId: 'a', name: 'A1', url: 'https://a1.example/', description: '', order: 0 },
    ],
  };
  const expected = {
    categories: ['First', 'Second'],
    sites: [
      { name: 'A1', url: 'https://a1.example/', description: '', category: 'First' },
      { name: 'A2', url: 'https://a2.example/', description: 'x', category: 'First' },
      { name: 'B1', url: 'https://b1.example/', description: '', category: 'Second' },
      { name: 'Loose', url: 'https://loose.example/', description: '', category: '' },
    ],
    skipped: 0,
  };
  assert.deepEqual(parseImportFile(JSON.stringify(navigation)), expected);
  const configExport = { format: 'mincode-config', version: 2, values: {}, plugins: { 'web-navigation': { kv: { navigation, 'defaults-version': 3 }, files: {} } } };
  assert.deepEqual(parseImportFile(JSON.stringify(configExport)), expected);

  assert.deepEqual(parseImportFile('[{"title":"T","href":"https://t.example/"}]').sites,
    [{ name: 'T', url: 'https://t.example/', description: '', category: '' }]);
  for (const unsupported of ['{"hello":1}', '{bad json', 'plain text', '42']) assert.equal(parseImportFile(unsupported), null);
});

test('imports are split into payloads under the action limit, categories first', () => {
  const sites = Array.from({ length: 1200 }, (_, index) => ({ name: `S${index}`, url: `https://s${index}.example/${'p'.repeat(200)}`, description: '', category: 'C' }));
  const batches = importBatches({ categories: ['C'], sites });
  assert.ok(batches.length > 2);
  assert.deepEqual(batches[0].categories, ['C']);
  assert.ok(batches.slice(1).every(batch => batch.categories.length === 0));
  assert.equal(batches.reduce((count, batch) => count + batch.sites.length, 0), 1200);
  for (const batch of batches) assert.ok(new TextEncoder().encode(JSON.stringify(batch)).byteLength <= 200 * 1024 && batch.sites.length <= 500);
  assert.deepEqual(importBatches({ categories: ['Only'], sites: [] }), [{ categories: ['Only'], sites: [] }]);
});

test('entity decoding leaves unknown or invalid references alone', () => {
  assert.equal(decodeEntities('&amp;&#39;&#x4E2D;&unknown;&#99999999;'), "&'中&unknown;&#99999999;");
});
