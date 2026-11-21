#!/usr/bin/env node
// Generates official-plugins/crossborder-toolbox/backend/data/holidays.json, the
// five-year holiday calendar of the Cross-border Toolbox plugin (design §6.5).
//
// Dataset: python-holidays (https://github.com/vacanza/holidays), MIT licence,
// pinned to HOLIDAYS_VERSION below. The committed holidays.json is the source of
// truth; this script is run by hand when the data is refreshed (once a year, or
// when a country announces changes) and is not part of the build or the tests.
//
// One-off regeneration (needs network once, for uv to fetch the pinned package
// into its cache; nothing is added to the repository's package.json):
//
//   node tools/plugins/crossborder-holidays.mjs                  # years: this year + 4
//   node tools/plugins/crossborder-holidays.mjs --from 2026 --verified-at 2026-10-30
//
// which runs, through `uv` (https://docs.astral.sh/uv/):
//
//   uv run --no-project --with holidays==0.107 -- python3 -I -c <DUMP_PY> <spec>
//
// Without uv, use any Python 3 with the same pinned version installed, e.g. a
// throw-away virtualenv:
//
//   python3 -m venv /tmp/holidays-venv && /tmp/holidays-venv/bin/pip install holidays==0.107
//   node tools/plugins/crossborder-holidays.mjs --python /tmp/holidays-venv/bin/python
//
// `--dump-only raw.json` writes the raw dump instead of holidays.json, and
// `--raw raw.json` transforms a saved dump without running Python at all.
//
// The dump is turned into the plugin format by buildHolidays (pure, tested
// offline in crossborder-holidays.test.mjs):
//   - public      national public holidays (including "(observed)" substitute days
//                 and, for China, the announced 调休 days off);
//   - regional    holidays of at least REGIONAL_MIN_SUBDIVISIONS states / provinces
//                 that are not national;
//   - observance  widely kept days that are not statutory (OBSERVANCE_CATEGORIES);
//   - commerce    rule-based shopping dates (commerceDays), not from the dataset.
// English names are the dataset's en_US names; Chinese names are the dataset's
// zh_CN names where it has them (China) and otherwise the ZH table below. A name
// missing from the table stops the generation, so nothing ships untranslated.
import { spawnSync } from 'node:child_process';
import { readFile, writeFile } from 'node:fs/promises';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const HOLIDAYS_VERSION = '0.107';
export const DATASET = { name: 'python-holidays', version: HOLIDAYS_VERSION, license: 'MIT', url: 'https://github.com/vacanza/holidays' };
export const COUNTRIES = ['US', 'CA', 'MX', 'BR', 'GB', 'DE', 'FR', 'IT', 'ES', 'NL', 'PL', 'SE', 'JP', 'AU', 'SG', 'AE', 'SA', 'CN'];
export const YEARS_COVERED = 5;
export const REGIONAL_MIN_SUBDIVISIONS = 3;
// Non-statutory dataset categories kept as observances: US retail days (Mother's Day,
// Halloween…), Brazil's optional Carnival days, Sweden's de facto eves, the Dutch
// Liberation Day in non-lustrum years.
export const OBSERVANCE_CATEGORIES = { US: ['unofficial'], BR: ['optional'], NL: ['optional'], SE: ['de_facto'] };
export const OUTPUT = new URL('../../official-plugins/crossborder-toolbox/backend/data/holidays.json', import.meta.url);

const KIND_ORDER = { public: 0, regional: 1, observance: 2 };
const SUFFIX_ZH = { observed: '补假', estimated: '预计', 'observed, estimated': '补假，预计' };

// Chinese names for the dataset's en_US names (base names; "(observed)" and
// "(estimated)" suffixes are translated by translateName).
export const ZH = {
  // shared
  "New Year's Day": '元旦',
  "New Year's Eve": '跨年夜',
  'New Year Holiday': '新年假期',
  'Christmas Day': '圣诞节',
  'Christmas Eve': '平安夜',
  'Second Day of Christmas': '圣诞节次日',
  'Boxing Day': '节礼日',
  'Day After Christmas': '圣诞节次日',
  'Good Friday': '耶稣受难日',
  'Maundy Thursday': '圣周四',
  'Holy Thursday': '圣周四',
  'Holy Saturday': '复活节前夕',
  'Easter Saturday': '复活节前夕',
  'Easter Sunday': '复活节',
  'Easter Monday': '复活节星期一',
  'Ascension Day': '耶稣升天节',
  Pentecost: '圣灵降临节',
  'Pentecost Monday': '圣灵降临节星期一',
  'Pentecost Eve': '圣灵降临节前夕',
  'Corpus Christi': '基督圣体节',
  Epiphany: '主显节',
  "All Saints' Day": '诸圣节',
  "All Souls' Day": '万灵节',
  'Assumption Day': '圣母升天节',
  'Assumption Of Mary Day': '圣母升天节',
  'Immaculate Conception': '圣母无原罪始胎节',
  'Labor Day': '劳动节',
  'May Day': '五月节',
  'National Day': '国庆日',
  'Independence Day': '独立日',
  'Constitution Day': '宪法日',
  'Liberation Day': '解放日',
  'Victory Day': '胜利日',
  'Armistice Day': '停战纪念日',
  'Remembrance Day': '阵亡将士纪念日',
  'Family Day': '家庭日',
  'Civic Holiday': '公民假日',
  'Thanksgiving Day': '感恩节',
  "Saint Stephen's Day": '圣斯德望日',
  "Saint Joseph's Day": '圣若瑟节',
  "Saint George's Day": '圣乔治节',
  "Saint John's Day": '圣约翰节',
  "Saint James' Day": '圣雅各伯节',
  "Saint Patrick's Day": '圣帕特里克节',
  "Women's Day": '妇女节',
  "International Women's Day": '国际妇女节',
  'Carnival': '狂欢节',
  'Ash Wednesday': '圣灰星期三',
  'Eid al-Fitr': '开斋节',
  'Eid al-Fitr Holiday': '开斋节假期',
  'Eid al-Adha': '宰牲节',
  'Eid al-Adha Holiday': '宰牲节假期',
  'Day of Arafah': '阿拉法特日',
  'Islamic New Year': '伊斯兰新年',
  "Prophet's Birthday": '先知诞辰',
  'Election Day': '选举日',
  'Saint John the Baptist': '圣约翰节',
  "Saint Mark's Day": '圣马可节',
  "Saint Nicholas's Day": '圣尼古拉节',
};

// Names whose Chinese differs by country, or that only one country uses.
const ZH_BY_COUNTRY = {
  US: {
    'Martin Luther King Jr. Day': '马丁·路德·金纪念日',
    "Washington's Birthday": '总统日（华盛顿诞辰）',
    "President's Day": '总统日',
    "Presidents' Day": '总统日',
    'Memorial Day': '阵亡将士纪念日',
    'Juneteenth National Independence Day': '六月节',
    'Labor Day': '劳工节',
    'Columbus Day': '哥伦布日',
    'Veterans Day': '退伍军人节',
    "Valentine's Day": '情人节',
    "Mother's Day": '母亲节',
    "Father's Day": '父亲节',
    Halloween: '万圣节',
    'Groundhog Day': '土拨鼠日',
    'Day After Thanksgiving': '感恩节次日',
    'Friday After Thanksgiving': '感恩节次日',
    "Lincoln's Birthday": '林肯诞辰',
    'Susan B. Anthony Day': '苏珊·安东尼日',
    "Indigenous Peoples' Day": '原住民日',
    'Confederate Memorial Day': '南方邦联纪念日',
    'Cesar Chavez Day': '塞萨尔·查韦斯日',
    'Emancipation Day': '解放日',
    'Inauguration Day': '总统就职日',
  },
  CA: {
    'Canada Day': '加拿大国庆日',
    'Labor Day': '劳工节',
    'Victoria Day': '维多利亚日',
    'National Day for Truth and Reconciliation': '全国真相与和解日',
    'Remembrance Day': '国殇纪念日',
  },
  MX: {
    "Benito Juárez's birthday": '贝尼托·华雷斯诞辰',
    'Change of Federal Government': '联邦政府换届日',
    'Revolution Day': '革命纪念日',
  },
  BR: {
    'Universal Fraternization Day': '元旦',
    "Tiradentes' Day": '蒂拉登特斯日',
    "Worker's Day": '劳动节',
    'Our Lady of Aparecida': '圣母显灵节',
    'Republic Proclamation Day': '共和国宣告日',
    'National Day of Zumbi and Black Awareness': '黑人意识日',
    "Public Servant's Day": '公务员日',
  },
  GB: {
    'May Day': '五月初银行假日',
    'Spring Bank Holiday': '春季银行假日',
    'Late Summer Bank Holiday': '夏末银行假日',
  },
  DE: {
    'German Unity Day': '德国统一日',
    'Reformation Day': '宗教改革纪念日',
  },
  FR: {
    'National Day': '国庆日（巴士底日）',
    'Victory Day': '欧战胜利纪念日',
    'Armistice Day': '一战停战纪念日',
    'Victor Schoelcher Day': '维克多·舍尔歇纪念日',
  },
  IT: {
    'Republic Day': '共和国日',
    'National Unity Day': '国家统一与武装部队日',
    'Saint Francis of Assisi, Patron Saint of Italy': '圣方济各节',
  },
  NL: {
    "King's Day": '国王节',
  },
  PL: {
    'National Day': '劳动节',
    'National Day of the Third of May': '五三宪法日',
    'National Independence Day': '独立日',
  },
  SE: {
    'May Day': '劳动节',
    'Midsummer Eve': '仲夏夜',
    'Midsummer Day': '仲夏节',
  },
  JP: {
    'Coming of Age Day': '成人之日',
    'Foundation Day': '建国纪念日',
    "Emperor's Birthday": '天皇诞辰',
    'Vernal Equinox Day': '春分之日',
    'Showa Day': '昭和之日',
    'Constitution Day': '宪法纪念日',
    'Greenery Day': '绿之日',
    "Children's Day": '儿童之日',
    'Marine Day': '海之日',
    'Mountain Day': '山之日',
    'Respect for the Aged Day': '敬老之日',
    'Autumnal Equinox Day': '秋分之日',
    'Sports Day': '体育之日',
    'Culture Day': '文化之日',
    'Labor Thanksgiving Day': '勤劳感谢之日',
    'National Holiday': '国民休息日',
    'Substitute Holiday': '振替休日（补假）',
  },
  AU: {
    'Australia Day': '澳大利亚国庆日',
    'ANZAC Day': '澳新军团日',
    "King's Birthday": '国王诞辰',
  },
  SG: {
    'Chinese New Year': '农历新年',
    'Vesak Day': '卫塞节',
    'Diwali': '屠妖节',
    'Eid al-Adha': '哈芝节',
  },
  SA: {
    'Founding Day Holiday': '建国日假期',
    'National Day Holiday': '国庆日假期',
  },
};

/** Returns the Chinese name for a dataset en_US name, or throws when it is unknown. */
export function translateName(name, country = '') {
  const match = /^(.*) \((observed|estimated|observed, estimated)\)$/.exec(name);
  const base = match ? match[1] : name;
  const monday = /^Monday following (.+)$/.exec(base);
  if (monday) return `${translateName(monday[1], country)}（周一补假）`;
  const zh = ZH_BY_COUNTRY[country]?.[base] ?? ZH[base];
  if (!zh) throw new Error(`no Chinese name for ${JSON.stringify(name)}${country ? ` (${country})` : ''}`);
  return match ? `${zh}（${SUFFIX_ZH[match[2]]}）` : zh;
}

const pad = n => String(n).padStart(2, '0');
const isoDate = (year, month, day) => `${year}-${pad(month)}-${pad(day)}`;
const addDays = (iso, days) => {
  const d = new Date(`${iso}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() + days);
  return d.toISOString().slice(0, 10);
};

/** US Thanksgiving: the fourth Thursday of November. */
export function thanksgiving(year) {
  const firstWeekday = new Date(Date.UTC(year, 10, 1)).getUTCDay();
  const firstThursday = 1 + ((4 - firstWeekday + 7) % 7);
  return isoDate(year, 11, firstThursday + 21);
}

/** Rule-based shopping dates for each year of [from, to], sorted by date. */
export function commerceDays([from, to]) {
  const out = [];
  for (let year = from; year <= to; year += 1) {
    const thanks = thanksgiving(year);
    out.push(
      { date: isoDate(year, 1, 1), nameZh: '元旦', nameEn: "New Year's Day" },
      { date: isoDate(year, 11, 11), nameZh: '双十一', nameEn: "Singles' Day (11.11)" },
      { date: addDays(thanks, 1), nameZh: '黑色星期五', nameEn: 'Black Friday' },
      { date: addDays(thanks, 4), nameZh: '网络星期一', nameEn: 'Cyber Monday' },
      { date: isoDate(year, 12, 25), nameZh: '圣诞节', nameEn: 'Christmas' },
    );
  }
  return out.sort((a, b) => compare(a.date, b.date));
}

const compare = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
const DATE = /^(\d{4})-(\d{2})-(\d{2})$/;

/**
 * Turns a raw python-holidays dump into the holidays.json object. Pure and
 * deterministic: entries are de-duplicated and sorted, so the input order does
 * not matter.
 */
export function buildHolidays(raw, { verifiedAt }) {
  if (!DATE.test(verifiedAt ?? '')) throw new Error(`verifiedAt must be YYYY-MM-DD, got ${verifiedAt}`);
  const [from, to] = raw.years;
  if (!Number.isInteger(from) || !Number.isInteger(to) || to < from) throw new Error(`bad years ${raw.years}`);
  const { name, version, license, url } = raw.dataset ?? {};
  if (!name || !version || !license || !url) throw new Error('dump lacks dataset name, version, license or url');
  const missing = new Set();
  const countries = {};
  for (const code of COUNTRIES) {
    const rec = raw.countries?.[code];
    if (!rec) throw new Error(`the dump has no ${code}; every one of ${COUNTRIES.join(' ')} is required`);
    const byKey = new Map();
    const add = (item, kind) => {
      const match = DATE.exec(item.date);
      if (!match || Number(match[1]) < from || Number(match[1]) > to) throw new Error(`${code} ${item.name} on ${item.date} is outside ${from}–${to}`);
      let nameZh = item.nameZh;
      if (!nameZh) {
        try { nameZh = translateName(item.name, code); } catch { missing.add(`${code}: ${item.name}`); return; }
      }
      const key = `${item.date}\u0000${nameZh}`;
      const prev = byKey.get(key);
      if (!prev || KIND_ORDER[kind] < KIND_ORDER[prev.kind] || (kind === prev.kind && item.name < prev.nameEn)) byKey.set(key, { date: item.date, nameZh, nameEn: item.name, kind });
    };
    for (const item of rec.public ?? []) add(item, 'public');
    for (const item of rec.regional ?? []) if (item.subdivisions >= REGIONAL_MIN_SUBDIVISIONS) add(item, 'regional');
    for (const item of rec.observance ?? []) add(item, 'observance');
    if (!byKey.size && !missing.size) throw new Error(`the dump has no holidays for ${code}`);
    countries[code] = [...byKey.values()].sort((a, b) => compare(a.date, b.date) || KIND_ORDER[a.kind] - KIND_ORDER[b.kind] || compare(a.nameEn, b.nameEn));
  }
  if (missing.size) throw new Error(`no Chinese name for:\n  ${[...missing].sort().join('\n  ')}`);
  return {
    meta: { source: name, version, license, url, verifiedAt },
    years: [from, to],
    countries,
    commerce: commerceDays([from, to]),
  };
}

/** Serialises the holidays object: one entry per line, stable key order, trailing newline. */
export function formatHolidays(data) {
  const entry = item => JSON.stringify({ date: item.date, nameZh: item.nameZh, nameEn: item.nameEn, ...(item.kind ? { kind: item.kind } : {}) });
  const list = (items, indent) => (items.length ? `[\n${items.map(item => `${indent}  ${entry(item)}`).join(',\n')}\n${indent}]` : '[]');
  const { source, version, license, url, verifiedAt } = data.meta;
  const countries = Object.entries(data.countries).map(([code, items]) => `    ${JSON.stringify(code)}: ${list(items, '    ')}`);
  return [
    '{',
    `  "meta": ${JSON.stringify({ source, version, license, url, verifiedAt })},`,
    `  "years": ${JSON.stringify(data.years)},`,
    `  "countries": {\n${countries.join(',\n')}\n  },`,
    `  "commerce": ${list(data.commerce, '  ')}`,
    '}',
    '',
  ].join('\n');
}

// Python run inside uv's throw-away environment: dumps national, subdivision and
// selected-category holidays of every country with en_US (and zh_CN where the
// dataset has it) names. Kept free of policy; buildHolidays applies it.
const DUMP_PY = String.raw`
import json, sys
import holidays

spec = json.loads(sys.argv[1])
if holidays.__version__ != spec["dataset"]["version"]:
    raise SystemExit("holidays %s installed, %s required" % (holidays.__version__, spec["dataset"]["version"]))
years = range(spec["years"][0], spec["years"][1] + 1)

def rows(h, zh):
    out = []
    for d in sorted(h):
        names = h.get_list(d)
        zh_names = zh.get_list(d) if zh is not None else None
        if zh_names is not None and len(zh_names) != len(names):
            raise SystemExit("en_US and zh_CN names differ on %s" % d)
        for i, name in enumerate(names):
            row = {"date": d.isoformat(), "name": name}
            if zh_names is not None:
                row["nameZh"] = zh_names[i]
            out.append(row)
    return out

result = {"dataset": spec["dataset"], "years": spec["years"], "countries": {}}
for code in spec["countries"]:
    make = lambda **kw: holidays.country_holidays(code, years=years, **kw)
    national = make(language="en_US")
    cls = type(national)
    has_zh = "zh_CN" in cls.supported_languages
    rec = {"public": rows(national, make(language="zh_CN") if has_zh else None), "regional": [], "observance": []}
    counts = {}
    for sub in cls.subdivisions:
        h = make(subdiv=sub, language="en_US")
        for d in h:
            nat = set(national.get_list(d)) if d in national else set()
            for name in h.get_list(d):
                if name not in nat:
                    key = (d.isoformat(), name)
                    counts[key] = counts.get(key, 0) + 1
    rec["regional"] = [{"date": d, "name": n, "subdivisions": c} for (d, n), c in sorted(counts.items())]
    for cat in spec["observance"].get(code, []):
        rec["observance"] += rows(make(language="en_US", categories=(cat,)), make(language="zh_CN", categories=(cat,)) if has_zh else None)
    result["countries"][code] = rec
json.dump(result, sys.stdout, ensure_ascii=False, sort_keys=True)
`;

/**
 * The command that dumps the dataset: the given Python (which must have the pinned
 * holidays version installed), else uv with a throw-away environment.
 */
export function dumpCommand(years, { python } = {}) {
  const spec = JSON.stringify({ dataset: DATASET, years, countries: COUNTRIES, observance: OBSERVANCE_CATEGORIES });
  const script = ['-I', '-c', DUMP_PY, spec];
  if (python) return { command: python, args: script };
  return { command: 'uv', args: ['run', '--no-project', '--with', `holidays==${HOLIDAYS_VERSION}`, '--', 'python3', ...script] };
}

function dump(years, options) {
  const { command, args } = dumpCommand(years, options);
  const run = spawnSync(command, args, { encoding: 'utf8', maxBuffer: 64 * 1024 * 1024 });
  if (run.error) throw new Error(`${command}: ${run.error.message} (install uv, or pass --python / --raw, see the header)`);
  if (run.status !== 0) throw new Error(`python-holidays dump failed:\n${run.stderr}`);
  return JSON.parse(run.stdout);
}

function argValue(args, flag) {
  const at = args.indexOf(flag);
  return at >= 0 ? args[at + 1] : undefined;
}

async function main(args) {
  const today = new Date().toISOString().slice(0, 10);
  const from = Number(argValue(args, '--from') ?? today.slice(0, 4));
  const verifiedAt = argValue(args, '--verified-at') ?? today;
  const rawPath = argValue(args, '--raw');
  const raw = rawPath ? JSON.parse(await readFile(rawPath, 'utf8')) : dump([from, from + YEARS_COVERED - 1], { python: argValue(args, '--python') });
  const dumpOnly = argValue(args, '--dump-only');
  if (dumpOnly) {
    await writeFile(dumpOnly, `${JSON.stringify(raw)}\n`);
    return;
  }
  const out = argValue(args, '--out') ?? fileURLToPath(OUTPUT);
  const data = buildHolidays(raw, { verifiedAt });
  await writeFile(out, formatHolidays(data));
  const total = Object.values(data.countries).reduce((n, items) => n + items.length, 0);
  process.stderr.write(`wrote ${out}: ${data.years.join('–')}, ${Object.keys(data.countries).length} countries, ${total} holidays, ${data.commerce.length} commerce days\n`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv.slice(2)).catch(error => {
    process.stderr.write(`${error.message}\n`);
    process.exit(1);
  });
}
