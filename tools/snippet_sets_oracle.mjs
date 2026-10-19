// Optional fixture regeneration with a JavaScript engine supporting Unicode sets.
// Run: node tools/snippet_sets_oracle.mjs > go/rhunext/testdata/snippet-regex-sets.json
// Fixtures use stable characters shared by Unicode 16 and the engine's Unicode data.
const rows = [];
function add(Source, Flags = 'v', Input = '', Start = 0) {
  let regex;
  try { regex = new RegExp(Source, Flags.includes('d') ? Flags : Flags + 'd'); }
  catch { rows.push({ Source, Flags, Input, Start, Accepted: false, Captures: null }); return; }
  regex.lastIndex = Start;
  const match = regex.exec(Input);
  const byte = n => Buffer.byteLength(Input.slice(0, n));
  const Captures = match ? match.indices.map(span => span ? span.map(byte) : [-1, -1]) : null;
  rows.push({ Source, Flags, Input, Start: byte(Start), Accepted: true, Captures });
}
const grammar = [
  '[]', '[^]', '[a-z]', '[a&&b&&c]', '[a--b--c]', '[[[a-c][d-f]]&&[^e]]',
  String.raw`[^\q{a|b}]`, String.raw`[^\q{ab}&&a]`, String.raw`[^a--\q{ab}]`,
  String.raw`[^\q{ab}--\q{ab}]`, String.raw`[^\q{}]`, String.raw`[\P{RGI_Emoji}]`,
  String.raw`[^\p{RGI_Emoji}]`, String.raw`[\q{\d}]`, String.raw`[\q{\p{L}}]`,
  '[a&&]', '[&&a]', '[a&&bc]', '[ab&&c]', '[a--b&&c]', '[a&&b--c]',
  '[a-b-c]', '[-]', '[a-]', '[z-a]', String.raw`[a-\d]`, String.raw`[\d-a]`,
];
for (const cp of '()[]{}/-\\|&!#$%*+,.:;<=>?@^`~') {
  grammar.push(`[a${cp}]`, `[a${cp}${cp}]`, `[\\${cp}]`, `[\\q{${cp}}]`, `[\\q{\\${cp}}]`);
}
for (const source of grammar) add(source);
for (const flags of ['uv', 'vu', 'vv', 'gimsvyd']) add('', flags);
const atoms = [
  '[[a-z]&&[^aeiou]]', '[[a-z]--[aeiou]]', '[[a-z]&&[A-Z]]', '[[a-z][A-Z]&&[^aeiou]]',
  String.raw`[\q{ab|abc|a|}]`, String.raw`[\q{cd|ab}&&\q{cd|ef}]`,
  String.raw`[\q{cd|ab}--\q{cd|ef}]`, String.raw`[\q{Ab|Kſ|éa|😀x|x😀}]`,
  String.raw`[\q{AB|aB}&&\q{ab}]`, String.raw`[\q{AB|aB}--\q{ab}]`,
  String.raw`[\P{Lowercase_Letter}]`, String.raw`[^\p{Lowercase_Letter}]`,
  String.raw`[\w--[KkſS]]`, String.raw`[\W&&[^😀]]`,
  String.raw`[\q{\n|\|x|\{\}|\&\!|\x61b|\u{1f600}x}]`,
];
const inputs = ['', 'abc', 'abcd', 'cdef', 'AbAb', 'kS', 'Kſ', 'aA', 'xyz', 'boat', 'eat', '😀x😀', 'éaé', '\n|x{}&!ab'];
for (const atom of atoms) {
  for (const flags of ['v', 'iv', 'gv', 'yv']) {
    for (const input of inputs) {
      for (const source of [atom, `(${atom})c`, `(?<=(${atom}))`, `(${atom})+?`, `(${atom})\\1`, `(?=(${atom}))\\1`]) add(source, flags, input);
    }
  }
}
for (const atom of [String.raw`\p{Emoji_Keycap_Sequence}`, String.raw`\p{RGI_Emoji_ZWJ_Sequence}`, String.raw`\p{RGI_Emoji}`]) {
  for (const input of ['x1\ufe0f\u20e3', '🏳️‍🌈', '😀', '👨‍👩‍👧‍👦', 'x']) {
    add(atom, 'v', input); add(`(?<=${atom})`, 'v', input);
  }
}
let seed = 7919;
const next = n => { seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0; return seed % n; };
for (let i = 0; i < 512; i++) {
  const a = atoms[next(atoms.length)], b = atoms[next(atoms.length)];
  const op = ['&&', '--', ''][next(3)];
  add(`[${a}${op}${b}]`, next(2) ? 'iv' : 'v', inputs[next(inputs.length)]);
}
console.log(JSON.stringify(rows, null, 2));
