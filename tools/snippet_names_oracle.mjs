// Optional regeneration: node tools/snippet_names_oracle.mjs > go/rhunext/testdata/snippet-regex-names.json
// Stable Unicode identifiers and numeric spans avoid host Unicode-version differences.
const rows=[];
function add(Source,Flags='',Input='') {
  let r;
  try {r=new RegExp(Source,Flags+'d');}
  catch {rows.push({Source,Flags,Input,Accepted:false,Captures:null});return;}
  const match=r.exec(Input),byte=n=>Buffer.byteLength(Input.slice(0,n));
  rows.push({Source,Flags,Input,Accepted:true,Captures:match?match.indices.map(span=>span?span.map(byte):[-1,-1]):null});
}
const names=['x','é','e\u0301','𐐀','$','_','a0','a\u200c','a\u200d','中文','℘',String.raw`\u00e9`,String.raw`\u{e9}`,String.raw`\uD801\uDC00`,String.raw`\u{10400}`,'__proto__',String.raw`\u{00000061}`,String.raw`\u{0000000000010400}`];
for(const name of names) for(const flags of ['','u','v','iu','iv']) {
  for(const input of ['','a','aa','bb','ab','ba','abba','bbabc','aabb']) {
    for(const source of [`(?<${name}>a)\\k<${name}>`,`\\k<${name}>(?<${name}>a)`,`(?:(?<${name}>a)|(?<${name}>b))\\k<${name}>`,`(?:(?<${name}>a)|(?<${name}>b))+\\k<${name}>`,`(?<=(?:(?<${name}>a)|(?<${name}>b))\\k<${name}>)c`]) add(source,flags,input);
  }
}
const invalid=['','0x','😀','\u0301a','\u200ca','a-b','a b',String.raw`\x61`,String.raw`\u{}`,String.raw`\uD800`,String.raw`\uD801\u0041`,String.raw`\u0030x`,String.raw`a\u002d`,String.raw`\u{110000}`];
for(const name of invalid) for(const flags of ['','u','v']) add(`(?<${name}>a)`,flags,'a');
const structural=[
 String.raw`(?<x>a)(?<x>b)`,String.raw`(?<x>(?<x>a)|b)`,String.raw`(?<x>a)|(?<x>b)`,
 String.raw`(?:(?<x>a)|b)(?:(?<x>c)|d)`,String.raw`(?:(?<x>a)|(?:(?<x>b)|(?<x>c)))\k<x>`,
 String.raw`(?:(?<x>a)|(?<x>b)|c)\k<x>`,String.raw`(?:(?<x>)|(?<x>b))\k<x>`,
 String.raw`(?:(?<x>a)|(?<x>b)){0,3}\k<x>`,String.raw`(?:(?<x>a)|(?<x>b))*?\k<x>c`,
 String.raw`(?:(?<x>a)c|(?<x>b))\k<x>`,String.raw`(?:(?=(?<x>a))|(?=(?<x>b)))\k<x>`,
 String.raw`(?:(?!(?<x>a))|(?!(?<x>b)))\k<x>`,String.raw`(?:(?<x>a)|(?<x>b))\1\2`,
 String.raw`(?<é>a)\k<\u00e9>`,String.raw`(?<é>a)(?<é>b)\k<é>\k<é>`,
 String.raw`(?:(?<é>a)|(?<\u00e9>b))\k<\u{e9}>`,
 String.raw`(?<é>a)(?<\u00e9>b)`, String.raw`(?<x>a)|(?<x>b)(?<x>c)`,
 String.raw`(?:(?<x>a)|(?<y>b))(?:(?<x>c)|(?<y>d))`,
 String.raw`(?:(?<x>a)|(?<x>b))(?:(?<y>c)|(?<y>d))\k<x>\k<y>`,
 String.raw`(?<=(?:(?<x>a)|(?<x>b))\k<x>)c`,String.raw`(?<=(?:(?<x>a)|(?<x>b)))\k<x>`,
];
for(const source of structural) for(const flags of ['','u','v','iu','iv']) for(const input of ['','a','aa','bb','cc','abc','bbc','aabb','abbaac','abbaa','abbac','aaaac','acac','abdcad','abab','abba','😀abc']) add(source,flags,input);
for(const source of [String.raw`\u{00000061}`,String.raw`[\u{00000061}]`,String.raw`[\q{\u{00000061}}]`]) for(const flags of ['u','v']) add(source,flags,'a');
console.log(JSON.stringify(rows,null,2));
