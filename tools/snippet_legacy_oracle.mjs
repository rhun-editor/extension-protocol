// Optional: node tools/snippet_legacy_oracle.mjs > go/rhunext/testdata/snippet-regex-legacy.json
// Native noflag property/braced-scalar extensions and scalar astral consumption
// intentionally differ from JavaScript, so the oracle avoids those cases.
const rows=[];
function add(Source,Flags='',Input='') {
  let regex;
  try {regex=new RegExp(Source,Flags+'d');}
  catch {rows.push({Source,Flags,Input,Accepted:false,Captures:null});return;}
  const match=regex.exec(Input),byte=n=>Buffer.byteLength(Input.slice(0,n));
  rows.push({Source,Flags,Input,Accepted:true,Captures:match?match.indices.map(span=>span?span.map(byte):[-1,-1]):null});
}
const sources=[
 String.raw`\0`,String.raw`\000`,String.raw`\01`,String.raw`\08*`,String.raw`\89+`,String.raw`\377`,String.raw`\400`,String.raw`\777`,
 String.raw`\18*`,String.raw`(a)\18*`,String.raw`\01(a)`,String.raw`\08(a)`,String.raw`\08*(a)`,
 String.raw`\1(a)`,String.raw`(a)\1`,String.raw`(a)(b)\2\1`,String.raw`\2(a)(b)`,String.raw`\2[(](a)(?<x>b)`,
 String.raw`[\1]`,String.raw`(a)[\1]`,String.raw`[\08]`,String.raw`[\89]`,String.raw`[\B]`,String.raw`[\k]`,
 String.raw`\k<x>`,String.raw`\k`,String.raw`\k<x>(?<x>a)`,String.raw`(?<x>a)\k<missing>`,String.raw`(?<x>a)[\k]`,
 String.raw`[a-\d]`,String.raw`[\d-a]`,String.raw`[^a-\d]`,String.raw`[^\d-a]`,String.raw`[A-\w]`,String.raw`[\W-z]`,String.raw`[a-\s]`,String.raw`[\d-a-z]`,
 String.raw`\é+`,String.raw`\a`,String.raw`\_`,String.raw`\ `,String.raw`\c1`,String.raw`\c*`,String.raw`\c`,String.raw`[\c1]`,String.raw`[\c_]`,String.raw`[\c*]`,String.raw`[\c-0]`,
 String.raw`\xZZ`,String.raw`\x1+`,String.raw`\uZZZZ`,String.raw`\u00+`,String.raw`\u{}`,String.raw`\u{G0}`,String.raw`\p`,String.raw`\p{}`,String.raw`\p{NoSuch}`,String.raw`\p{1}`,String.raw`\P{NoSuch}`,
 'a{}','a{,2}','a{1','a{1,2','a{1,2,3}','a*{','a*{2}','a{3,2}','{2}','{x}','a{1}{x}','a]b','a}b','[z-a]',
 String.raw`(?=(a))?a`,String.raw`(?=(a))*a`,String.raw`(?=(a))+a`,String.raw`(?=(a)){2}a`,String.raw`(?!(b))?a`,String.raw`(?<=a)?a`,String.raw`^*`,
 String.raw`(?<\u00GG>a)`,String.raw`(?<\u{}>a)`,String.raw`(?<\u{G0}>a)`,String.raw`(?<a>x)\k<\u00GG>`,String.raw`(?<a>x)\k<\u{}>`,
];
const inputs=['','a','b','aa','ab','ba','abba','abb','aaa','1','899','\x01888','a\x01888','\x01a','\x008a','\x00888a','\x11','\x1f','\xff',' 0','?7','B','k<x>','xZZ','x111','uZZZZ','u00','u{}','u{G0}','p{}','p{NoSuch}','P{NoSuch}','a{','a{}','a{,2}','a{1','a{1,2','a{1,2,3}','{x}','a{x}','a]b','a}b','éé','-','z','\\c1','\\c','\\','*',' '];
for(const Source of sources) for(const Flags of ['','i','u','v']) for(const Input of inputs) add(Source,Flags,Input);
console.log(JSON.stringify(rows,null,2));
