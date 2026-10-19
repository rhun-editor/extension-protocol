// Optional regeneration: node tools/snippet_modifiers_oracle.mjs > go/rhunext/testdata/snippet-regex-modifiers.json
// Stable scalars avoid Unicode-version differences. Known engine ASCII/q singleton
// case-folding defects are tested separately from this oracle.
const rows=[];
function add(Source,Flags,Input) {
  let r;
  try {r=new RegExp(Source,Flags.includes('d')?Flags:Flags+'d');}
  catch {rows.push({Source,Flags,Input,Accepted:false,Captures:null});return;}
  const m=r.exec(Input),byte=n=>Buffer.byteLength(Input.slice(0,n));
  rows.push({Source,Flags,Input,Accepted:true,Captures:m?m.indices.map(s=>s?s.map(byte):[-1,-1]):null});
}
const sources=[
 '(?i:a)','(?-i:a)','(?i-:a)','(?im-s:a)','(?s-i:a)','(?i:(?-i:a))','(?-i:(?i:a))',
 '(?i:a)(?-i:b)c','(?i:[a-z])','(?-i:[a-z])','(?i:K)','(?i:ſ)','(?-i:K)',
 '(?s:.)','(?-s:.)','(?s:.(?-s:.).)','(?m:^a$)','(?-m:^a$)','(?m:^a(?-m:$))',
 String.raw`(?i:\bſ\b)`,String.raw`(?-i:\bſ\b)`,String.raw`(?i:\BK\B)`,
 String.raw`(?i:(a)\1)`,String.raw`(?i:(a))(?-i:\1)`,String.raw`(a)(?i:\1)`,
 String.raw`(?-i:(a)\1)`,String.raw`(?i:(?<é>a)\k<é>)`,
 String.raw`(?i:(?:(?<x>a)|(?<x>b))\k<x>)`,
 String.raw`(?i:(?=a)a)b`,String.raw`(?i:(?<=(a)))\1`,String.raw`(?i:(?=(?-i:a))a)`,
 String.raw`(?i:(?=(a))\1)`,String.raw`(?i:(?<=(a)\1))b`,String.raw`(?i:(?:(a)\1)+)b`,
 String.raw`(?i:a)|(?-i:A)`,String.raw`(?i:[\q{ab|abc}])`,String.raw`(?-i:[\q{ab|abc}])`,
 String.raw`(?i:\P{Lowercase_Letter})`,String.raw`(?-i:\P{Lowercase_Letter})`,
 '(?-:a)','(?ii:a)','(?i-i:a)','(?ims-ims:a)','(?g:a)','(?u:a)','(?v:a)','(?y:a)','(?d:a)',
 '(?i)a','(?i','(?i--m:a)','(?i :a)',
];
const inputs=['','a','A','b','B','aa','aA','Aa','AA','ab','AB','aBc','aBC','abc','ABC','abcab','ababB','a\nb','\na\n','a\r\nb','\na','a\n','\n','\r','\r\n','\u2028','\u2029','x\u2028a\u2029x','K','k','K','ſ','s','S','é','É','中'];
for(const source of sources)for(const flags of ['','i','m','s','u','iu','imsu','v','iv','gv','yv'])for(const input of inputs){
  if(source.includes('p{')||source.includes('P{'))if(!flags.includes('u')&&!flags.includes('v'))continue;
  add(source,flags,input);
}
console.log(JSON.stringify(rows,null,2));
