#!/usr/bin/env python3
"""Generate pinned Unicode16 snippet data. Normal builds use committed outputs.

python3 tools/snippet_unicode.py UCD.zip --go-out go/rhunext/snippet_unicode_data.go \
    --asm-out /path/to/rhun/src/app/snippetunicode.s
"""
import argparse
import hashlib
import json
import io
import os
import subprocess
from pathlib import Path
import tempfile
import zipfile

VERSION = '16.0.0'
SHA256 = 'c86dd81f2b14a43b0cc064aa5f89aa7241386801e35c59c7984e579832634eb2'
BINARY = set('''ASCII ASCII_Hex_Digit Alphabetic Any Assigned Bidi_Control Bidi_Mirrored
Case_Ignorable Cased Changes_When_Casefolded Changes_When_Casemapped Changes_When_Lowercased
Changes_When_NFKC_Casefolded Changes_When_Titlecased Changes_When_Uppercased Dash
Default_Ignorable_Code_Point Deprecated Diacritic Emoji Emoji_Component Emoji_Modifier
Emoji_Modifier_Base Emoji_Presentation Extended_Pictographic Extender Grapheme_Base
Grapheme_Extend Hex_Digit IDS_Binary_Operator IDS_Trinary_Operator ID_Continue ID_Start
Ideographic Join_Control Logical_Order_Exception Lowercase Math Noncharacter_Code_Point
Pattern_Syntax Pattern_White_Space Quotation_Mark Radical Regional_Indicator Sentence_Terminal
Soft_Dotted Terminal_Punctuation Unified_Ideograph Uppercase Variation_Selector White_Space
XID_Continue XID_Start'''.split())
FILES = {'UnicodeData.txt':2175362, 'CaseFolding.txt':86092, 'SpecialCasing.txt':16809,
 'Scripts.txt':189588, 'ScriptExtensions.txt':20576, 'PropertyAliases.txt':9389,
 'PropertyValueAliases.txt':80773, 'DerivedCoreProperties.txt':1115959,
 'DerivedNormalizationProps.txt':1372269, 'PropList.txt':144253,
 'emoji/emoji-data.txt':113024, 'ReadMe.txt':693}
SCALARS = [(0,0xd7ff),(0xe000,0x10ffff)]


def normalize(ranges):
    result=[]
    for lo,hi in sorted(ranges):
        if not 0<=lo<=hi<=0x10ffff: raise ValueError('invalid Unicode interval')
        if result and lo<=result[-1][1]+1: result[-1]=(result[-1][0],max(hi,result[-1][1]))
        else: result.append((lo,hi))
    return result


def subtract(left,right):
    left,right=normalize(left),normalize(right)
    result=[];j=0
    for lo,hi in left:
        while j<len(right) and right[j][1]<lo:j+=1
        k=j
        while k<len(right) and right[k][0]<=hi:
            a,b=right[k]
            if lo<a: result.append((lo,a-1))
            lo=max(lo,b+1)
            if lo>hi:break
            k+=1
        if lo<=hi:result.append((lo,hi))
    return result


def scalars(ranges):
    return subtract(ranges,[(0xd800,0xdfff)])


def interval(text):
    values=text.strip().split('..')
    lo=int(values[0],16);hi=int(values[-1],16)
    if len(values)>2 or not 0<=lo<=hi<=0x10ffff:raise ValueError('invalid source range')
    return lo,hi


def rows(text):
    for line in text.splitlines():
        line=line.split('#',1)[0].strip()
        if line:yield [s.strip() for s in line.split(';')]


def data_records(text):
    """Collapse UnicodeData First/Last pairs without host Unicode APIs."""
    pending=None;previous=-1
    for fields in rows(text):
        if len(fields)!=15:raise ValueError('invalid UnicodeData record')
        cp=int(fields[0],16)
        if not previous<cp<=0x10ffff:raise ValueError('unordered UnicodeData')
        previous=cp
        if fields[1].endswith(', First>'):
            if pending:raise ValueError('nested UnicodeData range')
            pending=(cp,fields);continue
        if fields[1].endswith(', Last>'):
            if not pending or pending[1][1].replace(', First>',', Last>')!=fields[1] or pending[1][2:]!=fields[2:]:raise ValueError('unpaired UnicodeData range')
            yield pending[0],cp,fields;pending=None;continue
        if pending:raise ValueError('missing UnicodeData range end')
        yield cp,cp,fields
    if pending:raise ValueError('unterminated UnicodeData range')


def build_model(texts):
    props={};fold={};upper={};lower={}
    def add(key,span):props.setdefault(key,[]).append(span)
    assigned=[];mirrored=[]
    for lo,hi,f in data_records(texts['UnicodeData.txt']):
        add('gc:'+f[2],(lo,hi));assigned.append((lo,hi))
        if f[9]=='Y':mirrored.append((lo,hi))
        if lo!=hi and (f[12] or f[13]):raise ValueError('case mapping on UnicodeData range')
        if f[12]:upper[lo]=(int(f[12],16),)
        if f[13]:lower[lo]=(int(f[13],16),)
    props['gc:Cn']=subtract(SCALARS,assigned)
    # UCD aliases include aggregate categories not present on individual records.
    for major in 'LMNPSZC':props['gc:'+major]=normalize([r for k,rs in props.items() if k.startswith('gc:'+major) and len(k)==5 for r in rs])
    props['gc:LC']=normalize(props['gc:Lu']+props['gc:Ll']+props['gc:Lt'])
    props['bin:Assigned']=assigned;props['bin:Any']=SCALARS;props['bin:ASCII']=[(0,127)];props['bin:Bidi_Mirrored']=mirrored
    for name in ['DerivedCoreProperties.txt','DerivedNormalizationProps.txt','PropList.txt','emoji/emoji-data.txt']:
        for f in rows(texts[name]):
            if len(f)==2 and f[1] in BINARY:add('bin:'+f[1],interval(f[0]))
    for f in rows(texts['Scripts.txt']):add('sc:'+f[1],interval(f[0]))
    covered=[r for k,rs in props.items() if k.startswith('sc:') for r in rs]
    props['sc:Unknown']=subtract(SCALARS,covered)
    gc_alias={};sc_alias={}
    for f in rows(texts['PropertyValueAliases.txt']):
        if f[0]=='gc':
            for alias in f[1:]:gc_alias[alias]=f[1]
        elif f[0]=='sc':
            for alias in f[1:]:sc_alias[alias]=f[2]
    for name in set(sc_alias.values()):props.setdefault('sc:'+name,[])
    overrides=[];scx={}
    for f in rows(texts['ScriptExtensions.txt']):
        span=interval(f[0]);overrides.append(span)
        for alias in f[1].split():
            name=sc_alias[alias];scx.setdefault(name,[]).append(span)
    for k,rs in list(props.items()):
        if k.startswith('sc:'):props['scx:'+k[3:]]=subtract(rs,overrides)+scx.get(k[3:],[])
    for name,rs in scx.items():
        if 'scx:'+name not in props:props['scx:'+name]=rs
    for f in rows(texts['CaseFolding.txt']):
        if f[1] in ('C','S'):
            target=tuple(int(x,16) for x in f[2].split())
            if len(target)!=1:raise ValueError('non-simple casefold')
            fold[int(f[0],16)]=target[0]
    for f in rows(texts['SpecialCasing.txt']):
        if len(f)>=5 and not f[4]:
            cp=int(f[0],16);lower[cp]=tuple(int(x,16) for x in f[1].split());upper[cp]=tuple(int(x,16) for x in f[3].split())
    for mapping in [upper,lower]:
        for cp,value in list(mapping.items()):
            if value==(cp,):del mapping[cp];continue
            if not 1<=len(value)<=3 or any(not scalar(x) for x in (cp,*value)):raise ValueError('invalid case mapping')
    if any(not scalar(cp) or not scalar(to) or cp==to or fold.get(to,to)!=to for cp,to in fold.items()):raise ValueError('invalid simple fold')
    for key in props:props[key]=scalars(props[key])
    aliases={}
    for alias,name in gc_alias.items():
        if 'gc:'+name not in props:raise ValueError('missing category '+name)
        aliases['gc:'+alias]='gc:'+name
    for alias,name in sc_alias.items():
        for prefix in ('sc:','scx:'):
            if prefix+name not in props:raise ValueError('missing script '+name)
            aliases[prefix+alias]=prefix+name
    for f in rows(texts['PropertyAliases.txt']):
        if f[1] in BINARY:
            for alias in f:aliases['bin:'+alias]='bin:'+f[1]
    for name in BINARY:
        if 'bin:'+name not in props:raise ValueError('missing binary property '+name)
        aliases['bin:'+name]='bin:'+name
    if len(BINARY)!=53:raise ValueError('unexpected scalar binary inventory')
    return props,aliases,fold,upper,lower


def scalar(cp):return 0<=cp<=0x10ffff and not 0xd800<=cp<=0xdfff


def read_ucd(path):
    if Path(path).stat().st_size!=9020779:raise ValueError('unexpected Unicode16 UCD.zip size')
    with Path(path).open('rb') as f:raw=f.read(9020780)
    if len(raw)!=9020779:raise ValueError('unexpected Unicode16 UCD.zip size')
    if hashlib.sha256(raw).hexdigest()!=SHA256:raise ValueError('UCD.zip SHA256 does not match Unicode16 pin')
    texts={}
    with zipfile.ZipFile(io.BytesIO(raw)) as z:
        for name,size in FILES.items():
            if z.getinfo(name).file_size!=size:raise ValueError('unexpected UCD member size')
            texts[name]=z.read(name).decode('utf-8')
    if 'Version 16.0.0' not in texts['ReadMe.txt']:raise ValueError('unexpected Unicode version')
    return texts


def go_source(model):
    props,aliases,fold,upper,lower=model
    lines=['// Code generated by tools/snippet_unicode.py; DO NOT EDIT.',
        '// Unicode16 source https://www.unicode.org/Public/zipped/16.0.0/UCD.zip',
        '// SHA256 '+SHA256,'// Unicode data license: LICENSE-Unicode.txt','package rhunext','',
        'const snippetUnicodeVersion = "'+VERSION+'"','var snippetUnicodeProperties = map[string][]snippetRuneRange{']
    for key,rs in sorted(props.items()):lines.append(json.dumps(key)+':{'+','.join('{0x%x,0x%x}'%r for r in rs)+'},')
    lines+=['}','var snippetUnicodeAliases = map[string]string{']
    for key,value in sorted(aliases.items()):lines.append(json.dumps(key)+':'+json.dumps(value)+',')
    lines+=['}','var snippetUnicodeFolds = [...]snippetFoldEntry{']
    lines+=['{0x%x,0x%x},'%(cp,to) for cp,to in sorted(fold.items())]
    lines+=['}']
    for name,mapping in [('Upper',upper),('Lower',lower)]:
        lines+=['var snippetUnicode'+name+' = [...]snippetCaseEntry{']
        for cp,seq in sorted(mapping.items()):
            # Go strings use explicit UTF8 bytes, independent of formatter Unicode support.
            value=''.join('\\x%02x'%b for b in ''.join(map(chr,seq)).encode())
            lines.append('{0x%x,"%s"},'%(cp,value))
        lines+=['}']
    return '\n'.join(lines)+'\n'


NATIVE_CODE='''.include "rhun.inc"
.text
# cp -> full simple Unicode fold; existing find folding remains unchanged.
FN snippet_unicode_fold
    mov eax, edi
    cmp edi, 0x10ffff
    ja 9f
    lea r8, [rip + .Lsu_fold]
    xor ecx, ecx
    mov edx, @FOLDCOUNT@
1:  cmp ecx, edx
    jae 9f
    lea r9d, [rcx + rdx]
    shr r9d, 1
    cmp edi, [r8 + r9*8]
    je 3f
    jb 2f
    lea ecx, [r9 + 1]
    jmp 1b
2:  mov edx, r9d
    jmp 1b
3:  mov eax, [r8 + r9*8 + 4]
9:  ret
FN snippet_unicode_upper
    lea r8, [rip + .Lsu_upper]
    mov edx, @UPPERCOUNT@
    jmp .Lsu_map
FN snippet_unicode_lower
    lea r8, [rip + .Lsu_lower]
    mov edx, @LOWERCOUNT@
.Lsu_map:
    xor eax, eax
    cmp edi, 0x10ffff
    ja 8f
    xor ecx, ecx
1:  cmp ecx, edx
    jae 8f
    lea r9d, [rcx + rdx]
    shr r9d, 1
    mov r10d, r9d
    shl r10d, 4
    cmp edi, [r8 + r10]
    je 3f
    jb 2f
    lea ecx, [r9 + 1]
    jmp 1b
2:  mov edx, r9d
    jmp 1b
3:  mov ecx, [r8 + r10 + 4]
    mov edx, [r8 + r10 + 8]
    lea rax, [rip + .Lsu_blob]
    add rax, rcx
    ret
8:  xor edx, edx
    ret
FN snippet_unicode_cased
    lea r8, [rip + .Lsu_cased]
    mov edx, @CASEDCOUNT@
    jmp .Lsu_predicate
FN snippet_unicode_case_ignorable
    lea r8, [rip + .Lsu_ignorable]
    mov edx, @IGNORABLECOUNT@
    jmp .Lsu_predicate
FN snippet_unicode_letter
    lea r8, [rip + .Lsu_letter]
    mov edx, @LETTERCOUNT@
    jmp .Lsu_predicate
FN snippet_unicode_upper_letter
    lea r8, [rip + .Lsu_upper_letter]
    mov edx, @UPPER_LETTERCOUNT@
    jmp .Lsu_predicate
FN snippet_unicode_lower_letter
    lea r8, [rip + .Lsu_lower_letter]
    mov edx, @LOWER_LETTERCOUNT@
.Lsu_predicate:
    xor eax, eax
    cmp edi, 0x10ffff
    ja 9f
    xor ecx, ecx
1:  cmp ecx, edx
    jae 9f
    lea r9d, [rcx + rdx]
    shr r9d, 1
    cmp edi, [r8 + r9*8]
    jb 2f
    cmp edi, [r8 + r9*8 + 4]
    jbe 3f
    lea ecx, [r9 + 1]
    jmp 1b
2:  mov edx, r9d
    jmp 1b
3:  mov eax, 1
9:  ret
'''


def asm_source(model):
    props,_,fold,upper,lower=model
    cased,ignorable=props['bin:Cased'],props['bin:Case_Ignorable']
    predicates=[('cased',cased),('ignorable',ignorable),('letter',props['gc:L']),('upper_letter',props['gc:Lu']),('lower_letter',props['gc:Ll'])]
    code=NATIVE_CODE
    for name,rs in predicates:code=code.replace('@'+name.upper()+'COUNT@',str(len(rs)))
    for name,n in [('FOLD',len(fold)),('UPPER',len(upper)),('LOWER',len(lower)),('CASED',len(cased)),('IGNORABLE',len(ignorable))]:code=code.replace('@'+name+'COUNT@',str(n))
    lines=['# Generated by extension-protocol/tools/snippet_unicode.py; do not edit.',
        '# Unicode '+VERSION+'; UCD.zip SHA256 '+SHA256,
        '# Unicode data license: assets/licenses/LICENSE-Unicode.txt',code,'.section .rodata','.p2align 3','.Lsu_fold:']
    lines+=['.long 0x%x,0x%x'%(cp,to) for cp,to in sorted(fold.items())]
    blob=bytearray();positions={}
    for name,mapping in [('upper',upper),('lower',lower)]:
        lines+=['.p2align 3','.Lsu_'+name+':']
        for cp,seq in sorted(mapping.items()):
            value=''.join(map(chr,seq)).encode('utf-8')
            if len(value)>12:raise ValueError('case mapping too long')
            if value not in positions:positions[value]=len(blob);blob.extend(value)
            lines.append('.long 0x%x,%d,%d,0'%(cp,positions[value],len(value)))
    for name,rs in predicates:
        lines+=['.p2align 3','.Lsu_'+name+':']+['.long 0x%x,0x%x'%r for r in rs]
    lines+=['.Lsu_blob:']
    for i in range(0,len(blob),16):lines.append('.byte '+','.join('0x%02x'%b for b in blob[i:i+16]))
    return '\n'.join(lines)+'\n'


def write_atomic(path,text):
    path=Path(path);raw=text.encode('utf-8')
    if path.exists() and path.read_bytes()==raw:return
    path.parent.mkdir(parents=True,exist_ok=True)
    tmp=None
    try:
        with tempfile.NamedTemporaryFile(dir=path.parent,delete=False) as f:
            tmp=f.name;f.write(raw)
        os.replace(tmp,path);tmp=None
    finally:
        if tmp:os.unlink(tmp)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('ucd');parser.add_argument('--go-out',required=True);parser.add_argument('--asm-out',required=True)
    args=parser.parse_args()
    targets=[Path(args.go_out).resolve(),Path(args.asm_out).resolve()]
    if targets[0]==targets[1] or Path(args.ucd).resolve() in targets:parser.error('outputs must be distinct from each other and the source ZIP')
    model=build_model(read_ucd(args.ucd))
    go = subprocess.run(['gofmt'], input=go_source(model), text=True, capture_output=True, check=True).stdout
    outputs=[(args.go_out,go),(args.asm_out,asm_source(model))]
    for path,text in outputs:write_atomic(path,text)

if __name__=='__main__':main()
