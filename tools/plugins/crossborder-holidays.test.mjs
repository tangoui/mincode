// Offline tests for the holiday data generator: the pure transform from a raw
// python-holidays dump to backend/data/holidays.json. Run with
//   node --test tools/plugins/crossborder-holidays.test.mjs
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { COUNTRIES, buildHolidays, commerceDays, dumpCommand, formatHolidays, thanksgiving, translateName } from './crossborder-holidays.mjs';

const DATASET = { name: 'python-holidays', version: '0.107', license: 'MIT', url: 'https://github.com/vacanza/holidays' };

// A two-year build: US and CN in detail, every other country with one entry; the dump
// lists entries in no particular order.
function fixture() {
  const others = Object.fromEntries(COUNTRIES.filter(code => code !== 'US' && code !== 'CN').map(code => [code, { public: [{ date: '2026-01-01', name: "New Year's Day" }], regional: [], observance: [] }]));
  return {
    dataset: DATASET,
    years: [2026, 2027],
    countries: {
      US: {
        public: [
          { date: '2027-11-25', name: 'Thanksgiving Day' },
          { date: '2026-11-26', name: 'Thanksgiving Day' },
          { date: '2026-12-25', name: 'Christmas Day' },
          { date: '2026-07-03', name: 'Independence Day (observed)' },
        ],
        regional: [
          { date: '2026-11-27', name: 'Day After Thanksgiving', subdivisions: 8 },
          { date: '2026-03-31', name: 'Cesar Chavez Day', subdivisions: 1 },
        ],
        observance: [
          { date: '2026-10-31', name: 'Halloween' },
          // Also a public holiday that day: the public entry wins.
          { date: '2026-12-25', name: 'Christmas Day' },
        ],
      },
      CN: {
        public: [
          { date: '2026-10-01', name: 'National Day', nameZh: '国庆节' },
          { date: '2026-02-17', name: 'Chinese New Year (Spring Festival)', nameZh: '春节' },
        ],
        regional: [],
        observance: [],
      },
      ...others,
    },
  };
}

test('thanksgiving is the fourth Thursday of November', () => {
  assert.equal(thanksgiving(2026), '2026-11-26');
  assert.equal(thanksgiving(2027), '2027-11-25');
  assert.equal(thanksgiving(2028), '2028-11-23');
  assert.equal(thanksgiving(2029), '2029-11-22');
  assert.equal(thanksgiving(2030), '2030-11-28');
});

test('commerce days follow the rules for every year', () => {
  const days = commerceDays([2026, 2026]);
  assert.deepEqual(days.map(day => day.date), ['2026-01-01', '2026-11-11', '2026-11-27', '2026-11-30', '2026-12-25']);
  const byDate = Object.fromEntries(days.map(day => [day.date, day]));
  assert.equal(byDate['2026-11-27'].nameZh, '黑色星期五');
  assert.equal(byDate['2026-11-27'].nameEn, 'Black Friday');
  assert.equal(byDate['2026-11-30'].nameEn, 'Cyber Monday');
  assert.equal(byDate['2026-11-11'].nameZh, '双十一');
  assert.equal(commerceDays([2026, 2030]).length, 25);
});

test('names translate with their observed / estimated suffixes', () => {
  assert.equal(translateName('Christmas Day'), '圣诞节');
  assert.equal(translateName('Independence Day (observed)'), '独立日（补假）');
  assert.equal(translateName('Eid al-Fitr (estimated)'), '开斋节（预计）');
  assert.equal(translateName('Eid al-Fitr (observed, estimated)'), '开斋节（补假，预计）');
  assert.equal(translateName('Monday following Epiphany', 'ES'), '主显节（周一补假）');
  // Country-specific names win over the shared table.
  assert.equal(translateName('Labor Day', 'US'), '劳工节');
  assert.equal(translateName('Labor Day', 'DE'), '劳动节');
  assert.throws(() => translateName('Totally Unknown Day'), /Totally Unknown Day/);
});

test('the transform keeps national, widely observed regional and observance days', () => {
  const out = buildHolidays(fixture(), { verifiedAt: '2026-10-30' });
  assert.deepEqual(out.meta, { source: 'python-holidays', version: '0.107', license: 'MIT', url: 'https://github.com/vacanza/holidays', verifiedAt: '2026-10-30' });
  assert.deepEqual(out.years, [2026, 2027]);
  assert.deepEqual(Object.keys(out.countries), COUNTRIES);
  assert.deepEqual(out.countries.US, [
    { date: '2026-07-03', nameZh: '独立日（补假）', nameEn: 'Independence Day (observed)', kind: 'public' },
    { date: '2026-10-31', nameZh: '万圣节', nameEn: 'Halloween', kind: 'observance' },
    { date: '2026-11-26', nameZh: '感恩节', nameEn: 'Thanksgiving Day', kind: 'public' },
    { date: '2026-11-27', nameZh: '感恩节次日', nameEn: 'Day After Thanksgiving', kind: 'regional' },
    { date: '2026-12-25', nameZh: '圣诞节', nameEn: 'Christmas Day', kind: 'public' },
    { date: '2027-11-25', nameZh: '感恩节', nameEn: 'Thanksgiving Day', kind: 'public' },
  ]);
  // Chinese names come from the dataset's own zh_CN localisation when it has one.
  assert.deepEqual(out.countries.CN.map(item => item.nameZh), ['春节', '国庆节']);
  assert.equal(out.commerce.length, 10);
});

test('the same input gives byte-identical output, whatever the input order', () => {
  const a = formatHolidays(buildHolidays(fixture(), { verifiedAt: '2026-10-30' }));
  const shuffled = fixture();
  shuffled.countries.US.public.reverse();
  shuffled.countries.US.observance.reverse();
  shuffled.countries = Object.fromEntries(Object.entries(shuffled.countries).reverse());
  const b = formatHolidays(buildHolidays(shuffled, { verifiedAt: '2026-10-30' }));
  assert.equal(a, b);
  assert.ok(a.endsWith('\n'));
  assert.deepEqual(JSON.parse(a), buildHolidays(fixture(), { verifiedAt: '2026-10-30' }));
});

test('the transform rejects incomplete dumps', () => {
  const bad = fixture();
  bad.countries.US.public.push({ date: '2028-01-01', name: "New Year's Day" });
  assert.throws(() => buildHolidays(bad, { verifiedAt: '2026-10-30' }), /outside/);
  const unknown = fixture();
  unknown.countries.US.public.push({ date: '2026-05-05', name: 'Totally Unknown Day' });
  assert.throws(() => buildHolidays(unknown, { verifiedAt: '2026-10-30' }), /Totally Unknown Day/);
  assert.throws(() => buildHolidays(fixture(), { verifiedAt: '30/10/2026' }), /verifiedAt/);
  // Every one of the 18 countries must be in the dump, with at least one holiday.
  const missing = fixture();
  delete missing.countries.SA;
  assert.throws(() => buildHolidays(missing, { verifiedAt: '2026-10-30' }), /SA/);
  const emptied = fixture();
  emptied.countries.JP.public = [];
  assert.throws(() => buildHolidays(emptied, { verifiedAt: '2026-10-30' }), /JP/);
});

test('the dump runs through uv, or through any Python given with --python', () => {
  const viaUv = dumpCommand([2026, 2030]);
  assert.equal(viaUv.command, 'uv');
  assert.deepEqual(viaUv.args.slice(0, 6), ['run', '--no-project', '--with', 'holidays==0.107', '--', 'python3']);
  const viaPython = dumpCommand([2026, 2030], { python: '/tmp/venv/bin/python' });
  assert.equal(viaPython.command, '/tmp/venv/bin/python');
  assert.deepEqual(viaPython.args.slice(0, 2), ['-I', '-c']);
  // Both pass the same spec as the last argument.
  assert.equal(viaPython.args.at(-1), viaUv.args.at(-1));
  assert.deepEqual(JSON.parse(viaPython.args.at(-1)).years, [2026, 2030]);
});

test('the committed holidays.json covers five years of every country', async () => {
  const committed = await readFile(new URL('../../official-plugins/crossborder-toolbox/backend/data/holidays.json', import.meta.url), 'utf8');
  const data = JSON.parse(committed);
  assert.equal(data.years[1] - data.years[0], 4);
  assert.deepEqual(Object.keys(data.countries), COUNTRIES);
  for (const code of COUNTRIES) assert.ok(data.countries[code].some(item => item.kind === 'public'), code);
  // The committed file is exactly what the formatter writes for its own content.
  assert.equal(formatHolidays(data), committed);
});
